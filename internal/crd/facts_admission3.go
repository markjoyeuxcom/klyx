package crd

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	factBuilders["admissionregistration.k8s.io/validatingadmissionpolicies"] = validatingAdmissionPolicyFacts
	factBuilders["admissionregistration.k8s.io/validatingadmissionpolicybindings"] = validatingAdmissionPolicyBindingFacts
}

// admissionPolicyTruncate keeps a long CEL type-check warning readable in a row.
func admissionPolicyTruncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

// admissionPolicyResourceRuleStr summarizes one matchConstraints.resourceRules[]
// entry as a "<group>/<resources>" label + a "CREATE,UPDATE" operations value.
func admissionPolicyResourceRuleStr(m map[string]interface{}) (label, value string) {
	groups := stringSlice(asSlice(m["apiGroups"]))
	resources := stringSlice(asSlice(m["resources"]))
	ops := stringSlice(asSlice(m["operations"]))
	g := strings.Join(groups, ",")
	if g == "" {
		g = "core"
	}
	r := strings.Join(resources, ",")
	label = strings.TrimSuffix(g+"/"+r, "/")
	value = strings.Join(ops, ",")
	return label, value
}

// validatingAdmissionPolicyFacts surfaces a CEL ValidatingAdmissionPolicy's data
// path: how many validations it runs, its failure policy, its param binding, the
// resources it matches, and - the health headline - any status.typeChecking
// expression warnings (CEL authored against the wrong types, a silent misfire).
func validatingAdmissionPolicyFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact

	warnings := gslice(u, "status", "typeChecking", "expressionWarnings")
	typeTone := ""
	if len(warnings) > 0 {
		typeTone = "danger"
	}
	facts = append(facts, well("", "type warnings", fmt.Sprintf("%d", len(warnings)), typeTone))

	facts = append(facts, well("", "validations", fmt.Sprintf("%d", len(gslice(u, "spec", "validations"))), ""))

	if fp := gstr(u, "spec", "failurePolicy"); fp != "" {
		facts = append(facts, row("", "failure policy", fp, ""))
	}

	if pk := asMap(u.Object["spec"]); pk != nil {
		if kind := asMap(pk["paramKind"]); kind != nil {
			apiVersion, _ := kind["apiVersion"].(string)
			k, _ := kind["kind"].(string)
			if v := strings.TrimSpace(apiVersion + " " + k); v != "" {
				facts = append(facts, row("", "params", v, ""))
			}
		}
	}

	for _, rr := range gslice(u, "spec", "matchConstraints", "resourceRules") {
		m := asMap(rr)
		if m == nil {
			continue
		}
		label, value := admissionPolicyResourceRuleStr(m)
		if label == "" {
			continue
		}
		facts = append(facts, row("Matches", label, value, ""))
	}

	for i, w := range warnings {
		m := asMap(w)
		if m == nil {
			continue
		}
		fieldRef, _ := m["fieldRef"].(string)
		warning, _ := m["warning"].(string)
		val := admissionPolicyTruncate(fieldRef+": "+warning, 160)
		facts = append(facts, row("Type Warnings", fmt.Sprintf("warning %d", i+1), val, "danger"))
	}

	return facts
}

// validatingAdmissionPolicyBindingFacts surfaces which policy a binding activates,
// the enforcement actions it takes, and the param object it resolves.
func validatingAdmissionPolicyBindingFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact

	if actions := stringSlice(gslice(u, "spec", "validationActions")); len(actions) > 0 {
		facts = append(facts, row("", "enforcement", strings.Join(actions, ", "), ""))
	}

	if p := gstr(u, "spec", "policyName"); p != "" {
		facts = append(facts, row("", "binds policy", p, ""))
	}

	if pr := admissionPolicyParamRef(u); pr != nil {
		if name, _ := pr["name"].(string); name != "" {
			v := name
			if nfa, _ := pr["parameterNotFoundAction"].(string); nfa != "" {
				v += " (" + nfa + ")"
			}
			facts = append(facts, row("", "params", v, ""))
		}
	}

	return facts
}

// admissionPolicyParamRef returns spec.paramRef as a map (nil-tolerant).
func admissionPolicyParamRef(u *unstructured.Unstructured) map[string]interface{} {
	if spec := asMap(u.Object["spec"]); spec != nil {
		return asMap(spec["paramRef"])
	}
	return nil
}
