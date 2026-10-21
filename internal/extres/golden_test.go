package extres

import (
	"encoding/json"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// S11a §4: Klyx's extended-request formula equals the unmodified upstream
// component-helpers PodRequests (status resources off) at every supported
// minor, v0.32.13 to v0.37.1 (testdata/extreq_golden.json, with each
// module's options and go.sum hash).
func TestRequestsEqualUpstreamPodRequests(t *testing.T) {
	var fixtures []struct {
		Name string     `json:"name"`
		Pod  corev1.Pod `json:"pod"`
	}
	var golden struct {
		Models map[string]struct {
			Module  string                      `json:"module"`
			Results map[string]map[string]int64 `json:"results"`
		} `json:"models"`
	}
	for path, v := range map[string]any{"testdata/extreq_fixtures.json": &fixtures, "testdata/extreq_golden.json": &golden} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	if len(golden.Models) != 6 || len(fixtures) < 12 {
		t.Fatalf("golden: %d models, %d fixtures", len(golden.Models), len(fixtures))
	}
	// a node advertising every name, so every pair counts against it
	big := resource.MustParse("9223372036854775807")
	all := corev1.ResourceList{"nvidia.com/gpu": big, "nvidia.com/mig-1g.5gb": big, "example.com/x": big}
	for minor, model := range golden.Models {
		for _, f := range fixtures {
			p := f.Pod
			p.Spec.NodeName = "n"
			r := Reserve([]*corev1.Node{node("n", all, all.DeepCopy(), nil)}, []*corev1.Pod{&p})
			want := model.Results[f.Name]
			for _, e := range r.Rows[0].Entries {
				w, ok := want[e.Name]
				if !ok {
					w = 0
				}
				if e.Requested != Known(w) {
					t.Errorf("%s %s %s: Klyx %+v, %s %d", minor, f.Name, e.Name, e.Requested, model.Module, w)
				}
			}
		}
	}
}
