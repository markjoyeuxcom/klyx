package crd

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FactBuilder turns a fetched object into its salient summary facts. Builders are
// PURE (unstructured in, []Fact out) and read only fields already on the object -
// no extra API calls. Adding a new kind is "a pure func + a registry line + a
// test", the scale property the flat Fact slot buys over per-kind typed structs.
type FactBuilder func(u *unstructured.Unstructured) []Fact

// factBuilders is keyed by group+"/"+plural (core group = ""). Unregistered
// kinds return nil and render metadata-only, exactly as before.
var factBuilders = map[string]FactBuilder{
	"gateway.networking.k8s.io/httproutes":      httpRouteFacts,
	"gateway.networking.k8s.io/gateways":        gatewayFacts,
	"gateway.networking.k8s.io/gatewayclasses":  gatewayClassFacts,
	"gateway.networking.k8s.io/referencegrants": referenceGrantFacts,
	"/configmaps":                       configMapFacts,
	"/secrets":                          secretFacts,
	"/persistentvolumeclaims":           pvcFacts,
	"/persistentvolumes":                pvFacts,
	"/namespaces":                       namespaceFacts,
	"storage.k8s.io/storageclasses":     storageClassFacts,
	"scheduling.k8s.io/priorityclasses": priorityClassFacts,
	"coordination.k8s.io/leases":        leaseFacts,
	"networking.k8s.io/ingressclasses":  ingressClassFacts,
	"batch/cronjobs":                    cronJobFacts,
}

// BuildFacts dispatches to the registered builder for group/plural, or nil.
func BuildFacts(group, plural string, u *unstructured.Unstructured) []Fact {
	if u == nil {
		return nil
	}
	if b, ok := factBuilders[group+"/"+plural]; ok {
		return b(u)
	}
	return nil
}

// --- Fact constructors ---

func well(group, label, value, tone string) Fact {
	return Fact{Group: group, Label: label, Value: value, Tone: tone, Kind: "well"}
}
func row(group, label, value, tone string) Fact {
	return Fact{Group: group, Label: label, Value: value, Tone: tone, Kind: "row"}
}

// --- field readers (nil-tolerant) ---

func gstr(u *unstructured.Unstructured, path ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, path...)
	return s
}
func gslice(u *unstructured.Unstructured, path ...string) []interface{} {
	s, _, _ := unstructured.NestedSlice(u.Object, path...)
	return s
}
func gbool(u *unstructured.Unstructured, path ...string) bool {
	b, _, _ := unstructured.NestedBool(u.Object, path...)
	return b
}
func gint(u *unstructured.Unstructured, path ...string) (int64, bool) {
	n, ok, _ := unstructured.NestedInt64(u.Object, path...)
	return n, ok
}
func gstrmap(u *unstructured.Unstructured, path ...string) map[string]string {
	m, _, _ := unstructured.NestedStringMap(u.Object, path...)
	return m
}

func stringSlice(items []interface{}) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}
func asSlice(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}

func numStr(v interface{}) string {
	switch t := v.(type) {
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%d", int64(t))
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func joinLimited(items []string, max int) string {
	if len(items) > max {
		return strings.Join(items[:max], ", ") + fmt.Sprintf(" +%d more", len(items)-max)
	}
	return strings.Join(items, ", ")
}

func formatBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
}

// managedByFact turns the well-known Helm/Flux ownership annotations into a
// single readable provenance row.
func managedByFact(u *unstructured.Unstructured) (Fact, bool) {
	ann := u.GetAnnotations()
	switch {
	case ann["meta.helm.sh/release-name"] != "":
		return row("", "managed by", "Helm: "+ann["meta.helm.sh/release-name"], "info"), true
	case ann["kustomize.toolkit.fluxcd.io/name"] != "":
		return row("", "managed by", "Flux: "+ann["kustomize.toolkit.fluxcd.io/name"], "info"), true
	case ann["helm.toolkit.fluxcd.io/name"] != "":
		return row("", "managed by", "Flux HelmRelease: "+ann["helm.toolkit.fluxcd.io/name"], "info"), true
	}
	return Fact{}, false
}

func isDefaultClass(u *unstructured.Unstructured, annotation string) bool {
	return u.GetAnnotations()[annotation] == "true"
}

func phaseTone(p string) string {
	switch p {
	case "Bound", "Active", "Available", "Attached":
		return "success"
	case "Pending":
		return "warning"
	case "Lost", "Failed":
		return "danger"
	case "Terminating", "Released":
		return "warning"
	}
	return ""
}

// --- builders ---

func httpRouteFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if hosts := stringSlice(gslice(u, "spec", "hostnames")); len(hosts) > 0 {
		facts = append(facts, row("Routing", "hostnames", joinLimited(hosts, 6), ""))
	} else {
		facts = append(facts, row("Routing", "hostnames", "(all)", ""))
	}
	var parents []string
	for _, p := range gslice(u, "spec", "parentRefs") {
		m := asMap(p)
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		if ns, _ := m["namespace"].(string); ns != "" {
			name = ns + "/" + name
		}
		if sec, _ := m["sectionName"].(string); sec != "" {
			name += " (" + sec + ")"
		}
		parents = append(parents, name)
	}
	if len(parents) > 0 {
		facts = append(facts, row("Routing", "parents", joinLimited(parents, 4), ""))
	}
	rules := gslice(u, "spec", "rules")
	facts = append(facts, well("Routing", "rules", fmt.Sprintf("%d", len(rules)), ""))
	var backends []string
	for _, r := range rules {
		for _, b := range asSlice(asMap(r)["backendRefs"]) {
			bm := asMap(b)
			name, _ := bm["name"].(string)
			if name == "" {
				continue
			}
			s := name
			if p := numStr(bm["port"]); p != "" {
				s += ":" + p
			}
			if w := numStr(bm["weight"]); w != "" {
				s += " (w" + w + ")"
			}
			backends = append(backends, s)
		}
	}
	if len(backends) > 0 {
		facts = append(facts, row("Routing", "backends", joinLimited(backends, 6), ""))
	}
	return facts
}

func gatewayFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if cls := gstr(u, "spec", "gatewayClassName"); cls != "" {
		facts = append(facts, row("Gateway", "class", cls, ""))
	}
	var addrs []string
	for _, a := range gslice(u, "status", "addresses") {
		if v, _ := asMap(a)["value"].(string); v != "" {
			addrs = append(addrs, v)
		}
	}
	if len(addrs) > 0 {
		facts = append(facts, row("Gateway", "address", joinLimited(addrs, 4), ""))
	}
	specListeners := gslice(u, "spec", "listeners")
	var ports []string
	for _, l := range specListeners {
		m := asMap(l)
		name, _ := m["name"].(string)
		proto, _ := m["protocol"].(string)
		s := strings.TrimSpace(fmt.Sprintf("%s %s/%s", name, numStr(m["port"]), proto))
		if h, _ := m["hostname"].(string); h != "" {
			s += " " + h
		}
		ports = append(ports, s)
	}
	var attached int64
	for _, l := range gslice(u, "status", "listeners") {
		if n, ok := gintFrom(asMap(l), "attachedRoutes"); ok {
			attached += n
		}
	}
	facts = append(facts, well("Gateway", "listeners", fmt.Sprintf("%d", len(specListeners)), ""))
	if len(ports) > 0 {
		facts = append(facts, row("Gateway", "ports", joinLimited(ports, 6), ""))
	}
	facts = append(facts, well("Gateway", "attached routes", fmt.Sprintf("%d", attached), ""))
	return facts
}

func gintFrom(m map[string]interface{}, key string) (int64, bool) {
	switch t := m[key].(type) {
	case int64:
		return t, true
	case float64:
		return int64(t), true
	}
	return 0, false
}

func gatewayClassFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if c := gstr(u, "spec", "controllerName"); c != "" {
		facts = append(facts, row("", "controller", c, ""))
	}
	if d := gstr(u, "spec", "description"); d != "" {
		facts = append(facts, row("", "description", d, ""))
	}
	return facts
}

func referenceGrantFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	var from []string
	for _, f := range gslice(u, "spec", "from") {
		m := asMap(f)
		kind, _ := m["kind"].(string)
		ns, _ := m["namespace"].(string)
		from = append(from, strings.TrimSpace(kind+" "+ns))
	}
	if len(from) > 0 {
		facts = append(facts, row("Grant", "from", joinLimited(from, 4), ""))
	}
	var to []string
	for _, t := range gslice(u, "spec", "to") {
		m := asMap(t)
		kind, _ := m["kind"].(string)
		name, _ := m["name"].(string)
		if name == "" {
			name = "(all)"
		}
		to = append(to, strings.TrimSpace(kind+" "+name))
	}
	if len(to) > 0 {
		facts = append(facts, row("Grant", "to", joinLimited(to, 4), ""))
	}
	return facts
}

func configMapFacts(u *unstructured.Unstructured) []Fact {
	data := gstrmap(u, "data")
	bin := gstrmap(u, "binaryData")
	facts := []Fact{well("", "keys", fmt.Sprintf("%d", len(data)+len(bin)), "")}
	if gbool(u, "immutable") {
		facts = append(facts, well("", "immutable", "yes", "info"))
	}
	total := 0
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n := len(data[k])
		total += n
		// name + size only - a ConfigMap's key NAMES are not secret, but values
		// are never emitted as facts (use the YAML tab for content).
		facts = append(facts, row("Data", k, formatBytes(n), ""))
	}
	binKeys := make([]string, 0, len(bin))
	for k := range bin {
		binKeys = append(binKeys, k)
	}
	sort.Strings(binKeys)
	for _, k := range binKeys {
		n := len(bin[k]) * 3 / 4 // base64 -> approx decoded size
		total += n
		facts = append(facts, row("Data", k+" (binary)", formatBytes(n), ""))
	}
	sizeTone := ""
	if total > 900*1024 {
		sizeTone = "warning" // approaching the ~1 MiB etcd object ceiling
	}
	facts = append(facts, well("", "size", formatBytes(total), sizeTone))
	if mb, ok := managedByFact(u); ok {
		facts = append(facts, mb)
	}
	return facts
}

// secretFacts reads ONLY type/immutable/provenance - never the data map. It is
// also invoked on the already-masked object, so secret values cannot leak here.
func secretFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if t := gstr(u, "type"); t != "" {
		facts = append(facts, row("", "type", t, ""))
	}
	if gbool(u, "immutable") {
		facts = append(facts, well("", "immutable", "yes", "info"))
	}
	if mb, ok := managedByFact(u); ok {
		facts = append(facts, mb)
	}
	if c := u.GetAnnotations()["cert-manager.io/certificate-name"]; c != "" {
		facts = append(facts, row("", "issued for", "Certificate: "+c, "info"))
	}
	return facts
}

func pvcFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if phase := gstr(u, "status", "phase"); phase != "" {
		facts = append(facts, well("", "phase", phase, phaseTone(phase)))
	}
	if cap := gstr(u, "status", "capacity", "storage"); cap != "" {
		facts = append(facts, well("Storage", "capacity", cap, ""))
	} else if req := gstr(u, "spec", "resources", "requests", "storage"); req != "" {
		facts = append(facts, well("Storage", "requested", req, ""))
	}
	if sc := gstr(u, "spec", "storageClassName"); sc != "" {
		facts = append(facts, row("Storage", "class", sc, ""))
	}
	if modes := stringSlice(gslice(u, "spec", "accessModes")); len(modes) > 0 {
		facts = append(facts, row("Storage", "access", strings.Join(modes, ", "), ""))
	}
	if vm := gstr(u, "spec", "volumeMode"); vm != "" {
		facts = append(facts, row("Storage", "mode", vm, ""))
	}
	if vol := gstr(u, "spec", "volumeName"); vol != "" {
		facts = append(facts, row("Storage", "volume", vol, ""))
	}
	return facts
}

func pvFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if phase := gstr(u, "status", "phase"); phase != "" {
		facts = append(facts, well("", "phase", phase, phaseTone(phase)))
	}
	if cap := gstr(u, "spec", "capacity", "storage"); cap != "" {
		facts = append(facts, well("Storage", "capacity", cap, ""))
	}
	if rp := gstr(u, "spec", "persistentVolumeReclaimPolicy"); rp != "" {
		tone := ""
		if rp == "Delete" {
			tone = "warning"
		}
		facts = append(facts, row("Storage", "reclaim", rp, tone))
	}
	if sc := gstr(u, "spec", "storageClassName"); sc != "" {
		facts = append(facts, row("Storage", "class", sc, ""))
	}
	if modes := stringSlice(gslice(u, "spec", "accessModes")); len(modes) > 0 {
		facts = append(facts, row("Storage", "access", strings.Join(modes, ", "), ""))
	}
	if drv := gstr(u, "spec", "csi", "driver"); drv != "" {
		facts = append(facts, row("Storage", "driver", drv, ""))
	}
	if cn := gstr(u, "spec", "claimRef", "name"); cn != "" {
		facts = append(facts, row("Storage", "claim", strings.TrimPrefix(gstr(u, "spec", "claimRef", "namespace")+"/"+cn, "/"), ""))
	}
	return facts
}

func namespaceFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if phase := gstr(u, "status", "phase"); phase != "" {
		facts = append(facts, well("", "phase", phase, phaseTone(phase)))
	}
	labels := u.GetLabels()
	for _, mode := range []string{"enforce", "audit", "warn"} {
		if v := labels["pod-security.kubernetes.io/"+mode]; v != "" {
			facts = append(facts, row("Pod Security", mode, v, ""))
		}
	}
	return facts
}

func storageClassFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if isDefaultClass(u, "storageclass.kubernetes.io/is-default-class") {
		facts = append(facts, well("", "default", "yes", "info"))
	}
	if p := gstr(u, "provisioner"); p != "" {
		facts = append(facts, row("", "provisioner", p, ""))
	}
	if rp := gstr(u, "reclaimPolicy"); rp != "" {
		facts = append(facts, row("", "reclaim", rp, ""))
	}
	if vbm := gstr(u, "volumeBindingMode"); vbm != "" {
		facts = append(facts, row("", "binding", vbm, ""))
	}
	if gbool(u, "allowVolumeExpansion") {
		facts = append(facts, well("", "expandable", "yes", ""))
	}
	return facts
}

func priorityClassFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if v, ok := gint(u, "value"); ok {
		facts = append(facts, well("", "value", fmt.Sprintf("%d", v), ""))
	}
	if gbool(u, "globalDefault") {
		facts = append(facts, well("", "global default", "yes", "info"))
	}
	if pp := gstr(u, "preemptionPolicy"); pp != "" {
		facts = append(facts, row("", "preemption", pp, ""))
	}
	return facts
}

func leaseFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if h := gstr(u, "spec", "holderIdentity"); h != "" {
		facts = append(facts, row("", "holder", h, ""))
	}
	if rt := gstr(u, "spec", "renewTime"); rt != "" {
		facts = append(facts, row("", "renewed", rt, ""))
	}
	if d, ok := gint(u, "spec", "leaseDurationSeconds"); ok {
		facts = append(facts, well("", "duration", fmt.Sprintf("%ds", d), ""))
	}
	if t, ok := gint(u, "spec", "leaseTransitions"); ok {
		facts = append(facts, well("", "transitions", fmt.Sprintf("%d", t), ""))
	}
	return facts
}

func ingressClassFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if c := gstr(u, "spec", "controller"); c != "" {
		facts = append(facts, row("", "controller", c, ""))
	}
	if isDefaultClass(u, "ingressclass.kubernetes.io/is-default-class") {
		facts = append(facts, well("", "default", "yes", "info"))
	}
	return facts
}

func cronJobFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if s := gstr(u, "spec", "schedule"); s != "" {
		facts = append(facts, row("Schedule", "cron", s, ""))
	}
	if tz := gstr(u, "spec", "timeZone"); tz != "" {
		facts = append(facts, row("Schedule", "timezone", tz, ""))
	}
	if gbool(u, "spec", "suspend") {
		facts = append(facts, well("", "suspended", "yes", "warning"))
	}
	facts = append(facts, well("", "active", fmt.Sprintf("%d", len(gslice(u, "status", "active"))), ""))
	if t := gstr(u, "status", "lastScheduleTime"); t != "" {
		facts = append(facts, row("Schedule", "last run", t, ""))
	}
	if cp := gstr(u, "spec", "concurrencyPolicy"); cp != "" {
		facts = append(facts, row("Schedule", "concurrency", cp, ""))
	}
	return facts
}
