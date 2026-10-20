package appbridge

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitops/composition"
	"github.com/moomora/klyx/internal/gitops/depgraph"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/gitops/notify"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/urlscrub"
	"github.com/moomora/klyx/internal/workloads"
)

// GitOpsConn is the per-cluster surface GitOpsService needs. The lists are
// the health Observer's shared rows (1c slice 3 §5.3); this service keeps
// the on-demand reads and the day-2 verbs.
type GitOpsConn interface {
	// FluxObject reads one Flux object directly at its served version
	// (design §5b): NotFound is distinguishable from an unreadable object.
	FluxObject(ctx context.Context, kind, ns, name string) (*unstructured.Unstructured, error)
	FluxEvents(ctx context.Context, kind, ns, name string) ([]workloads.EventSummary, error)
	FluxAvailable() bool
	FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error)
	// ServerSideDiff dry-runs objects built from the Git cache (slice 6e).
	ServerSideDiff(ctx context.Context, objs []*unstructured.Unstructured, inventory []string, prune bool) ([]cachediff.Change, error)
	// The verbs write only the armed object (uid): a same-name replacement
	// is refused (fleet.ErrReplaced), never written (design §5b).
	Reconcile(ctx context.Context, kind, ns, name, uid string) error
	ReconcileWithSourceSteps(ctx context.Context, kind, ns, name, uid string) ([]fleet.Step, error)
	SetSuspend(ctx context.Context, kind, ns, name, uid string, suspend bool) error
	// DependencyGraph walks the GitOps dependency graph (The Line slice 8b).
	DependencyGraph(ctx context.Context, kind, ns, name, uid string) depgraph.Graph
	// The Flux Operator (coverage S1a §5): one annotation verb on the armed
	// object, and one object read for the inspector's on-demand facts.
	FluxOpAct(ctx context.Context, kind, ns, name, uid, verb string) error
	FluxOpObject(ctx context.Context, kind, ns, name string) (*unstructured.Unstructured, error)
	// HealthRowsNow is one fresh Observer capture of a part's rows: the
	// image inspector's applied lines read the reconciliations (S1b §2).
	HealthRowsNow(ctx context.Context, task, part string) (observe.Rows, bool)
	// FluxNotifications reads Providers and Alerts on demand (S1c §3).
	FluxNotifications(ctx context.Context) notify.Read
}

// GitOpsService is bound to JS: the Flux lens's on-demand reads (detail, Git
// link, diff) and its day-2 verbs.
type GitOpsService struct {
	lookup func(string) (GitOpsConn, bool)
	now    func() time.Time
	// cacheDiff is the Git cache's diff (The Line slice 6e): it answers when
	// the Kustomization's repository is approved, else the flux CLI does.
	cacheDiff func(ctx context.Context, conn GitOpsConn, ns, name string) (FluxDiffDTO, bool)
}

// SetGitOpsCacheDiff wires the Git cache's diff into the Flux lens's
// "Compute diff". A package-level setter, so it is not bound to the webview.
func SetGitOpsCacheDiff(s *GitOpsService, g *GitService) {
	s.cacheDiff = g.cacheDiff
}

func NewGitOpsService(lookup func(string) (GitOpsConn, bool), now func() time.Time) *GitOpsService {
	return &GitOpsService{lookup: lookup, now: now}
}

const actionTimeout = 30 * time.Second

// diffTimeout is generous: `flux diff` builds the kustomization locally
// (including SOPS decryption) and dry-runs it against the apiserver.
const diffTimeout = 120 * time.Second

// Reconcile, ReconcileWithSource and SetSuspend drive the Flux controllers on a
// live cluster - day-2 mutations, not desired-state authoring - so on a
// protected cluster they consume the typed-confirmation grant first, exactly
// like ArgoService.SyncApp and every other destructive verb. The frontend
// dialogs arm the grant on confirm; gating here keeps arm and check paired
// one-to-one so a frontend bug can never fire a Flux mutation on a protected
// cluster without the typed confirmation, and no un-consumed grant can linger
// to authorize an unrelated later action (ProtectGuard: a guard against
// mistakes, not a security boundary).
// uid is the object the verb was armed against (the row the operator
// confirmed): only that object is written.
func (s *GitOpsService) Reconcile(cluster, kind, namespace, name, uid string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.Reconcile(ctx, kind, namespace, name, uid); err != nil {
		return failed(ctx, conn, err)
	}
	return ActionResultDTO{OK: true}
}

func (s *GitOpsService) ReconcileWithSource(cluster, kind, namespace, name, uid string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	// coverage S3b §2.3: each step's outcome, the refused source's kept
	// even when the target is written
	steps, err := conn.ReconcileWithSourceSteps(ctx, kind, namespace, name, uid)
	var outs []stepOutcome
	for _, st := range steps {
		outs = append(outs, stepOutcome{verb: st.Verb, subject: st.Subject, written: st.Written, err: st.Err})
	}
	r := ActionResultDTO{OK: err == nil}
	if err != nil {
		r = failed(ctx, conn, err)
	}
	if len(outs) > 1 {
		r.Steps = stepsDTO(ctx, conn, outs)
	}
	return r
}

// FluxDiff runs an on-demand `flux diff` for a Kustomization (M10-f). Bound to
// JS, request/response, never auto-run. Available=false when the CLI is absent
// (the UI hides the button); the gate (suspended/failing only) is enforced in
// the conn and any refusal/CLI failure surfaces in Error.
func (s *GitOpsService) FluxDiff(cluster, namespace, name, path string) FluxDiffDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return FluxDiffDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	if s.cacheDiff != nil && path == "" {
		if d, ok := s.cacheDiff(ctx, conn, namespace, name); ok {
			return d
		}
	}
	if !conn.FluxAvailable() {
		return FluxDiffDTO{Available: false, Error: "the flux CLI was not found on PATH", Via: "flux CLI"}
	}
	res, err := conn.FluxDiffKustomization(ctx, namespace, name, path)
	if err != nil {
		return FluxDiffDTO{Available: true, Error: err.Error(), Via: "flux CLI"}
	}
	return FluxDiffDTO{Available: true, HasChanges: res.HasChanges, Output: res.Output, Error: res.Err, Via: "flux CLI"}
}

func (s *GitOpsService) SetSuspend(cluster, kind, namespace, name, uid string, suspend bool) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.SetSuspend(ctx, kind, namespace, name, uid, suspend); err != nil {
		return failed(ctx, conn, err)
	}
	return ActionResultDTO{OK: true}
}

// GetResourceDetail reads the detail view for one Flux resource directly
// (design §5b): its object at the served version, carrying UID, generation
// and as-of. A failed read is the request's outcome (Error, keyed by
// kind/namespace/name only) - never an empty detail; the lens shows a detail
// only for the selected row's UID.
func (s *GitOpsService) GetResourceDetail(cluster, kind, namespace, name string) ResourceDetailDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return detailErrorDTO(kind, namespace, name, "cluster not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	u, err := conn.FluxObject(ctx, kind, namespace, name)
	if err != nil {
		return detailErrorDTO(kind, namespace, name, err.Error())
	}
	d := toDetailDTO(flux.ParseDetail(u))
	d.UID, d.Generation, d.AsOfUnix = string(u.GetUID()), u.GetGeneration(), s.now().Unix()
	// Embed the bound source's health so a stuck resource's root cause (a source
	// that is not pulling) is visible in the detail panel without a separate
	// read. Its absence is never silent: not found and unreadable each say so.
	if ref, ok := flux.BoundSource(u); ok {
		what := fmt.Sprintf("bound source %s %s/%s", ref.Kind, ref.Namespace, ref.Name)
		su, err := conn.FluxObject(ctx, ref.Kind, ref.Namespace, ref.Name)
		switch {
		case err == nil:
			src := toSourceDTO(flux.ParseSource(su))
			if ref.Kind == string(flux.ExternalArtifactKind) {
				// S1c §4: an artifact's verdict comes from the artifact and
				// Ready, never Ready alone; its producer is its own line
				if v, perr := composition.Project(composition.KindArtifact)(su); perr == nil {
					a := composition.ParseArtifact(v.(*composition.Object))
					src.Rule = toRuleDTO(composition.ArtifactRule(a))
					d.SourceProducer = s.artifactProducer(ctx, conn, a)
				} else {
					d.SourceProducer = &SourceProducerDTO{State: composition.ProducerUnreadable}
				}
			}
			d.Source = &src
		case apierrors.IsNotFound(err):
			d.SourceWarning = what + " was not found - its health is unknown"
		default:
			d.SourceWarning = what + " is unverified: " + err.Error()
		}
	}
	switch flux.Kind(kind) {
	case flux.KustomizationKind:
		d.SourceNote = flux.FeatureGateNote(flux.ParseKustomization(u))
	case flux.HelmReleaseKind:
		d.SourceNote = flux.FeatureGateNote(flux.ParseHelmRelease(u))
	}
	// Drift surface: the controller's own record of what it did (M10-e).
	if evs, err := conn.FluxEvents(ctx, kind, namespace, name); err == nil {
		for _, e := range evs {
			d.Events = append(d.Events, toEventRowDTO(e))
		}
	}
	return d
}

// artifactProducer resolves the producer of the artifact just read - its
// own recorded identity and sourceRef - among the generators of the shared
// composition rows, whose verdicts carry the resume tracker's evidence
// (S1c §3). A same-name predecessor's row lends it nothing (Codex's S1c
// implementation review, finding 2); a generators read that is not current
// says so, never a health.
func (s *GitOpsService) artifactProducer(ctx context.Context, conn GitOpsConn, a composition.Artifact) *SourceProducerDTO {
	rows, ok := conn.HealthRowsNow(ctx, healthsrc.TaskFlux, "composition")
	cov := composition.CoverageComplete
	switch kindState(rows, ok, healthsrc.KindArtifactGenerators) {
	case joinUnreadable:
		cov = composition.CoverageUnreadable
	case notify.ProviderIncomplete:
		cov = composition.CoveragePartial
	}
	set, _ := rows.Rows.(healthsrc.RowSet[healthsrc.CompositionRow])
	var gens []composition.Generator
	evals := map[string]health.NativeEval{}
	if cov != composition.CoverageUnreadable {
		for _, r := range set.Rows {
			if g := r.Data.Generator; g != nil {
				gens = append(gens, *g)
				evals[g.UID] = r.Eval
			}
		}
	}
	return sourceProducerDTO(healthsrc.ProducerViewOf(a, gens, cov, evals))
}

// detailErrorDTO is a failed detail read for the requested resource: its
// key and the error, never an identity or detail evidence.
func detailErrorDTO(kind, namespace, name, msg string) ResourceDetailDTO {
	return ResourceDetailDTO{Kind: kind, Namespace: namespace, Name: name, Error: msg}
}

// ResolveGitLink resolves a Kustomization's GitRepository source to a browsable
// link (or a copyable reference). Zero-value DTO for non-Kustomizations or a
// non-GitRepository source; a read that fails says so (Error), never "no
// Git source" (design §5b).
func (s *GitOpsService) ResolveGitLink(cluster, kind, namespace, name string) GitLinkDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return GitLinkDTO{Error: "cluster not connected"}
	}
	if flux.Kind(kind) != flux.KustomizationKind {
		return GitLinkDTO{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	u, err := conn.FluxObject(ctx, kind, namespace, name)
	if err != nil {
		return GitLinkDTO{Error: fmt.Sprintf("%s %s/%s: %v", kind, namespace, name, err)}
	}
	src := flux.ParseKustomizationSource(u)
	if src.SourceKind != "GitRepository" || src.SourceName == "" {
		return GitLinkDTO{}
	}
	repo, err := conn.FluxObject(ctx, "GitRepository", src.SourceNamespace, src.SourceName)
	if err != nil {
		return GitLinkDTO{Error: fmt.Sprintf("GitRepository %s/%s: %v", src.SourceNamespace, src.SourceName, err)}
	}
	url, _, _ := unstructured.NestedString(repo.Object, "spec", "url")
	if url == "" {
		return GitLinkDTO{}
	}
	link := flux.ResolveGitLink(url, src.Path, src.Revision)
	return GitLinkDTO{URL: link.URL, IsDeepLink: link.IsDeepLink, CopyText: link.CopyText}
}

// FluxOpAct runs one Flux Operator verb (S1a §5: reconcile, fetch, suspend,
// resume, refresh) on the armed object. On a protected cluster it consumes
// the typed-confirmation grant first, as every Flux verb does. A verb
// reports "requested"; only the object's status says it was evaluated.
func (s *GitOpsService) FluxOpAct(cluster, kind, namespace, name, uid, verb string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.FluxOpAct(ctx, kind, namespace, name, uid, verb); err != nil {
		return failedScrubbed(ctx, conn, err)
	}
	return ActionResultDTO{OK: true}
}

// GetFluxOpDetail reads one operator object for the inspector: its history
// (without digests), inventory and readyExprs, from the sanitised object,
// and its events. A failed read is the request's outcome, never an empty
// detail; a failed events read says so.
//
// It is fenced by the UID the inspector shows (Codex's S1a implementation
// review, finding 9): a same-name replacement read in between is refused,
// never stamped with the old object's identity, and only the shown
// object's events are kept. The reply carries the UID it describes.
func (s *GitOpsService) GetFluxOpDetail(cluster, kind, namespace, name, uid string) FluxOpDetailDTO {
	if uid == "" {
		return FluxOpDetailDTO{Error: "no identity to read: the inspector names no UID"}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return FluxOpDetailDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	u, err := conn.FluxOpObject(ctx, kind, namespace, name)
	if err != nil {
		return FluxOpDetailDTO{Error: urlscrub.Text(err.Error()).Text}
	}
	if got := string(u.GetUID()); got != uid {
		return FluxOpDetailDTO{UID: got, Replaced: true, Error: kind + " " + namespace + "/" + name + " was replaced (its UID changed); nothing about the new object is shown here"}
	}
	d := fluxOpDetail(u)
	d.UID = uid
	evs, err := conn.FluxEvents(ctx, kind, namespace, name)
	if err != nil {
		d.EventsError = urlscrub.Text(err.Error()).Text
		return d
	}
	for _, e := range evs {
		if e.UID == uid {
			d.Events = append(d.Events, toEventRowDTO(e))
		}
	}
	return d
}
