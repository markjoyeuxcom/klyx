package alertmanager

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseAlerts(t *testing.T) {
	body := `[
	 {"fingerprint":"a1","labels":{"alertname":"A","severity":"critical"},"annotations":{"summary":"s"},
	  "startsAt":"2026-10-19T10:00:00Z","updatedAt":"2026-10-19T10:01:00.5Z","endsAt":"2026-10-19T12:00:00Z",
	  "status":{"state":"active","silencedBy":[],"inhibitedBy":[],"mutedBy":[]},
	  "receivers":[{"name":"lab"},{"name":"lab"}],"generatorURL":"http://prom/graph"},
	 {"fingerprint":"b2","labels":{"alertname":"B"},"status":{"state":"suppressed","silencedBy":["s1"],"inhibitedBy":[]},"receivers":[]},
	 {"fingerprint":"c3","labels":{},"status":{"state":"unprocessed"}},
	 {"fingerprint":"d4","labels":{"alertname":"D"},"status":{"state":"pending-upstream"}}
	]`
	got, err := ParseAlerts([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].State != "active" || got[1].State != "suppressed" || got[2].State != "unprocessed" || got[3].State != "pending-upstream" {
		t.Fatalf("states: %+v", got)
	}
	a := got[0]
	if !reflect.DeepEqual(a.Receivers, []string{"lab", "lab"}) || a.Annotations["summary"] != "s" || a.GeneratorURL != "http://prom/graph" ||
		!a.UpdatedAt.Equal(time.Date(2026, 10, 19, 10, 1, 0, 5e8, time.UTC)) || !a.EndsAt.Equal(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("alert: %+v", a)
	}
	if !reflect.DeepEqual(got[1].SilencedBy, []string{"s1"}) {
		t.Fatalf("silenced: %+v", got[1])
	}
}

// A response that is not the v2 shape fails the read whole (S8a §3).
func TestParseFailsWhole(t *testing.T) {
	for name, body := range map[string]string{
		"not json":          `<html>`,
		"an object":         `{"alerts":[]}`,
		"no fingerprint":    `[{"labels":{},"status":{"state":"active"}}]`,
		"empty fingerprint": `[{"fingerprint":"","labels":{},"status":{"state":"active"}}]`,
		"no labels":         `[{"fingerprint":"a","status":{"state":"active"}}]`,
		"no status":         `[{"fingerprint":"a","labels":{}}]`,
		"no state":          `[{"fingerprint":"a","labels":{},"status":{}}]`,
		"bad time":          `[{"fingerprint":"a","labels":{},"status":{"state":"active"},"startsAt":"yesterday"}]`,
		"second bad":        `[{"fingerprint":"a","labels":{},"status":{"state":"active"}},{"fingerprint":"b","labels":{}}]`,
	} {
		if got, err := ParseAlerts([]byte(body)); !errors.Is(err, ErrShape) || got != nil {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	for name, body := range map[string]string{
		"not json":       `nope`,
		"no receiver":    `[{"labels":{},"alerts":[]}]`,
		"empty receiver": `[{"labels":{},"receiver":{"name":""},"alerts":[]}]`,
		"alert no fp":    `[{"labels":{},"receiver":{"name":"r"},"alerts":[{"status":{"state":"active"}}]}]`,
		"alert no state": `[{"labels":{},"receiver":{"name":"r"},"alerts":[{"fingerprint":"a","status":{}}]}]`,
	} {
		if got, err := ParseGroups([]byte(body)); !errors.Is(err, ErrShape) || got != nil {
			t.Errorf("groups %s: %v %v", name, got, err)
		}
	}
	if _, err := ParseStatus([]byte(`{"cluster":{"status":"ready"}}`)); !errors.Is(err, ErrShape) {
		t.Errorf("status without versionInfo: %v", err)
	}
}

func TestParseGroupsAndStatus(t *testing.T) {
	g, err := ParseGroups([]byte(`[{"labels":{"alertname":"M"},"receiver":{"name":"lab-muted"},
	  "alerts":[{"fingerprint":"m1","labels":{},"status":{"state":"suppressed","mutedBy":["always"],"silencedBy":[],"inhibitedBy":[]}}]}]`))
	if err != nil || len(g) != 1 || g[0].Receiver != "lab-muted" || g[0].Alerts[0].Fingerprint != "m1" || !reflect.DeepEqual(g[0].Alerts[0].MutedBy, []string{"always"}) {
		t.Fatalf("groups: %+v %v", g, err)
	}
	s, err := ParseStatus([]byte(`{"versionInfo":{"version":"0.34.1"},"cluster":{"status":"ready","peers":[{},{},{}]},"uptime":"x","config":{}}`))
	if err != nil || s.Version != "0.34.1" || s.ClusterStatus != "ready" || s.Peers != 3 {
		t.Fatalf("status: %+v %v", s, err)
	}
}

func TestFilterParamsAndMatches(t *testing.T) {
	got := FilterParams(map[string]string{"env": "prd", "cluster": `a"b\c`})
	if !reflect.DeepEqual(got, []string{`cluster="a\"b\\c"`, `env="prd"`}) {
		t.Fatalf("filter: %q", got)
	}
	m := map[string]string{"cluster": "a"}
	if !Matches(map[string]string{"cluster": "a", "x": "y"}, m) || Matches(map[string]string{"cluster": "b"}, m) || Matches(map[string]string{}, m) || !Matches(map[string]string{}, nil) {
		t.Fatal("matches")
	}
}

func TestSeverityOf(t *testing.T) {
	for v, want := range map[string]Severity{"critical": SeverityCritical, "CRITICAL": SeverityCritical, "Warning": SeverityWarning,
		"info": SeverityOther, "none": SeverityOther, "": SeverityOther, "page": SeverityOther} {
		if got := SeverityOf(map[string]string{"severity": v}); got != want {
			t.Errorf("%q: %s", v, got)
		}
	}
}

func TestSubjectOf(t *testing.T) {
	for _, tc := range []struct {
		labels         map[string]string
		kind, ns, name string
		also           []string
		container      string
	}{
		{map[string]string{"namespace": "team", "pod": "api-1", "container": "app"}, "Pod", "team", "api-1", nil, "app"},
		{map[string]string{"namespace": "team", "deployment": "api"}, "Deployment", "team", "api", nil, ""},
		{map[string]string{"namespace": "team", "statefulset": "db"}, "StatefulSet", "team", "db", nil, ""},
		{map[string]string{"namespace": "team", "daemonset": "agent"}, "DaemonSet", "team", "agent", nil, ""},
		{map[string]string{"namespace": "team", "job_name": "backup-1"}, "Job", "team", "backup-1", nil, ""},
		{map[string]string{"namespace": "team", "cronjob": "backup"}, "CronJob", "team", "backup", nil, ""},
		{map[string]string{"namespace": "team", "persistentvolumeclaim": "data"}, "PersistentVolumeClaim", "team", "data", nil, ""},
		{map[string]string{"namespace": "team", "horizontalpodautoscaler": "api"}, "HorizontalPodAutoscaler", "team", "api", nil, ""},
		{map[string]string{"node": "n1"}, "Node", "", "n1", nil, ""},
		{map[string]string{"namespace": "team"}, "Namespace", "", "team", nil, ""},
		// precedence: the pod is the subject, the rest also named
		{map[string]string{"namespace": "team", "pod": "api-1", "deployment": "api", "node": "n1"}, "Pod", "team", "api-1", []string{"Deployment", "Node"}, ""},
		// a namespaced kind without its namespace names nothing
		{map[string]string{"pod": "api-1"}, "", "", "", nil, ""},
		// labels that describe the scrape never name a subject
		{map[string]string{"job": "kube-state-metrics", "instance": "10.0.0.1:8080", "service": "ksm", "endpoint": "http", "container": "app"}, "", "", "", nil, ""},
		// with a namespace they still name nothing but the namespace
		{map[string]string{"namespace": "monitoring", "job": "kube-state-metrics", "instance": "10.0.0.1:8080", "service": "ksm", "endpoint": "http", "container": "app"}, "Namespace", "", "monitoring", nil, ""},
	} {
		s := SubjectOf(tc.labels)
		var also []string
		for _, a := range s.AlsoNames {
			also = append(also, a.Kind)
		}
		if s.Kind != tc.kind || s.Namespace != tc.ns || s.Name != tc.name || !reflect.DeepEqual(also, tc.also) || s.Container != tc.container {
			t.Errorf("%v: %+v", tc.labels, s)
		}
	}
	if s := SubjectOf(map[string]string{"namespace": "team", "pod": "p"}); !reflect.DeepEqual(s.Basis, []string{"namespace", "pod"}) {
		t.Fatalf("basis: %v", s.Basis)
	}
}

func TestClip(t *testing.T) {
	if Clip("short", 10) != "short" {
		t.Fatal("under the cap")
	}
	c := Clip(strings.Repeat("é", 10), 9) // 2 bytes each
	if len(c) > 9 || !strings.HasSuffix(c, "…") || !strings.HasPrefix(c, "éééé") && !strings.HasPrefix(c, "éé") {
		t.Fatalf("clip: %q (%d)", c, len(c))
	}
	for i := 0; i < len(c); {
		r := []rune(c[i:])[0]
		if r == '�' {
			t.Fatalf("split rune: %q", c)
		}
		i += len(string(r))
	}
}
