package upgrade

import (
	"strings"
	"testing"
)

func TestParseMinor(t *testing.T) {
	cases := map[string]int{
		"v1.31.4":             31,
		"v1.30.3+k3s1":        30,
		"v1.29.8-eks-a737599": 29,
		"1.28.0":              28,
	}
	for in, want := range cases {
		got, ok := ParseMinor(in)
		if !ok || got != want {
			t.Errorf("%q: got %d %v", in, got, ok)
		}
	}
	if _, ok := ParseMinor("garbage"); ok {
		t.Error("garbage must not parse")
	}
}

func TestSkewFindings(t *testing.T) {
	out := SkewFindings("v1.31.4", map[string]string{
		"fresh":    "v1.31.4", // in sync: no finding
		"one-back": "v1.30.6", // normal mid-upgrade: no finding
		"edge":     "v1.28.9", // 3 behind: warn
		"ancient":  "v1.27.1", // 4 behind: danger
		"newer":    "v1.32.0", // kubelet newer: danger
	})
	byNode := map[string]SkewFinding{}
	for _, f := range out {
		byNode[f.Node] = f
	}
	if len(out) != 3 {
		t.Fatalf("want 3 findings, got %+v", out)
	}
	if byNode["edge"].Level != "warn" || byNode["ancient"].Level != "danger" {
		t.Fatalf("levels: %+v", byNode)
	}
	if byNode["newer"].Level != "danger" || !strings.Contains(byNode["newer"].Detail, "NEWER") {
		t.Fatalf("newer kubelet: %+v", byNode["newer"])
	}
}

func TestRemovalVerdict(t *testing.T) {
	api := DeprecatedAPI{RemovedInMinor: 29}
	if v := RemovalVerdict(api, 29); v != "REMOVED in 1.29" {
		t.Fatalf("at target: %q", v)
	}
	if v := RemovalVerdict(api, 28); !strings.Contains(v, "after the target") {
		t.Fatalf("before target: %q", v)
	}
}

func TestEOLDate(t *testing.T) {
	if _, ok := EOLDate(31); !ok {
		t.Fatal("1.31 must have an EOL date")
	}
	if _, ok := EOLDate(12); ok {
		t.Fatal("ancient minor must be absent")
	}
}
