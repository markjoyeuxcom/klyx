package appbridge

import (
	"context"
	"reflect"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/workloads"
)

// GitOpsConn is the per-cluster watch surface GitOpsService needs.
type GitOpsConn interface {
	OpenGitOps()
	CloseGitOps()
	GitOpsResources() []flux.Resource
	GitOpsObject(kind, namespace, name string) (*unstructured.Unstructured, bool)
	GitOpsSources() []flux.Source
	// GitOpsWatchHealth reports informer sync + the last watch error, so a
	// failing watch renders as an error instead of an empty list.
	GitOpsWatchHealth() (synced bool, lastErr string)
	GitOpsSourceObject(kind, namespace, name string) (*unstructured.Unstructured, bool)
	FluxEvents(ctx context.Context, kind, ns, name string) ([]workloads.EventSummary, error)
	FluxAvailable() bool
	FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error)
	Reconcile(ctx context.Context, kind, ns, name string) error
	ReconcileWithSource(ctx context.Context, kind, ns, name string) error
	SetSuspend(ctx context.Context, kind, ns, name string, suspend bool) error
	SourceURL(ctx context.Context, kind, ns, name string) (string, bool)
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

// GitOpsUpdatedEvent is emitted with { cluster, resources }.
const GitOpsUpdatedEvent = "gitops:updated"

type gitOpsPayload struct {
	Cluster   string            `json:"cluster"`
	Resources []FluxResourceDTO `json:"resources"`
	Sources   []FluxSourceDTO   `json:"sources"`
	// WatchError is set while the watch has not synced AND has recorded an
	// error (RBAC denied, CRD mismatch): the view must show the failure, not
	// an empty list. Empty once the informers sync.
	WatchError string `json:"watchError,omitempty"`
}

// GitOpsService is bound to JS. Open starts a cluster's lazy watch and pushes
// gitops:updated on a tick; Close stops it.
type GitOpsService struct {
	lookup   func(string) (GitOpsConn, bool)
	em       Emitter
	now      func() time.Time
	interval time.Duration

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewGitOpsService(lookup func(string) (GitOpsConn, bool), em Emitter, now func() time.Time, interval time.Duration) *GitOpsService {
	return &GitOpsService{lookup: lookup, em: em, now: now, interval: interval, cancels: map[string]context.CancelFunc{}}
}

func (s *GitOpsService) Open(cluster string) {
	conn, ok := s.lookup(cluster)
	if !ok {
		return
	}
	s.mu.Lock()
	if _, active := s.cancels[cluster]; active {
		s.mu.Unlock()
		return // idempotent
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancels[cluster] = cancel
	s.mu.Unlock()

	conn.OpenGitOps()

	// Re-check after the (slow, discovery-bound) open: a Close that landed in
	// between already cancelled our entry, and the watch it closed at the conn
	// may have been re-opened by the line above - tear it down again so no
	// consumer-less watch survives.
	s.mu.Lock()
	_, stillActive := s.cancels[cluster]
	s.mu.Unlock()
	if !stillActive {
		conn.CloseGitOps()
		return
	}
	safeGo("gitops-push:"+cluster, func() { s.pushLoop(ctx, cluster, conn) })
}

func (s *GitOpsService) Close(cluster string) {
	s.mu.Lock()
	cancel := s.cancels[cluster]
	delete(s.cancels, cluster)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn, ok := s.lookup(cluster); ok {
		conn.CloseGitOps()
	}
}

// pushHeartbeat bounds how long an unchanged push payload may be skipped. The
// DTOs carry relative ages (seconds since applied / until expiry) that the
// frontend renders as-is, so a periodic re-emit keeps them honest even when
// nothing on the cluster changed; every tick in between that carries no domain
// change is skipped instead of re-serialized into the webview.
const pushHeartbeat = 30 * time.Second

// gitopsDomainSnap is the change-detection unit for the gitops push loop: the
// parsed domain values (absolute timestamps - stable between ticks), not the
// DTOs (relative ages - different every second).
type gitopsDomainSnap struct {
	res      []flux.Resource
	srcs     []flux.Source
	watchErr string
}

func (s *GitOpsService) pushLoop(ctx context.Context, cluster string, conn GitOpsConn) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	var last *gitopsDomainSnap
	var lastEmit time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := s.now()
			watchErr := ""
			if synced, lastErr := conn.GitOpsWatchHealth(); !synced && lastErr != "" {
				watchErr = lastErr
			}
			snap := gitopsDomainSnap{res: conn.GitOpsResources(), srcs: conn.GitOpsSources(), watchErr: watchErr}
			if last != nil && now.Sub(lastEmit) < pushHeartbeat && reflect.DeepEqual(*last, snap) {
				continue
			}
			last = &snap
			lastEmit = now

			dtos := make([]FluxResourceDTO, 0, len(snap.res))
			for _, r := range snap.res {
				dtos = append(dtos, ToFluxDTO(r, now))
			}
			srcDTOs := make([]FluxSourceDTO, 0, len(snap.srcs))
			for _, sr := range snap.srcs {
				srcDTOs = append(srcDTOs, toSourceDTO(sr))
			}
			s.em.Emit(GitOpsUpdatedEvent, gitOpsPayload{Cluster: cluster, Resources: dtos, Sources: srcDTOs, WatchError: watchErr})
		}
	}
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
// one-to-one so a frontend bug (or a hostile webview call) can never fire a
// Flux mutation on a protected cluster without the typed confirmation, and no
// un-consumed grant can linger to authorize an unrelated later action.
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

// GetResourceDetail returns the detail view for one Flux resource from the live
// watch store. Zero-value DTO when the cluster/object isn't available.
func (s *GitOpsService) GetResourceDetail(cluster, kind, namespace, name string) ResourceDetailDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ResourceDetailDTO{}
	}
	u, ok := conn.GitOpsObject(kind, namespace, name)
	if !ok {
		return ResourceDetailDTO{}
	}
	d := toDetailDTO(flux.ParseDetail(u))
	// Embed the bound source's health so a stuck resource's root cause (a source
	// that is not pulling) is visible in the detail panel without a separate read.
	if ref, ok := flux.BoundSource(u); ok {
		if su, ok := conn.GitOpsSourceObject(ref.Kind, ref.Namespace, ref.Name); ok {
			src := toSourceDTO(flux.ParseSource(su))
			d.Source = &src
		}
	}
	// Drift surface: the controller's own record of what it did (M10-e).
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if evs, err := conn.FluxEvents(ctx, kind, namespace, name); err == nil {
		for _, e := range evs {
			d.Events = append(d.Events, toEventRowDTO(e))
		}
	}
	return d
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
// link (or a copyable reference). Zero-value DTO for non-Kustomizations, a
// non-GitRepository source, or any lookup miss.
func (s *GitOpsService) ResolveGitLink(cluster, kind, namespace, name string) GitLinkDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return GitLinkDTO{}
	}
	if flux.Kind(kind) != flux.KustomizationKind {
		return GitLinkDTO{}
	}
	u, ok := conn.GitOpsObject(kind, namespace, name)
	if !ok {
		return GitLinkDTO{}
	}
	src := flux.ParseKustomizationSource(u)
	if src.SourceKind != "GitRepository" || src.SourceName == "" {
		return GitLinkDTO{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	url, ok := conn.SourceURL(ctx, "GitRepository", src.SourceNamespace, src.SourceName)
	if !ok {
		return GitLinkDTO{}
	}
	link := flux.ResolveGitLink(url, src.Path, src.Revision)
	return GitLinkDTO{URL: link.URL, IsDeepLink: link.IsDeepLink, CopyText: link.CopyText}
}
