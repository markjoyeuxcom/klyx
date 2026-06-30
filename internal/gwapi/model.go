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
}

type GatewayNode struct {
	Namespace, Name, ClassName string
	Listeners                  []Listener
	Addresses                  []GatewayAddress
	Accepted, Programmed       bool
	Policies                   []PolicyRef // M5-b; empty in M5-a
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
	Pods                   PodCount      // for the primary Service backend
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
}
