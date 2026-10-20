package appbridge

import (
	"github.com/moomora/klyx/internal/urlscrub"
	"time"

	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/healthsrc"
)

// ActionResultDTO is the result of an operational write. Error carries the
// cluster's message (e.g. an RBAC 403) for display.
type ActionResultDTO struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	// Admission is the refused request's admission finding (coverage
	// S3b §5), when the server attributes the refusal.
	Admission *AdmissionFindingDTO `json:"admission,omitempty"`
	// Steps are a composite action's requests and their outcomes.
	Steps []StepDTO `json:"steps,omitempty"`
}

// FluxResourceDTO is the JSON projection of a Flux reconciliation resource.
type FluxResourceDTO struct {
	UID               string `json:"uid,omitempty"` // identity (1c slice 3 §2a)
	GVR               string `json:"gvr,omitempty"` // the served group/version/resource
	Kind              string `json:"kind"`
	Namespace         string `json:"namespace"`
	Name              string `json:"name"`
	Ready             string `json:"ready"`
	Reason            string `json:"reason"`
	Message           string `json:"message"`
	Revision          string `json:"revision"`
	ReadySinceSeconds int64  `json:"readySinceSeconds"`
	// ReadySinceUnix is the Ready condition's transition time itself (0 when
	// unknown): shared rows are pushed only when they change, so the lens
	// ticks the age from it (1c slice 3 §3).
	ReadySinceUnix int64 `json:"readySinceUnix,omitempty"`
	// Stale: the spec changed and the controller has not observed it yet -
	// Ready reflects the previous spec; the row renders as transitioning.
	Stale              bool  `json:"stale"`
	Generation         int64 `json:"generation"`
	ObservedGeneration int64 `json:"observedGeneration"`
	// ReadyGeneration is the Ready condition's own observedGeneration (0
	// when it carries none): the generation the verdict describes, which the
	// tooltips name (coverage S1b §4.1).
	ReadyGeneration int64 `json:"readyGeneration,omitempty"`
	Suspended       bool  `json:"suspended"`
	// Owner is who applies the object: the verbs' consequence line (C4).
	Owner           *FluxOpOwnerDTO `json:"owner,omitempty"`
	SourceKind      string          `json:"sourceKind"`
	SourceName      string          `json:"sourceName"`
	SourceNamespace string          `json:"sourceNamespace"`
	// LastAttemptedRevision and History are The Line's attempted and
	// deployment facts (§3.2): the revision last tried, and a HelmRelease's
	// history newest first (only a "deployed" entry is deployment evidence).
	LastAttemptedRevision string           `json:"lastAttemptedRevision,omitempty"`
	History               []HelmHistoryDTO `json:"history,omitempty"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

// HelmHistoryDTO is one HelmRelease status.history snapshot.
type HelmHistoryDTO struct {
	Version          int64  `json:"version"`
	ChartName        string `json:"chartName,omitempty"`
	ChartVersion     string `json:"chartVersion,omitempty"`
	ConfigDigest     string `json:"configDigest,omitempty"`
	Status           string `json:"status,omitempty"`
	LastDeployedUnix int64  `json:"lastDeployedUnix,omitempty"`
}

func ToFluxDTO(r flux.Resource, now time.Time) FluxResourceDTO {
	dto := fluxResourceFields(r, now)
	dto.Rule = toRuleDTO(flux.ReconciliationRule(r))
	return dto
}

// fluxResourceFields is the pure field conversion: no rule (the rows
// converter copies the capture's evaluation, slice 3 §2a).
func fluxResourceFields(r flux.Resource, now time.Time) FluxResourceDTO {
	age, since := int64(0), int64(0)
	if !r.ReadySince.IsZero() {
		age = int64(now.Sub(r.ReadySince).Seconds())
		if age < 0 {
			age = 0
		}
		since = r.ReadySince.Unix()
	}
	return FluxResourceDTO{
		Kind:                  string(r.Kind),
		Namespace:             r.Namespace,
		Name:                  r.Name,
		Ready:                 string(r.Ready),
		Reason:                r.Reason,
		Message:               urlscrub.Text(r.Message).Text,
		Revision:              r.Revision,
		ReadySinceSeconds:     age,
		ReadySinceUnix:        since,
		Stale:                 r.Stale,
		Generation:            r.Generation,
		ObservedGeneration:    r.ObservedGeneration,
		ReadyGeneration:       r.ReadyObservedGeneration,
		Suspended:             r.Suspended,
		SourceKind:            r.SourceKind,
		SourceName:            r.SourceName,
		SourceNamespace:       r.SourceNamespace,
		LastAttemptedRevision: r.LastAttemptedRevision,
		History:               helmHistoryDTOs(r.History),
		Owner:                 fluxOwnerDTO(r.Owner),
	}
}

func fluxOwnerDTO(o *flux.Owner) *FluxOpOwnerDTO {
	if o == nil {
		return nil
	}
	return &FluxOpOwnerDTO{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
}

func helmHistoryDTOs(h []flux.HelmHistory) []HelmHistoryDTO {
	if len(h) == 0 {
		return nil
	}
	out := make([]HelmHistoryDTO, 0, len(h))
	for _, e := range h {
		d := HelmHistoryDTO{Version: e.Version, ChartName: e.ChartName, ChartVersion: e.ChartVersion, ConfigDigest: e.ConfigDigest, Status: e.Status}
		if !e.LastDeployed.IsZero() {
			d.LastDeployedUnix = e.LastDeployed.Unix()
		}
		out = append(out, d)
	}
	return out
}

// GitLinkDTO is a resolved Git navigation target for a Flux resource.
type GitLinkDTO struct {
	URL        string `json:"url"`
	IsDeepLink bool   `json:"isDeepLink"`
	CopyText   string `json:"copyText"`
	// Error: a read on the way failed - the link is unverified, never "no
	// Git source".
	Error string `json:"error,omitempty"`
}

// FluxSourceDTO is the JSON projection of a Flux source object's fetch state.
type FluxSourceDTO struct {
	UID        string `json:"uid,omitempty"`
	GVR        string `json:"gvr,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Ready      string `json:"ready"`
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	Revision   string `json:"revision"`
	URL        string `json:"url"`
	Suspended  bool   `json:"suspended"`
	// RefKind/RefName are the tracked reference (branch, tag, semver, commit,
	// name or digest); ArtifactUpdatedUnix the artifact's lastUpdateTime and
	// ReadySinceUnix the Ready condition's transition time, 0 when absent.
	RefKind             string `json:"refKind,omitempty"`
	RefName             string `json:"refName,omitempty"`
	ArtifactUpdatedUnix int64  `json:"artifactUpdatedUnix,omitempty"`
	ReadySinceUnix      int64  `json:"readySinceUnix,omitempty"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

func toSourceDTO(s flux.Source) FluxSourceDTO {
	dto := sourceFields(s)
	dto.Rule = toRuleDTO(flux.SourceRule(s))
	return dto
}

// sourceFields is the pure field conversion (no rule).
func sourceFields(s flux.Source) FluxSourceDTO {
	return FluxSourceDTO{
		Kind:                string(s.Kind),
		Namespace:           s.Namespace,
		Name:                s.Name,
		Ready:               string(s.Ready),
		Reason:              s.Reason,
		Message:             urlscrub.Text(s.Message).Text,
		Revision:            s.Revision,
		URL:                 urlscrub.Field(s.URL).Text,
		Suspended:           s.Suspended,
		RefKind:             s.RefKind,
		RefName:             s.RefName,
		ArtifactUpdatedUnix: unixOrZero(s.ArtifactUpdated),
		ReadySinceUnix:      unixOrZero(s.ReadySince),
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
	// LastTransition (RFC3339) and ObservedGeneration as the condition
	// records them; ""/-1 when it records none. Set by the generic inspector
	// (The Line slice 15b).
	LastTransition     string `json:"lastTransition,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
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
	// Via says which path computed it: "cache" (built from the Git cache,
	// The Line slice 6e) or "flux CLI". Revision is the commit the cache
	// diff built; Notes say what was not applied; Changes are its
	// per-object results.
	Via      string              `json:"via,omitempty"`
	Revision string              `json:"revision,omitempty"`
	Notes    []string            `json:"notes,omitempty"`
	Changes  []FluxDiffChangeDTO `json:"changes,omitempty"`
}

// FluxDiffChangeDTO is one object's diff result: never a claim about
// delivery or cause.
type FluxDiffChangeDTO struct {
	Action    string `json:"action"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Diff      string `json:"diff,omitempty"`
	Error     string `json:"error,omitempty"`
	// OmittedChanged names sections that changed but are shown omitted
	// because they may carry secrets (coverage S0b §6).
	OmittedChanged []string `json:"omittedChanged,omitempty"`
}

// SourceProducerDTO is a bound artifact's producer as the evidence
// establishes it (S1c §2): Label and Role only for a producer whose health
// may be shown, never the artifact's own verdict.
type SourceProducerDTO struct {
	State             string `json:"state"`
	Kind              string `json:"kind,omitempty"`
	Namespace         string `json:"namespace,omitempty"`
	Name              string `json:"name,omitempty"`
	Label             string `json:"label,omitempty"`
	Role              string `json:"role,omitempty"`
	Failing           bool   `json:"failing,omitempty"`
	RecordedNamespace string `json:"recordedNamespace,omitempty"`
	RecordedName      string `json:"recordedName,omitempty"`
	RecordedGone      bool   `json:"recordedGone,omitempty"`
	RecordedLookup    string `json:"recordedLookup,omitempty"`
}

func sourceProducerDTO(p *healthsrc.ProducerView) *SourceProducerDTO {
	return &SourceProducerDTO{State: p.State, Kind: p.Kind, Namespace: p.Namespace, Name: p.Name, Label: p.Label, Role: p.Role,
		Failing: p.GeneratorFailing, RecordedNamespace: p.RecordedNamespace, RecordedName: p.RecordedName, RecordedGone: p.RecordedGone, RecordedLookup: p.RecordedLookup}
}

type ResourceDetailDTO struct {
	// UID, Generation and AsOfUnix: the read object's identity and when it was
	// read (design §5b) - the lens shows a detail only for the selected
	// row's UID. Error: the read failed; the DTO then carries only the
	// requested kind/namespace/name. SourceWarning: the bound source could
	// not be read (not found, or unverified) - never a silent gap.
	UID           string `json:"uid,omitempty"`
	Generation    int64  `json:"generation,omitempty"`
	AsOfUnix      int64  `json:"asOfUnix,omitempty"`
	Error         string `json:"error,omitempty"`
	SourceWarning string `json:"sourceWarning,omitempty"`
	// Coverage S1c §2: a bound ExternalArtifact's producer, its own line
	// after the artifact's verdict; SourceNote names a feature-gate refusal.
	SourceProducer    *SourceProducerDTO  `json:"sourceProducer,omitempty"`
	SourceNote        string              `json:"sourceNote,omitempty"`
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
		out.Conditions = append(out.Conditions, ConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: urlscrub.Text(c.Message).Text})
	}
	for _, e := range d.Inventory {
		out.Inventory = append(out.Inventory, InventoryEntryDTO{Group: e.Group, Version: e.Version, Kind: e.Kind, Namespace: e.Namespace, Name: e.Name})
	}
	for _, dep := range d.DependsOn {
		out.DependsOn = append(out.DependsOn, DependencyRefDTO{Namespace: dep.Namespace, Name: dep.Name})
	}
	return out
}
