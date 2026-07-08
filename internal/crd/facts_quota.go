package crd

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	factBuilders["/resourcequotas"] = resourceQuotaFacts
	factBuilders["/limitranges"] = limitRangeFacts
}

// quotaPct returns used/hard*100 and whether it could be computed. It guards
// unparseable quantities and a zero (or unparseable) hard denominator.
func quotaPct(used, hard string) (float64, bool) {
	hq, err := resource.ParseQuantity(hard)
	if err != nil {
		return 0, false
	}
	hf := hq.AsApproximateFloat64()
	if hf == 0 {
		return 0, false
	}
	uq, err := resource.ParseQuantity(used)
	if err != nil {
		return 0, false
	}
	return uq.AsApproximateFloat64() / hf * 100, true
}

// quotaStrMap reads a nested string->string map (quantity values arrive as JSON
// strings), nil-tolerant.
func quotaStrMap(m map[string]interface{}) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func resourceQuotaFacts(u *unstructured.Unstructured) []Fact {
	hard, _, _ := unstructured.NestedMap(u.Object, "status", "hard")
	if len(hard) == 0 {
		// Not yet realized by the controller; fall back to the spec.hard request.
		hard, _, _ = unstructured.NestedMap(u.Object, "spec", "hard")
	}
	hardStr := quotaStrMap(hard)
	usedRaw, _, _ := unstructured.NestedMap(u.Object, "status", "used")
	usedStr := quotaStrMap(usedRaw)

	type qrow struct {
		res   string
		value string
		tone  string
		pct   float64
		hasP  bool
	}
	rows := make([]qrow, 0, len(hardStr))
	var overName string
	var overPct float64
	for res, h := range hardStr {
		used := usedStr[res]
		if used == "" {
			used = "0"
		}
		value := used + " / " + h
		tone := ""
		pct, ok := quotaPct(used, h)
		if ok {
			// Non-zero-but-tiny usage rounds to "0%" and reads as untouched;
			// show "<1%" so a quota with real (small) usage is not mistaken for
			// empty. "0%" stays 0% only when usage is genuinely zero.
			if pct > 0 && pct < 1 {
				value += " (<1%)"
			} else {
				value += fmt.Sprintf(" (%.0f%%)", pct)
			}
			switch {
			case pct >= 100:
				tone = "danger"
			case pct >= 90:
				tone = "warning"
			}
			if pct >= 100 && pct > overPct {
				overPct = pct
				overName = res
			}
		}
		rows = append(rows, qrow{res: res, value: value, tone: tone, pct: pct, hasP: ok})
	}
	// Pressure-first: highest pct at the top; unparseable rows sink, then by name.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].hasP != rows[j].hasP {
			return rows[i].hasP
		}
		if rows[i].pct != rows[j].pct {
			return rows[i].pct > rows[j].pct
		}
		return rows[i].res < rows[j].res
	})

	facts := []Fact{well("", "resources", fmt.Sprintf("%d", len(hardStr)), "")}
	if overName != "" {
		facts = append(facts, well("", "over budget", overName, "danger"))
	}
	if scopes := stringSlice(gslice(u, "spec", "scopes")); len(scopes) > 0 {
		facts = append(facts, row("Scopes", "scopes", strings.Join(scopes, ", "), ""))
	}
	for _, r := range rows {
		facts = append(facts, row("Quota", r.res, r.value, r.tone))
	}
	return facts
}

func limitRangeFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	for _, l := range gslice(u, "spec", "limits") {
		m := asMap(l)
		typ, _ := m["type"].(string)
		if typ == "" {
			typ = "Limit"
		}
		def := quotaStrMap(asMap(m["default"]))
		req := quotaStrMap(asMap(m["defaultRequest"]))
		max := quotaStrMap(asMap(m["max"]))
		min := quotaStrMap(asMap(m["min"]))

		resSet := map[string]struct{}{}
		for _, src := range []map[string]string{def, req, max, min} {
			for k := range src {
				resSet[k] = struct{}{}
			}
		}
		resKeys := make([]string, 0, len(resSet))
		for k := range resSet {
			resKeys = append(resKeys, k)
		}
		sort.Strings(resKeys)

		for _, res := range resKeys {
			var parts []string
			if v := def[res]; v != "" {
				parts = append(parts, "default "+v)
			}
			if v := req[res]; v != "" {
				parts = append(parts, "request "+v)
			}
			if v := max[res]; v != "" {
				parts = append(parts, "max "+v)
			}
			if v := min[res]; v != "" {
				parts = append(parts, "min "+v)
			}
			if len(parts) == 0 {
				continue
			}
			facts = append(facts, row("Limits", typ+" "+res, strings.Join(parts, ", "), ""))
		}
	}
	return facts
}
