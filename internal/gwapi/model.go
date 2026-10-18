// Package gwapi parses Gateway API objects (read as unstructured) into a
// vocabulary-correct topology: Gateway -> HTTPRoute -> Service -> Pods, with
// per-Gateway-scoped route status. Pure: no client-go dependency beyond
// unstructured. Policy attachment is M5-b (the Policies fields stay empty here).
package gwapi

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
// ObservedGeneration is the generation the condition describes;
// LastTransitionTime is when its status last changed (zero when absent).
type Cond struct {
	Status, Reason, Message string
	ObservedGeneration      int64
	LastTransitionTime      time.Time
}

type GatewayNode struct {
	Namespace, Name, ClassName string
	Listeners                  []Listener
	Addresses                  []GatewayAddress
	Accepted, Programmed       bool
	Policies                   []PolicyRef // M5-b; empty in M5-a
	// AllowedListeners is spec.allowedListeners.namespaces as written (S7
	// §4); AttachedListenerSets the controller's count, nil when unreported.
	AllowedListeners     NamespaceAllowance
	AttachedListenerSets *int32

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
	// RuleCount is len(spec.rules): a rule with no backendRefs (a direct
	// response or a redirect) is still a rule.
	RuleCount int
	// UID and Fingerprints (by rule index) identify what was read, so a
	// reading taken apart from this snapshot can be checked against it.
	UID          string
	Fingerprints []string
	Policies     []PolicyRef   // M5-b; empty in M5-a
	Services     []ServiceNode // resolved Service backends; lane shows primary
	Pods         PodCount      // for the first RESOLVED Service backend (legacy lane)

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
	// TLSMode is spec tls.mode as written: "" when unset (the API defaults
	// it to Terminate).
	TLSMode string
	// AllowedRoutes is the listener's allowedRoutes as written (S7 §3):
	// nil when unset, where the API's default is routes from the same
	// namespace, of the protocol's kinds.
	AllowedRoutes *AllowedRoutes
	// Status is the controller's status.listeners entry of this name (S7
	// §3): nil when none was reported.
	Status *ListenerStatus
}

// AllowedRoutes is a listener's allowedRoutes: which namespaces and kinds
// of routes may attach.
type AllowedRoutes struct {
	Namespaces NamespaceAllowance
	Kinds      []RouteKind // as written: empty means the protocol's kinds
}

// NamespaceAllowance is a namespaces field as written - a listener's
// allowedRoutes.namespaces or a Gateway's allowedListeners.namespaces.
// Set: the field was written at all. From is as written ("" when unset);
// the field's own default applies (Same for routes, None for ListenerSets).
// SelectorInvalid: a selector was written that Klyx could not read.
type NamespaceAllowance struct {
	Set             bool
	From            string
	Selector        *metav1.LabelSelector
	SelectorInvalid bool
}

// RouteKind is one route group and kind; Group as written ("" is the
// Gateway API group by the API's default).
type RouteKind struct{ Group, Kind string }

// ListenerStatus is one listener's status as its controller reported it:
// the route kinds it supports, how many routes attached, and every
// condition in the order reported.
type ListenerStatus struct {
	SupportedKinds []RouteKind
	AttachedRoutes int32
	Conditions     []TypedCond
}

// TypedCond is one condition with its type.
type TypedCond struct {
	Type string
	Cond
}

// Of returns the condition of a type ("" status when absent).
func (s *ListenerStatus) Of(condType string) Cond {
	if s == nil {
		return Cond{}
	}
	for _, c := range s.Conditions {
		if c.Type == condType {
			return c.Cond
		}
	}
	return Cond{}
}

type GatewayAddress struct{ Type, Value string }
type CertificateRef struct {
	Group, Kind, Namespace, Name string
}

// Match is one rule match; Rule is the index of the spec.rules entry it
// belongs to.
type Match struct {
	PathType, PathValue, Method string
	Rule                        int
	// GRPCRoute's method match (S7b §5): service and method, either empty
	// when the match leaves it open.
	GRPCService, GRPCMethod string
}
type Backend struct {
	Group, Kind, Name, Namespace string
	Port, Weight                 int32
	// Transport is the protocol the route reaches it over (S7b §5): UDP
	// for a UDPRoute, TCP otherwise; "" before resolution.
	Transport string
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
	// BackendNoPort: the Service was read (current) and has no port of the
	// backendRef's number for the route's transport (S7b §5).
	BackendNoPort BackendState = "no-port"
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
	// AllowedListeners and AttachedListenerSets as GatewayNode's (S7 §4).
	AllowedListeners     NamespaceAllowance
	AttachedListenerSets *int32
	// Stale reports that the spec changed and the controller has not observed
	// it yet (the Programmed condition's observedGeneration lags
	// metadata.generation). While stale, Accepted/Programmed reflect the
	// PREVIOUS spec - the UI must present the row as transitioning.
	Stale              bool
	Generation         int64
	ObservedGeneration int64

	AcceptedCond, ProgrammedCond Cond
}
