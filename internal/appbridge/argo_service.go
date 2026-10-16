package appbridge

import (
	"context"
	"github.com/moomora/klyx/internal/urlscrub"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/moomora/klyx/internal/gitops/argo"
)

const argoTimeout = 30 * time.Second

// ArgoConn is the per-cluster surface ArgoService needs (lookup seam).
// The Applications and ApplicationSets are the health Observer's shared rows
// (1c slice 3 §5.4); AppProjects stay a fetch. The verbs write only the armed
// Application (uid): a same-name replacement is refused (design §5b).
type ArgoConn interface {
	ListArgoProjects(ctx context.Context) ([]argo.Project, error)
	RefreshArgoApp(ctx context.Context, namespace, name, uid string) error
	SyncArgoApp(ctx context.Context, namespace, name, uid string, opts argo.SyncOptions) error
}

// ArgoConditionDTO is one Application condition (Argo: type + message).
type ArgoConditionDTO struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type ArgoResourceDTO struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Health    string `json:"health"`
}

type ArgoSyncOptionsDTO struct {
	Revision        string            `json:"revision"`
	Strategy        string            `json:"strategy"`
	Prune           bool              `json:"prune"`
	DryRun          bool              `json:"dryRun"`
	Force           bool              `json:"force"`
	Replace         bool              `json:"replace"`
	ServerSideApply bool              `json:"serverSideApply"`
	Resources       []ArgoResourceDTO `json:"resources"`
}

type ArgoResourceRuleDTO struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
}

type ArgoProjectDTO struct {
	Namespace                  string                `json:"namespace"`
	Name                       string                `json:"name"`
	Description                string                `json:"description"`
	SourceRepos                []string              `json:"sourceRepos"`
	Destinations               []string              `json:"destinations"`
	Roles                      int                   `json:"roles"`
	RoleNames                  []string              `json:"roleNames"`
	OrphanWarn                 bool                  `json:"orphanWarn"`
	ClusterResourceWhitelist   []ArgoResourceRuleDTO `json:"clusterResourceWhitelist"`
	ClusterResourceBlacklist   []ArgoResourceRuleDTO `json:"clusterResourceBlacklist"`
	NamespaceResourceWhitelist []ArgoResourceRuleDTO `json:"namespaceResourceWhitelist"`
	NamespaceResourceBlacklist []ArgoResourceRuleDTO `json:"namespaceResourceBlacklist"`
}

type ArgoAppSetConditionDTO struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type ArgoApplicationSetDTO struct {
	UID            string                   `json:"uid,omitempty"` // identity (1c slice 3 §2a)
	GVR            string                   `json:"gvr,omitempty"`
	Generation     int64                    `json:"generation,omitempty"`
	Namespace      string                   `json:"namespace"`
	Name           string                   `json:"name"`
	Generators     []string                 `json:"generators"`
	TemplateName   string                   `json:"templateName"`
	Project        string                   `json:"project"`
	RepoURL        string                   `json:"repoURL"`
	Path           string                   `json:"path"`
	TargetRevision string                   `json:"targetRevision"`
	DestNamespace  string                   `json:"destNamespace"`
	DestServer     string                   `json:"destServer"`
	GeneratedApps  int                      `json:"generatedApps"`
	GeneratedNames []string                 `json:"generatedNames"`
	Healthy        bool                     `json:"healthy"`
	Conditions     []ArgoAppSetConditionDTO `json:"conditions"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

// ArgoAppDTO is one Argo CD Application in Argo's own vocabulary.
type ArgoAppDTO struct {
	UID            string `json:"uid,omitempty"` // identity (1c slice 3 §2a)
	GVR            string `json:"gvr,omitempty"`
	Generation     int64  `json:"generation,omitempty"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	Project        string `json:"project"`
	SyncStatus     string `json:"syncStatus"`   // Synced | OutOfSync | Unknown
	HealthStatus   string `json:"healthStatus"` // Healthy | Progressing | Degraded | Suspended | Missing | Unknown
	Broken         bool   `json:"broken"`
	Revision       string `json:"revision"`
	RepoURL        string `json:"repoURL"`
	Path           string `json:"path"`
	Chart          string `json:"chart"`
	TargetRevision string `json:"targetRevision"`
	ExtraSources   int    `json:"extraSources"`
	DestNamespace  string `json:"destNamespace"`
	// DestServer / DestName say WHICH cluster the app's resources live on;
	// the inventory is only the control plane's own objects when the
	// destination is in-cluster.
	DestServer     string             `json:"destServer"`
	DestName       string             `json:"destName"`
	ApplicationSet string             `json:"applicationSet"`
	AutoSync       bool               `json:"autoSync"`
	OpPhase        string             `json:"opPhase"`
	OpMessage      string             `json:"opMessage"`
	Conditions     []ArgoConditionDTO `json:"conditions"`
	Resources      []ArgoResourceDTO  `json:"resources"`
	ReconciledUnix int64              `json:"reconciledUnix"` // 0 = never
	// The Line §3.2: every source, the compared revisions (desired, paired
	// with sync.comparedTo's sources), the deployment history newest first
	// (each entry with its own sources), and the running or last operation.
	Sources   []ArgoSourceDTO  `json:"sources,omitempty"`
	Compared  ArgoRevisionsDTO `json:"compared"`
	History   []ArgoHistoryDTO `json:"history,omitempty"`
	Operation ArgoOperationDTO `json:"operation"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

// ArgoSourceDTO is one Application source as written.
type ArgoSourceDTO struct {
	RepoURL        string `json:"repoURL"`
	Path           string `json:"path,omitempty"`
	Chart          string `json:"chart,omitempty"`
	TargetRevision string `json:"targetRevision,omitempty"`
	Ref            string `json:"ref,omitempty"`
}

// ArgoRevisionsDTO pairs revisions with their sources, index for index.
type ArgoRevisionsDTO struct {
	Sources   []ArgoSourceDTO `json:"sources,omitempty"`
	Revisions []string        `json:"revisions,omitempty"`
}

// ArgoHistoryDTO is one completed deployment.
type ArgoHistoryDTO struct {
	ID             int64 `json:"id"`
	DeployedAtUnix int64 `json:"deployedAtUnix,omitempty"`
	ArgoRevisionsDTO
}

// ArgoOperationDTO is status.operationState.
type ArgoOperationDTO struct {
	Phase          string           `json:"phase,omitempty"`
	Message        string           `json:"message,omitempty"`
	StartedAtUnix  int64            `json:"startedAtUnix,omitempty"`
	FinishedAtUnix int64            `json:"finishedAtUnix,omitempty"`
	Sync           ArgoRevisionsDTO `json:"sync"`
}

func argoSourceDTOs(ss []argo.Source) []ArgoSourceDTO {
	if len(ss) == 0 {
		return nil
	}
	out := make([]ArgoSourceDTO, 0, len(ss))
	for _, s := range ss {
		out = append(out, ArgoSourceDTO{RepoURL: urlscrub.Field(s.RepoURL).Text, Path: s.Path, Chart: s.Chart, TargetRevision: s.TargetRevision, Ref: s.Ref})
	}
	return out
}

func argoRevisionsDTO(r argo.Revisions) ArgoRevisionsDTO {
	return ArgoRevisionsDTO{Sources: argoSourceDTOs(r.Sources), Revisions: append([]string(nil), r.Revisions...)}
}

type ArgoProjectsResultDTO struct {
	Available bool             `json:"available"`
	Message   string           `json:"message,omitempty"`
	Error     string           `json:"error,omitempty"`
	Projects  []ArgoProjectDTO `json:"projects"`
}

// ArgoService is bound to JS: Application listing + the two imperative
// triggers (refresh, sync). Like every write verb it returns ActionResultDTO.
// On a protected cluster both consume the typed-confirmation grant the
// frontend ConfirmDialog arms (checkProtected) - the backend, not the dialog,
// is the gate.
type ArgoService struct {
	lookup func(string) (ArgoConn, bool)
}

func NewArgoService(lookup func(string) (ArgoConn, bool)) *ArgoService {
	return &ArgoService{lookup: lookup}
}

func (s *ArgoService) ListProjects(cluster string) ArgoProjectsResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ArgoProjectsResultDTO{Message: "cluster not connected: " + cluster, Error: "cluster not connected: " + cluster, Projects: []ArgoProjectDTO{}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), argoTimeout)
	defer cancel()
	projects, err := conn.ListArgoProjects(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ArgoProjectsResultDTO{Message: "Argo CD projects not detected (no appprojects.argoproj.io resource)", Projects: []ArgoProjectDTO{}}
		}
		return ArgoProjectsResultDTO{Message: err.Error(), Error: err.Error(), Projects: []ArgoProjectDTO{}}
	}
	out := make([]ArgoProjectDTO, 0, len(projects))
	for _, p := range projects {
		out = append(out, ArgoProjectDTO{
			Namespace:                  p.Namespace,
			Name:                       p.Name,
			Description:                p.Description,
			SourceRepos:                scrubAll(p.SourceRepos, true),
			Destinations:               scrubAll(p.Destinations, false),
			Roles:                      p.Roles,
			RoleNames:                  sliceOrEmpty(p.RoleNames),
			OrphanWarn:                 p.OrphanWarn,
			ClusterResourceWhitelist:   toArgoResourceRuleDTOs(p.ClusterResourceWhitelist),
			ClusterResourceBlacklist:   toArgoResourceRuleDTOs(p.ClusterResourceBlacklist),
			NamespaceResourceWhitelist: toArgoResourceRuleDTOs(p.NamespaceResourceWhitelist),
			NamespaceResourceBlacklist: toArgoResourceRuleDTOs(p.NamespaceResourceBlacklist),
		})
	}
	return ArgoProjectsResultDTO{Available: true, Projects: out}
}

// scrubAll applies the URL rule to each entry at the backend boundary
// (coverage S0b; Codex's implementation review finding 2): a repository URL
// as a field, a destination ("server/namespace") as text. Never nil.
func scrubAll(in []string, field bool) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if field {
			out = append(out, urlscrub.Field(v).Text)
		} else {
			out = append(out, urlscrub.Text(v).Text)
		}
	}
	return out
}

func sliceOrEmpty[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

func toArgoResourceRuleDTOs(in []argo.ResourceRule) []ArgoResourceRuleDTO {
	out := make([]ArgoResourceRuleDTO, 0, len(in))
	for _, r := range in {
		out = append(out, ArgoResourceRuleDTO{Group: r.Group, Kind: r.Kind})
	}
	return out
}

// RefreshApp triggers a re-compare against the source (refresh annotation).
// It patches the live Application - the same class of write as Flux's
// reconcile annotation - so it consumes the protected-cluster grant like
// Reconcile does (review 2026-10-03, D: it was ungated, and the grant the
// confirm dialog armed was left unconsumed for another verb to use).
// uid is the Application the verb was armed against: only it is written.
func (s *ArgoService) RefreshApp(cluster, namespace, name, uid string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), argoTimeout)
	defer cancel()
	if err := conn.RefreshArgoApp(ctx, namespace, name, uid); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// SyncApp starts a sync at the app's target revision (""=HEAD) with Argo's
// operation options. Prune/force/etc are explicit user choices from the modal
// (prune defaults OFF there and is never set implicitly - it deletes live
// resources, so it must be an opt-in per sync; the M6 "never prune" contract
// was relaxed to opt-in when the sync modal shipped).
func (s *ArgoService) SyncApp(cluster, namespace, name, uid string, opts ArgoSyncOptionsDTO) ActionResultDTO {
	// Dry-run mutates nothing; a real sync (prune/force/replace included)
	// consumes the protection grant.
	if !opts.DryRun {
		if err := checkProtected(cluster); err != nil {
			return ActionResultDTO{Error: err.Error()}
		}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), argoTimeout)
	defer cancel()
	if err := conn.SyncArgoApp(ctx, namespace, name, uid, toArgoSyncOptions(opts)); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

func toArgoSyncOptions(opts ArgoSyncOptionsDTO) argo.SyncOptions {
	out := argo.SyncOptions{
		Revision:        opts.Revision,
		Strategy:        argo.SyncStrategy(opts.Strategy),
		Prune:           opts.Prune,
		DryRun:          opts.DryRun,
		Force:           opts.Force,
		Replace:         opts.Replace,
		ServerSideApply: opts.ServerSideApply,
		Resources:       make([]argo.Resource, 0, len(opts.Resources)),
	}
	for _, r := range opts.Resources {
		out.Resources = append(out.Resources, argo.Resource{
			Group:     r.Group,
			Kind:      r.Kind,
			Namespace: r.Namespace,
			Name:      r.Name,
			Status:    r.Status,
			Health:    r.Health,
		})
	}
	return out
}

func toArgoAppDTO(a argo.App) ArgoAppDTO {
	dto := argoAppFields(a)
	dto.Rule = toRuleDTO(argo.AppRule(a))
	return dto
}

// argoAppFields is the pure field conversion: no rule (the rows converter
// copies the capture's evaluation, slice 3 §2a).
func argoAppFields(a argo.App) ArgoAppDTO {
	dto := ArgoAppDTO{
		Namespace:      a.Namespace,
		Name:           a.Name,
		Project:        a.Project,
		SyncStatus:     a.SyncStatus,
		HealthStatus:   a.HealthStatus,
		Broken:         a.Broken(),
		Revision:       a.Revision,
		RepoURL:        urlscrub.Field(a.RepoURL).Text,
		Path:           a.Path,
		Chart:          a.Chart,
		TargetRevision: a.TargetRevision,
		ExtraSources:   a.ExtraSources,
		DestNamespace:  a.DestNamespace,
		DestServer:     a.DestServer,
		DestName:       a.DestName,
		ApplicationSet: a.ApplicationSet,
		AutoSync:       a.AutoSync,
		OpPhase:        a.OpPhase,
		OpMessage:      urlscrub.Text(a.OpMessage).Text,
		Conditions:     make([]ArgoConditionDTO, 0, len(a.Conditions)),
		Resources:      make([]ArgoResourceDTO, 0, len(a.Resources)),
	}
	for _, c := range a.Conditions {
		dto.Conditions = append(dto.Conditions, ArgoConditionDTO{Type: c.Type, Message: urlscrub.Text(c.Message).Text})
	}
	for _, r := range a.Resources {
		dto.Resources = append(dto.Resources, ArgoResourceDTO{
			Group:     r.Group,
			Kind:      r.Kind,
			Namespace: r.Namespace,
			Name:      r.Name,
			Status:    r.Status,
			Health:    r.Health,
		})
	}
	if !a.ReconciledAt.IsZero() {
		dto.ReconciledUnix = a.ReconciledAt.Unix()
	}
	dto.Sources = argoSourceDTOs(a.Sources)
	dto.Compared = argoRevisionsDTO(a.Compared)
	for _, h := range a.History {
		dto.History = append(dto.History, ArgoHistoryDTO{ID: h.ID, DeployedAtUnix: unixOrZero(h.DeployedAt), ArgoRevisionsDTO: argoRevisionsDTO(h.Revisions)})
	}
	dto.Operation = ArgoOperationDTO{Phase: a.Operation.Phase, Message: urlscrub.Text(a.Operation.Message).Text,
		StartedAtUnix: unixOrZero(a.Operation.StartedAt), FinishedAtUnix: unixOrZero(a.Operation.FinishedAt), Sync: argoRevisionsDTO(a.Operation.Sync)}
	return dto
}

func toArgoAppSetDTO(a argo.ApplicationSet) ArgoApplicationSetDTO {
	dto := argoAppSetFields(a)
	dto.Rule = toRuleDTO(argo.AppSetRule(a))
	return dto
}

// argoAppSetFields is the pure field conversion (no rule).
func argoAppSetFields(a argo.ApplicationSet) ArgoApplicationSetDTO {
	dto := ArgoApplicationSetDTO{
		Namespace:      a.Namespace,
		Name:           a.Name,
		Generators:     a.Generators,
		TemplateName:   a.TemplateName,
		Project:        a.Project,
		RepoURL:        urlscrub.Field(a.RepoURL).Text,
		Path:           a.Path,
		TargetRevision: a.TargetRevision,
		DestNamespace:  a.DestNamespace,
		DestServer:     a.DestServer,
		GeneratedApps:  a.GeneratedApps,
		GeneratedNames: sliceOrEmpty(a.GeneratedNames),
		Healthy:        a.Healthy,
		Conditions:     make([]ArgoAppSetConditionDTO, 0, len(a.Conditions)),
	}
	for _, c := range a.Conditions {
		dto.Conditions = append(dto.Conditions, ArgoAppSetConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: urlscrub.Text(c.Message).Text})
	}
	return dto
}
