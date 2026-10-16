package cachediff

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/crd"
)

func rs(template, secretValue, wait string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSet",
		"metadata": map[string]any{"name": "rs", "namespace": "team-a",
			"annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": template}},
		"spec": map[string]any{
			"resourcesTemplate": template,
			"resources": []any{map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "s"},
				"stringData": map[string]any{"p": secretValue}}},
			"wait": wait,
		},
	}}
}

// Coverage S0b §6: a change that sanitises to identical text is still
// reported; an unchanged omitted section beside a visible change is not;
// last-applied is never reported; and the diff text carries no payload.
func TestOmittedChanges(t *testing.T) {
	cases := []struct {
		name       string
		live, dry  *unstructured.Unstructured
		want       []string
		visibleDif bool
	}{
		{"same-length template change", rs("aaaa", "x", "true"), rs("bbbb", "x", "true"), []string{"spec.resourcesTemplate"}, false},
		{"secret value-only change", rs("t", "PAYLOADold1", "true"), rs("t", "PAYLOADnew2", "true"), []string{"spec.resources[0].stringData"}, false},
		{"unchanged omitted beside a visible change", rs("t", "x", "true"), rs("t", "x", "false"), nil, true},
	}
	for _, c := range cases {
		got := omittedChanges(c.live, c.dry)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		ls := crd.SanitizeEmbedded("fluxcd.controlplane.io", "ResourceSet", c.live.Object)
		ds := crd.SanitizeEmbedded("fluxcd.controlplane.io", "ResourceSet", c.dry.Object)
		diff, err := unifiedDiff(&unstructured.Unstructured{Object: ls.Object}, &unstructured.Unstructured{Object: ds.Object})
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"aaaa", "bbbb", "PAYLOADold1", "PAYLOADnew2"} {
			if strings.Contains(diff, needle) {
				t.Errorf("%s: %q crossed in the diff:\n%s", c.name, needle, diff)
			}
		}
		if c.visibleDif != (diff != "") {
			t.Errorf("%s: visible diff %v, got %q", c.name, c.visibleDif, diff)
		}
	}
}

// Codex's S0b implementation review, finding 7: a value-only change under a
// path with dotted segments (Jsonnet extVars, an ApplicationSet plugin's
// input parameters) is still reported.
func TestOmittedChangesNestedSegments(t *testing.T) {
	app := func(v string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"spec": map[string]any{"source": map[string]any{"directory": map[string]any{"jsonnet": map[string]any{
				"extVars": []any{map[string]any{"name": "x", "value": v}}}}}}}}
	}
	if got := omittedChanges(app("first"), app("other")); len(got) == 0 {
		t.Error("a Jsonnet value-only change is invisible")
	}
	set := func(v string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet",
			"spec": map[string]any{"generators": []any{map[string]any{"plugin": map[string]any{
				"input": map[string]any{"parameters": map[string]any{"value": v}}}}}}}}
	}
	if got := omittedChanges(set("first"), set("other")); len(got) == 0 {
		t.Error("a plugin input parameter change is invisible")
	}
	if got := omittedChanges(set("same"), set("same")); len(got) != 0 {
		t.Errorf("no change reported as one: %v", got)
	}
}
