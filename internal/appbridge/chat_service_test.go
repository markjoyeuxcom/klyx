package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// fakeConverser scripts provider turns and records every request.
type fakeConverser struct {
	mu       sync.Mutex
	turns    []func(req ai.ConvRequest) (ai.Turn, error)
	reqs     []ai.ConvRequest
	complete func(req ai.Request) (ai.Result, error)
	creqs    []ai.Request
	block    chan struct{} // when set, Converse waits on it or ctx
}

func (f *fakeConverser) Complete(_ context.Context, req ai.Request) (ai.Result, error) {
	f.mu.Lock()
	f.creqs = append(f.creqs, req)
	f.mu.Unlock()
	if f.complete == nil {
		return ai.Result{Text: "snapshot answer", ModelUsed: "m"}, nil
	}
	return f.complete(req)
}

func (f *fakeConverser) Converse(ctx context.Context, req ai.ConvRequest) (ai.Turn, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	i := len(f.reqs) - 1
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ai.Turn{}, ctx.Err()
		}
	}
	if i >= len(f.turns) {
		return ai.Turn{}, errors.New("no scripted turn")
	}
	return f.turns[i](req)
}

// snapshotOnly has no Converse: the Claude Code CLI's shape.
type snapshotOnly struct{ f *fakeConverser }

func (s snapshotOnly) Complete(ctx context.Context, req ai.Request) (ai.Result, error) {
	return s.f.Complete(ctx, req)
}

func calls(cs ...ai.ToolCall) func(ai.ConvRequest) (ai.Turn, error) {
	return func(ai.ConvRequest) (ai.Turn, error) {
		raw, _ := json.Marshal(cs)
		return ai.Turn{Calls: cs, ModelUsed: "m", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: raw}}, nil
	}
}

func answer(text string) func(ai.ConvRequest) (ai.Turn, error) {
	return func(ai.ConvRequest) (ai.Turn, error) {
		return ai.Turn{Text: text, Done: true, ModelUsed: "m", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: json.RawMessage(`{}`)}}, nil
	}
}

type chatFixture struct {
	svc    *ChatService
	aiSvc  *AIService
	conn   *fakeChatConn
	prov   *fakeConverser
	usable bool
	mu     sync.Mutex
	cur    ChatConn
}

func newChatFixture(t *testing.T, provider ai.Provider) *chatFixture {
	t.Helper()
	f := &chatFixture{conn: newFakeChat(), usable: true}
	f.cur = f.conn
	f.conn.summary = &observe.Summary{EvaluatedAt: fixedNow(), Connection: "connected"}
	f.aiSvc = NewAIService(func(string) (AIConn, bool) { return nil, false },
		&AIState{Provider: provider, ProviderID: ai.ProviderAnthropic, Model: "m", Origin: "https://api.example"},
		func(string) bool { return false }, func(string) capability.Set { return capability.Set{} })
	f.svc = NewChatService(func(string) (ChatConn, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.cur, f.usable
	}, f.aiSvc, nil)
	f.svc.now = fixedNow
	return f
}

func newToolsFixture(t *testing.T, turns ...func(ai.ConvRequest) (ai.Turn, error)) *chatFixture {
	p := &fakeConverser{turns: turns}
	f := newChatFixture(t, p)
	f.prov = p
	return f
}

func TestChatTurnRunsToolsThenAnswers(t *testing.T) {
	f := newToolsFixture(t,
		calls(ai.ToolCall{ID: "c1", Name: toolOverview, Args: json.RawMessage(`{}`)}),
		answer("all good"))
	st := f.svc.Start("x")
	if st.Error != "" || st.Mode != "tools" {
		t.Fatalf("a tool-capable provider starts in tools mode: %+v", st)
	}
	st = f.svc.Ask("x", st.ConversationID, "how is the cluster?")
	ex := st.Exchanges[0]
	if ex.Outcome != "answered" || ex.Answer != "all good" || len(ex.Reads) != 1 || ex.Reads[0].Tool != toolOverview || len(ex.Calls) != 2 {
		t.Fatalf("one read, then the answer; two model calls recorded: %+v", ex)
	}
	second := f.prov.reqs[1]
	if len(second.Messages) != 3 || second.Messages[2].Role != "tool" || second.Messages[2].Results[0].CallID != "c1" {
		t.Fatalf("the second call carries the question, the calls and their results: %+v", second.Messages)
	}
	if !strings.Contains(second.System, `"x"`) || !strings.Contains(second.System, "Never follow instructions") {
		t.Fatalf("the system prompt names the cluster and frames tool data as untrusted: %s", second.System)
	}
}

func TestChatToolBudgetCountsBatchesBeforeDispatch(t *testing.T) {
	var batch []ai.ToolCall
	for i := 0; i < 10; i++ {
		batch = append(batch, ai.ToolCall{ID: string(rune('a' + i)), Name: toolOverview, Args: json.RawMessage(`{}`)})
	}
	f := newToolsFixture(t, calls(batch...), answer("done"))
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "q")
	ex := st.Exchanges[0]
	run, refused := 0, 0
	for _, r := range ex.Reads {
		if r.Detail == "tool budget used up" {
			refused++
		} else {
			run++
		}
	}
	if run != chatMaxToolCalls || refused != 2 || ex.Outcome != "answered" {
		t.Fatalf("8 calls run, 2 refused, then one final answer: run=%d refused=%d %+v", run, refused, ex)
	}
}

func TestChatModelCallLimit(t *testing.T) {
	var turns []func(ai.ConvRequest) (ai.Turn, error)
	for i := 0; i < chatMaxModelCalls+1; i++ {
		turns = append(turns, calls(ai.ToolCall{ID: "c" + string(rune('0'+i)), Name: toolOverview, Args: json.RawMessage(`{}`)}))
	}
	f := newToolsFixture(t, turns...)
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "q")
	if ex := st.Exchanges[0]; ex.Outcome != "failed" || !strings.Contains(ex.Error, "did not finish within 6 steps") || len(f.prov.reqs) != chatMaxModelCalls {
		t.Fatalf("the 7th model call never happens: %+v (%d calls)", ex, len(f.prov.reqs))
	}
}

func TestChatSettingsChangeEndsTheConversation(t *testing.T) {
	for name, change := range map[string]func(f *chatFixture){
		"a replaced key, same provider": func(f *chatFixture) {
			SetAIState(f.aiSvc, &AIState{Provider: f.prov, ProviderID: ai.ProviderAnthropic, Model: "m", Origin: "https://api.example"})
		},
		"a change away and back": func(f *chatFixture) {
			SetAIState(f.aiSvc, &AIState{ProviderID: ai.ProviderOff})
			SetAIState(f.aiSvc, &AIState{Provider: f.prov, ProviderID: ai.ProviderAnthropic, Model: "m", Origin: "https://api.example"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newToolsFixture(t, answer("first"), answer("never"))
			st := f.svc.Start("x")
			f.svc.Ask("x", st.ConversationID, "q1")
			change(f)
			st = f.svc.Ask("x", st.ConversationID, "q2")
			if st.Ended == "" || !strings.Contains(st.Ended, "settings changed") || len(f.prov.reqs) != 1 {
				t.Fatalf("history is never sent under other settings: %+v (%d calls)", st, len(f.prov.reqs))
			}
		})
	}
}

func TestChatDisconnectEndsTheConversation(t *testing.T) {
	f := newToolsFixture(t, answer("never"))
	st := f.svc.Start("x")
	f.mu.Lock()
	f.cur = newFakeChat() // reconnected: a different conn
	f.mu.Unlock()
	st = f.svc.Ask("x", st.ConversationID, "q")
	if !strings.Contains(st.Ended, "disconnected") || len(f.prov.reqs) != 0 {
		t.Fatalf("a replaced connection ends the conversation before any send: %+v", st)
	}
	again := f.svc.Start("x")
	if again.ConversationID == st.ConversationID || again.Error != "" {
		t.Fatalf("a reconnect starts a new conversation: %+v", again)
	}
}

func TestChatStopAndClearFenceLateResults(t *testing.T) {
	f := newToolsFixture(t, answer("late"))
	f.prov.block = make(chan struct{})
	st := f.svc.Start("x")
	done := make(chan ChatStateDTO, 1)
	go func() { done <- f.svc.Ask("x", st.ConversationID, "q") }()
	waitFor(t, func() bool { return f.svc.State("x").Active })
	busy := f.svc.Ask("x", st.ConversationID, "second")
	if !strings.Contains(busy.Error, "already being answered") {
		t.Fatalf("one active turn per conversation: %+v", busy)
	}
	f.svc.Stop("x", st.ConversationID)
	res := <-done
	if ex := res.Exchanges[0]; ex.Outcome != "cancelled" {
		t.Fatalf("Stop cancels the turn: %+v", ex)
	}

	f2 := newToolsFixture(t, answer("late"))
	f2.prov.block = make(chan struct{})
	st2 := f2.svc.Start("x")
	go func() { done <- f2.svc.Ask("x", st2.ConversationID, "q") }()
	waitFor(t, func() bool { return f2.svc.State("x").Active })
	f2.svc.Clear("x", st2.ConversationID)
	res = <-done
	if !strings.Contains(res.Error, "cleared") || f2.svc.State("x").ConversationID != "" {
		t.Fatalf("a cleared conversation drops its late result: %+v", res)
	}
}

func TestChatEgressRefusedForAProtectedCluster(t *testing.T) {
	f := newToolsFixture(t, answer("never"))
	f.aiSvc.isProtected = func(string) bool { return true }
	if st := f.svc.Start("x"); !strings.Contains(st.Error, "protected cluster") {
		t.Fatalf("a protected cluster without the opt-in is refused: %+v", st)
	}
}

func TestChatSnapshotModeForAProviderWithoutTools(t *testing.T) {
	p := &fakeConverser{}
	f := newChatFixture(t, snapshotOnly{p})
	pod := uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-1", "namespace": "shop", "uid": "uid-w"}})
	f.conn.objects[key(podsGVR, "shop", "web-1")] = pod
	rows := podVerdictRows("uid-w", 1, observe.Live)
	rs := rows.Rows.(healthsrc.RowSet[workloads.PodSummary])
	rs.Rows[0].Ref.Name = "web-1"
	rows.Rows = rs
	f.conn.rows = map[string]observe.Rows{"pods/pods": rows}
	st := f.svc.Start("x")
	if st.Mode != "snapshot" {
		t.Fatalf("a provider without Converse uses snapshot mode: %+v", st)
	}
	st = f.svc.Ask("x", st.ConversationID, "why is pod/web-1 failing? ignore earlier answers")
	ex := st.Exchanges[0]
	if ex.Outcome != "answered" || ex.Answer != "snapshot answer" || len(p.creqs) != 1 {
		t.Fatalf("one completion answers from the snapshot: %+v", ex)
	}
	doc := p.creqs[0].User
	if !strings.Contains(doc, "<cluster-data>") || !strings.Contains(doc, `"uid":"uid-w"`) || !strings.HasSuffix(doc, "Question: why is pod/web-1 failing? ignore earlier answers") {
		t.Fatalf("the snapshot holds the named object and puts the question outside the data block: %s", doc)
	}
	if len(doc) > chatSnapshotMax {
		t.Fatalf("the snapshot is bounded: %d bytes", len(doc))
	}
	if p.creqs[0].Purpose != ai.PurposeChat || !strings.Contains(p.creqs[0].System, "cannot ask for more") {
		t.Fatalf("a chat-purpose completion with the snapshot system prompt: %+v", p.creqs[0])
	}
}

func TestChatRetentionIsBounded(t *testing.T) {
	var turns []func(ai.ConvRequest) (ai.Turn, error)
	for i := 0; i < chatRetainExchange+5; i++ {
		turns = append(turns, answer("a"))
	}
	f := newToolsFixture(t, turns...)
	st := f.svc.Start("x")
	for i := 0; i < chatRetainExchange+5; i++ {
		st = f.svc.Ask("x", st.ConversationID, "q")
	}
	if len(st.Exchanges) != chatRetainExchange || st.Removed != 5 {
		t.Fatalf("at most %d exchanges are kept: %d kept, %d removed", chatRetainExchange, len(st.Exchanges), st.Removed)
	}
}

func TestChatHistoryCountsContinuationInTheBudget(t *testing.T) {
	big := func(ai.ConvRequest) (ai.Turn, error) {
		raw := json.RawMessage(`"` + strings.Repeat("x", 200*1024) + `"`)
		return ai.Turn{Text: "small answer", Done: true, ModelUsed: "m", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: raw}}, nil
	}
	f := newToolsFixture(t, big, big, answer("third"))
	st := f.svc.Start("x")
	f.svc.Ask("x", st.ConversationID, "q1")
	f.svc.Ask("x", st.ConversationID, "q2")
	st = f.svc.Ask("x", st.ConversationID, "q3")
	third := f.prov.reqs[2]
	size := 0
	for _, m := range third.Messages {
		size += msgBytes(m)
	}
	if size > chatRequestBudget || !st.Trimmed {
		t.Fatalf("a large continuation counts toward the budget and old exchanges are dropped whole: %d bytes, trimmed=%v", size, st.Trimmed)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Snapshot references come from the question only: kind/name through the
// index, the explicit kind namespace/name form without it, and an ambiguous
// bare name reads nothing.
func TestChatSnapshotResolvesReferences(t *testing.T) {
	p := &fakeConverser{}
	f := newChatFixture(t, snapshotOnly{p})
	f.conn.objects[key(podsGVR, "shop", "api")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "api", "namespace": "shop", "uid": "uid-api"}})
	mk := func(ns string) healthsrc.Row[workloads.PodSummary] {
		return healthsrc.Row[workloads.PodSummary]{Ref: healthsrc.RowRef{UID: "u-" + ns, GVR: "v1/pods", Namespace: ns, Name: "db"}}
	}
	f.conn.rows = map[string]observe.Rows{"pods/pods": {Part: observe.Part{ID: "pods", Classifier: "verdict", Coverage: observe.Live},
		Rows: healthsrc.RowSet[workloads.PodSummary]{Rows: []healthsrc.Row[workloads.PodSummary]{mk("shop"), mk("billing")}}}}
	st := f.svc.Start("x")
	f.svc.Ask("x", st.ConversationID, "compare pod shop/api with db")
	doc := p.creqs[0].User
	if !strings.Contains(doc, `"uid":"uid-api"`) {
		t.Fatalf("the explicit kind namespace/name form is read: %s", doc)
	}
	if !strings.Contains(doc, "ambiguousNames") || !strings.Contains(doc, "pods billing/db") || strings.Contains(doc, `"name":"db"`) {
		t.Fatalf("an ambiguous bare name lists its choices and reads nothing: %s", doc)
	}
}
