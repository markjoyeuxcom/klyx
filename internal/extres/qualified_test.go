package extres

import (
	"math/rand/v2"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// qualified must answer exactly as Kubernetes' IsQualifiedName (S11a §8).

func upstreamQualified(s string) bool { return len(validation.IsQualifiedName(s)) == 0 }

// upstreamExtended is Kubernetes' IsExtendedResourceName for a name outside
// the native set, with its qualified-name check as upstream builds it.
func upstreamExtended(s string) bool {
	return strings.Contains(s, "/") && !strings.Contains(s, corev1.ResourceDefaultNamespacePrefix) &&
		!strings.HasPrefix(s, corev1.DefaultResourceRequestsPrefix) && upstreamQualified(corev1.DefaultResourceRequestsPrefix+s)
}

func agree(t *testing.T, s string) {
	t.Helper()
	if got, want := qualified(s), upstreamQualified(s); got != want {
		t.Fatalf("qualified(%q) = %v, upstream %v", s, got, want)
	}
	if got, want := IsExtended(corev1.ResourceName(s)), upstreamExtended(s); got != want {
		t.Fatalf("IsExtended(%q) = %v, upstream %v", s, got, want)
	}
}

var qualifiedSeeds = []string{
	"", "a", "A", "-", "a-", "-a", "a_b", "a.b", "a..b", "_a", "a_", "nvidia.com/gpu", "requests.nvidia.com/gpu",
	"/gpu", "nvidia.com/", "a/b/c", "Nvidia.com/gpu", "nvidia.com/GPU", "nvidia..com/gpu", ".nvidia.com/gpu", "nvidia.com./gpu",
	"nvidia-.com/gpu", "-nvidia.com/gpu", "nvidia.com/-gpu", "nvidia.com/gpu-", "nvidia.com/g.p_u-1", "1.2.3/4",
	"é.com/gpu", "nvidia.com/gpü", "nvidia.com/gpu\n", "nvidia.com/\xff", "a\x00/b",
	strings.Repeat("a", 63), strings.Repeat("a", 64), "x/" + strings.Repeat("a", 63), "x/" + strings.Repeat("a", 64),
	strings.Repeat("a", 253) + "/b", strings.Repeat("a", 254) + "/b", strings.Repeat("a.", 126) + "a/b", strings.Repeat("a.", 127) + "a/b",
	strings.Repeat("a", 244) + "/b", strings.Repeat("a", 245) + "/b", "kubernetes.io/x", "x.kubernetes.io/y", "requests.x/y", "deviceclass.resource.kubernetes.io/gpu",
}

func TestQualifiedEqualsUpstream(t *testing.T) {
	for _, s := range qualifiedSeeds {
		agree(t, s)
	}
	// random strings over the alphabet the rules distinguish, at every
	// length around the limits
	const alphabet = "aZ09-_./é\n"
	rng := rand.New(rand.NewPCG(11, 1))
	runes := []rune(alphabet)
	for i := 0; i < 200000; i++ {
		n := rng.IntN(12)
		if i%50 == 0 {
			n = 240 + rng.IntN(80)
		}
		var b strings.Builder
		for j := 0; j < n; j++ {
			// mostly letters, so long valid names occur
			if rng.IntN(4) > 0 {
				b.WriteByte("az09"[rng.IntN(4)])
			} else {
				b.WriteRune(runes[rng.IntN(len(runes))])
			}
		}
		agree(t, b.String())
	}
}

func FuzzQualified(f *testing.F) {
	for _, s := range qualifiedSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { agree(t, s) })
}
