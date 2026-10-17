package velero

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func restoreObj(spec, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1", "kind": "Restore",
		"metadata": map[string]any{"namespace": "velero", "name": "r1", "uid": "11111111-2222-3333-4444-555555555555", "creationTimestamp": "2026-10-18T01:00:00Z"},
		"spec":     spec, "status": status,
	}}
}

// Hooks, selectors and free text never cross; names are validated; the
// resource modifier is named only (S2b §5).
func TestProjectRestoreWithholds(t *testing.T) {
	u := restoreObj(map[string]any{
		"backupName": "daily-20261017020000", "scheduleName": "Not_A_Name " + needle,
		"includedNamespaces": []any{"apps", needle + "/x"}, "excludedNamespaces": []any{"kube-system"},
		"namespaceMapping":   map[string]any{"apps": "apps-restored", needle: "x"},
		"labelSelector":      map[string]any{"matchLabels": map[string]any{"app": needle}},
		"orLabelSelectors":   []any{map[string]any{"matchLabels": map[string]any{"k": needle}}},
		"hooks":              map[string]any{"resources": []any{map[string]any{"name": "h", "postHooks": []any{map[string]any{"exec": map[string]any{"command": []any{"sh", "-c", needle}}}}}}},
		"existingResourcePolicy": "update", "itemOperationTimeout": "4h0m0s",
		"resourceModifier": map[string]any{"kind": "ConfigMap", "name": "mods"},
		"resourcePolicy":   map[string]any{"kind": "Secret", "name": "not-shown"},
		"restorePVs":       true,
	}, map[string]any{
		"phase": "Failed", "failureReason": "restore execution failed: " + needle, "errors": int64(3),
		"validationErrors": []any{"No backups found for schedule", "https://u:" + needle + "@h/x"},
		"progress":         map[string]any{"totalItems": int64(10), "itemsRestored": int64(4)},
	})
	r := ProjectRestore(u)
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), needle) {
		t.Fatalf("disclosed: %s", b)
	}
	if r.BackupName != "daily-20261017020000" || !r.ScheduleNameInvalid || r.ScheduleName != "" {
		t.Fatalf("source: %+v", r)
	}
	if r.IncludedCount != 2 || r.Included[1] != "(invalid name)" || r.MappingCount != 2 || r.Mapping[1].From != "apps" && r.Mapping[0].From != "(invalid name)" {
		t.Fatalf("namespaces: %+v %+v", r.Included, r.Mapping)
	}
	if !r.LabelSelector || r.OrLabelSelectors != 1 || r.Hooks != 1 || r.ResourceModifier != "mods" || r.ResourcePolicy != "" {
		t.Fatalf("counts: %+v", r)
	}
	if r.Failure == nil || r.Failure.Code != "restore-execution-failed" || r.ValidationCount != 2 || r.Validation[0].Code != "restore-schedule-none" || !r.Validation[1].Withheld {
		t.Fatalf("classes: %+v %+v", r.Failure, r.Validation)
	}
	if r.ItemsTotal != 10 || r.ItemsRestored != 4 || r.Errors != 3 || r.RestorePVs == nil || !*r.RestorePVs {
		t.Fatalf("status: %+v", r)
	}
}

// The worst case the CRD allows fits MaxObject: the lists are cut from
// their tails, their counts kept (S2b §3).
func TestProjectRestoreFitsWorstCase(t *testing.T) {
	long := func(i int) string { return fmt.Sprintf("%s%02d", strings.Repeat("n", 61), i) }
	var inc, exc []any
	mapping := map[string]any{}
	for i := 0; i < 100; i++ {
		inc, exc = append(inc, long(i)), append(exc, long(i))
		mapping[long(i)] = long(99 - i)
	}
	var ve []any
	for i := 0; i < 10; i++ {
		ve = append(ve, "Error retrieving backup: AccessDenied no such host StatusCode: 403 ExpiredToken")
	}
	u := restoreObj(map[string]any{"backupName": strings.Repeat("b", 253), "scheduleName": strings.Repeat("s", 253),
		"includedNamespaces": inc, "excludedNamespaces": exc, "namespaceMapping": mapping,
		"existingResourcePolicy": strings.Repeat("x", 500), "itemOperationTimeout": strings.Repeat("y", 500),
		"resourceModifier": map[string]any{"kind": "ConfigMap", "name": strings.Repeat("m", 253)},
		"resourcePolicy":   map[string]any{"kind": "configmap", "name": strings.Repeat("p", 253)},
	}, map[string]any{"phase": strings.Repeat("P", 500), "validationErrors": ve, "failureReason": "restore execution failed: AccessDenied no such host StatusCode: 503"})
	r := ProjectRestore(u)
	if r.Size > MaxObject || size(r) != r.Size {
		t.Fatalf("size %d (measured %d) > %d", r.Size, size(r), MaxObject)
	}
	if !r.Cut || r.MappingCount != 100 || r.IncludedCount != 100 || r.ExcludedCount != 100 {
		t.Fatalf("counts: cut=%v %d %d %d", r.Cut, r.MappingCount, r.IncludedCount, r.ExcludedCount)
	}
	if len(r.Mapping) > maxMapping || len(r.Included) > maxRestoreNamespaces {
		t.Fatalf("caps: %d %d", len(r.Mapping), len(r.Included))
	}
	// a small Restore is never cut
	if s := ProjectRestore(restoreObj(map[string]any{"backupName": "b"}, map[string]any{"phase": "Completed"})); s.Cut {
		t.Fatal("cut a small restore")
	}
}

func TestProjectRestoresNewestFirst(t *testing.T) {
	a, b := restoreObj(nil, nil), restoreObj(nil, nil)
	a.SetName("a")
	b.SetName("b")
	b.Object["metadata"].(map[string]any)["creationTimestamp"] = "2026-10-18T02:00:00Z"
	out := ProjectRestores([]unstructured.Unstructured{*a, *b})
	if out[0].Name != "b" || out[1].Name != "a" {
		t.Fatalf("%s %s", out[0].Name, out[1].Name)
	}
}
