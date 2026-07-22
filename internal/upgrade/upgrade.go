// Package upgrade holds the pure logic of the upgrade-readiness lens: the
// curated deprecated-API table, upstream EOL dates, and version-skew findings.
// The fleet layer supplies live discovery/count data; nothing here talks to a
// cluster.
package upgrade

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DeprecatedAPI is one API version that is (or will be) removed. The table is
// curated, not exhaustive: it carries the versions a running fleet in the
// 1.25+ era can still realistically serve. Extend as releases ship.
type DeprecatedAPI struct {
	Group          string
	Version        string
	Resource       string // lowercase plural
	Kind           string
	RemovedInMinor int    // Kubernetes minor that removes the API (0 = deprecated, removal unscheduled)
	ReplacedBy     string // the API to migrate to, "" when there is none
}

// Table is the curated deprecated/removed API set, oldest removals first.
var Table = []DeprecatedAPI{
	{"policy", "v1beta1", "podsecuritypolicies", "PodSecurityPolicy", 25, "Pod Security Admission"},
	{"batch", "v1beta1", "cronjobs", "CronJob", 25, "batch/v1 CronJob"},
	{"discovery.k8s.io", "v1beta1", "endpointslices", "EndpointSlice", 25, "discovery.k8s.io/v1 EndpointSlice"},
	{"events.k8s.io", "v1beta1", "events", "Event", 25, "events.k8s.io/v1 Event"},
	{"autoscaling", "v2beta1", "horizontalpodautoscalers", "HorizontalPodAutoscaler", 25, "autoscaling/v2 HorizontalPodAutoscaler"},
	{"node.k8s.io", "v1beta1", "runtimeclasses", "RuntimeClass", 25, "node.k8s.io/v1 RuntimeClass"},
	{"policy", "v1beta1", "poddisruptionbudgets", "PodDisruptionBudget", 25, "policy/v1 PodDisruptionBudget"},
	{"autoscaling", "v2beta2", "horizontalpodautoscalers", "HorizontalPodAutoscaler", 26, "autoscaling/v2 HorizontalPodAutoscaler"},
	{"flowcontrol.apiserver.k8s.io", "v1beta1", "flowschemas", "FlowSchema", 26, "flowcontrol.apiserver.k8s.io/v1 FlowSchema"},
	{"storage.k8s.io", "v1beta1", "csistoragecapacities", "CSIStorageCapacity", 27, "storage.k8s.io/v1 CSIStorageCapacity"},
	{"flowcontrol.apiserver.k8s.io", "v1beta2", "flowschemas", "FlowSchema", 29, "flowcontrol.apiserver.k8s.io/v1 FlowSchema"},
	{"flowcontrol.apiserver.k8s.io", "v1beta3", "flowschemas", "FlowSchema", 32, "flowcontrol.apiserver.k8s.io/v1 FlowSchema"},
	{"flowcontrol.apiserver.k8s.io", "v1beta3", "prioritylevelconfigurations", "PriorityLevelConfiguration", 32, "flowcontrol.apiserver.k8s.io/v1 PriorityLevelConfiguration"},
}

// eolByMinor maps a Kubernetes minor to its upstream end-of-support date
// (~14 months after release; the last few entries are the projected cadence -
// extend as releases ship). Provider windows differ: AKS/EKS roughly track
// upstream, with paid LTS beyond it; the lens states this.
var eolByMinor = map[int]string{
	28: "2024-10-28",
	29: "2025-02-28",
	30: "2025-06-28",
	31: "2025-10-28",
	32: "2026-02-28",
	33: "2026-06-28",
	34: "2026-10-28",
	35: "2027-02-28",
	36: "2027-06-28",
}

// EOLDate returns the upstream end-of-support date for a minor, or zero time
// when the table does not carry it (too old, or beyond the projected cadence).
func EOLDate(minor int) (time.Time, bool) {
	s, ok := eolByMinor[minor]
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil
}

// ParseMinor extracts the minor from a GitVersion like "v1.31.4" or
// "v1.30.3+k3s1" / "v1.29.8-eks-a737599".
func ParseMinor(gitVersion string) (int, bool) {
	v := strings.TrimPrefix(gitVersion, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, false
	}
	minor := parts[1]
	// Strip any suffix a provider appends to the minor itself (rare, defensive).
	minor = strings.TrimFunc(minor, func(r rune) bool { return r < '0' || r > '9' })
	n, err := strconv.Atoi(minor)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// SkewFinding is one node whose kubelet version violates (or approaches) the
// supported skew against the control plane.
type SkewFinding struct {
	Node    string
	Version string
	Delta   int    // controlPlaneMinor - kubeletMinor (negative = kubelet NEWER)
	Level   string // "warn" | "danger"
	Detail  string
}

// SkewFindings evaluates kubelet skew against the control plane. Policy:
// kubelet may be up to 3 minors OLDER (since 1.28); a kubelet NEWER than the
// API server is unsupported outright. In-window skew of 1-2 minors is normal
// mid-upgrade and is NOT flagged; 3 is the edge (warn), >3 is out of support
// (danger).
func SkewFindings(controlPlane string, nodes map[string]string) []SkewFinding {
	cpMinor, ok := ParseMinor(controlPlane)
	if !ok {
		return nil
	}
	var out []SkewFinding
	for node, ver := range nodes {
		nMinor, ok := ParseMinor(ver)
		if !ok {
			continue
		}
		delta := cpMinor - nMinor
		switch {
		case delta < 0:
			out = append(out, SkewFinding{Node: node, Version: ver, Delta: delta, Level: "danger",
				Detail: "kubelet is NEWER than the control plane - unsupported"})
		case delta == 3:
			out = append(out, SkewFinding{Node: node, Version: ver, Delta: delta, Level: "warn",
				Detail: "kubelet at the 3-minor skew limit - upgrade nodes before the next control-plane bump"})
		case delta > 3:
			out = append(out, SkewFinding{Node: node, Version: ver, Delta: delta, Level: "danger",
				Detail: fmt.Sprintf("kubelet %d minors behind - outside the supported skew window", delta)})
		}
	}
	return out
}

// RemovalVerdict frames one deprecated API against a target minor.
func RemovalVerdict(api DeprecatedAPI, targetMinor int) string {
	if api.RemovedInMinor == 0 {
		return "deprecated (removal unscheduled)"
	}
	if targetMinor >= api.RemovedInMinor {
		return fmt.Sprintf("REMOVED in 1.%d", api.RemovedInMinor)
	}
	return fmt.Sprintf("removed in 1.%d (after the target)", api.RemovedInMinor)
}
