package health

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/extres"
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
	// coverage S11a §3: an extended resource whose allocatable reads below
	// its capacity, read over every key before any display cut
	gaps := extres.Gaps(n)
	gapMsg, gapNative := gapWords(gaps)
	native += gapNative

	switch {
	case ready != nil && ready.Status == corev1.ConditionFalse: // N3
		return Verdict{Kind: Fault, Label: "NotReady", Reason: orDefault(ready.Reason, "NotReady"), Message: joinNonEmpty(ready.Message, pressureMsg), Native: native, Rule: "node.not-ready"}
	case ready == nil: // N1; known pressure is still reported as evidence
		return Verdict{Kind: Unknown, Label: "Ready not reported", Reason: "ReadyMissing", Message: pressureMsg, Native: native, Rule: "node.ready-missing"}
	case ready.Status != corev1.ConditionTrue: // N2 (Unknown)
		return Verdict{Kind: Unknown, Label: "Kubelet not reporting", Reason: orDefault(ready.Reason, "NodeStatusUnknown"), Message: joinNonEmpty(ready.Message, pressureMsg), Native: native, Rule: "node.ready-unknown"}
	case len(pressure) > 0: // N4, naming a capacity gap beside it
		label := pressureMsg
		if len(gaps) > 0 {
			label += " · resources not allocatable"
		}
		return Verdict{Kind: Degraded, Label: label, Reason: "Pressure", Message: gapMsg, Native: native, Rule: "node.pressure"}
	case len(gaps) > 0: // S11a: units not allocatable to new pods, whatever the cause
		return Verdict{Kind: Degraded, Label: "Resources not allocatable", Reason: "NotAllocatable", Message: gapMsg, Native: native, Rule: "node.resources-not-allocatable"}
	case n.Spec.Unschedulable: // N5
		return Verdict{Kind: Paused, Label: "Cordoned", Reason: "Cordoned", Native: native, Rule: "node.cordoned"}
	}
	return Verdict{Kind: OK, Label: "Ready", Reason: "Ready", Native: native, Rule: "node.ready"} // N6
}

// gapNamed bounds the resources a capacity-gap reading names (S11a §2.0).
const gapNamed = 4

// GapCause is the capacity gap's cause, said as an interpretation: the node
// alone does not say which applies (S11a §3).
const GapCause = "For a device-plugin resource the kubelet reports unhealthy devices, or a plugin disconnected in the last 5 minutes, this way; " +
	"a manually advertised resource reads so until the kubelet's next sync. The node does not say which."

// gapWords words the gaps: the numbers as the finding, at most gapNamed
// resources named, then the cause as an interpretation.
func gapWords(gaps []extres.Gap) (msg, native string) {
	if len(gaps) == 0 {
		return "", ""
	}
	var parts, nat []string
	for i, g := range gaps {
		if i == gapNamed {
			break
		}
		parts = append(parts, g.Name+": "+g.Allocatable+" of "+g.Capacity+" allocatable")
		nat = append(nat, g.Name+" allocatable "+g.Allocatable+", capacity "+g.Capacity)
	}
	more := ""
	if len(gaps) > gapNamed {
		more = " and " + strconv.Itoa(len(gaps)-gapNamed) + " more"
	}
	return strings.Join(parts, "; ") + more + ". " + GapCause, " · " + strings.Join(nat, "; ") + more
}

func condStatus(c *corev1.NodeCondition) string {
	if c == nil {
		return "missing"
	}
	return string(c.Status)
}
