package loki

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func names(bs []Branch) []string {
	var out []string
	for _, b := range bs {
		n := b.NSLabel + "/" + []string{"pod", "k8s_pod_name", "neither"}[b.Pod]
		if b.WithoutNamespace {
			n += "/-namespace"
		}
		out = append(out, n)
	}
	return out
}

// S8b §3: the labels only prune; the "neither" branch always runs; both
// namespace spellings keep their streams disjoint (Codex's review of
// 6438d43: a union of names cannot choose one shape for all streams).
func TestBranches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels []string
		want   string
	}{
		{"sd", []string{"cluster", "namespace", "pod", "container", "job", "stream", "service_name"}, "[namespace/pod namespace/neither]"},
		{"alloy", []string{"cluster", "k8s_cluster_name", "namespace", "container", "job", "service_name"}, "[namespace/neither]"},
		{"otel", []string{"k8s_cluster_name", "k8s_namespace_name", "k8s_pod_name", "k8s_container_name", "service_name"}, "[k8s_namespace_name/k8s_pod_name k8s_namespace_name/neither]"},
		{"sd and alloy under one match", []string{"cluster", "namespace", "pod", "container"}, "[namespace/pod namespace/neither]"},
		{"all three under one match", []string{"cluster", "k8s_cluster_name", "namespace", "k8s_namespace_name", "pod", "k8s_pod_name"},
			"[namespace/pod namespace/k8s_pod_name namespace/neither k8s_namespace_name/pod/-namespace k8s_namespace_name/k8s_pod_name/-namespace k8s_namespace_name/neither/-namespace]"},
	} {
		got, err := Branches(tc.labels)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if fmt.Sprint(names(got)) != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, names(got), tc.want)
		}
	}
	if _, err := Branches([]string{"cluster", "pod"}); !errors.Is(err, ErrNoNamespaceLabel) {
		t.Fatalf("no namespace label: %v", err)
	}
}

// Each branch's LogQL, in both modes, with and without a container; name
// mode never adds the UID, even where a shipper indexed it.
func TestQuery(t *testing.T) {
	tg := Target{Match: map[string]string{"cluster": "prd", "a": "x"}, Namespace: "team", Pod: "api-1", UID: "u-1"}
	for _, tc := range []struct {
		b    Branch
		m    Mode
		c    string
		want string
	}{
		{Branch{NSLabel: "namespace", Pod: PodIndexed}, ModeName, "",
			`{a="x", cluster="prd", namespace="team", pod="api-1"}`},
		{Branch{NSLabel: "k8s_namespace_name", WithoutNamespace: true, Pod: K8sPodIndexed}, ModeUID, "",
			`{a="x", cluster="prd", k8s_namespace_name="team", namespace="", k8s_pod_name="api-1", pod=""} | k8s_pod_uid="u-1"`},
		{Branch{NSLabel: "namespace", Pod: PodNotIndexed}, ModeName, "web",
			`{a="x", cluster="prd", namespace="team", pod="", k8s_pod_name=""} | pod="api-1" or k8s_pod_name="api-1" | container="web" or k8s_container_name="web"`},
		{Branch{NSLabel: "namespace", Pod: PodNotIndexed}, ModeUID, "web",
			`{a="x", cluster="prd", namespace="team", pod="", k8s_pod_name=""} | pod="api-1" or k8s_pod_name="api-1" | container="web" or k8s_container_name="web" | k8s_pod_uid="u-1"`},
	} {
		tg.Container = tc.c
		if got := Query(tc.b, tg, tc.m); got != tc.want {
			t.Errorf("\n got %s\nwant %s", got, tc.want)
		}
	}
	tg.Container = ""
	if q := Query(Branch{NSLabel: "namespace", Pod: PodIndexed}, tg, ModeName); strings.Contains(q, "k8s_pod_uid") {
		t.Fatalf("name mode added the UID: %s", q)
	}
	// values are quoted
	q := Query(Branch{NSLabel: "namespace", Pod: PodIndexed}, Target{Namespace: `te"am`, Pod: `a\b`}, ModeName)
	if q != `{namespace="te\"am", pod="a\\b"}` {
		t.Fatalf("quoting: %s", q)
	}
}

// S8b §5: strict parsing, both encodings.
func TestParseStreams(t *testing.T) {
	cat := `{"status":"success","data":{"resultType":"streams","encodingFlags":["categorize-labels"],"result":[
	  {"stream":{"namespace":"n","pod":"p"},"values":[["1792418924000000000","line a",{"structuredMetadata":{"k8s_pod_uid":"u","detected_level":"info"}}],
	                                                 ["1792418864000000000","line b",{}]]}]}}`
	es, err := ParseStreams([]byte(cat))
	if err != nil || len(es) != 2 || es[0].UID() != "u" || es[0].Stream["pod"] != "p" || es[0].TS != 1792418924000000000 || es[1].Meta == nil || es[1].UID() != "" {
		t.Fatalf("categorized: %+v %v", es, err)
	}
	merged := `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"namespace":"n","k8s_pod_uid":"u2"},"values":[["5","x"]]}]}}`
	es, err = ParseStreams([]byte(merged))
	if err != nil || len(es) != 1 || es[0].Meta != nil || es[0].UID() != "u2" {
		t.Fatalf("merged: %+v %v", es, err)
	}
	empty := `{"status":"success","data":{"resultType":"streams","result":[]}}`
	if es, err := ParseStreams([]byte(empty)); err != nil || len(es) != 0 {
		t.Fatalf("empty: %v %v", es, err)
	}
	for name, body := range map[string]string{
		"null":          `null`,
		"not json":      `max entries limit per query exceeded`,
		"error status":  `{"status":"error","data":{"resultType":"streams","result":[]}}`,
		"no data":       `{"status":"success"}`,
		"matrix":        `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
		"null result":   `{"status":"success","data":{"resultType":"streams","result":null}}`,
		"null stream":   `{"status":"success","data":{"resultType":"streams","result":[null]}}`,
		"no values":     `{"status":"success","data":{"resultType":"streams","result":[{"stream":{}}]}}`,
		"bad ts":        `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["yesterday","x"]]}]}}`,
		"numeric ts":    `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[[5,"x"]]}]}}`,
		"one element":   `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["5"]]}]}}`,
		"bad meta":      `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["5","x",{"structuredMetadata":"no"}]]}]}}`,
		"second is bad": `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["5","x"],["6",7]]}]}}`,
	} {
		if es, err := ParseStreams([]byte(body)); !errors.Is(err, ErrShape) || es != nil {
			t.Errorf("%s: %v %v", name, es, err)
		}
	}
}

// selects is a branch's stream selector and filters applied to one stream,
// with LogQL's semantics: an empty matcher selects a stream without the
// label; a label filter reads index labels and structured metadata.
func selects(b Branch, t Target, stream, meta map[string]string) bool {
	get := func(k string) (string, bool) {
		if v, ok := stream[k]; ok {
			return v, true
		}
		v, ok := meta[k]
		return v, ok
	}
	if stream[b.NSLabel] != t.Namespace {
		return false
	}
	if _, ok := stream[LabelNamespace]; b.WithoutNamespace && ok {
		return false
	}
	_, hasPod := stream[LabelPod]
	_, hasK8sPod := stream[LabelK8sPod]
	switch b.Pod {
	case PodIndexed:
		return stream[LabelPod] == t.Pod
	case K8sPodIndexed:
		return stream[LabelK8sPod] == t.Pod && !hasPod
	}
	if hasPod || hasK8sPod {
		return false
	}
	v1, ok1 := get(LabelPod)
	v2, ok2 := get(LabelK8sPod)
	return (ok1 && v1 == t.Pod) || (ok2 && v2 == t.Pod)
}

// Every shape a pod's stream can take - each namespace spelling or both,
// the pod indexed as pod, as k8s_pod_name, as both, or as structured
// metadata in either spelling - is selected by exactly one branch, with
// every shape under one match (Codex's model of c2b4e26, as a test).
func TestBranchesSelectEveryShapeExactlyOnce(t *testing.T) {
	tg := Target{Namespace: "team", Pod: "api-1"}
	nsShapes := []map[string]string{{"namespace": "team"}, {"k8s_namespace_name": "team"}, {"namespace": "team", "k8s_namespace_name": "team"}}
	type podShape struct{ stream, meta map[string]string }
	podShapes := []podShape{
		{map[string]string{"pod": "api-1"}, nil},
		{map[string]string{"k8s_pod_name": "api-1"}, nil},
		{map[string]string{"pod": "api-1", "k8s_pod_name": "api-1"}, nil},
		{nil, map[string]string{"pod": "api-1"}},
		{nil, map[string]string{"k8s_pod_name": "api-1"}},
		{nil, map[string]string{"pod": "api-1", "k8s_pod_name": "api-1"}},
		{map[string]string{"pod": "api-1"}, map[string]string{"k8s_pod_name": "api-1"}},
	}
	// the labels read lists every name any stream carries
	bs, err := Branches([]string{"namespace", "k8s_namespace_name", "pod", "k8s_pod_name"})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ns := range nsShapes {
		for _, ps := range podShapes {
			stream := map[string]string{}
			for k, v := range ns {
				stream[k] = v
			}
			for k, v := range ps.stream {
				stream[k] = v
			}
			hits := 0
			for _, b := range bs {
				if selects(b, tg, stream, ps.meta) {
					hits++
				}
			}
			if hits != 1 {
				t.Errorf("stream %v meta %v: selected by %d branches", stream, ps.meta, hits)
			}
			// another pod's stream of the same shape is selected by none
			other := Target{Namespace: "team", Pod: "api-2"}
			for _, b := range bs {
				if selects(b, other, stream, ps.meta) {
					t.Errorf("another pod selected stream %v", stream)
				}
			}
			n++
		}
	}
	if n != 21 {
		t.Fatalf("shapes: %d", n)
	}
}
