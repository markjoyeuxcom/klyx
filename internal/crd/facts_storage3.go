package crd

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	factBuilders["storage.k8s.io/csidrivers"] = csiDriverFacts
	factBuilders["storage.k8s.io/csinodes"] = csiNodeFacts
	factBuilders["storage.k8s.io/volumeattachments"] = volumeAttachmentFacts
	factBuilders["node.k8s.io/runtimeclasses"] = runtimeClassFacts
}

// storageYesNo renders a boolean as the compact yes/no a counter well expects.
func storageYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// storageMapStr renders a string map as sorted "k=v,k=v" prose (nodeSelector,
// podFixed overhead). Empty map -> "".
func storageMapStr(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func csiDriverFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	facts = append(facts, well("", "attach required", storageYesNo(gbool(u, "spec", "attachRequired")), ""))
	facts = append(facts, well("", "pod info on mount", storageYesNo(gbool(u, "spec", "podInfoOnMount")), ""))
	if modes := stringSlice(gslice(u, "spec", "volumeLifecycleModes")); len(modes) > 0 {
		facts = append(facts, row("", "lifecycle modes", strings.Join(modes, ", "), ""))
	}
	if gbool(u, "spec", "storageCapacity") {
		facts = append(facts, well("", "storage capacity", "yes", ""))
	}
	if p := gstr(u, "spec", "fsGroupPolicy"); p != "" {
		facts = append(facts, row("", "fsGroup policy", p, ""))
	}
	if gbool(u, "spec", "requiresRepublish") {
		facts = append(facts, well("", "requires republish", "yes", ""))
	}
	return facts
}

func csiNodeFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	drivers := gslice(u, "spec", "drivers")
	facts = append(facts, well("", "drivers", fmt.Sprintf("%d", len(drivers)), ""))
	for _, d := range drivers {
		m := asMap(d)
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		nodeID, _ := m["nodeID"].(string)
		var parts []string
		parts = append(parts, "node "+nodeID)
		if cnt := numStr(asMap(m["allocatable"])["count"]); cnt != "" {
			parts = append(parts, "max "+cnt)
		}
		if zones := stringSlice(asSlice(m["topologyKeys"])); len(zones) > 0 {
			parts = append(parts, "zones "+strings.Join(zones, ", "))
		}
		facts = append(facts, row("Drivers", name, strings.Join(parts, " · "), ""))
	}
	return facts
}

func volumeAttachmentFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	attached := gbool(u, "status", "attached")
	tone := ""
	if attached {
		tone = "success"
	}
	facts = append(facts, well("", "attached", storageYesNo(attached), tone))
	if a := gstr(u, "spec", "attacher"); a != "" {
		facts = append(facts, row("", "attacher", a, ""))
	}
	if n := gstr(u, "spec", "nodeName"); n != "" {
		facts = append(facts, row("", "node", n, ""))
	}
	if v := gstr(u, "spec", "source", "persistentVolumeName"); v != "" {
		facts = append(facts, row("", "source volume", v, ""))
	}
	if e := gstr(u, "status", "attachError", "message"); e != "" {
		facts = append(facts, row("", "attach error", e, "danger"))
	}
	if e := gstr(u, "status", "detachError", "message"); e != "" {
		facts = append(facts, row("", "detach error", e, "danger"))
	}
	return facts
}

func runtimeClassFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if h := gstr(u, "handler"); h != "" {
		facts = append(facts, row("", "handler", h, ""))
	}
	if sel := storageMapStr(gstrmap(u, "scheduling", "nodeSelector")); sel != "" {
		facts = append(facts, row("", "node selector", sel, ""))
	}
	if tols := gslice(u, "scheduling", "tolerations"); len(tols) > 0 {
		facts = append(facts, well("", "tolerations", fmt.Sprintf("%d", len(tols)), ""))
	}
	if oh := storageMapStr(gstrmap(u, "overhead", "podFixed")); oh != "" {
		facts = append(facts, row("", "pod overhead", oh, ""))
	}
	return facts
}
