package record

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const conv = "inv-c1"

var (
	noTarget = Subject{Kind: "ScaledObject", Namespace: "klyx-s9-lab", Name: "no-target", UID: "uid-nt"}
	web      = Subject{Kind: "Deployment", Namespace: "klyx-a1-app", Name: "web"}
)

func preds() []Predicate {
	return []Predicate{
		{ID: "e3-event", Kind: KindEvent, Subject: noTarget, Pattern: `deployments\.apps "no-such-deployment" not found`,
			Reads: []ReadSpec{{Tool: "get_events", Args: map[string]string{"namespace": "klyx-s9-lab", "name": "no-target"}},
				{Tool: "get_resource", Args: map[string]string{"kind": "scaledobject", "namespace": "klyx-s9-lab", "name": "no-target"}}}},
		{ID: "e3-condition", Kind: KindCondition, Subject: noTarget, Type: "Ready", Status: "False", Reason: "ScaledObjectCheckFailed", Pattern: `no-such-deployment`,
			Reads: []ReadSpec{{Tool: "get_resource", Args: map[string]string{"kind": "ScaledObject", "namespace": "klyx-s9-lab", "name": "no-target"}}}},
		{ID: "e1-log", Kind: KindLog, Subject: web, Pods: []string{"pod-1", "pod-2"},
			Reads: []ReadSpec{{Tool: "get_pod_logs", Args: map[string]string{"namespace": "klyx-a1-app"}}}},
		{ID: "e1-diff", Kind: KindDiff, Subject: web, Pattern: `env\[MODE\]`,
			Reads: []ReadSpec{{Tool: "git_changes", Args: map[string]string{"kind": "Kustomization", "namespace": "klyx-a1-flux", "name": "a1-web"}}}},
	}
}

func rec(t *testing.T) *Recorder {
	t.Helper()
	r, err := New(conv, preds())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func env(t *testing.T, tool, id string, truncated bool, data any, checks ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"tool": tool, "readId": id, "truncated": truncated, "data": data, "checks": checks})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func args(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

func outcome(t *testing.T, f Facts, id string) Outcome {
	t.Helper()
	for _, o := range f.Predicates {
		if o.Predicate == id {
			return o
		}
	}
	t.Fatalf("no outcome for %s in %+v", id, f)
	return Outcome{}
}

var eventsArgs = args(map[string]any{"namespace": "klyx-s9-lab", "kind": "ScaledObject", "name": "no-target", "limit": 20})

func detailed() map[string]any {
	return map[string]any{"type": "Warning", "reason": "ScaledObjectCheckFailed", "message": `deployments.apps "no-such-deployment" not found`, "object": "ScaledObject/no-target", "objectUID": "uid-nt"}
}

func generic() map[string]any {
	return map[string]any{"type": "Warning", "reason": "ScaledObjectCheckFailed", "message": "Target resource doesn't exist", "object": "ScaledObject/no-target", "objectUID": "uid-nt"}
}

func TestADeliveredMatchEstablishesAndBindsOnlyItsOwnChecks(t *testing.T) {
	r := rec(t)
	// the aggregate event check names its reason, never the message: it is
	// not bound, so a citation of it alone is not grounded
	agg := map[string]any{"id": "r2.c1", "subject": map[string]any{"kind": "ScaledObject", "namespace": "klyx-s9-lab", "name": "no-target"}, "statement": "ScaledObjectCheckFailed: 2 events"}
	r.Read(conv, "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", false, []any{generic(), detailed()}, agg))
	r.Sent(conv, []string{"r2"})
	o := outcome(t, r.Facts(), "e3-event")
	if !o.Established || o.Read != "r2" || len(o.Checks) != 0 {
		t.Fatalf("want established at r2 with no bound check, got %+v", o)
	}
}

func TestADiffCheckNamingThePathIsBound(t *testing.T) {
	r := rec(t)
	data := map[string]any{"objects": []any{map[string]any{"kind": "Deployment", "namespace": "klyx-a1-app", "name": "web", "change": "changed",
		"hidden": []any{map[string]any{"path": "spec.template.containers[web].env[MODE]", "change": "changed (not shown)"}}}}}
	bound := map[string]any{"id": "r3.c1", "subject": map[string]any{"kind": "Deployment", "namespace": "klyx-a1-app", "name": "web"}, "statement": "changed Deployment klyx-a1-app/web (by name): spec.template.containers[web].env[MODE], changed (not shown)"}
	other := map[string]any{"id": "r3.c2", "subject": map[string]any{"kind": "Deployment", "namespace": "klyx-a1-app", "name": "web"}, "statement": "changed Deployment klyx-a1-app/web (by name): labels[release]"}
	r.Read(conv, "git_changes", args(map[string]any{"kind": "Kustomization", "namespace": "klyx-a1-flux", "name": "a1-web"}), "r3", false,
		env(t, "git_changes", "r3", false, data, bound, other))
	r.Sent(conv, []string{"r3"})
	o := outcome(t, r.Facts(), "e1-diff")
	if !o.Established || !reflect.DeepEqual(o.Checks, []string{"r3.c1"}) {
		t.Fatalf("want r3.c1 bound alone, got %+v", o)
	}
}

func TestOnlyThePlantedMessageEstablishesAnEvent(t *testing.T) {
	r := rec(t)
	r.Read(conv, "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", false, []any{generic()}))
	r.Sent(conv, []string{"r2"})
	if o := outcome(t, r.Facts(), "e3-event"); o.Established || o.Reason != NoMatch {
		t.Fatalf("a generic same-reason event must not establish it: %+v", o)
	}
}

func TestAnEventInTheAnchorsWarningEventsCounts(t *testing.T) {
	r := rec(t)
	data := map[string]any{"object": map[string]any{"kind": "ScaledObject", "uid": "uid-nt",
		"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "ScaledObjectCheckFailed", "message": `ScaledObject doesn't have correct scaleTargetRef specification: deployments.apps "no-such-deployment" not found`}}},
		"warningEvents": []any{detailed()}}
	r.Read(conv, "get_resource", args(map[string]any{"kind": "ScaledObject", "namespace": "klyx-s9-lab", "name": "no-target"}), "r1", false, env(t, "get_resource", "r1", false, data))
	r.Sent(conv, []string{"r1"})
	f := r.Facts()
	if o := outcome(t, f, "e3-event"); !o.Established || o.Read != "r1" {
		t.Fatalf("event: %+v", o)
	}
	if o := outcome(t, f, "e3-condition"); !o.Established || o.Read != "r1" {
		t.Fatalf("condition: %+v", o)
	}
}

func TestAConditionOfAnotherObjectDoesNotCount(t *testing.T) {
	r := rec(t)
	data := map[string]any{"object": map[string]any{"kind": "ScaledObject", "uid": "uid-bad-auth",
		"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "ScaledObjectCheckFailed", "message": "no-such-deployment"}}}}
	r.Read(conv, "get_resource", args(map[string]any{"kind": "ScaledObject", "namespace": "klyx-s9-lab", "name": "bad-auth"}), "r4", false, env(t, "get_resource", "r4", false, data))
	r.Sent(conv, []string{"r4"})
	if o := outcome(t, r.Facts(), "e3-condition"); o.Established || o.Reason != NotRequested {
		t.Fatalf("another object's read is not relevant: %+v", o)
	}
}

func logRead(t *testing.T, r *Recorder, id, instance, pod string, lines int) {
	t.Helper()
	data := map[string]any{"podUID": pod, "container": "web", "instance": instance, "lines": lines, "text": strings.Repeat("crash\n", lines)}
	r.Read(conv, "get_pod_logs", args(map[string]any{"namespace": "klyx-a1-app", "pod": "web-x", "previous": instance == "previous"}), id, false, env(t, "get_pod_logs", id, false, data))
}

func TestOnlyAPreviousLogOfAPlantedPodCounts(t *testing.T) {
	r := rec(t)
	logRead(t, r, "r2", "current", "pod-1", 3)
	logRead(t, r, "r3", "previous", "pod-9", 3) // not the plant's pod
	r.Sent(conv, []string{"r2", "r3"})
	if o := outcome(t, r.Facts(), "e1-log"); o.Established || o.Reason != NoMatch {
		t.Fatalf("a current log, or another pod's, must not establish it: %+v", o)
	}
	logRead(t, r, "r4", "previous", "pod-2", 3)
	r.Sent(conv, []string{"r4"})
	if o := outcome(t, r.Facts(), "e1-log"); !o.Established || o.Read != "r4" || !o.Previous {
		t.Fatalf("want r4, previous: %+v", o)
	}
}

func TestAnEmptyPreviousLogDoesNotCount(t *testing.T) {
	r := rec(t)
	logRead(t, r, "r2", "previous", "pod-1", 0)
	r.Sent(conv, []string{"r2"})
	if o := outcome(t, r.Facts(), "e1-log"); o.Established {
		t.Fatalf("no line was delivered: %+v", o)
	}
}

func TestReasons(t *testing.T) {
	cases := []struct {
		name string
		run  func(r *Recorder)
		want string
	}{
		{"never asked for in tools mode", func(r *Recorder) {}, NotRequested},
		{"not in the snapshot plan", func(r *Recorder) { r.Mode(conv, "snapshot") }, NotPlanned},
		{"the read failed", func(r *Recorder) { r.Read(conv, "get_events", eventsArgs, "r2", true, nil) }, ReadFailed},
		{"acquired, matched, the turn ended first", func(r *Recorder) {
			r.Read(conv, "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", false, []any{detailed()}))
		}, TurnEnded},
		{"matched, carried only by a request refused locally", func(r *Recorder) {
			r.Read(conv, "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", false, []any{detailed()}))
			r.Refused(conv, []string{"r2"})
		}, NotSent},
		{"matched, left out by the snapshot fill", func(r *Recorder) {
			r.Mode(conv, "snapshot")
			r.Read(conv, "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", false, []any{detailed()}))
			r.Omitted(conv, []string{"r2"})
		}, NotSent},
		{"delivered after a cap took the planted item", func(r *Recorder) {
			r.Read(conv, "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", true, []any{generic()}))
			r.Sent(conv, []string{"r2"})
		}, Capped},
		{"delivered whole, no match", func(r *Recorder) {
			r.Read(conv, "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", false, []any{generic()}))
			r.Sent(conv, []string{"r2"})
		}, NoMatch},
		{"a failure, then a delivered whole read without the match", func(r *Recorder) {
			r.Read(conv, "get_events", eventsArgs, "r2", true, nil)
			r.Read(conv, "get_events", eventsArgs, "r3", false, env(t, "get_events", "r3", false, []any{generic()}))
			r.Sent(conv, []string{"r3"})
		}, NoMatch},
		{"events of another conversation are ignored", func(r *Recorder) {
			r.Read("inv-other", "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", false, []any{detailed()}))
			r.Sent("inv-other", []string{"r2"})
		}, NotRequested},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := rec(t)
			c.run(r)
			o := outcome(t, r.Facts(), "e3-event")
			if o.Established || o.Reason != c.want {
				t.Fatalf("want %s, got %+v", c.want, o)
			}
		})
	}
}

func TestALaterDeliveredMatchWins(t *testing.T) {
	r := rec(t)
	r.Read(conv, "get_events", eventsArgs, "r2", true, nil)
	r.Read(conv, "get_events", eventsArgs, "r3", false, env(t, "get_events", "r3", false, []any{detailed()}))
	r.Refused(conv, []string{"r3"})
	r.Sent(conv, []string{"r3"}) // the retried request was sent
	if o := outcome(t, r.Facts(), "e3-event"); !o.Established || o.Read != "r3" {
		t.Fatalf("a refused request retried and sent is delivered: %+v", o)
	}
}

func TestTheFirstDeliveredMatchIsTheRead(t *testing.T) {
	r := rec(t)
	r.Read(conv, "get_events", eventsArgs, "r2", false, env(t, "get_events", "r2", false, []any{detailed()}))
	r.Read(conv, "get_events", eventsArgs, "r3", false, env(t, "get_events", "r3", false, []any{detailed()}))
	r.Sent(conv, []string{"r3"})
	r.Sent(conv, []string{"r2", "r3"}) // carried again: counts once
	f := r.Facts()
	if o := outcome(t, f, "e3-event"); o.Read != "r2" || f.Reads != 2 {
		t.Fatalf("want the first read in order and two reads, got %+v (%d reads)", o, f.Reads)
	}
}

func TestReportStates(t *testing.T) {
	cases := []struct {
		name string
		run  func(r *Recorder)
		want string
	}{
		{"no report_findings call", func(r *Recorder) {}, ReportMissing},
		{"every call refused", func(r *Recorder) { r.ToolsReport(conv, false); r.ToolsReport(conv, false) }, ReportRejected},
		{"refused, then accepted", func(r *Recorder) { r.ToolsReport(conv, false); r.ToolsReport(conv, true) }, ReportAccepted},
		{"another conversation's report", func(r *Recorder) { r.ToolsReport("inv-other", true) }, ReportMissing},
		{"snapshot: no block", func(r *Recorder) { r.Mode(conv, "snapshot"); r.SnapshotReport(conv, false, false) }, ReportMissing},
		{"snapshot: a malformed block", func(r *Recorder) { r.Mode(conv, "snapshot"); r.SnapshotReport(conv, true, false) }, ReportMalform},
		{"snapshot: a parsed block", func(r *Recorder) { r.Mode(conv, "snapshot"); r.SnapshotReport(conv, true, true) }, ReportAccepted},
		{"tools fell back to snapshot, which answered without a block", func(r *Recorder) {
			r.Mode(conv, "snapshot")
			r.SnapshotReport(conv, false, false)
		}, ReportMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := rec(t)
			c.run(r)
			if got := r.Facts().Report; got != c.want {
				t.Fatalf("want %s, got %s", c.want, got)
			}
		})
	}
}

func TestTheRecordIsBounded(t *testing.T) {
	r := rec(t)
	for i := 0; i < MaxReads+5; i++ {
		r.Read(conv, "get_events", eventsArgs, "", false, env(t, "get_events", "", false, []any{generic()}))
	}
	f := r.Facts()
	if f.Reads != MaxReads || !f.Overflow {
		t.Fatalf("want %d reads and overflow, got %d %v", MaxReads, f.Reads, f.Overflow)
	}
}

func TestNothingOfABodyIsKept(t *testing.T) {
	r := rec(t)
	secret := "hunter2-planted-log-line"
	data := map[string]any{"podUID": "pod-1", "instance": "previous", "lines": 1, "text": secret}
	r.Read(conv, "get_pod_logs", args(map[string]any{"namespace": "klyx-a1-app", "pod": "web-x"}), "r2", false, env(t, "get_pod_logs", "r2", false, data))
	r.Sent(conv, []string{"r2"})
	b, err := json.Marshal(r.Facts())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("the facts carry a body: %s", b)
	}
	for _, rd := range r.reads {
		if strings.Contains(strings.Join([]string{rd.id, rd.tool}, " "), secret) {
			t.Fatal("a read keeps a body")
		}
	}
}

func TestBadPredicatesAreRefused(t *testing.T) {
	for _, p := range []Predicate{
		{ID: "x", Kind: KindEvent},
		{ID: "x", Kind: KindDiff, Pattern: "("},
		{ID: "x", Kind: KindLog},
		{ID: "x", Kind: "rumour", Pattern: "a"},
	} {
		if _, err := New(conv, []Predicate{p}); err == nil {
			t.Errorf("%+v was accepted", p)
		}
	}
}
