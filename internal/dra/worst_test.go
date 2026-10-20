package dra

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The worst objects the API admits, projected (S11b §7): each kind's
// MaxObject is set from these, so no valid object is left out for its size
// alone.

func name(n int, i int) string {
	tag := fmt.Sprintf("%06d", i)
	return strings.Repeat("a", n-len(tag)) + tag
}

func worstSlices() map[string]map[string]any {
	node := name(253, 0)
	out := map[string]map[string]any{}
	// 1.34-1.37: 128 plain devices
	var plain []any
	for i := 0; i < 128; i++ {
		plain = append(plain, map[string]any{"name": name(63, i)})
	}
	out["128 plain devices"] = map[string]any{"metadata": map[string]any{"name": name(253, 1), "uid": name(36, 1)},
		"spec": map[string]any{"driver": name(63, 2), "pool": map[string]any{"name": name(253, 3), "generation": int64(1), "resourceSliceCount": int64(1)}, "nodeName": node, "devices": plain}}
	// 1.35-1.37: 64 devices, per-device node names, 16 taints, 2 x 32 counters each
	var adv []any
	for i := 0; i < 64; i++ {
		var taints, cons []any
		for t := 0; t < 16; t++ {
			taints = append(taints, map[string]any{"key": name(316, t), "value": name(63, t), "effect": "NoExecute"})
		}
		for c := 0; c < 2; c++ {
			counters := map[string]any{}
			for k := 0; k < 32; k++ {
				counters[name(63, i*64+c*32+k)] = map[string]any{"value": "1"}
			}
			cons = append(cons, map[string]any{"counterSet": name(63, c), "counters": counters, "compatibilityGroups": []any{"a", "b"}})
		}
		adv = append(adv, map[string]any{"name": name(63, i), "nodeName": name(253, i), "taints": taints, "consumesCounters": cons})
	}
	out["64 devices, 16 taints, 2 x 32 counters"] = map[string]any{"metadata": map[string]any{"name": name(253, 1), "uid": name(36, 1)},
		"spec": map[string]any{"driver": name(63, 2), "pool": map[string]any{"name": name(253, 3), "generation": int64(1), "resourceSliceCount": int64(1)},
			"perDeviceNodeSelection": true, "devices": adv}}
	// 1.34: 32 counter sets of 32, and 128 devices consuming 1,024 counters
	var sets, dev34 []any
	for st := 0; st < 32; st++ {
		counters := map[string]any{}
		for k := 0; k < 32; k++ {
			counters[name(63, st*32+k)] = map[string]any{"value": "1"}
		}
		sets = append(sets, map[string]any{"name": name(63, st), "counters": counters})
	}
	for i := 0; i < 128; i++ {
		counters := map[string]any{}
		for k := 0; k < 8; k++ {
			counters[name(63, i*8+k)] = map[string]any{"value": "1"}
		}
		var taints []any
		for t := 0; t < 4; t++ {
			taints = append(taints, map[string]any{"key": name(316, t), "effect": "NoSchedule"})
		}
		dev34 = append(dev34, map[string]any{"name": name(63, i), "taints": taints, "consumesCounters": []any{map[string]any{"counterSet": name(63, i%32), "counters": counters}}})
	}
	out["1.34: 32 x 32 shared counters, 128 devices consuming 1,024"] = map[string]any{"metadata": map[string]any{"name": name(253, 1), "uid": name(36, 1)},
		"spec": map[string]any{"driver": name(63, 2), "pool": map[string]any{"name": name(253, 3), "generation": int64(1), "resourceSliceCount": int64(1)}, "nodeName": node,
			"sharedCounters": sets, "devices": dev34}}
	return out
}

func worstClaim() map[string]any {
	var reqs, results, reserved, devs, fins []any
	for r := 0; r < 32; r++ {
		var subs []any
		for x := 0; x < 8; x++ {
			subs = append(subs, map[string]any{"name": name(63, x), "deviceClassName": name(253, r*8+x)})
		}
		reqs = append(reqs, map[string]any{"name": name(63, r), "firstAvailable": subs})
		results = append(results, map[string]any{"request": name(63, r) + "/" + name(63, r), "driver": name(63, r), "pool": name(253, r), "device": name(63, r), "shareID": name(36, r), "adminAccess": true})
		var conds []any
		for c := 0; c < 8; c++ {
			// every byte of the kept message escapes in JSON
			conds = append(conds, map[string]any{"type": name(316, c), "status": "Unknown", "reason": name(1024, c), "message": strings.Repeat("\"", 32767) + "\x01"})
		}
		devs = append(devs, map[string]any{"driver": name(63, r), "pool": name(253, r), "device": name(63, r), "shareID": name(36, r), "conditions": conds})
	}
	for i := 0; i < 256; i++ {
		reserved = append(reserved, map[string]any{"resource": name(63, i), "name": name(253, i), "uid": name(36, i)})
	}
	for i := 0; i < 64; i++ {
		fins = append(fins, name(316, i))
	}
	return map[string]any{"metadata": map[string]any{"name": name(253, 0), "namespace": name(63, 0), "uid": name(36, 0), "finalizers": fins,
		"annotations":     map[string]any{AnnotationPodClaimName: name(63, 1), AnnotationExtended: "true"},
		"ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": "Pod", "name": name(253, 2), "uid": name(36, 2), "controller": true}}},
		"spec":   map[string]any{"devices": map[string]any{"requests": reqs}},
		"status": map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": results}}, "reservedFor": reserved, "devices": devs}}
}

func worstClass() map[string]any {
	var sels, cfg []any
	for i := 0; i < 32; i++ {
		sels = append(sels, map[string]any{"cel": map[string]any{"expression": name(10*1024, i)}})
		cfg = append(cfg, map[string]any{"opaque": map[string]any{"driver": name(63, i), "parameters": map[string]any{"x": name(10*1024, i)}}})
	}
	return map[string]any{"metadata": map[string]any{"name": name(253, 0), "uid": name(36, 0)},
		"spec": map[string]any{"selectors": sels, "config": cfg, "extendedResourceName": name(253, 1) + "/" + name(63, 1)}}
}

func TestWorstObjectsFitMaxObject(t *testing.T) {
	check := func(resource, what string, obj map[string]any) {
		t.Helper()
		v, err := Project(resource)(&unstructured.Unstructured{Object: obj})
		if err != nil {
			t.Fatal(err)
		}
		n := SizeOf(v)
		t.Logf("%s, %s: %d B projected (MaxObject %d)", resource, what, n, MaxObject(resource))
		if n > MaxObject(resource) {
			t.Errorf("%s, %s: %d B, over MaxObject %d", resource, what, n, MaxObject(resource))
		}
	}
	for what, obj := range worstSlices() {
		check("resourceslices", what, obj)
	}
	check("resourceclaims", "32 requests of 8 subrequests, 32 results and device statuses of 8 conditions, 256 consumers, 64 finalizers", worstClaim())
	check("deviceclasses", "32 selectors and configs, the longest extended resource name", worstClass())
	if c := ProjectClaim(&unstructured.Unstructured{Object: worstClaim()}); c.Truncated || len(c.Finalizers) != MaxFinalizersKept || c.FinalizersOmitted != 56 ||
		len(c.Devices[0].Conditions[0].Reason) > MaxTokenBytes+len("…") || Printable("a\x01\x7fb") != "a  b" {
		t.Fatalf("the worst claim is within the API's bounds, its finalizers and tokens cut: %v %d %d", c.Truncated, len(c.Finalizers), c.FinalizersOmitted)
	}
}
