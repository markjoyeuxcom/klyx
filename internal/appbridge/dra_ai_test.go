package appbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Coverage S11b §6: synthetic needles in every never-sent field, searched
// for in each path's serialized outgoing document (Codex's two design
// probes among them).

const (
	needleCEL     = "NeedleCEL7x"
	needleOpaque  = "NeedleOpaque7x"
	needleAlloc   = "NeedleAllocCfg7x"
	needleData    = "NeedleDeviceData7x"
	needleNet     = "NeedleNetData7x"
	needleCondMsg = "NeedleCondMsg7x"
	needleAttr    = "NeedleAttr7x"
	needleCap     = "NeedleCap7x"
	needleTaint   = "NeedleTaint7x"
)

var allNeedles = []string{needleCEL, needleOpaque, needleAlloc, needleData, needleNet, needleCondMsg, needleAttr, needleCap, needleTaint}

func draNeedleObjects() map[string]map[string]any {
	claimSpec := map[string]any{"devices": map[string]any{
		"requests": []any{map[string]any{"name": "gpu", "exactly": map[string]any{"deviceClassName": "gpu.example.com", "allocationMode": "ExactCount", "count": int64(1),
			"selectors": []any{map[string]any{"cel": map[string]any{"expression": "device.attributes[\"x\"].model == \"" + needleCEL + "\""}}}}},
			map[string]any{"name": "alt", "firstAvailable": []any{map[string]any{"name": "a", "deviceClassName": "c2",
				"selectors": []any{map[string]any{"cel": map[string]any{"expression": needleCEL}}}}}}},
		"config": []any{map[string]any{"opaque": map[string]any{"driver": "gpu.example.com", "parameters": map[string]any{"detail": needleOpaque}}}}}}
	return map[string]map[string]any{
		"deviceclasses": {"apiVersion": "resource.k8s.io/v1", "kind": "DeviceClass", "metadata": map[string]any{"name": "c", "uid": "u-c"},
			"spec": map[string]any{"selectors": []any{map[string]any{"cel": map[string]any{"expression": "device.driver == \"" + needleCEL + "\""}}},
				"config": []any{map[string]any{"opaque": map[string]any{"driver": "gpu.example.com", "parameters": map[string]any{"x": needleOpaque}}}}}},
		"resourceclaims": {"apiVersion": "resource.k8s.io/v1", "kind": "ResourceClaim", "metadata": map[string]any{"name": "c", "namespace": "ns", "uid": "u-rc"},
			"spec": claimSpec,
			"status": map[string]any{"allocation": map[string]any{"devices": map[string]any{
				"results": []any{map[string]any{"request": "gpu", "driver": "gpu.example.com", "pool": "n1", "device": "gpu-0"}},
				"config":  []any{map[string]any{"source": "FromClaim", "opaque": map[string]any{"driver": "gpu.example.com", "parameters": map[string]any{"y": needleAlloc}}}}}},
				"devices": []any{map[string]any{"driver": "gpu.example.com", "pool": "n1", "device": "gpu-0", "data": map[string]any{"pci": needleData},
					"networkData": map[string]any{"ips": []any{needleNet}},
					"conditions":  []any{map[string]any{"type": "Ready", "status": "False", "reason": "Failed", "message": needleCondMsg}}}}}},
		"resourceclaimtemplates": {"apiVersion": "resource.k8s.io/v1", "kind": "ResourceClaimTemplate", "metadata": map[string]any{"name": "t", "namespace": "ns", "uid": "u-t"},
			"spec": map[string]any{"spec": claimSpec}},
		"resourceslices": {"apiVersion": "resource.k8s.io/v1", "kind": "ResourceSlice", "metadata": map[string]any{"name": "s", "uid": "u-s"},
			"spec": map[string]any{"driver": "gpu.example.com", "pool": map[string]any{"name": "n1", "generation": int64(1), "resourceSliceCount": int64(1)}, "nodeName": "n1",
				"devices": []any{map[string]any{"name": "gpu-0", "attributes": map[string]any{"uuid": map[string]any{"string": needleAttr}},
					"capacity": map[string]any{"memory": map[string]any{"value": "80Gi", "requestPolicy": map[string]any{"note": needleCap}}},
					"taints":   []any{map[string]any{"key": needleTaint, "value": needleTaint, "effect": "NoSchedule"}}}}}},
		"devicetaintrules": {"apiVersion": "resource.k8s.io/v1", "kind": "DeviceTaintRule", "metadata": map[string]any{"name": "r", "uid": "u-r"},
			"spec": map[string]any{"deviceSelector": map[string]any{"selectors": []any{map[string]any{"cel": map[string]any{"expression": needleCEL}}}},
				"taint": map[string]any{"key": needleTaint, "value": needleTaint, "effect": "NoExecute"}}},
	}
}

func draNoNeedle(t *testing.T, where string, b []byte) {
	t.Helper()
	for _, n := range allNeedles {
		if strings.Contains(string(b), n) {
			t.Errorf("%s: %s crossed: %s", where, n, b)
		}
	}
}

func TestDRAChatProjectionCarriesNoNeedle(t *testing.T) {
	for resource, obj := range draNeedleObjects() {
		kind, _ := obj["kind"].(string)
		clean := crd.SanitizeEmbedded("resource.k8s.io", kind, obj)
		got := chatProject(schema.GroupVersionResource{Group: "resource.k8s.io", Version: "v1", Resource: resource}, &unstructured.Unstructured{Object: clean.Object})
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		draNoNeedle(t, "chat get "+resource, b)
		// what crosses still says something: the allowed facts
		switch resource {
		case "resourceclaims":
			if !strings.Contains(string(b), `"device":"gpu-0"`) || !strings.Contains(string(b), `"deviceClassName":"gpu.example.com"`) || !strings.Contains(string(b), `"reason":"Failed"`) {
				t.Errorf("a claim keeps its identities: %s", b)
			}
		case "resourceslices":
			if !strings.Contains(string(b), `"gpu-0"`) || !strings.Contains(string(b), "NoSchedule") {
				t.Errorf("a slice keeps its devices and taint effects: %s", b)
			}
		}
	}
}

func TestDRASuggestFixManifestCarriesNoNeedle(t *testing.T) {
	for resource, obj := range draNeedleObjects() {
		kind, _ := obj["kind"].(string)
		clean := crd.SanitizeEmbedded("resource.k8s.io", kind, obj)
		b, err := json.Marshal(clean.Object)
		if err != nil {
			t.Fatal(err)
		}
		shown, _, err := redactManifestForAI(string(b), false, draResources["resource.k8s.io/"+resource])
		if resource == "resourceslices" {
			if err == nil || shown != "" {
				t.Fatalf("a slice's manifest is never sent: %q %v", shown, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		draNoNeedle(t, "suggest-fix "+resource, []byte(shown))
		if !strings.Contains(shown, draOmitted) {
			t.Errorf("%s: the marker says a field was omitted: %s", resource, shown)
		}
	}
}

func TestDRAExplainEvidenceCarriesNoNeedle(t *testing.T) {
	ev := explainEvidence{Target: evidenceObject{Kind: "ResourceClaim", Name: "c", Conditions: []evidenceCondition{{Type: "Ready", Status: "False", Reason: "x y", Message: needleCondMsg}},
		Facts: []evidenceFact{{Label: "cel", Value: needleCEL}}},
		Related: []evidenceObject{{Kind: "DeviceClass", Name: "gpu", Facts: []evidenceFact{{Label: "selector", Value: needleCEL}}},
			{Kind: "Pod", Name: "p", Facts: []evidenceFact{{Label: "image", Value: "app:1"}}}}}
	out := draEvidence(ev)
	b, _ := json.Marshal(out)
	draNoNeedle(t, "explain evidence", b)
	if out.Target.Conditions[0].Reason != "reason withheld" || len(out.Related[1].Facts) != 1 {
		t.Fatalf("a free-text reason is withheld; another kind is untouched: %+v", out)
	}
	bundle := draBundle(resourceBundle{Root: resourceView{Kind: "ResourceClaimTemplate", Conditions: []conditionView{{Type: "X", Status: "True", Message: needleCondMsg}},
		Facts: []factView{{Label: "cel", Value: needleCEL}}}})
	b, _ = json.Marshal(bundle)
	draNoNeedle(t, "suggest-fix bundle", b)
}

func TestDRAExplainRowsCarryNoMessage(t *testing.T) {
	row := healthsrc.Row[healthsrc.DRAClaimRow]{Ref: healthsrc.RowRef{UID: "u"}, Eval: health.NativeEval{Role: health.Degraded, Label: "Device not ready"},
		Data: healthsrc.DRAClaimRow{Claim: dra.Claim{Namespace: "ns", Name: "c", Allocated: true,
			Devices: []dra.DeviceStatus{{Driver: "d", Pool: "p", Device: "g", Conditions: []dra.Cond{{Type: "Ready", Status: "False", Reason: "Failed", Message: needleCondMsg}}}}},
			Verdict:   health.Verdict{Kind: health.Degraded, Label: "Device not ready", Message: "d/p/g: Ready False"},
			Consumers: []dra.ConsumerReading{{Pod: "p", Row: "b", Kind: health.Degraded, Label: "x", Reason: "cannot-allocate", Message: needleCondMsg}}}}
	dto := toDRAClaimDTO(row)
	b, _ := json.Marshal(dto)
	var m any
	_ = json.Unmarshal(b, &m)
	out, _ := json.Marshal((&scrubber{}).project(m, aiEvidenceFields[healthsrc.TaskDRA+"/claims"]))
	draNoNeedle(t, "explain claims row", out)
	if !strings.Contains(string(out), `"reason":"Failed"`) || !strings.Contains(string(out), `"meaning"`) {
		t.Fatalf("the claim row keeps reason tokens and the fixed meaning: %s", out)
	}
	_ = time.Now
}
