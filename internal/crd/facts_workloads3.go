package crd

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	factBuilders["apps/replicasets"] = replicaSetFacts
}

// replicaSetFacts summarizes a bare ReplicaSet: the ready/desired headline plus
// the revision's container images (the reason you open a RS directly rather than
// its Deployment). The owning Deployment and the pod list are reverse lookups and
// are deliberately not derived here.
func replicaSetFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact

	desired, hasDesired := gint(u, "spec", "replicas")
	ready, _ := gint(u, "status", "readyReplicas")

	// ready <- readyReplicas/replicas. Healthy only when the full desired count is
	// ready and something is actually wanted; a desired==0 RS is an intentionally
	// retired/scaled-down revision, not a fault.
	readyTone := ""
	if hasDesired && desired > 0 {
		if ready >= desired {
			readyTone = "success"
		} else {
			readyTone = "warning"
		}
	}
	facts = append(facts, well("", "ready", fmt.Sprintf("%d/%d", ready, desired), readyTone))

	if avail, ok := gint(u, "status", "availableReplicas"); ok {
		facts = append(facts, well("", "available", fmt.Sprintf("%d", avail), ""))
	}
	if cur, ok := gint(u, "status", "replicas"); ok {
		facts = append(facts, well("", "current", fmt.Sprintf("%d", cur), ""))
	}
	if hasDesired {
		facts = append(facts, well("", "desired", fmt.Sprintf("%d", desired), ""))
	}

	var images []string
	for _, c := range gslice(u, "spec", "template", "spec", "containers") {
		if img, _ := asMap(c)["image"].(string); img != "" {
			images = append(images, img)
		}
	}
	if len(images) > 0 {
		facts = append(facts, row("", "image", joinLimited(images, 6), ""))
	}

	return facts
}
