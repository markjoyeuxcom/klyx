package appbridge

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitops/flux"
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
	Reconcile(ctx context.Context, kind, ns, name string) error
	ReconcileWithSource(ctx context.Context, kind, ns, name string) error
	SetSuspend(ctx context.Context, kind, ns, name string, suspend bool) error
	// GitOpsSummaryFlux performs a cluster-wide on-demand LIST and returns counts.
	// Separated from fleet.GitOpsSummary to keep GitOpsConn free of fleet types.
	GitOpsSummaryFlux(ctx context.Context) (fluxPresent bool, total, notReady, suspended int, err error)
}

// GitOpsSummaryDTO is the serialised form of GitOpsSummary for the JS bridge.
type GitOpsSummaryDTO struct {
	FluxPresent bool   `json:"fluxPresent"`
	Total       int    `json:"total"`
	NotReady    int    `json:"notReady"`
	Suspended   int    `json:"suspended"`
	Error       string `json:"error,omitempty"` // non-empty = the summary read failed (never confuse with FluxPresent:false)
}

// GitOpsService is bound to JS: the Flux lens's on-demand reads (detail, Git
// link, diff, summary) and its day-2 verbs.
type GitOpsService struct {
	lookup func(string) (GitOpsConn, bool)
	now    func() time.Time
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
func (s *GitOpsService) Reconcile(cluster, kind, namespace, name string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.Reconcile(ctx, kind, namespace, name); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

func (s *GitOpsService) ReconcileWithSource(cluster, kind, namespace, name string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.ReconcileWithSource(ctx, kind, namespace, name); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
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
	if !conn.FluxAvailable() {
		return FluxDiffDTO{Available: false, Error: "the flux CLI was not found on PATH"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	res, err := conn.FluxDiffKustomization(ctx, namespace, name, path)
	if err != nil {
		return FluxDiffDTO{Available: true, Error: err.Error()}
	}
	return FluxDiffDTO{Available: true, HasChanges: res.HasChanges, Output: res.Output, Error: res.Err}
}

func (s *GitOpsService) SetSuspend(cluster, kind, namespace, name string, suspend bool) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.SetSuspend(ctx, kind, namespace, name, suspend); err != nil {
		return ActionResultDTO{Error: err.Error()}
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
			d.Source = &src
		case apierrors.IsNotFound(err):
			d.SourceWarning = what + " was not found - its health is unknown"
		default:
			d.SourceWarning = what + " is unverified: " + err.Error()
		}
	}
	// Drift surface: the controller's own record of what it did (M10-e).
	if evs, err := conn.FluxEvents(ctx, kind, namespace, name); err == nil {
		for _, e := range evs {
			d.Events = append(d.Events, toEventRowDTO(e))
		}
	}
	return d
}

// detailErrorDTO is a failed detail read for the requested resource: its
// key and the error, never an identity or detail evidence.
func detailErrorDTO(kind, namespace, name, msg string) ResourceDetailDTO {
	return ResourceDetailDTO{Kind: kind, Namespace: namespace, Name: name, Error: msg}
}

// GetGitOpsSummary returns a point-in-time Flux summary for the named cluster.
// A cluster miss returns {FluxPresent: false} - semantically "no Flux here", so
// the strip tile hides. A list error is different: it returns {Error: ...} so
// the tile renders the failure marker ("—") rather than masquerading as a
// Flux-free cluster (error-honesty - a transient RBAC/network fault must not
// read as "no Flux").
func (s *GitOpsService) GetGitOpsSummary(cluster string) GitOpsSummaryDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return GitOpsSummaryDTO{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	present, total, notReady, suspended, err := conn.GitOpsSummaryFlux(ctx)
	if err != nil {
		return GitOpsSummaryDTO{Error: err.Error()}
	}
	return GitOpsSummaryDTO{FluxPresent: present, Total: total, NotReady: notReady, Suspended: suspended}
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
