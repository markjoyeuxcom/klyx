package crd

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// A protected kind's list (coverage S0b §3, revision 3): the server's cells
// are never released, and cells evaluated over the sanitised object cannot
// carry a payload even when a column definition changed between reads.
func TestLocalPage_ServerCellsNeverReleased(t *testing.T) {
	obj := map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSet",
		"metadata": map[string]any{"name": "rs", "namespace": "team-a", "creationTimestamp": "2026-10-16T12:00:00Z"},
		"spec":     map[string]any{"inputs": []any{map[string]any{"value": ndl}}, "resourcesTemplate": ndl},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}
	raw, _ := json.Marshal(obj)
	tbl := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{{Name: "Name", Format: "name"}, {Name: "Value", Type: "string",
			Description: "Custom resource definition column (in JSONPath format): .status.phase"}},
		Rows: []metav1.TableRow{{Cells: []any{"rs", "server-only-" + ndl}, Object: runtime.RawExtension{Raw: raw}}},
	}
	// The cached definition says Value selects .spec.inputs[0].value (it
	// changed since the Table was produced), plus an ancestor and a safe one.
	cols := LocalColumns([]PrinterColumn{
		{Name: "Value", Type: "string", JSONPath: ".spec.inputs[0].value"},
		{Name: "Spec", Type: "string", JSONPath: ".spec"},
		{Name: "Ready", Type: "string", JSONPath: `.status.conditions[?(@.type=="Ready")].status`},
	})
	page, err := LocalPage(tbl, cols)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(page)
	if strings.Contains(string(b), ndl) {
		t.Fatalf("a payload crossed: %s", b)
	}
	cells := page.Items[0].Cells
	// the ancestor column prints the sanitised subtree as JSON (its "<" is
	// escaped), so it shows placeholders, never the payload
	if cells[0] != OmittedValue || !strings.Contains(cells[1], "omitted") || cells[2] != "True" {
		t.Fatalf("cells: %q", cells)
	}
	// no definition: no custom cells, the rows still list
	if p, _ := LocalPage(tbl, nil); p.Columns != nil || p.Items[0].Cells != nil || p.Items[0].Name != "rs" {
		t.Fatalf("no definition: %+v", p)
	}
}

func TestLocalSummary(t *testing.T) {
	s := SanitizeEmbedded("kustomize.toolkit.fluxcd.io", "Kustomization", map[string]any{
		"spec": map[string]any{"path": "./apps", "postBuild": map[string]any{"substitute": map[string]any{"T": ndl}}}})
	sum := LocalSummary(LocalColumns([]PrinterColumn{{Name: "Path", Type: "string", JSONPath: ".spec.path"}, {Name: "Vars", Type: "string", JSONPath: ".spec.postBuild.substitute"}}), s.Object)
	b, _ := json.Marshal(sum)
	if strings.Contains(string(b), ndl) || sum[0].Value != "./apps" {
		t.Fatalf("summary: %s", b)
	}
}
