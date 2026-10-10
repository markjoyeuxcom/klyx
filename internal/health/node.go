package health

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Node classifies one node (spec rows N1-N6). Cordon is an operation fact
// the caller carries separately; it only becomes the verdict when nothing
// worse is known.
func Node(n *corev1.Node) Verdict {
	var ready *corev1.NodeCondition
	var pressure []string
	for i := range n.Status.Conditions {
		c := &n.Status.Conditions[i]
		switch c.Type {
		case corev1.NodeReady:
			ready = c
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			if c.Status == corev1.ConditionTrue {
				pressure = append(pressure, string(c.Type))
			}
		}
	}
	native := "Ready=" + condStatus(ready)
	if n.Spec.Unschedulable {
		native += " · cordoned"
	}
	pressureMsg := strings.Join(pressure, ", ")

	switch {
	case ready != nil && ready.Status == corev1.ConditionFalse: // N3
		return Verdict{Kind: Fault, Label: "NotReady", Reason: orDefault(ready.Reason, "NotReady"), Message: joinNonEmpty(ready.Message, pressureMsg), Native: native, Rule: "node.not-ready"}
	case ready == nil: // N1; known pressure is still reported as evidence
		return Verdict{Kind: Unknown, Label: "Ready not reported", Reason: "ReadyMissing", Message: pressureMsg, Native: native, Rule: "node.ready-missing"}
	case ready.Status != corev1.ConditionTrue: // N2 (Unknown)
		return Verdict{Kind: Unknown, Label: "Kubelet not reporting", Reason: orDefault(ready.Reason, "NodeStatusUnknown"), Message: joinNonEmpty(ready.Message, pressureMsg), Native: native, Rule: "node.ready-unknown"}
	case len(pressure) > 0: // N4
		return Verdict{Kind: Degraded, Label: pressureMsg, Reason: "Pressure", Native: native, Rule: "node.pressure"}
	case n.Spec.Unschedulable: // N5
		return Verdict{Kind: Paused, Label: "Cordoned", Reason: "Cordoned", Native: native, Rule: "node.cordoned"}
	}
	return Verdict{Kind: OK, Label: "Ready", Reason: "Ready", Native: native, Rule: "node.ready"} // N6
}

func condStatus(c *corev1.NodeCondition) string {
	if c == nil {
		return "missing"
	}
	return string(c.Status)
}
