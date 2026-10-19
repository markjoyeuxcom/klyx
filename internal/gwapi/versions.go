package gwapi

// Group is the Gateway API's group. GroupExperimental is the experimental
// channel's own group (S7 §7): recognised, never read.
const (
	Group             = "gateway.networking.k8s.io"
	GroupExperimental = "gateway.networking.x-k8s.io"
)

// versions are each resource's read candidates, in order (S7 §2): the
// first one served is read, never the group's preferred version, which
// says nothing about one resource (the experimental channel serves
// tlsroutes at v1 and at both alphas, and v1.4 served it at alphas only).
// The fields Klyx reads have one shape across a resource's candidates.
var versions = map[string][]string{
	"gatewayclasses":  {"v1", "v1beta1"},
	"gateways":        {"v1", "v1beta1"},
	"httproutes":      {"v1", "v1beta1"},
	"grpcroutes":      {"v1"},
	"listenersets":    {"v1"},
	"tlsroutes":       {"v1", "v1alpha3", "v1alpha2"},
	"tcproutes":       {"v1", "v1alpha2"},
	"udproutes":       {"v1", "v1alpha2"},
	"referencegrants": {"v1", "v1beta1"},
}

// Versions returns a resource's candidates, in order: nil for a resource
// Klyx does not read.
func Versions(resource string) []string {
	return append([]string(nil), versions[resource]...)
}

// IsCandidate reports whether version is one of resource's candidates.
func IsCandidate(resource, version string) bool {
	for _, v := range versions[resource] {
		if v == version {
			return true
		}
	}
	return false
}
