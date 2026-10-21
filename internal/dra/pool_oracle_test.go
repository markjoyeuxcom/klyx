package dra

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The pool oracle (S11b §9): testdata/pools holds the fixtures and the
// outcomes of the scheduler's own allocator (structured.NewAllocator) at
// v0.33.13 to v0.37.1, run from every node a fixture names and from n9,
// which none names, with the partitionable-devices feature on and off where
// a fixture uses it (MODULES records each module's hash; the generator
// lives in the session scratchpad, s11b-golden). Klyx's per-variant outcome
// must equal the oracle's everywhere, except where Klyx says not
// established: a node selector, and counters under a 1.33 or 1.34
// scheduler.

type oracleRow struct {
	Name    string   `json:"name"`
	Node    string   `json:"node"`
	Variant string   `json:"variant"`
	Outcome string   `json:"outcome"`
	Devices []string `json:"devices"`
}

type oracleFixture struct {
	Name   string           `json:"name"`
	Slices []map[string]any `json:"slices"`
}

func TestPoolsEqualTheSchedulersAllocator(t *testing.T) {
	var fx []oracleFixture
	readJSON(t, "testdata/pools/fixtures.json", &fx)
	slicesOf := map[string][]Slice{}
	for _, f := range fx {
		for _, m := range f.Slices {
			slicesOf[f.Name] = append(slicesOf[f.Name], ProjectSlice(&unstructured.Unstructured{Object: m}))
		}
	}
	minors := map[string]int{"v0.33.13": 33, "v0.34.12": 34, "v0.35.9": 35, "v0.36.5": 36, "v0.37.1": 37}
	compared, unestablished := 0, 0
	for file, minor := range minors {
		var rows []oracleRow
		readJSON(t, "testdata/pools/oracle-"+file+".json", &rows)
		if len(rows) != 62 {
			t.Fatalf("%s: %d oracle rows", file, len(rows))
		}
		for _, r := range rows {
			node := r.Node
			if node == "n9" {
				node = ""
			}
			got := ViewOutcome(slicesOf[r.Name], node, Variant{Minor: minor, Partitionable: strings.Contains(r.Variant, "PartitionableDevices=on")})
			where := file + " " + r.Name + " " + r.Node + " " + r.Variant
			if got.Kind == OutUnestablished {
				// only a selector, or counters under 1.33/1.34, may stay unestablished
				if !strings.Contains(got.Rule, "node selector") && !(minor <= 34 && strings.Contains(got.Rule, "counter-based")) {
					t.Errorf("%s: not established (%s), the oracle says %s", where, got.Rule, r.Outcome)
				}
				unestablished++
				continue
			}
			want := r.Outcome
			kind := got.Kind
			if kind == OutObsolete {
				kind = OutNone // the allocator neither errs nor allocates
			}
			if kind != want {
				t.Errorf("%s: Klyx %s %v, the oracle %s %v", where, got.Kind, got.Devices, r.Outcome, r.Devices)
				continue
			}
			if want == OutComplete && !reflect.DeepEqual(got.Devices, sorted(r.Devices)) {
				t.Errorf("%s: devices %v, the oracle %v", where, got.Devices, r.Devices)
			}
			compared++
		}
	}
	// 15 selector views (three fixtures, five minors) and 40 counter views
	// (20 per minor at 1.33 and 1.34) stay not established; every other
	// outcome is compared
	if compared != 255 || unestablished != 55 {
		t.Fatalf("%d outcomes compared, %d not established; want 255 and 55", compared, unestablished)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
