package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/loki"
)

// AI A1 §3.6 and §8: get_log_history through the real history service: the
// pod's UID only from the conversation's own reads, which identity the read
// used said every time, the whole text scrubbed before each line is capped,
// and the private view closed after.

type clusterHistory struct {
	s       *LogHistoryService
	cluster string
}

func (c clusterHistory) history(ctx context.Context, req PodHistoryRequestDTO) LogHistoryPageDTO {
	req.Cluster = c.cluster
	return c.s.history(ctx, req)
}

func historyTools(s *LogHistoryService, held ...string) *chatTools {
	t := newChatTools(newFakeChat(), fixedNow)
	t.history = clusterHistory{s: s, cluster: "c"}
	t.ledgerUIDs = func(ns, name string) []string {
		if ns == "team" && name == "api-1" {
			return held
		}
		return nil
	}
	return t
}

func runHistory(t *testing.T, tools *chatTools, args string) (chatEnvelope, ChatRead, chatLogHistoryData) {
	t.Helper()
	res := tools.Run(context.Background(), toolLogHistory, json.RawMessage(args))
	if len(res.JSON) > chatResultMax {
		t.Fatalf("%d bytes", len(res.JSON))
	}
	var env chatEnvelope
	if err := json.Unmarshal(res.JSON, &env); err != nil {
		t.Fatal(err)
	}
	var d chatLogHistoryData
	b, _ := json.Marshal(env.Data)
	_ = json.Unmarshal(b, &d)
	return env, res.Read, d
}

func TestLogHistoryUsesOnlyAUIDTheLedgerHolds(t *testing.T) {
	f := otelFake()
	f.entries[q(podBranch, loki.ModeName, "")] = lines(300, tNow.Add(-time.Minute), "u-1", "name")
	f.entries[q(podBranch, loki.ModeName, "u-1")] = lines(300, tNow.Add(-time.Minute), "u-1", "name")
	f.entries[q(podBranch, loki.ModeUID, "u-1")] = lines(300, tNow.Add(-time.Minute), "u-1", "uid")
	s := histSvc(f)

	env, read, d := runHistory(t, historyTools(s), `{"namespace":"team","pod":"api-1"}`)
	if read.Outcome != "completed" || d.Identity != "by namespace and name: every pod of that name in the window" || d.Lines != 100 ||
		env.Complete || len(read.Refs) != 0 || d.Window != "1h" || !strings.Contains(env.Note, d.Identity) {
		t.Fatalf("no UID held: by name, the tail kept: %+v %+v %+v", env, read, d.Identity)
	}
	if !strings.Contains(d.Text, " web name 0") {
		t.Fatalf("%.200s", d.Text)
	}

	env, read, d = runHistory(t, historyTools(s, "u-1"), `{"namespace":"team","pod":"api-1","tail":5,"window":"6h"}`)
	if d.Identity != "by the pod's UID u-1" || d.Lines != 5 || len(read.Refs) != 1 || read.Refs[0].UID != "u-1" || !strings.Contains(d.Text, " web uid 0") {
		t.Fatalf("the one UID the ledger holds is used: %+v %+v", d, read)
	}

	before := len(f.queries)
	for _, tc := range []struct {
		held []string
		args string
		want string
	}{
		{[]string{"u-1", "u-2"}, `{"namespace":"team","pod":"api-1"}`, "pass one of their UIDs as uid (u-1, u-2)"},
		{[]string{"u-1"}, `{"namespace":"team","pod":"api-1","uid":"u-9"}`, "accepted only when this conversation's own reads hold it"},
		{nil, `{"namespace":"team","pod":"api-1","uid":"u-1"}`, "accepted only when"},
		{nil, `{"namespace":"team","pod":"api-1","window":"7d"}`, "window must be"},
	} {
		env, read, _ := runHistory(t, historyTools(s, tc.held...), tc.args)
		if read.Outcome != "failed" || !strings.Contains(env.Errors[0], tc.want) {
			t.Fatalf("%s: %+v", tc.args, env.Errors)
		}
	}
	if len(f.queries) != before {
		t.Fatal("a refused call queries nothing")
	}
	_, _, d = runHistory(t, historyTools(s, "u-1", "u-2"), `{"namespace":"team","pod":"api-1","uid":"u-1"}`)
	if d.Identity != "by the pod's UID u-1" {
		t.Fatalf("one of several held UIDs, passed: %+v", d)
	}
	s.mu.Lock()
	open := len(s.views)
	s.mu.Unlock()
	if open != 0 {
		t.Fatalf("chat's views are closed after each read: %d open", open)
	}
}

func TestLogHistoryScrubsTheWholeTextThenCapsEachLine(t *testing.T) {
	f := otelFake()
	es := lines(3, tNow.Add(-time.Minute), "", "plain")
	es[0].Line = "auth token ghp_abcdefghijklmnopqrstuvwxyz0123456789 used"
	es[1].Line = strings.Repeat("x", 3000)
	f.entries[q(podBranch, loki.ModeName, "")] = es
	env, read, d := runHistory(t, historyTools(histSvc(f)), `{"namespace":"team","pod":"api-1"}`)
	if strings.Contains(d.Text, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") || !strings.Contains(d.Text, "…[line truncated]") || read.Redacted < 2 ||
		!strings.Contains(env.Note, "not guaranteed secret-free") {
		t.Fatalf("%d %.300s", read.Redacted, d.Text)
	}
}

func TestLogHistorySaysNoStreamsAndFailures(t *testing.T) {
	f := otelFake()
	f.labels = map[string][]string{}
	env, read, d := runHistory(t, historyTools(histSvc(f)), `{"namespace":"team","pod":"api-1"}`)
	if read.Outcome != "completed" || d.Lines != 0 || !strings.Contains(env.Note, "says nothing about whether the pod logged") {
		t.Fatalf("%+v", env)
	}
	f = otelFake()
	f.entries[q(podBranch, loki.ModeName, "")] = lines(3, tNow.Add(-time.Minute), "", "x")
	f.err = errors.New("loki answered 502")
	env, read, _ = runHistory(t, historyTools(histSvc(f)), `{"namespace":"team","pod":"api-1"}`)
	if read.Outcome != "failed" || !strings.Contains(env.Errors[0], "502") {
		t.Fatalf("%+v", env)
	}
	tools := historyTools(histSvc(f))
	tools.history = nil
	if _, read, _ := runHistory(t, tools, `{"namespace":"team","pod":"api-1"}`); read.Outcome != "failed" {
		t.Fatal("no Loki in the session is a failed read")
	}
}

func TestLogHistoryTakesTheUIDFromAnEarlierReadInTheConversation(t *testing.T) {
	f := newToolsFixture(t,
		calls(ai.ToolCall{ID: "c1", Name: toolGet, Args: json.RawMessage(`{"kind":"pod","namespace":"team","name":"api-1"}`)}), answer("read"),
		calls(ai.ToolCall{ID: "c2", Name: toolLogHistory, Args: json.RawMessage(`{"namespace":"team","pod":"api-1"}`)}), answer("history"))
	f.conn.objects[key(podsGVR, "team", "api-1")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "api-1", "namespace": "team", "uid": "u-1"}})
	h := otelFake()
	h.entries[q(podBranch, loki.ModeName, "u-1")] = lines(3, tNow.Add(-time.Minute), "u-1", "name")
	h.entries[q(podBranch, loki.ModeUID, "u-1")] = lines(3, tNow.Add(-time.Minute), "u-1", "uid")
	SetChatLogHistory(f.svc, histSvc(h))
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "read it", nil)
	st = f.svc.Ask("x", st.ConversationID, "its history", nil)
	r := st.Exchanges[1].Reads[0]
	if r.Tool != toolLogHistory || r.Outcome != "completed" || r.Detail != "3 lines, by the pod's UID u-1" {
		t.Fatalf("the UID the earlier read returned is the one used: %+v", r)
	}
}

func TestLogHistoryReadsLokiOnlyBehindTheTurnsGuard(t *testing.T) {
	var f *chatFixture
	f = newToolsFixture(t, func(ai.ConvRequest) (ai.Turn, error) {
		// the connection is lost after the model asked for the read
		f.mu.Lock()
		f.usable = false
		f.mu.Unlock()
		return calls(ai.ToolCall{ID: "c1", Name: toolLogHistory, Args: json.RawMessage(`{"namespace":"team","pod":"api-1"}`)})(ai.ConvRequest{})
	}, answer("never"))
	h := otelFake()
	h.entries[q(podBranch, loki.ModeName, "")] = lines(3, tNow.Add(-time.Minute), "", "x")
	SetChatLogHistory(f.svc, histSvc(h))
	st := f.svc.Start("x")
	_ = f.svc.Ask("x", st.ConversationID, "history", nil)
	if len(h.queries) != 0 {
		t.Fatalf("Loki was read after the guard failed: %v", h.queries)
	}
}

type spyHistory struct{ n int }

func (s *spyHistory) history(context.Context, PodHistoryRequestDTO) LogHistoryPageDTO {
	s.n++
	return LogHistoryPageDTO{}
}

func TestLogHistoryEndsWithItsTurn(t *testing.T) {
	f := otelFake()
	f.entries[q(podBranch, loki.ModeName, "")] = lines(3, tNow.Add(-time.Minute), "", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := historyTools(histSvc(f)).Run(ctx, toolLogHistory, json.RawMessage(`{"namespace":"team","pod":"api-1"}`))
	if res.Read.Outcome != "failed" || res.Read.Detail != "cancelled" || len(f.queries) != 0 {
		t.Fatalf("a cancelled turn's read queries nothing and is said as cancelled: %+v %v", res.Read, f.queries)
	}
	// the seam's own guard, whatever the dispatch checked before it
	spy := &spyHistory{}
	g := guardedHistory{r: &turnRunner{abort: errors.New("the conversation ended")}, h: spy}
	if p := g.history(context.Background(), PodHistoryRequestDTO{}); spy.n != 0 || p.Error == "" {
		t.Fatalf("%+v %d", p, spy.n)
	}
}
