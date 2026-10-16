package appbridge

import (
	"sort"

	"github.com/moomora/klyx/internal/crd"
)

// CRDKindDTO is one custom-resource kind within a group.
type CRDKindDTO struct {
	Kind       string   `json:"kind"`
	Plural     string   `json:"plural"`
	Scope      string   `json:"scope"`
	Version    string   `json:"version"`
	Operator   string   `json:"operator"`
	ShortNames []string `json:"shortNames"`
}

// CRDGroupDTO is an API group with its curated category and kinds.
type CRDGroupDTO struct {
	Group    string       `json:"group"`
	Category string       `json:"category"`
	Kinds    []CRDKindDTO `json:"kinds"`
}

// CRDListDTO is the CRD browser payload. Error marks a FAILED read (cluster
// not connected, list error): an empty Groups then means "unreadable", never
// "no CRDs installed" (review 2026-10-03 #10).
type CRDListDTO struct {
	Groups []CRDGroupDTO `json:"groups"`
	Error  string        `json:"error,omitempty"`
	// Fallback: the CRD list failed with this error and Groups come from
	// discovery instead - kinds without definitions (The Line slice 15c).
	Fallback string `json:"fallback,omitempty"`
	// FallbackMissing names the groups discovery could not read ("group:
	// reason"): the fallback is partial, and says so.
	FallbackMissing []string `json:"fallbackMissing,omitempty"`
}

// CRDCountDTO is a hybrid instance count for one kind. Error marks a failed
// count (e.g. forbidden): Count is then meaningless, never a real zero.
type CRDCountDTO struct {
	Count  int    `json:"count"`
	Capped bool   `json:"capped"`
	Error  string `json:"error,omitempty"`
}

// InstanceDTO is the list view of one instance: its metadata, a hand-written
// list's fields, or the server's printer-column cells (in the page's Columns
// order).
type InstanceDTO struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Created   string            `json:"created"` // RFC3339; "" when unset
	Fields    map[string]string `json:"fields,omitempty"`
	Cells     []string          `json:"cells,omitempty"`
}

// InstanceColumnDTO is one of the server's printer columns (The Line slice
// 15a). Priority 0 is kubectl's default view; 1 and above is -o wide.
// JSONPath is set only when the server's description carries it.
type InstanceColumnDTO struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Format      string `json:"format"`
	Description string `json:"description"`
	JSONPath    string `json:"jsonPath"`
	Priority    int32  `json:"priority"`
	Numeric     bool   `json:"numeric"`
	// ConditionType is set only when the JSONPath proves the column is a
	// condition's status; a list may mark rows from it (design review 1).
	ConditionType string `json:"conditionType,omitempty"`
}

// InstancePageDTO is one page of instances plus the next continue token.
type InstancePageDTO struct {
	Items     []InstanceDTO `json:"items"`
	NextToken string        `json:"nextToken"`
	// Error marks a failed page read: empty Items then mean "unreadable", not
	// "no instances" (review 2026-10-03 #10).
	Error string `json:"error,omitempty"`
	// NotServed: the cluster does not serve this resource at all (e.g. a CRD
	// that is not installed) - a definitive zero, distinct from a failed read.
	NotServed bool `json:"notServed,omitempty"`
	// Columns are the server's printer columns when the page was read as a
	// table; absent for a hand-written list or a server without tables.
	Columns []InstanceColumnDTO `json:"columns,omitempty"`
}

// EventDTO is a describe-style event.
type EventDTO struct {
	Type     string `json:"type"` // Normal | Warning
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	Count    int    `json:"count"`
	LastSeen string `json:"lastSeen"` // RFC3339; "" when unset
}

// SecretKeyDTO carries the name and decoded byte-length of one key in a
// Secret's data map. The value itself never crosses the bridge here.
type SecretKeyDTO struct {
	Key   string `json:"key"`
	Bytes int    `json:"bytes"`
}

// RelatedRefDTO is a navigable relationship from one resource detail to another.
type RelatedRefDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Group     string `json:"group"`
	Version   string `json:"version"`
	Plural    string `json:"plural"`
	Scope     string `json:"scope"`
	Relation  string `json:"relation"`
}

// FactDTO is one salient summary value for the inspector's Summary tab - the
// data path surfaced as a readable fact instead of hiding in the YAML tab.
// AdhocResultDTO is the per-document outcome of an ad-hoc create. On dry-run the
// namespaceOwner* fields carry the prune-reach verdict for the target namespace
// (empty = no managing unit DETECTED - a heuristic negative, never "safe").
type AdhocResultDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Error     string `json:"error"`

	NamespaceOwnerKind      string `json:"namespaceOwnerKind"`
	NamespaceOwnerName      string `json:"namespaceOwnerName"`
	NamespaceOwnerNamespace string `json:"namespaceOwnerNamespace"`
	NamespacePrunes         bool   `json:"namespacePrunes"`
	// NamespaceManaged: the reaching unit manages the Namespace object itself
	// (removing it from Git while pruning deletes the namespace and this
	// object). ArgoTracking*: whether a reaching Argo app would treat the
	// object as its own - "tracked" (the controller's tracking method is known
	// and its metadata matches), "possible" (metadata matches, method unknown)
	// or "" - the only way its pruning reaches a new object (review 2026-10-03
	// #13, batch-4 R2). ArgoConfigKnown: argocd-cm's tracking method was read.
	NamespaceManaged   bool   `json:"namespaceManaged"`
	ArgoTracking       string `json:"argoTracking"`
	ArgoTrackingApp    string `json:"argoTrackingApp"`
	ArgoTrackingPrunes bool   `json:"argoTrackingPrunes"`
	ArgoPruneOption    string `json:"argoPruneOption"` // the object's Prune= sync option: "false" | "confirm" | ""
	ArgoConfigKnown    bool   `json:"argoConfigKnown"`
}

// AdhocObjectDTO is one Klyx-created scratch object (label klyx.dev/adhoc) in
// the cleanup inventory.
type AdhocObjectDTO struct {
	Kind       string `json:"kind"`
	Group      string `json:"group"`
	Version    string `json:"version"`
	Plural     string `json:"plural"`
	Scope      string `json:"scope"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	AgeSeconds int64  `json:"ageSeconds"`
}

// AdhocInventoryDTO is the scratch inventory. Unread names the kinds whose
// list failed - non-empty means the inventory is incomplete. Error is set when
// the sweep itself failed (no discovery, not connected); Objects is then
// unreadable, never "no scratch objects".
type AdhocInventoryDTO struct {
	Objects []AdhocObjectDTO `json:"objects"`
	Unread  []string         `json:"unread"`
	Error   string           `json:"error,omitempty"`
}

// GitOpsOwnershipDTO is the authoritative pre-write verdict for one target
// object: which GitOps unit OWNS it (an edit reverts on the next reconcile) and
// which unit REACHES its namespace - context, not a pruning verdict: a unit
// prunes only its own objects, and NamespaceManaged marks the one that could
// delete the namespace itself (fleet.GitOpsOwnership). Empty namespace-owner
// fields mean "no managing unit DETECTED" - a heuristic negative, never a
// safety guarantee.
type GitOpsOwnershipDTO struct {
	OwnerKind      string `json:"ownerKind"`
	OwnerNamespace string `json:"ownerNamespace"`
	OwnerName      string `json:"ownerName"`
	OwnerPrunes    bool   `json:"ownerPrunes"`
	OwnerSuspended bool   `json:"ownerSuspended"`
	Basis          string `json:"basis"`

	NamespaceOwnerKind      string `json:"namespaceOwnerKind"`
	NamespaceOwnerNamespace string `json:"namespaceOwnerNamespace"`
	NamespaceOwnerName      string `json:"namespaceOwnerName"`
	NamespacePrunes         bool   `json:"namespacePrunes"`
	NamespaceManaged        bool   `json:"namespaceManaged"`
	NamespaceBasis          string `json:"namespaceBasis"`
	// ArgoApps: every Argo Application reaching the namespace (not only the
	// namespace manager).
	ArgoApps []ArgoAppRefDTO `json:"argoApps"`

	Warnings []string `json:"warnings"`
}

// Kind "well" renders as an inset counter well, "row" as a labeled row, "meter"
// as a used/total bar (Pct drives the fill); Group is an optional section header
// ("" = headline facts).
type FactDTO struct {
	Group string  `json:"group"`
	Label string  `json:"label"`
	Value string  `json:"value"`
	Tone  string  `json:"tone"`
	Kind  string  `json:"kind"`
	Pct   float64 `json:"pct,omitempty"`
}

// InstanceDetailDTO is the full per-instance detail.
type InstanceDetailDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// UID and Generation identify the exact object read: Explain and
	// Suggest-fix are fenced by it.
	UID         string            `json:"uid"`
	Generation  int64             `json:"generation"`
	Created     string            `json:"created"` // RFC3339; "" when unset
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	Conditions  []ConditionDTO    `json:"conditions"`
	Events      []EventDTO        `json:"events"`
	YAML        string            `json:"yaml"`
	// SecretKeys is non-empty only for v1 Secrets. YAML values are masked;
	// call RevealSecretKey to fetch an individual decoded value.
	SecretKeys []SecretKeyDTO `json:"secretKeys,omitempty"`
	// ServiceBacking is non-nil only for v1 Services; omitted for everything else.
	ServiceBacking *ServiceBackingDTO `json:"serviceBacking,omitempty"`
	// HPAScaling is non-nil only for autoscaling HPAs; omitted for everything else.
	HPAScaling *HPAScalingDTO `json:"hpaScaling,omitempty"`
	// Related is populated for objects with useful direct relationships.
	Related []RelatedRefDTO `json:"related,omitempty"`
	// Facts are per-kind salient summary values (the data path) for kinds with a
	// registered builder; empty for kinds without one.
	Facts []FactDTO `json:"facts,omitempty"`
	// The Line slice 15b. Summary is the object's own table row at every
	// priority, read in the same response as the object.
	Summary []SummaryCellDTO `json:"summary,omitempty"`
	// ObservedGeneration is status.observedGeneration; -1 when none.
	ObservedGeneration int64 `json:"observedGeneration"`
	// Deleting is metadata.deletionTimestamp (RFC3339); "" when not deleting.
	Deleting   string   `json:"deleting,omitempty"`
	Finalizers []string `json:"finalizers,omitempty"`
	// Owners are metadata.ownerReferences as recorded.
	Owners []OwnerRefDTO `json:"owners,omitempty"`
	// Managers summarise managedFields: manager, operation, subresource, time.
	Managers []FieldManagerDTO `json:"managers,omitempty"`
	// FieldsJSON is the sanitised object without apiVersion, kind and
	// metadata, for the inspector's tree; "" with FieldsOmitted saying why
	// ("secret", "too large", "unreadable").
	FieldsJSON    string `json:"fieldsJSON,omitempty"`
	FieldsOmitted string `json:"fieldsOmitted,omitempty"`
	// EditEligible is false when Klyx shows this object sanitised (coverage
	// S0b): Edit is hidden, and the backend apply refuses regardless.
	// DescribeMode is "withheld" or "url-scrubbed".
	EditEligible bool   `json:"editEligible"`
	DescribeMode string `json:"describeMode,omitempty"`
	// Error is set when the detail read failed; the zero DTO is never an object.
	Error string `json:"error,omitempty"`
	// NotFound: the read failed because the object does not exist (an
	// authoritative absence, The Line slice 9 note §4.2); any other failure
	// proves nothing either way.
	NotFound bool `json:"notFound,omitempty"`
}

// SummaryCellDTO is one printer column's value for an object.
type SummaryCellDTO struct {
	InstanceColumnDTO
	Value string `json:"value"`
}

// OwnerRefDTO is one recorded owner reference.
type OwnerRefDTO struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller bool   `json:"controller,omitempty"`
}

// FieldManagerDTO is one managedFields entry without its field set.
type FieldManagerDTO struct {
	Manager     string `json:"manager"`
	Operation   string `json:"operation"`
	Subresource string `json:"subresource,omitempty"`
	Time        string `json:"time,omitempty"`
}

// RevealResultDTO is returned by RevealSecretKey. On success Value is the
// decoded plaintext and Error is "". On failure Value is "" and Error carries
// the human-readable reason. The value is not logged anywhere.
type RevealResultDTO struct {
	Value string `json:"value"`
	Error string `json:"error"`
}

// ServicePortDTO is one port from a Service's spec.ports.
type ServicePortDTO struct {
	Name     string `json:"name"`
	Port     int32  `json:"port"`
	Protocol string `json:"protocol"`
}

// EndpointAddrDTO is one address from the EndpointSlices backing a Service.
type EndpointAddrDTO struct {
	IP         string `json:"ip"`
	Ready      bool   `json:"ready"`
	TargetKind string `json:"targetKind"`
	TargetName string `json:"targetName"`
}

// ServiceBackingDTO carries endpoint health for a v1 Service. It is non-nil
// only on instance detail responses for v1 Services. The omitempty tag means
// a nil pointer is omitted from the JSON payload entirely (non-service detail
// receives no serviceBacking key at all).
type ServiceBackingDTO struct {
	Ports     []ServicePortDTO  `json:"ports"`
	Ready     int               `json:"ready"`
	NotReady  int               `json:"notReady"`
	Addresses []EndpointAddrDTO `json:"addresses"`
	Selector  map[string]string `json:"selector"`
}

// HPAMetricDTO is one spec.metrics[] entry with its matched current value.
type HPAMetricDTO struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Target  string `json:"target"`
	Current string `json:"current"` // "" when unknown
}

// HPAScalingDTO carries the scaling summary for an autoscaling
// HorizontalPodAutoscaler. It is non-nil only on instance detail responses
// for autoscaling HPAs. The omitempty tag omits it for all other kinds.
type HPAScalingDTO struct {
	MinReplicas     int32          `json:"minReplicas"`
	MaxReplicas     int32          `json:"maxReplicas"`
	CurrentReplicas int32          `json:"currentReplicas"`
	DesiredReplicas int32          `json:"desiredReplicas"`
	TargetKind      string         `json:"targetKind"`
	TargetName      string         `json:"targetName"`
	LastScaleUnix   int64          `json:"lastScaleUnix"` // 0 = never scaled
	Metrics         []HPAMetricDTO `json:"metrics"`
}

// groupCRDs groups parsed CRDs by API group, attaches the curated category, and
// sorts groups and kinds by name for a stable UI.
func groupCRDs(infos []crd.Info) []CRDGroupDTO {
	byGroup := map[string][]CRDKindDTO{}
	for _, i := range infos {
		byGroup[i.Group] = append(byGroup[i.Group], CRDKindDTO{
			Kind: i.Kind, Plural: i.Plural, Scope: i.Scope,
			Version: i.Version, Operator: i.Operator, ShortNames: i.ShortNames,
		})
	}
	out := make([]CRDGroupDTO, 0, len(byGroup))
	for group, kinds := range byGroup {
		sort.Slice(kinds, func(a, b int) bool { return kinds[a].Kind < kinds[b].Kind })
		out = append(out, CRDGroupDTO{Group: group, Category: crd.Category(group), Kinds: kinds})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Group < out[b].Group })
	return out
}

// ArgoAppRefDTO mirrors fleet.ArgoAppRef.
type ArgoAppRefDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Prunes    bool   `json:"prunes"`
}
