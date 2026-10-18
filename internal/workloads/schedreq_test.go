package workloads

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type schedFixture struct {
	Name string     `json:"name"`
	Pod  corev1.Pod `json:"pod"`
}

type schedGolden struct {
	Models []struct {
		Minor   string                      `json:"minor"`
		Module  string                      `json:"module"`
		Options string                      `json:"options"`
		GoSum   []string                    `json:"goSum"`
		Results map[string]map[string]int64 `json:"results"`
	} `json:"models"`
}

func loadSchedGolden(t *testing.T) ([]schedFixture, schedGolden) {
	t.Helper()
	var fs []schedFixture
	var g schedGolden
	for path, v := range map[string]any{"testdata/schedreq_fixtures.json": &fs, "testdata/schedreq_golden.json": &g} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	if len(fs) == 0 || len(g.Models) != len(schedModels) {
		t.Fatalf("golden data incomplete: %d fixtures, %d models", len(fs), len(g.Models))
	}
	return fs, g
}

// Coverage S6 §2: each model equals the unmodified upstream helper of its
// minor, at the scheduler's default-gate options, on every fixture.
func TestSchedModelsMatchUpstreamGolden(t *testing.T) {
	fs, g := loadSchedGolden(t)
	for _, gm := range g.Models {
		minor, err := strconv.Atoi(strings.TrimPrefix(gm.Minor, "1."))
		if err != nil {
			t.Fatal(err)
		}
		if gm.Module == "" || gm.Options == "" || len(gm.GoSum) < 4 {
			t.Fatalf("1.%d: provenance missing", minor)
		}
		m := SchedModelFor(minor)
		for _, f := range fs {
			want := gm.Results[f.Name]
			got := m.Requests(&f.Pod)
			cpu, mem := got[corev1.ResourceCPU], got[corev1.ResourceMemory]
			if cpu.MilliValue() != want["cpuMilli"] || mem.Value() != want["memoryBytes"] {
				t.Errorf("1.%d %s: cpu %dm mem %d, upstream %dm %d", minor, f.Name, cpu.MilliValue(), mem.Value(), want["cpuMilli"], want["memoryBytes"])
			}
		}
	}
}

// The unknown model is the per-resource maximum over every model, per pod.
func TestSchedUnknownIsTheMaximum(t *testing.T) {
	fs, g := loadSchedGolden(t)
	for _, f := range fs {
		var maxCPU, maxMem int64
		for _, gm := range g.Models {
			maxCPU = max(maxCPU, gm.Results[f.Name]["cpuMilli"])
			maxMem = max(maxMem, gm.Results[f.Name]["memoryBytes"])
		}
		got := SchedUnknown.Requests(&f.Pod)
		cpu, mem := got[corev1.ResourceCPU], got[corev1.ResourceMemory]
		if cpu.MilliValue() != maxCPU || mem.Value() != maxMem {
			t.Errorf("%s: unknown %dm %d, max %dm %d", f.Name, cpu.MilliValue(), mem.Value(), maxCPU, maxMem)
		}
	}
}

func TestSchedModelSelection(t *testing.T) {
	cases := map[string]string{
		"v1.37.1": "assuming kube-scheduler 1.37", "v1.34.2-eks-113cf36": "assuming kube-scheduler 1.34",
		"v1.31.0+k3s1": "1.32 or older", "v1.40.0": "newer than Klyx's models", "": "unknown", "garbage": "unknown",
	}
	for v, want := range cases {
		if got := SchedModelForVersion(v).Assumption(); !strings.Contains(got, want) {
			t.Errorf("%q: %q", v, got)
		}
	}
	if alt, ok := SchedModelFor(37).Alternative(); !ok || alt.Minor != 36 {
		t.Fatalf("1.37's alternative is 1.36: %+v", alt)
	}
	if _, ok := SchedModelFor(32).Alternative(); ok {
		t.Fatal("1.32 has no modelled alternative")
	}
	if _, ok := SchedUnknown.Alternative(); ok {
		t.Fatal("unknown already takes every model")
	}
}

// Desired is spec only: a pod mid-resize needs its desired request on a
// new node, never its old allocation.
func TestSchedDesiredIsSpec(t *testing.T) {
	fs, _ := loadSchedGolden(t)
	for _, f := range fs {
		if f.Name != "down-deferred" {
			continue
		}
		d := SchedModelFor(37).Desired(&f.Pod)
		if cpu := d[corev1.ResourceCPU]; cpu.Cmp(resource.MustParse("100m")) != 0 {
			t.Fatalf("desired cpu: %s", cpu.String())
		}
	}
}
