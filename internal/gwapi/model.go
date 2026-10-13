// Package gwapi parses Gateway API objects (read as unstructured) into a
// vocabulary-correct topology: Gateway -> HTTPRoute -> Service -> Pods, with
// per-Gateway-scoped route status. Pure: no client-go dependency beyond
// unstructured. Policy attachment is M5-b (the Policies fields stay empty here).
package gwapi

// Topology is the per-Gateway data path.
type Topology struct {
	Gateway         GatewayNode
	Routes          []RouteNode // one lane each
	ClusterPolicies []PolicyRef // broad/empty CCNPs - header context, not per-service (M5-b-ii)
	Warnings        []string    // soft, non-fatal issues (filled by the fleet layer)

	// RoutesComplete is true only when the HTTPRoute list succeeded: an
	// absent route is then proven not attached. RoutesError names the
	// failure otherwise (Routes is empty, never "no routes").
	RoutesComplete bool
	RoutesError    string
}

// Cond is one status condition as observed. Status is "True", "False",
// "Unknown", or "" when no controller has written the condition - a boolean
// cannot tell a rejection from a status nobody has reported yet.
// ObservedGeneration is the generation the condition describes.
type Cond struct {
	Status, Reason, Message string
	ObservedGeneration      int64
}

type GatewayNode struct {
	Namespace, Name, ClassName string
	Listeners                  []Listener
	Addresses                  []GatewayAddress
	Accepted, Programmed       bool
	Policies                   []PolicyRef // M5-b; empty in M5-a

	AcceptedCond, ProgrammedCond Cond
	Generation                   int64
}

type RouteNode struct {
	Namespace, Name        string
	Hostnames              []string
	Matches                []Match
	ParentSectionName      string
	Accepted, ResolvedRefs bool // scoped to THIS Gateway's parentRef
	Backends               []Backend
	Policies               []PolicyRef   // M5-b; empty in M5-a
	Services               []ServiceNode // resolved Service backends; lane shows primary
	Pods                   PodCount      // for the first RESOLVED Service backend (legacy lane)

	// ParentFound reports a status.parents entry for THIS Gateway (and
	// listener); without one the conditions below are absent, not False.
	ParentFound                    bool
	AcceptedCond, ResolvedRefsCond Cond
	Generation                     int64
}

type ServiceNode struct {
	Namespace, Name, Type string
	Port                  int32
	Selector              map[string]string // svc spec.selector; internal, for CNP label matching
	Global                bool              // Cilium global service (service.cilium.io/global=true)
	Policies              []PolicyRef       // M5-b-i: precise (BackendTLSPolicy)
	CNPs                  []PolicyRef       // M5-b-ii: inferred Cilium; empty here
	Resolved              bool              // false when the Service could not be read
}

type Listener struct {
	Name, Protocol, Hostname string
	Port                     int32
	CertificateRefs          []CertificateRef
}
type GatewayAddress struct{ Type, Value string }
type CertificateRef struct {
	Group, Kind, Namespace, Name string
}
type Match struct{ PathType, PathValue, Method string }
type Backend struct {
	Kind, Name, Namespace string
	Port, Weight          int32
	// WeightSet: the backendRef wrote a weight. Unset means the Gateway API
	// default of 1; an explicit 0 sends this backend no traffic.
	WeightSet bool
	// Rule is the index of the spec.rules entry that lists this backend.
	// Weights split traffic within one rule's backendRefs only.
	Rule int

	// Filled by the fleet layer, per backend, so a lane is diagnosed from
	// its own backend's evidence.
	State     BackendState
	Error     string         // why State is unreadable
	Endpoints *EndpointCount // resolved Service backends only
}

// CarriesTraffic: the backend receives a share of the rule's requests - its
// weight is unset (default 1) or positive.
func (b Backend) CarriesTraffic() bool { return !b.WeightSet || b.Weight > 0 }

// BackendState is what Klyx could establish about one backendRef.
type BackendState string

const (
	BackendResolved    BackendState = "resolved"    // the Service was read
	BackendNotFound    BackendState = "not-found"   // the API said NotFound
	BackendUnreadable  BackendState = "unreadable"  // forbidden, timeout, ... - existence unknown
	BackendUnsupported BackendState = "unsupported" // not a core Service; Klyx does not evaluate it
)

// EndpointCount is a Service's EndpointSlice endpoints - endpoints, not
// pods: an endpoint may have no pod TargetRef (NonPod counts those).
// Error is set when the slices could not be read (the counts are then 0).
type EndpointCount struct {
	Ready, Total, NonPod int
	Error                string
}
type PodCount struct {
	Ready, Total int
	Unknown      bool // EndpointSlices were unavailable
}
type PolicyRef struct {
	Kind, Namespace, Name string

	// Target metadata - first-class, NOT encoded in Details.
	TargetKind, TargetNamespace, TargetName, TargetSectionName string

	Summary  string          // chip text: feature presence only, never values
	Details  []PolicyDetail  // panel/tooltip rows: decoded values, deterministic order
	Inferred bool            // false for all M5-b-i Envoy policies; reserved for Cilium (M5-b-ii)
	Match    PolicyMatchKind // empty for precise policies; set for inferred Cilium policies
}

// PolicyMatchKind describes HOW an inferred policy matched. An expressions-only policy
// never produces a PolicyRef (warned + skipped), so there is no "matchExpressions" value.
type PolicyMatchKind string

const (
	MatchSelector      PolicyMatchKind = "selector"
	MatchNamespaceWide PolicyMatchKind = "namespace-wide"
	MatchClusterWide   PolicyMatchKind = "cluster-wide"
)

// PolicyDetail is one decoded key/value row (e.g. "retries" -> "3").
type PolicyDetail struct{ Key, Value string }

// PolicyDecode is what a per-kind decoder returns.
type PolicyDecode struct {
	Summary string
	Details []PolicyDetail
}

// TargetRef is a policy's targetRef (Namespace holds the raw value; empty until
// resolved by BuildPolicyRefs, which defaults it to the policy's namespace).
type TargetRef struct{ Group, Kind, Namespace, Name, SectionName string }
type GatewayRef struct {
	Namespace, Name, ClassName string
	Accepted, Programmed       bool
	Addresses                  []GatewayAddress
	Listeners                  []Listener
	// Stale reports that the spec changed and the controller has not observed
	// it yet (the Programmed condition's observedGeneration lags
	// metadata.generation). While stale, Accepted/Programmed reflect the
	// PREVIOUS spec - the UI must present the row as transitioning.
	Stale              bool
	Generation         int64
	ObservedGeneration int64

	AcceptedCond, ProgrammedCond Cond
}
