package crd

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	factBuilders["batch/jobs"] = jobFacts
	factBuilders["policy/poddisruptionbudgets"] = pdbFacts
}

// jobFacts surfaces a batch/Job's run data path - the completions/active/failed
// counters and wall-clock duration a PE reads to see whether a Job made
// progress. The Complete/Failed status.conditions drive the verdict, so we never
// synthesize one here.
func jobFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact

	succeeded, _ := gint(u, "status", "succeeded")
	if completions, ok := gint(u, "spec", "completions"); ok {
		facts = append(facts, well("", "completions", fmt.Sprintf("%d/%d", succeeded, completions), ""))
	} else {
		facts = append(facts, well("", "completions", fmt.Sprintf("%d", succeeded), ""))
	}

	active, _ := gint(u, "status", "active")
	facts = append(facts, well("", "active", fmt.Sprintf("%d", active), ""))

	failed, _ := gint(u, "status", "failed")
	failTone := ""
	if failed > 0 {
		failTone = "danger"
	}
	facts = append(facts, well("", "failed", fmt.Sprintf("%d", failed), failTone))

	if parallelism, ok := gint(u, "spec", "parallelism"); ok {
		facts = append(facts, well("", "parallelism", fmt.Sprintf("%d", parallelism), ""))
	}
	if backoff, ok := gint(u, "spec", "backoffLimit"); ok {
		facts = append(facts, well("", "backoff", fmt.Sprintf("%d", backoff), ""))
	}

	if mode := gstr(u, "spec", "completionMode"); mode == "Indexed" {
		facts = append(facts, row("", "mode", mode, ""))
	}

	if d := jobDuration(u); d != "" {
		facts = append(facts, row("", "duration", d, ""))
	}

	return facts
}

// jobDuration renders completionTime-startTime as "3m12s", or "running Xs" while
// the Job is still executing (startTime set, no completionTime). Returns "" when
// there is no usable start time.
func jobDuration(u *unstructured.Unstructured) string {
	startStr := gstr(u, "status", "startTime")
	if startStr == "" {
		return ""
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		return ""
	}
	if endStr := gstr(u, "status", "completionTime"); endStr != "" {
		end, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			return ""
		}
		return end.Sub(start).Round(time.Second).String()
	}
	return "running " + time.Since(start).Round(time.Second).String()
}

// pdbFacts surfaces a PodDisruptionBudget's guard state. "disruptions allowed"
// is the number a PE checks before every drain: 0 means a drain is blocked right
// now, so it earns a warning tone. Vocabulary is blocked/guarding, not failed.
func pdbFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact

	currentHealthy, chOK := gint(u, "status", "currentHealthy")

	if allowed, ok := gint(u, "status", "disruptionsAllowed"); ok {
		tone := ""
		if allowed == 0 {
			tone = "warning" // a drain is blocked right now
		}
		facts = append(facts, well("", "disruptions allowed", fmt.Sprintf("%d", allowed), tone))
	}

	if chOK {
		facts = append(facts, well("", "healthy", fmt.Sprintf("%d", currentHealthy), ""))
	}

	if desired, ok := gint(u, "status", "desiredHealthy"); ok {
		tone := ""
		if chOK && currentHealthy < desired {
			tone = "warning" // guarding fewer pods than it wants
		}
		facts = append(facts, well("", "desired", fmt.Sprintf("%d", desired), tone))
	}

	if expected, ok := gint(u, "status", "expectedPods"); ok {
		facts = append(facts, well("", "expected", fmt.Sprintf("%d", expected), ""))
	}

	if v := pdbScalar(u, "spec", "minAvailable"); v != "" {
		facts = append(facts, well("", "min available", v, ""))
	}
	if v := pdbScalar(u, "spec", "maxUnavailable"); v != "" {
		facts = append(facts, well("", "max unavailable", v, ""))
	}

	if disrupted, _, _ := unstructured.NestedMap(u.Object, "status", "disruptedPods"); len(disrupted) > 0 {
		facts = append(facts, well("", "disrupted", fmt.Sprintf("%d", len(disrupted)), ""))
	}

	return facts
}

// pdbScalar reads an IntOrString spec field (minAvailable/maxUnavailable may be a
// count or a percentage string) as its display string, "" when unset.
func pdbScalar(u *unstructured.Unstructured, path ...string) string {
	v, ok, _ := unstructured.NestedFieldNoCopy(u.Object, path...)
	if !ok {
		return ""
	}
	return numStr(v)
}
