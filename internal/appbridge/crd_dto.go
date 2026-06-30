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

// CRDCountDTO is a hybrid instance count for one kind.
type CRDCountDTO struct {
	Count  int  `json:"count"`
	Capped bool `json:"capped"`
}

// InstanceDTO is the metadata-only view of one instance.
type InstanceDTO struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Created   string            `json:"created"` // RFC3339; "" when unset
	Fields    map[string]string `json:"fields,omitempty"`
}

// InstancePageDTO is one page of instances plus the next continue token.
type InstancePageDTO struct {
	Items     []InstanceDTO `json:"items"`
	NextToken string        `json:"nextToken"`
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

// InstanceDetailDTO is the full per-instance detail.
type InstanceDetailDTO struct {
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
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
