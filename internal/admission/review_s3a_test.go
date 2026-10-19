package admission

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"testing"
)

func TestReviewS3aParamGVKIsLossless(t *testing.T) {
	group := strings.Repeat("a", 62) + "." + strings.Repeat("b", 62) + "." + strings.Repeat("c", 62) + "." + strings.Repeat("d", 62)
	av := group + "/v1"
	p := ProjectPolicy(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "p"}, "spec": map[string]any{"paramKind": map[string]any{"apiVersion": av, "kind": "Limit"}}}}, Validating)
	g, v, k, _ := p.ParamKind.GVK()
	if g != group || v != "v1" || k != "Limit" {
		t.Fatalf("classification GVK clipped from %q to %q", av, p.ParamKind.APIVersion)
	}
}
