// Package extres reads extended resources (coverage S11a): a node's
// advertised extended resources with their vendor facts, the gap between
// capacity and allocatable, and what pods reserve on each node, without
// counting a DRA-served request against a node. Pure: no client, no clock.
package extres

import (
	"math"
	"math/big"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// The bounds (S11a §2.0).
const (
	// MaxNodeEntries is a node's kept entries; the rest are counted.
	MaxNodeEntries = 16
	// MaxFactsPerResource and MaxFactResources bound the vendor facts.
	MaxFactsPerResource = 6
	MaxFactResources    = 8
	// MaxFactValue clips a label value (a label value is at most 63 bytes).
	MaxFactValue = 63
	// MaxRowEntries is a reservations row's kept entries.
	MaxRowEntries = 16
	// MaxTracked is the distinct names a node's aggregation tracks beyond
	// the names the node advertises.
	MaxTracked = 256
	// MaxTotals is the totals part's rows.
	MaxTotals = 32
	// MaxPodEntries and MaxContainerEntries bound the pod inspector's list.
	MaxPodEntries       = 32
	MaxContainerEntries = 16
)

// DeviceClassPrefix is the implicit extended resource name of a DeviceClass
// (KEP-5004).
const DeviceClassPrefix = "deviceclass.resource.kubernetes.io/"

// IsExtended is Kubernetes' IsExtendedResourceName (pkg/apis/core/v1/helper):
// outside the kubernetes.io namespace, not "requests."-prefixed, and a
// qualified name once prefixed with "requests.". A node's names (S11a §2).
func IsExtended(name corev1.ResourceName) bool {
	s := string(name)
	if !strings.Contains(s, "/") || strings.Contains(s, corev1.ResourceDefaultNamespacePrefix) ||
		strings.HasPrefix(s, corev1.DefaultResourceRequestsPrefix) {
		return false
	}
	// qualified("requests." + s) without building it: s has a slash, and
	// "requests" is a valid DNS label, so the prefixed domain is valid when
	// the domain is and the prefix leaves it within 253 bytes
	domain, local, _ := strings.Cut(s, "/")
	return len(corev1.DefaultResourceRequestsPrefix)+len(domain) <= 253 && !strings.Contains(local, "/") && dnsSubdomain(domain) && labelName(local)
}

// qualified is Kubernetes' IsQualifiedName (content.IsLabelKey) reporting no
// error, as a byte scan: its regular expressions cost about 4us on a long
// name, which a pod naming thousands of resources multiplies on every
// evaluation (S11a §8). A fuzz test holds it equal to the upstream function.
func qualified(s string) bool {
	prefix, name, slash := strings.Cut(s, "/")
	if !slash {
		return labelName(prefix)
	}
	return !strings.Contains(name, "/") && dnsSubdomain(prefix) && labelName(name)
}

// labelName is the name part: 1 to 63 bytes of [-A-Za-z0-9_.], starting and
// ending with [A-Za-z0-9].
func labelName(s string) bool {
	if len(s) == 0 || len(s) > 63 || !alnum(s[0]) || !alnum(s[len(s)-1]) {
		return false
	}
	for i := 1; i < len(s)-1; i++ {
		if c := s[i]; !alnum(c) && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// dnsSubdomain is an RFC 1123 subdomain of at most 253 bytes: dot-separated
// labels of [-a-z0-9], each starting and ending with [a-z0-9].
func dnsSubdomain(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '.' {
			continue
		}
		label := s[start:i]
		if len(label) == 0 || !lowerAlnum(label[0]) || !lowerAlnum(label[len(label)-1]) {
			return false
		}
		for j := 1; j < len(label)-1; j++ {
			if c := label[j]; !lowerAlnum(c) && c != '-' {
				return false
			}
		}
		start = i + 1
	}
	return true
}

func lowerAlnum(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

func alnum(c byte) bool { return lowerAlnum(c) || c >= 'A' && c <= 'Z' }

// IsDRAExtended is the scheduler's IsDRAExtendedResourceName: an extended
// name or a DeviceClass's implicit name. A pod's requests (S11a §2).
func IsDRAExtended(name corev1.ResourceName) bool {
	return IsExtended(name) || strings.HasPrefix(string(name), DeviceClassPrefix)
}

// Qty is an exact quantity. OK is false when it could not be established:
// a value that is not a whole number in range, or a sum that overflowed.
type Qty struct {
	V  int64
	OK bool
}

// Known is an established quantity.
func Known(v int64) Qty { return Qty{V: v, OK: true} }

// qtyOf reads a quantity exactly (extended resources are integers).
// AsInt64 covers only the fast representation, so a large value is read
// through its decimal form: established only when it is a whole,
// non-negative number within int64.
func qtyOf(q resource.Quantity) Qty {
	if v, ok := q.AsInt64(); ok {
		if v < 0 {
			return Qty{}
		}
		return Known(v)
	}
	d := q.AsDec()
	u := new(big.Int).Set(d.UnscaledBig()) // the value is u x 10^-scale
	ten := big.NewInt(10)
	switch s := int64(d.Scale()); {
	case s < 0:
		u.Mul(u, new(big.Int).Exp(ten, big.NewInt(-s), nil))
	case s > 0:
		rem := new(big.Int)
		u.QuoRem(u, new(big.Int).Exp(ten, big.NewInt(s), nil), rem)
		if rem.Sign() != 0 {
			return Qty{}
		}
	}
	if !u.IsInt64() || u.Sign() < 0 {
		return Qty{}
	}
	return Known(u.Int64())
}

// Add sums two quantities; an overflow is not established, never a wrapped
// number.
func (a Qty) Add(b Qty) Qty {
	if !a.OK || !b.OK || (b.V > 0 && a.V > math.MaxInt64-b.V) {
		return Qty{}
	}
	return Known(a.V + b.V)
}

// max is the larger of two established quantities.
func (a Qty) max(b Qty) Qty {
	if !a.OK || !b.OK {
		return Qty{}
	}
	if b.V > a.V {
		return b
	}
	return a
}

var amdPartition = regexp.MustCompile(`^(spx|dpx|qpx|cpx)_nps(1|2|4|8)$`)

// Accelerator: a name the accelerator vendors' device plugins advertise
// (S11a §5). Any name in nvidia.com/ or amd.com/ counts, since those
// domains are the vendors' own.
func Accelerator(name string) bool {
	switch {
	case strings.HasPrefix(name, "nvidia.com/"), strings.HasPrefix(name, "amd.com/"), strings.HasPrefix(name, "habana.ai/"):
		return true
	}
	switch name {
	case "gpu.intel.com/i915", "gpu.intel.com/xe", "aws.amazon.com/neuron", "aws.amazon.com/neuroncore":
		return true
	}
	return false
}

// Unit is a resource's unit, when Klyx knows it (S11a §5): a MIG device for
// MIG profiles and for nvidia.com/gpu under MIG single, a GPU for whole
// NVIDIA and AMD GPUs, a partition for AMD's partition names. "" otherwise:
// the resource's own unit, unexplained.
func Unit(name, migStrategy string) string {
	switch {
	case strings.HasPrefix(name, "nvidia.com/mig-"):
		return "MIG device"
	case name == "nvidia.com/gpu" || name == "nvidia.com/gpu.shared":
		if migStrategy == "single" {
			return "MIG device"
		}
		return "GPU"
	case name == "amd.com/gpu":
		return "GPU"
	case strings.HasPrefix(name, "amd.com/") && amdPartition.MatchString(strings.TrimPrefix(name, "amd.com/")):
		return "partition"
	}
	return ""
}

// Partition names an AMD partition resource's modes: "CPX compute, NPS4
// memory"; "" for any other name.
func Partition(name string) string {
	m := amdPartition.FindStringSubmatch(strings.TrimPrefix(name, "amd.com/"))
	if m == nil || !strings.HasPrefix(name, "amd.com/") {
		return ""
	}
	return strings.ToUpper(m[1]) + " compute, NPS" + m[2] + " memory"
}
