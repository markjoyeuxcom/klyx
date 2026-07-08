package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/gitops/flux"
)

// ActionResultDTO is the result of an operational write. Error carries the
// cluster's message (e.g. an RBAC 403) for display.
type ActionResultDTO struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// FluxResourceDTO is the JSON projection of a Flux reconciliation resource.
type FluxResourceDTO struct {
	Kind              string `json:"kind"`
	Namespace         string `json:"namespace"`
	Name              string `json:"name"`
	Ready             string `json:"ready"`
	Reason            string `json:"reason"`
	Message           string `json:"message"`
	Revision          string `json:"revision"`
	ReadySinceSeconds int64  `json:"readySinceSeconds"`
	// Stale: the spec changed and the controller has not observed it yet -
	// Ready reflects the previous spec; the row renders as transitioning.
	Stale              bool   `json:"stale"`
	Generation         int64  `json:"generation"`
	ObservedGeneration int64  `json:"observedGeneration"`
	Suspended          bool   `json:"suspended"`
	SourceKind         string `json:"sourceKind"`
	SourceName         string `json:"sourceName"`
}

func ToFluxDTO(r flux.Resource, now time.Time) FluxResourceDTO {
	age := int64(0)
	if !r.ReadySince.IsZero() {
		age = int64(now.Sub(r.ReadySince).Seconds())
		if age < 0 {
			age = 0
		}
	}
	return FluxResourceDTO{
		Kind:               string(r.Kind),
		Namespace:          r.Namespace,
		Name:               r.Name,
		Ready:              string(r.Ready),
		Reason:             r.Reason,
		Message:            r.Message,
		Revision:           r.Revision,
		ReadySinceSeconds:  age,
		Stale:              r.Stale,
		Generation:         r.Generation,
		ObservedGeneration: r.ObservedGeneration,
		Suspended:          r.Suspended,
		SourceKind:         r.SourceKind,
		SourceName:         r.SourceName,
	}
}

// GitLinkDTO is a resolved Git navigation target for a Flux resource.
type GitLinkDTO struct {
	URL        string `json:"url"`
	IsDeepLink bool   `json:"isDeepLink"`
	CopyText   string `json:"copyText"`
}

// FluxSourceDTO is the JSON projection of a Flux source object's fetch state.
type FluxSourceDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ready     string `json:"ready"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Revision  string `json:"revision"`
	URL       string `json:"url"`
	Suspended bool   `json:"suspended"`
}

func toSourceDTO(s flux.Source) FluxSourceDTO {
	return FluxSourceDTO{
		Kind:      string(s.Kind),
		Namespace: s.Namespace,
		Name:      s.Name,
		Ready:     string(s.Ready),
		Reason:    s.Reason,
		Message:   s.Message,
		Revision:  s.Revision,
		URL:       s.URL,
		Suspended: s.Suspended,
	}
}

type ConditionDTO struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// Scope attributes a nested per-sub-object condition (e.g. "parent envoy-gw/eg",
	// "listener https"); "" for a standard status.conditions[] entry. Populated by
	// the generic inspector for Gateway API kinds; empty everywhere else.
	Scope string `json:"scope,omitempty"`
}

type InventoryEntryDTO struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type DependencyRefDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// FluxDiffDTO is the result of an on-demand `flux diff` (M10-f). Available is
// false when the flux CLI isn't installed (the UI hides the affordance).
type FluxDiffDTO struct {
	Available  bool   `json:"available"`
	HasChanges bool   `json:"hasChanges"`
	Output     string `json:"output"`
	Error      string `json:"error"`
}

type ResourceDetailDTO struct {
	Kind              string              `json:"kind"`
	Namespace         string              `json:"namespace"`
	Name              string              `json:"name"`
	Suspended         bool                `json:"suspended"`
	Reason            string              `json:"reason"`
	AppliedRevision   string              `json:"appliedRevision"`
	AttemptedRevision string              `json:"attemptedRevision"`
	ApplyFailed       bool                `json:"applyFailed"`
	Conditions        []ConditionDTO      `json:"conditions"`
	Inventory         []InventoryEntryDTO `json:"inventory"`
	Source            *FluxSourceDTO      `json:"source"`
	DependsOn         []DependencyRefDTO  `json:"dependsOn"`
	Events            []EventRowDTO       `json:"events"`
}

func toDetailDTO(d flux.Detail) ResourceDetailDTO {
	out := ResourceDetailDTO{
		Kind:              string(d.Kind),
		Namespace:         d.Namespace,
		Name:              d.Name,
		Suspended:         d.Suspended,
		Reason:            d.Reason,
		AppliedRevision:   d.AppliedRevision,
		AttemptedRevision: d.AttemptedRevision,
		// The attempted-vs-applied apply-failure signal is only valid for
		// Kustomization: both fields exist and share the same revision format.
		// HelmRelease v2 has no lastAppliedRevision (only lastAttemptedRevision),
		// so this heuristic would always false-positive; its failure state is
		// conveyed by the Ready/Released conditions instead.
		ApplyFailed: d.Kind == flux.KustomizationKind && d.AttemptedRevision != "" && d.AttemptedRevision != d.AppliedRevision,
	}
	for _, c := range d.Conditions {
		out.Conditions = append(out.Conditions, ConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message})
	}
	for _, e := range d.Inventory {
		out.Inventory = append(out.Inventory, InventoryEntryDTO{Group: e.Group, Version: e.Version, Kind: e.Kind, Namespace: e.Namespace, Name: e.Name})
	}
	for _, dep := range d.DependsOn {
		out.DependsOn = append(out.DependsOn, DependencyRefDTO{Namespace: dep.Namespace, Name: dep.Name})
	}
	return out
}
