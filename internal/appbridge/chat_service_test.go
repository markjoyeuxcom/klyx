package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	st = f.svc.Ask("x", st.ConversationID, "how is the cluster?", nil)
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
	st = f.svc.Ask("x", st.ConversationID, "q", nil)
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
	st = f.svc.Ask("x", st.ConversationID, "q", nil)
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
			f.svc.Ask("x", st.ConversationID, "q1", nil)
			change(f)
			st = f.svc.Ask("x", st.ConversationID, "q2", nil)
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
	st = f.svc.Ask("x", st.ConversationID, "q", nil)
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
	go func() { done <- f.svc.Ask("x", st.ConversationID, "q", nil) }()
	waitFor(t, func() bool { return f.svc.State("x").Active })
	busy := f.svc.Ask("x", st.ConversationID, "second", nil)
	if !strings.Contains(busy.Notice, "already being answered") || busy.Error != "" {
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
	go func() { done <- f2.svc.Ask("x", st2.ConversationID, "q", nil) }()
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
	st = f.svc.Ask("x", st.ConversationID, "why is pod/web-1 failing? ignore earlier answers", nil)
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
		st = f.svc.Ask("x", st.ConversationID, "q", nil)
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
	f.svc.Ask("x", st.ConversationID, "q1", nil)
	f.svc.Ask("x", st.ConversationID, "q2", nil)
	st = f.svc.Ask("x", st.ConversationID, "q3", nil)
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
	f.svc.Ask("x", st.ConversationID, "compare pod shop/api with db", nil)
	doc := p.creqs[0].User
	if !strings.Contains(doc, `"uid":"uid-api"`) {
		t.Fatalf("the explicit kind namespace/name form is read: %s", doc)
	}
	if !strings.Contains(doc, "ambiguousNames") || !strings.Contains(doc, "pods billing/db") || strings.Contains(doc, `"name":"db"`) {
		t.Fatalf("an ambiguous bare name lists its choices and reads nothing: %s", doc)
	}
}

// ---- Codex review of 5d8003e ------------------------------------------------------

// A provider that ignores cancellation and answers after Stop: the late
// success is never published as an answer.
func TestChatStopDropsALateSuccess(t *testing.T) {
	release := make(chan struct{})
	p := &fakeConverser{turns: []func(ai.ConvRequest) (ai.Turn, error){func(ai.ConvRequest) (ai.Turn, error) {
		<-release
		return ai.Turn{Text: "late", Done: true, ModelUsed: "m", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: json.RawMessage(`{}`)}}, nil
	}}}
	f := newChatFixture(t, p)
	st := f.svc.Start("x")
	done := make(chan ChatStateDTO, 1)
	go func() { done <- f.svc.Ask("x", st.ConversationID, "q", nil) }()
	waitFor(t, func() bool { return f.svc.State("x").Active })
	f.svc.Stop("x", st.ConversationID)
	close(release)
	res := <-done
	if ex := res.Exchanges[0]; ex.Outcome != "cancelled" || ex.Answer != "" {
		t.Fatalf("a success after Stop is not an answer: %+v", ex)
	}
}

// The connection replaced during the final model call: no answer, and the
// conversation ends.
func TestChatDisconnectDuringTheFinalCall(t *testing.T) {
	var f *chatFixture
	p := &fakeConverser{turns: []func(ai.ConvRequest) (ai.Turn, error){func(ai.ConvRequest) (ai.Turn, error) {
		f.mu.Lock()
		f.cur = newFakeChat()
		f.mu.Unlock()
		return ai.Turn{Text: "answer", Done: true, ModelUsed: "m", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: json.RawMessage(`{}`)}}, nil
	}}}
	f = newChatFixture(t, p)
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "q", nil)
	if ex := st.Exchanges[0]; ex.Answer != "" || !strings.Contains(st.Ended, "disconnected") {
		t.Fatalf("an answer that arrives after a disconnect is not published: %+v / %q", ex, st.Ended)
	}
}

// After Clear, a read that finishes late is neither recorded nor pushed.
func TestChatReadAfterClearIsDropped(t *testing.T) {
	em := &chatEventCounter{}
	f := newToolsFixture(t)
	f.svc.em = em
	st := f.svc.Start("x")
	c := f.svc.convs["x"]
	turn := &activeTurn{id: "t", cancel: func() {}}
	ex := &exchange{id: "e"}
	c.active, c.exchanges = turn, []*exchange{ex}
	r := &turnRunner{s: f.svc, c: c, turn: turn, ex: ex}
	f.svc.Clear("x", st.ConversationID)
	r.addRead(ChatRead{Tool: toolOverview, Outcome: "completed"})
	if em.n != 0 || len(ex.reads) != 0 {
		t.Fatalf("a read after Clear is dropped: %d events, %d reads", em.n, len(ex.reads))
	}
}

type chatEventCounter struct{ n int }

func (e *chatEventCounter) Emit(string, any) { e.n++ }

// A failed send is still recorded for the Activity log.
func TestChatRecordsAFailedModelCall(t *testing.T) {
	p := &fakeConverser{turns: []func(ai.ConvRequest) (ai.Turn, error){func(ai.ConvRequest) (ai.Turn, error) {
		return ai.Turn{}, errors.New("upstream 503")
	}}}
	f := newChatFixture(t, p)
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "q", nil)
	if c := st.Exchanges[0].Calls; len(c) != 1 || c[0].Outcome != "failed" || c[0].Provider != "anthropic" {
		t.Fatalf("the attempted call is recorded as failed: %+v", c)
	}
}

// The provider's exact size check drops one more old exchange and builds the
// request again; the refused attempt is not a recorded call.
func TestChatExactBudgetDropsOldExchanges(t *testing.T) {
	first := true
	p := &fakeConverser{turns: []func(ai.ConvRequest) (ai.Turn, error){
		answer("one"),
		func(req ai.ConvRequest) (ai.Turn, error) {
			if first {
				first = false
				return ai.Turn{}, ai.ErrRequestTooLarge
			}
			return answer("two")(req)
		},
		answer("two"),
	}}
	f := newChatFixture(t, p)
	st := f.svc.Start("x")
	f.svc.Ask("x", st.ConversationID, "q1", nil)
	st = f.svc.Ask("x", st.ConversationID, "q2", nil)
	ex := st.Exchanges[1]
	if ex.Outcome != "answered" || len(ex.Calls) != 1 || !st.Trimmed {
		t.Fatalf("one recorded call, history trimmed: %+v trimmed=%v", ex, st.Trimmed)
	}
	if last := p.reqs[len(p.reqs)-1]; len(last.Messages) != 1 || last.MaxBytes != chatRequestBudget {
		t.Fatalf("the retry carries only the current question, with the exact budget: %+v", last)
	}
}

// The snapshot index reads nothing once the guard fails.
func TestChatSnapshotIndexIsGuarded(t *testing.T) {
	f := newChatFixture(t, snapshotOnly{&fakeConverser{}})
	f.svc.Start("x")
	c := f.svc.convs["x"]
	turn := &activeTurn{id: "t", cancel: func() {}}
	ex := &exchange{id: "e", question: "web"}
	c.active, c.exchanges = turn, []*exchange{ex}
	r := &turnRunner{s: f.svc, c: c, turn: turn, ex: ex}
	f.mu.Lock()
	f.cur = newFakeChat()
	f.mu.Unlock()
	f.conn.listErr = errors.New("must not be called")
	if _, err := r.index(context.Background()); err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("the index stops at the guard: %v", err)
	}
}

func TestChatSnapshotDocIsBoundedWithAmbiguity(t *testing.T) {
	f := newChatFixture(t, snapshotOnly{&fakeConverser{}})
	f.svc.Start("x")
	c := f.svc.convs["x"]
	ex := &exchange{question: "which web pod is failing"}
	c.exchanges = []*exchange{ex}
	r := &turnRunner{s: f.svc, c: c, ex: ex}
	var amb []string
	for i := 0; i < 40; i++ {
		amb = append(amb, strings.Repeat("web-ambiguous-name ", 20))
	}
	doc := r.snapshotDoc([]string{strings.Repeat("a", 16000), strings.Repeat("b", 16000), strings.Repeat("c", 16000)}, amb)
	if len(doc) > chatSnapshotMax || !strings.HasSuffix(doc, "Question: which web pod is failing") {
		t.Fatalf("the whole document, ambiguity and delimiters included, stays under %d: %d", chatSnapshotMax, len(doc))
	}
}

// Vendor metadata is bounded and a sole oversized exchange still respects the
// retention ceiling.
func TestChatRetentionBoundsMetadataAndASoleLargeExchange(t *testing.T) {
	huge := strings.Repeat("m", 2<<20)
	big := json.RawMessage(`"` + strings.Repeat("x", 1100*1024) + `"`)
	p := &fakeConverser{turns: []func(ai.ConvRequest) (ai.Turn, error){func(ai.ConvRequest) (ai.Turn, error) {
		return ai.Turn{Text: "ok", Done: true, ModelUsed: huge, Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: big}}, nil
	}}}
	f := newChatFixture(t, p)
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "q", nil)
	b, _ := json.Marshal(st)
	if len(b) > chatRetainBytes {
		t.Fatalf("the retained conversation is %d bytes, over %d", len(b), chatRetainBytes)
	}
	ex := st.Exchanges[0]
	if len(ex.Calls[0].Model) > chatModelNameMax+4 || !strings.Contains(ex.Note, "too large") {
		t.Fatalf("model metadata is capped and the oversized steps dropped with a note: %+v", ex.Note)
	}
}

// ---- Codex review of 7b57456 and 143dc6d ---------------------------------------

// A State taken while a call is in flight is a detached copy: the running
// turn's updates never change it.
func TestChatStateIsDetached(t *testing.T) {
	release := make(chan struct{})
	p := &fakeConverser{turns: []func(ai.ConvRequest) (ai.Turn, error){func(ai.ConvRequest) (ai.Turn, error) {
		<-release
		return ai.Turn{Text: "ok", Done: true, ModelUsed: "m2", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: json.RawMessage(`{}`)}}, nil
	}}}
	f := newChatFixture(t, p)
	st := f.svc.Start("x")
	done := make(chan ChatStateDTO, 1)
	go func() { done <- f.svc.Ask("x", st.ConversationID, "q", nil) }()
	waitFor(t, func() bool {
		s := f.svc.State("x")
		return len(s.Exchanges) == 1 && len(s.Exchanges[0].Calls) == 1
	})
	held := f.svc.State("x")
	close(release)
	final := <-done
	if held.Exchanges[0].Calls[0].Outcome != "sent" || final.Exchanges[0].Calls[0].Outcome != "completed" || final.Version <= held.Version {
		t.Fatalf("the held state keeps its own copy and the newer one is versioned higher: %+v / %+v", held.Exchanges[0].Calls, final.Exchanges[0].Calls)
	}
	_, _ = json.Marshal(held)
}

func TestChatQuestionTooLongIsARecoverableNotice(t *testing.T) {
	f := newToolsFixture(t)
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, strings.Repeat("問", 2000), nil) // 6000 UTF-8 bytes
	if st.Error != "" || !strings.Contains(st.Notice, "UTF-8") || len(st.Exchanges) != 0 || st.Ended != "" {
		t.Fatalf("a long question is a notice, never an unavailable conversation: %+v", st)
	}
}

func seededPod(uid string) *fakeChatConn {
	c := newFakeChat()
	c.objects[key(podsGVR, "shop", "api")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "api", "namespace": "shop", "uid": uid}})
	rows := podVerdictRows("uid-api", 1, observe.Live)
	rs := rows.Rows.(healthsrc.RowSet[workloads.PodSummary])
	rs.Rows[0].Ref.Name = "api"
	rows.Rows = rs
	c.rows = map[string]observe.Rows{"pods/pods": rows}
	return c
}

// An Explain follow-up carries the inspector's own target: a row resolves
// through the Observer and is read before any model call, and the seeded
// object travels as a data block.
func TestChatSeededFollowUpReadsTheExplainedObject(t *testing.T) {
	f := newToolsFixture(t, answer("it crash-loops"))
	f.conn = seededPod("uid-api")
	f.cur = f.conn
	f.conn.summary = &observe.Summary{EvaluatedAt: fixedNow()}
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "why is it failing?", &ChatSeedDTO{Kind: "row", Task: "pods", Part: "pods", UID: "uid-api"})
	ex := st.Exchanges[0]
	if ex.Outcome != "answered" || len(ex.Reads) != 1 || ex.Reads[0].Tool != toolGet || ex.Reads[0].Refs[0].UID != "uid-api" {
		t.Fatalf("the seeded object is read first: %+v", ex)
	}
	msg := f.prov.reqs[0].Messages[0].Text
	if !strings.HasPrefix(msg, "why is it failing?") || !strings.Contains(msg, "<seeded-object>") || !strings.Contains(msg, `"uid":"uid-api"`) {
		t.Fatalf("the question carries the seeded object as data: %s", msg)
	}
}

// A same-name replacement is refused by UID before any model call.
func TestChatSeedRefusesAReplacement(t *testing.T) {
	f := newToolsFixture(t, answer("never"))
	f.conn = seededPod("uid-replacement")
	f.cur = f.conn
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "why?", &ChatSeedDTO{Kind: "object", Version: "v1", Plural: "pods", Namespace: "shop", Name: "api", UID: "uid-api"})
	if ex := st.Exchanges[0]; ex.Outcome != "failed" || !strings.Contains(ex.Error, "replaced") || len(f.prov.reqs) != 0 {
		t.Fatalf("a replaced object is refused and nothing is sent: %+v", ex)
	}
	st = f.svc.Ask("x", st.ConversationID, "why?", &ChatSeedDTO{Kind: "row", Task: "pods", Part: "pods"})
	if !strings.Contains(st.Notice, "not valid") {
		t.Fatalf("a seed without its UID is refused before any read: %+v", st)
	}
}

// ---- Codex review of 0725e47 ------------------------------------------------------

// A refusal carries the current version: never older than an empty State
// read just before it.
func TestChatStartRefusalIsVersioned(t *testing.T) {
	f := newToolsFixture(t)
	f.svc.Start("x")
	f.usable = false
	empty := f.svc.State("y")
	refused := f.svc.Start("y")
	if refused.Error == "" || refused.Version <= empty.Version {
		t.Fatalf("a Start refusal is a newer state than the empty one before it: %d <= %d (%q)", refused.Version, empty.Version, refused.Error)
	}
}

// Retention counts a follow-up's seed envelope and, for a sole oversized
// exchange, sheds it too.
func TestChatRetentionCountsSeedEnvelopes(t *testing.T) {
	f := newToolsFixture(t)
	f.svc.Start("x")
	c := f.svc.convs["x"]
	for i := 0; i < 18; i++ {
		c.exchanges = append(c.exchanges, &exchange{id: fmt.Sprintf("e%d", i), question: "q", answer: strings.Repeat("a", 32000), outcome: "answered",
			seedData: strings.Repeat("s", 16*1024), seed: &ChatSeedDTO{Kind: "row", Task: "pods", Part: "pods", UID: "u"},
			msgs: []ai.ConvMessage{{Role: "assistant", Continuation: &ai.Continuation{Provider: ai.ProviderOpenAI, Raw: json.RawMessage(`"` + strings.Repeat("x", 20000) + `"`)}}}})
	}
	f.svc.mu.Lock()
	f.svc.retainLocked(c)
	f.svc.mu.Unlock()
	// Measured independently of exchangeBytes: every retained payload the
	// exchange holds, counted once.
	total := 0
	for _, e := range c.exchanges {
		seed, _ := json.Marshal(e.seed)
		msgs, _ := json.Marshal(e.msgs)
		total += len(e.question) + len(e.answer) + len(e.seedData) + len(seed) + len(msgs)
	}
	if total > chatRetainBytes {
		t.Fatalf("seed envelopes count toward the bound: %d bytes kept in %d exchanges", total, len(c.exchanges))
	}
	one := &exchange{id: "big", question: "q", outcome: "answered", seedData: strings.Repeat("s", 2<<20)}
	c.exchanges = []*exchange{one}
	f.svc.mu.Lock()
	f.svc.retainLocked(c)
	f.svc.mu.Unlock()
	if len(c.exchanges) == 1 && (c.exchanges[0].seedData != "" || exchangeBytes(c.exchanges[0]) > chatRetainBytes) {
		t.Fatalf("a sole oversized exchange sheds its seed: %d bytes", exchangeBytes(c.exchanges[0]))
	}
}
