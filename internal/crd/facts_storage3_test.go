package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_CSIDriver(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "CSIDriver",
		"spec": map[string]interface{}{
			"attachRequired":       true,
			"podInfoOnMount":       false,
			"volumeLifecycleModes": []interface{}{"Persistent", "Ephemeral"},
			"storageCapacity":      true,
			"fsGroupPolicy":        "File",
			"requiresRepublish":    true,
		},
	}}
	facts := BuildFacts("storage.k8s.io", "csidrivers", u)
	if f, ok := factByLabel(facts, "attach required"); !ok || f.Value != "yes" {
		t.Fatalf("attach required fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "pod info on mount"); !ok || f.Value != "no" {
		t.Fatalf("pod info on mount fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "lifecycle modes"); !ok || f.Value != "Persistent, Ephemeral" {
		t.Fatalf("lifecycle modes fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "storage capacity"); !ok || f.Value != "yes" {
		t.Fatalf("storage capacity fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "fsGroup policy"); !ok || f.Value != "File" {
		t.Fatalf("fsGroup policy fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "requires republish"); !ok || f.Value != "yes" {
		t.Fatalf("requires republish fact wrong: %+v", f)
	}
}

func TestBuildFacts_CSIDriver_QuietDefaults(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "CSIDriver",
		"spec": map[string]interface{}{
			"attachRequired": false,
			"podInfoOnMount": true,
		},
	}}
	facts := BuildFacts("storage.k8s.io", "csidrivers", u)
	if f, ok := factByLabel(facts, "attach required"); !ok || f.Value != "no" {
		t.Fatalf("attach required fact wrong: %+v", f)
	}
	// false-valued optional wells/rows are omitted entirely.
	if _, ok := factByLabel(facts, "storage capacity"); ok {
		t.Fatalf("storage capacity must be omitted when false")
	}
	if _, ok := factByLabel(facts, "requires republish"); ok {
		t.Fatalf("requires republish must be omitted when false")
	}
	if _, ok := factByLabel(facts, "lifecycle modes"); ok {
		t.Fatalf("lifecycle modes must be omitted when absent")
	}
}

func TestBuildFacts_CSINode(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "CSINode",
		"spec": map[string]interface{}{
			"drivers": []interface{}{
				map[string]interface{}{
					"name":         "disk.csi.azure.com",
					"nodeID":       "aks-node-0",
					"allocatable":  map[string]interface{}{"count": int64(8)},
					"topologyKeys": []interface{}{"topology.disk.csi.azure.com/zone"},
				},
			},
		},
	}}
	facts := BuildFacts("storage.k8s.io", "csinodes", u)
	if f, ok := factByLabel(facts, "drivers"); !ok || f.Value != "1" {
		t.Fatalf("drivers count fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "disk.csi.azure.com"); !ok || f.Value != "node aks-node-0 · max 8 · zones topology.disk.csi.azure.com/zone" {
		t.Fatalf("per-driver row fact wrong: %+v", f)
	}
}

func TestBuildFacts_VolumeAttachment(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "VolumeAttachment",
		"spec": map[string]interface{}{
			"attacher": "disk.csi.azure.com",
			"nodeName": "aks-node-0",
			"source":   map[string]interface{}{"persistentVolumeName": "pvc-abc"},
		},
		"status": map[string]interface{}{
			"attached":    true,
			"detachError": map[string]interface{}{"message": "still in use"},
		},
	}}
	facts := BuildFacts("storage.k8s.io", "volumeattachments", u)
	if f, ok := factByLabel(facts, "attached"); !ok || f.Value != "yes" || f.Tone != "success" {
		t.Fatalf("attached fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "attacher"); !ok || f.Value != "disk.csi.azure.com" {
		t.Fatalf("attacher fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "node"); !ok || f.Value != "aks-node-0" {
		t.Fatalf("node fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "source volume"); !ok || f.Value != "pvc-abc" {
		t.Fatalf("source volume fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "detach error"); !ok || f.Value != "still in use" || f.Tone != "danger" {
		t.Fatalf("detach error fact wrong: %+v", f)
	}
	if _, ok := factByLabel(facts, "attach error"); ok {
		t.Fatalf("attach error must be omitted when absent")
	}
}

func TestBuildFacts_VolumeAttachment_Detached(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":   "VolumeAttachment",
		"spec":   map[string]interface{}{"attacher": "ebs.csi.aws.com"},
		"status": map[string]interface{}{"attached": false},
	}}
	facts := BuildFacts("storage.k8s.io", "volumeattachments", u)
	if f, ok := factByLabel(facts, "attached"); !ok || f.Value != "no" || f.Tone != "" {
		t.Fatalf("detached attachment fact wrong (want no/no-tone): %+v", f)
	}
}

func TestBuildFacts_RuntimeClass(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":    "RuntimeClass",
		"handler": "gvisor",
		"overhead": map[string]interface{}{
			"podFixed": map[string]interface{}{"cpu": "250m", "memory": "120Mi"},
		},
		"scheduling": map[string]interface{}{
			"nodeSelector": map[string]interface{}{"sandbox": "true"},
			"tolerations": []interface{}{
				map[string]interface{}{"key": "sandbox", "operator": "Exists"},
			},
		},
	}}
	facts := BuildFacts("node.k8s.io", "runtimeclasses", u)
	if f, ok := factByLabel(facts, "handler"); !ok || f.Value != "gvisor" {
		t.Fatalf("handler fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "node selector"); !ok || f.Value != "sandbox=true" {
		t.Fatalf("node selector fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "tolerations"); !ok || f.Value != "1" {
		t.Fatalf("tolerations fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "pod overhead"); !ok || f.Value != "cpu=250m,memory=120Mi" {
		t.Fatalf("pod overhead fact wrong: %+v", f)
	}
}

func TestBuildFacts_RuntimeClass_Minimal(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":    "RuntimeClass",
		"handler": "runc",
	}}
	facts := BuildFacts("node.k8s.io", "runtimeclasses", u)
	if f, ok := factByLabel(facts, "handler"); !ok || f.Value != "runc" {
		t.Fatalf("handler fact wrong: %+v", f)
	}
	if _, ok := factByLabel(facts, "node selector"); ok {
		t.Fatalf("node selector must be omitted when absent")
	}
	if _, ok := factByLabel(facts, "tolerations"); ok {
		t.Fatalf("tolerations must be omitted when zero")
	}
	if _, ok := factByLabel(facts, "pod overhead"); ok {
		t.Fatalf("pod overhead must be omitted when absent")
	}
}
