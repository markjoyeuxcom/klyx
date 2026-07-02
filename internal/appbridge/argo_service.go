package appbridge

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/moomora/klyx/internal/gitops/argo"
)

const argoTimeout = 30 * time.Second

// ArgoConn is the per-cluster surface ArgoService needs (lookup seam).
type ArgoConn interface {
	ListArgoApps(ctx context.Context) ([]argo.App, error)
	ListArgoProjects(ctx context.Context) ([]argo.Project, error)
	ListArgoApplicationSets(ctx context.Context) ([]argo.ApplicationSet, error)
	RefreshArgoApp(ctx context.Context, namespace, name string) error
	SyncArgoApp(ctx context.Context, namespace, name string, opts argo.SyncOptions) error
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
}

// ArgoAppDTO is one Argo CD Application in Argo's own vocabulary.
type ArgoAppDTO struct {
	Namespace      string             `json:"namespace"`
	Name           string             `json:"name"`
	Project        string             `json:"project"`
	SyncStatus     string             `json:"syncStatus"`   // Synced | OutOfSync | Unknown
	HealthStatus   string             `json:"healthStatus"` // Healthy | Progressing | Degraded | Suspended | Missing | Unknown
	Broken         bool               `json:"broken"`
	Revision       string             `json:"revision"`
	RepoURL        string             `json:"repoURL"`
	Path           string             `json:"path"`
	Chart          string             `json:"chart"`
	TargetRevision string             `json:"targetRevision"`
	ExtraSources   int                `json:"extraSources"`
	DestNamespace  string             `json:"destNamespace"`
	ApplicationSet string             `json:"applicationSet"`
	AutoSync       bool               `json:"autoSync"`
	OpPhase        string             `json:"opPhase"`
	OpMessage      string             `json:"opMessage"`
	Conditions     []ArgoConditionDTO `json:"conditions"`
	Resources      []ArgoResourceDTO  `json:"resources"`
	ReconciledUnix int64              `json:"reconciledUnix"` // 0 = never
}

// ArgoResultDTO is the list payload. Available=false with Message covers both
// "Argo CD not installed" and a failed list - never an empty list pretending
// to be a healthy zero.
type ArgoResultDTO struct {
	Available bool         `json:"available"`
	Message   string       `json:"message,omitempty"`
	Apps      []ArgoAppDTO `json:"apps"`
}

type ArgoProjectsResultDTO struct {
	Available bool             `json:"available"`
	Message   string           `json:"message,omitempty"`
	Projects  []ArgoProjectDTO `json:"projects"`
}

type ArgoApplicationSetsResultDTO struct {
	Available       bool                    `json:"available"`
	Message         string                  `json:"message,omitempty"`
	ApplicationSets []ArgoApplicationSetDTO `json:"applicationSets"`
}

// ArgoService is bound to JS: Application listing + the two imperative
// triggers (refresh, sync). Like every write verb it returns ActionResultDTO;
// Protected gating happens in the frontend ConfirmDialog.
type ArgoService struct {
	lookup func(string) (ArgoConn, bool)
}

func NewArgoService(lookup func(string) (ArgoConn, bool)) *ArgoService {
	return &ArgoService{lookup: lookup}
}

// ListApplications returns every Application on the cluster, broken-first.
func (s *ArgoService) ListApplications(cluster string) ArgoResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ArgoResultDTO{Message: "cluster not connected: " + cluster, Apps: []ArgoAppDTO{}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), argoTimeout)
	defer cancel()
	apps, err := conn.ListArgoApps(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ArgoResultDTO{Message: "Argo CD not detected (no applications.argoproj.io resource)", Apps: []ArgoAppDTO{}}
		}
		return ArgoResultDTO{Message: err.Error(), Apps: []ArgoAppDTO{}}
	}
	out := make([]ArgoAppDTO, 0, len(apps))
	for _, a := range apps {
		dto := ArgoAppDTO{
			Namespace:      a.Namespace,
			Name:           a.Name,
			Project:        a.Project,
			SyncStatus:     a.SyncStatus,
			HealthStatus:   a.HealthStatus,
			Broken:         a.Broken(),
			Revision:       a.Revision,
			RepoURL:        a.RepoURL,
			Path:           a.Path,
			Chart:          a.Chart,
			TargetRevision: a.TargetRevision,
			ExtraSources:   a.ExtraSources,
			DestNamespace:  a.DestNamespace,
			ApplicationSet: a.ApplicationSet,
			AutoSync:       a.AutoSync,
			OpPhase:        a.OpPhase,
			OpMessage:      a.OpMessage,
			Conditions:     make([]ArgoConditionDTO, 0, len(a.Conditions)),
			Resources:      make([]ArgoResourceDTO, 0, len(a.Resources)),
		}
		for _, c := range a.Conditions {
			dto.Conditions = append(dto.Conditions, ArgoConditionDTO{Type: c.Type, Message: c.Message})
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
		out = append(out, dto)
	}
	return ArgoResultDTO{Available: true, Apps: out}
}

func (s *ArgoService) ListProjects(cluster string) ArgoProjectsResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ArgoProjectsResultDTO{Message: "cluster not connected: " + cluster, Projects: []ArgoProjectDTO{}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), argoTimeout)
	defer cancel()
	projects, err := conn.ListArgoProjects(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ArgoProjectsResultDTO{Message: "Argo CD projects not detected (no appprojects.argoproj.io resource)", Projects: []ArgoProjectDTO{}}
		}
		return ArgoProjectsResultDTO{Message: err.Error(), Projects: []ArgoProjectDTO{}}
	}
	out := make([]ArgoProjectDTO, 0, len(projects))
	for _, p := range projects {
		out = append(out, ArgoProjectDTO{
			Namespace:                  p.Namespace,
			Name:                       p.Name,
			Description:                p.Description,
			SourceRepos:                sliceOrEmpty(p.SourceRepos),
			Destinations:               sliceOrEmpty(p.Destinations),
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

func (s *ArgoService) ListApplicationSets(cluster string) ArgoApplicationSetsResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ArgoApplicationSetsResultDTO{Message: "cluster not connected: " + cluster, ApplicationSets: []ArgoApplicationSetDTO{}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), argoTimeout)
	defer cancel()
	sets, err := conn.ListArgoApplicationSets(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ArgoApplicationSetsResultDTO{Message: "Argo CD ApplicationSets not detected (no applicationsets.argoproj.io resource)", ApplicationSets: []ArgoApplicationSetDTO{}}
		}
		return ArgoApplicationSetsResultDTO{Message: err.Error(), ApplicationSets: []ArgoApplicationSetDTO{}}
	}
	out := make([]ArgoApplicationSetDTO, 0, len(sets))
	for _, a := range sets {
		dto := ArgoApplicationSetDTO{
			Namespace:      a.Namespace,
			Name:           a.Name,
			Generators:     a.Generators,
			TemplateName:   a.TemplateName,
			Project:        a.Project,
			RepoURL:        a.RepoURL,
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
			dto.Conditions = append(dto.Conditions, ArgoAppSetConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message})
		}
		out = append(out, dto)
	}
	return ArgoApplicationSetsResultDTO{Available: true, ApplicationSets: out}
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
func (s *ArgoService) RefreshApp(cluster, namespace, name string) ActionResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), argoTimeout)
	defer cancel()
	if err := conn.RefreshArgoApp(ctx, namespace, name); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// SyncApp starts a sync at the app's target revision (""=HEAD) with Argo's
// operation options. Prune/force/etc are explicit user choices from the modal
// (prune defaults OFF there and is never set implicitly - it deletes live
// resources, so it must be an opt-in per sync; the M6 "never prune" contract
// was relaxed to opt-in when the sync modal shipped).
func (s *ArgoService) SyncApp(cluster, namespace, name string, opts ArgoSyncOptionsDTO) ActionResultDTO {
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
	if err := conn.SyncArgoApp(ctx, namespace, name, toArgoSyncOptions(opts)); err != nil {
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
