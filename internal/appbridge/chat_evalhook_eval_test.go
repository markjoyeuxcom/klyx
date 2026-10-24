//go:build eval

package appbridge

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/evalrun/record"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

// The eval's record (eval design §5) sees an investigation through the
// real ChatService: each read as produced, each request sent or refused,
// the snapshot's fill and the report. Run with -tags eval.

func podPreds() []record.Predicate {
	return []record.Predicate{
		{ID: "log", Kind: record.KindLog, Subject: record.Subject{Kind: "Pod", Namespace: "shop", Name: "web"}, Pods: []string{"uid-p"},
			Reads: []record.ReadSpec{{Tool: toolLogs, Args: map[string]string{"namespace": "shop", "pod": "web"}}}},
		{ID: "event", Kind: record.KindEvent, Subject: record.Subject{Kind: "Pod", Namespace: "shop", Name: "web", UID: "uid-p"}, Pattern: `Back-off restarting failed container`,
			Reads: []record.ReadSpec{{Tool: toolEvents, Args: map[string]string{"namespace": "shop", "name": "web"}}}},
		{ID: "condition", Kind: record.KindCondition, Subject: record.Subject{Kind: "Pod", Namespace: "shop", Name: "web", UID: "uid-p"},
			Type: "Ready", Status: "False", Reason: "ContainersNotReady", Pattern: `unready status`,
			Reads: []record.ReadSpec{{Tool: toolGet, Args: map[string]string{"namespace": "shop", "name": "web"}}}},
	}
}

// crashingPod is podAnchor's pod with a failing Ready condition, a previous
// container log and a warning event about it.
func crashingPod(f *chatFixture) {
	f.conn.objects[key(podsGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-p"},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "ContainersNotReady",
			"message": "containers with unready status: [web]"}}}})
	f.conn.logs = fleet.LogSnapshot{PodUID: "uid-p", Container: "web", Previous: true, ReadAt: time.Now(), Text: "MODE=crash: exiting\n"}
	f.conn.events = []workloads.EventSummary{{Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container web", Count: 4,
		Namespace: "shop", Kind: "Pod", Name: "web", UID: "uid-p", LastSeenUnix: time.Now().Unix()}}
}

func tc(id, name string, args any) ai.ToolCall {
	b, _ := json.Marshal(args)
	return ai.ToolCall{ID: id, Name: name, Args: b}
}

func recordFor(t *testing.T, f *chatFixture, conv string) *record.Recorder {
	t.Helper()
	rec, err := record.New(conv, podPreds())
	if err != nil {
		t.Fatal(err)
	}
	SetEvalRecorder(f.svc, rec)
	t.Cleanup(func() { SetEvalRecorder(f.svc, nil) })
	return rec
}

func evalOutcome(t *testing.T, fs record.Facts, id string) record.Outcome {
	t.Helper()
	for _, o := range fs.Predicates {
		if o.Predicate == id {
			return o
		}
	}
	t.Fatalf("no %s in %+v", id, fs)
	return record.Outcome{}
}

const validReport = `{"findings":[{"type":"cause","statement":"the container exits at start","subject":{"kind":"Pod","namespace":"shop","name":"web"},"cites":["r2"]}]}`

func TestTheRecordSeesAToolsInvestigationAsDelivered(t *testing.T) {
	f := anchoredFixture(t,
		calls(tc("c1", toolLogs, map[string]any{"namespace": "shop", "pod": "web", "previous": true}),
			tc("c2", toolEvents, map[string]any{"namespace": "shop", "kind": "Pod", "name": "web"})),
		calls(tc("c3", toolFindings, json.RawMessage(validReport))),
		answer("done"))
	crashingPod(f)
	inv := f.svc.StartInvestigation("x", podAnchor)
	rec := recordFor(t, f, inv.ConversationID)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	if st.Exchanges[0].Outcome != "answered" {
		t.Fatalf("%+v", st.Exchanges[0])
	}
	fs := rec.Facts()
	if fs.Mode != "tools" || fs.Report != record.ReportAccepted {
		t.Fatalf("mode and report: %+v", fs)
	}
	if o := evalOutcome(t, fs, "condition"); !o.Established || o.Read != "r1" {
		t.Fatalf("the anchor's own read delivers its condition: %+v", o)
	}
	if o := evalOutcome(t, fs, "log"); !o.Established || !o.Previous {
		t.Fatalf("the previous log was delivered: %+v", o)
	}
	if o := evalOutcome(t, fs, "event"); !o.Established {
		t.Fatalf("the event was delivered: %+v", o)
	}
}

func TestAModelThatReportsAtOnceLeavesItsEvidenceNotRequested(t *testing.T) {
	f := anchoredFixture(t, calls(tc("c1", toolFindings, json.RawMessage(validReport))), answer("done"))
	crashingPod(f)
	inv := f.svc.StartInvestigation("x", podAnchor)
	rec := recordFor(t, f, inv.ConversationID)
	f.svc.Ask("x", inv.ConversationID, "", nil)
	fs := rec.Facts()
	if fs.Report != record.ReportAccepted {
		t.Fatalf("%+v", fs)
	}
	if o := evalOutcome(t, fs, "log"); o.Established || o.Reason != record.NotRequested {
		t.Fatalf("the model never asked for the log: %+v", o)
	}
	// the anchor's own read carries its warning events: delivered without
	// a request of the model's
	if o := evalOutcome(t, fs, "event"); !o.Established || o.Read != "r1" {
		t.Fatalf("the anchor's read delivered the event: %+v", o)
	}
}

func TestARefusedReportIsNotAReport(t *testing.T) {
	f := anchoredFixture(t, calls(tc("c1", toolFindings, json.RawMessage(`{"findings":"nope"}`))), answer("done"))
	crashingPod(f)
	inv := f.svc.StartInvestigation("x", podAnchor)
	rec := recordFor(t, f, inv.ConversationID)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	if st.Exchanges[0].Findings == nil {
		t.Fatal("A1 still shows its own missing evidence")
	}
	if fs := rec.Facts(); fs.Report != record.ReportRejected {
		t.Fatalf("a refused call is not an accepted report: %+v", fs)
	}
}

func TestNoReportAtAllIsMissing(t *testing.T) {
	f := anchoredFixture(t, answer("A registry outage caused the failure."))
	crashingPod(f)
	inv := f.svc.StartInvestigation("x", podAnchor)
	rec := recordFor(t, f, inv.ConversationID)
	f.svc.Ask("x", inv.ConversationID, "", nil)
	if fs := rec.Facts(); fs.Report != record.ReportMissing {
		t.Fatalf("%+v", fs)
	}
}

func TestARequestRefusedForItsSizeDeliversNothing(t *testing.T) {
	tooLarge := func(ai.ConvRequest) (ai.Turn, error) { return ai.Turn{}, ai.ErrRequestTooLarge }
	f := anchoredFixture(t, tooLarge)
	crashingPod(f)
	inv := f.svc.StartInvestigation("x", podAnchor)
	rec := recordFor(t, f, inv.ConversationID)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	if st.Exchanges[0].Outcome == "answered" {
		t.Fatalf("%+v", st.Exchanges[0])
	}
	if o := evalOutcome(t, rec.Facts(), "condition"); o.Established || o.Reason != record.NotSent {
		t.Fatalf("the anchor's read was only in a refused request: %+v", o)
	}
}

func TestAnotherConversationIsNotRecorded(t *testing.T) {
	f := anchoredFixture(t, calls(tc("c1", toolLogs, map[string]any{"namespace": "shop", "pod": "web", "previous": true})), answer("chat"))
	crashingPod(f)
	inv := f.svc.StartInvestigation("x", podAnchor)
	rec := recordFor(t, f, inv.ConversationID)
	chat := f.svc.Start("x")
	f.svc.Ask("x", chat.ConversationID, "logs?", nil)
	if fs := rec.Facts(); fs.Reads != 0 {
		t.Fatalf("the chat's reads reached the investigation's record: %+v", fs)
	}
}

func TestTheRecordSeesASnapshotInvestigation(t *testing.T) {
	cases := []struct {
		name, reply, want string
	}{
		{"a parsed block", "The web pods crash.\n\n```findings\n" + `{"findings":[]}` + "\n```\n", record.ReportAccepted},
		{"no block", "A registry outage caused the failure.", record.ReportMissing},
		{"a malformed block", "The web pods crash.\n\n```findings\n{not json\n```\n", record.ReportMalform},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := snapshotInvestigation(t, c.reply)
			planCluster(f)
			f.conn.logs = fleet.LogSnapshot{PodUID: "uid-p1", Container: "web", Previous: true, ReadAt: time.Now(), Text: "exiting\n"}
			inv := f.svc.StartInvestigation("x", ksAnchor)
			rec, err := record.New(inv.ConversationID, []record.Predicate{{ID: "log", Kind: record.KindLog, Pods: []string{"uid-p1"},
				Reads: []record.ReadSpec{{Tool: toolLogs, Args: map[string]string{"namespace": "shop"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			SetEvalRecorder(f.svc, rec)
			defer SetEvalRecorder(f.svc, nil)
			f.svc.Ask("x", inv.ConversationID, "", nil)
			fs := rec.Facts()
			if fs.Mode != "snapshot" || fs.Report != c.want {
				t.Fatalf("want snapshot and %s, got %+v", c.want, fs)
			}
			if o := evalOutcome(t, fs, "log"); !o.Established {
				t.Fatalf("the plan's previous log was in the document sent: %+v", o)
			}
		})
	}
}

func TestASnapshotSectionLeftOutIsNotSent(t *testing.T) {
	f, _ := snapshotInvestigation(t, "x")
	inv := f.svc.StartInvestigation("x", ksAnchor)
	rec, err := record.New(inv.ConversationID, []record.Predicate{{ID: "log", Kind: record.KindLog, Pods: []string{"uid-p1"},
		Reads: []record.ReadSpec{{Tool: toolLogs}}}})
	if err != nil {
		t.Fatal(err)
	}
	SetEvalRecorder(f.svc, rec)
	defer SetEvalRecorder(f.svc, nil)
	r := &turnRunner{s: f.svc, c: &conversation{id: inv.ConversationID}, ex: &exchange{}}
	logEnv := `{"tool":"get_pod_logs","readId":"r4","truncated":false,"data":{"podUID":"uid-p1","instance":"previous","lines":1,"text":"exiting"}}`
	r.evalRead(toolLogs, json.RawMessage(`{"namespace":"shop","pod":"web-1-a","previous":true}`), ChatToolResult{JSON: []byte(logEnv), Read: ChatRead{ID: "r4", Outcome: "completed"}})
	r.evalSnapshotDoc([]string{`{"readId":"r1"}`, logEnv}, []string{"r4"})
	r.evalSnapshotSent(nil)
	if o := evalOutcome(t, rec.Facts(), "log"); o.Established || o.Reason != record.NotSent {
		t.Fatalf("the fill left r4 out: %+v", o)
	}
}

// Codex's implementation review, finding 7: a provider that replied with a
// status received the request; a call that failed without a response
// leaves delivery not established.
func TestDeliveryFollowsWhatTheProviderSaid(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"HTTP 500 after receiving the request", &ai.HTTPStatusError{Code: 500, Body: "boom"}, ""},
		{"a transport error", errors.New("connection reset by peer"), record.DeliveryUncertain},
	}
	for _, c := range cases {
		t.Run("tools: "+c.name, func(t *testing.T) {
			fail := func(ai.ConvRequest) (ai.Turn, error) { return ai.Turn{}, c.err }
			f := anchoredFixture(t, fail)
			crashingPod(f)
			inv := f.svc.StartInvestigation("x", podAnchor)
			rec := recordFor(t, f, inv.ConversationID)
			f.svc.Ask("x", inv.ConversationID, "", nil)
			o := evalOutcome(t, rec.Facts(), "condition")
			if c.want == "" && (!o.Established || o.Read != "r1") {
				t.Fatalf("the provider received the anchor's read: %+v", o)
			}
			if c.want != "" && (o.Established || o.Reason != c.want) {
				t.Fatalf("want %s, got %+v", c.want, o)
			}
		})
		t.Run("snapshot: "+c.name, func(t *testing.T) {
			p := &fakeConverser{complete: func(ai.Request) (ai.Result, error) { return ai.Result{}, c.err }}
			f := newChatFixture(t, snapshotOnly{p})
			f.prov = p
			planCluster(f)
			f.conn.logs = fleet.LogSnapshot{PodUID: "uid-p1", Container: "web", Previous: true, ReadAt: time.Now(), Text: "exiting\n"}
			inv := f.svc.StartInvestigation("x", ksAnchor)
			rec, err := record.New(inv.ConversationID, []record.Predicate{{ID: "log", Kind: record.KindLog, Pods: []string{"uid-p1"},
				Reads: []record.ReadSpec{{Tool: toolLogs, Args: map[string]string{"namespace": "shop"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			SetEvalRecorder(f.svc, rec)
			defer SetEvalRecorder(f.svc, nil)
			f.svc.Ask("x", inv.ConversationID, "", nil)
			o := evalOutcome(t, rec.Facts(), "log")
			if c.want == "" && !o.Established {
				t.Fatalf("the provider received the document: %+v", o)
			}
			if c.want != "" && (o.Established || o.Reason != c.want) {
				t.Fatalf("want %s, got %+v", c.want, o)
			}
		})
	}
}
