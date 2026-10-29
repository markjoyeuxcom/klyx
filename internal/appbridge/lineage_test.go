package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/readfence"
)

// Settings S3a §4.2-§4.3: every AI read of alerts records its lineage, and
// every later send of what it informed checks the lineage still holds.

// canaryLineage carries values that must never leave Klyx: the lineage is
// a token checked at the send, never sent.
var canaryLineage = alertmanager.Lineage{Mode: "subscribed", Identity: "source|lgtm-we|r7|s8|aCANARY-ACCESS|tenant=|match=",
	Installation: "lgtm-we", InstRev: 7, SubRev: 8, Host: "prd", HostConn: 987654321, Access: "CANARY-ACCESS", CredRev: 123456789}

var lineageCanaries = []string{"CANARY-ACCESS", "987654321", "123456789", "r7|s8"}

// lineageTerms is the test's lineage check: a lineage holds until moved,
// and holdFor lets it hold for that many more checks first.
type lineageTerms struct {
	mu      sync.Mutex
	moved   map[string]string
	holdFor int
	checks  int
}

func withLineageCheck(t *testing.T) *lineageTerms {
	t.Helper()
	lt := &lineageTerms{moved: map[string]string{}}
	SetAlertLineageCheck(func(_ string, l alertmanager.Lineage) string {
		lt.mu.Lock()
		defer lt.mu.Unlock()
		lt.checks++
		if lt.holdFor > 0 {
			lt.holdFor--
			return ""
		}
		return lt.moved[l.Identity]
	})
	t.Cleanup(func() { alertLineageCheck.Store(nil) })
	return lt
}

func (lt *lineageTerms) move(l alertmanager.Lineage, why string) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.moved[l.Identity] = why
}

func (lt *lineageTerms) restore(l alertmanager.Lineage) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	delete(lt.moved, l.Identity)
}

const movedWhy = "lgtm-we changed since its alerts were read"

// alertsLineageChat is a fake connection whose alerts rows were read under
// canaryLineage, with a summary whose alerts part counts them.
func alertsLineageChat() *fakeChatConn {
	f := alertsChat(observe.KindCoverage{Kind: healthsrc.SnapAlerts, Coverage: observe.Live, AsOf: &alertSeen},
		amAlert("WebDown", alertmanager.StateActive, alertmanager.Subject{Kind: "Deployment", Namespace: "shop", Name: "web", Basis: []string{"namespace", "deployment"}}, "uid-d"))
	rows := f.rows[healthsrc.TaskAlerts+"/alerts"]
	ar := rows.Rows.(healthsrc.AlertRows)
	ar.Lineage = canaryLineage
	rows.Rows = ar
	rows.Part.Classifier, rows.Part.Coverage, rows.Part.Ver = "alerts", observe.Live, 9
	rows.Part.Alerts = &observe.AlertCounts{Counted: 2, Critical: 2}
	f.rows[healthsrc.TaskAlerts+"/alerts"] = rows
	f.summary = &observe.Summary{EvaluatedAt: fixedNow(), Connection: "connected", Tasks: []observe.Task{
		{ID: healthsrc.TaskAlerts, Coverage: observe.Live, Parts: []observe.Part{{ID: "alerts", Classifier: "alerts", Coverage: observe.Live, Ver: 8,
			Alerts: &observe.AlertCounts{Counted: 4, Critical: 4}}}}}}
	return f
}

// useConn makes conn the fixture's connection.
func (f *chatFixture) useConn(conn *fakeChatConn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conn, f.cur = conn, conn
}

func (f *chatFixture) lineagesOf(id string) []alertmanager.Lineage {
	f.svc.mu.Lock()
	defer f.svc.mu.Unlock()
	for _, c := range f.svc.convs {
		if c.id == id {
			return append([]alertmanager.Lineage(nil), c.lineages...)
		}
	}
	return nil
}

var alertsCall = ai.ToolCall{ID: "al", Name: toolAlerts, Args: json.RawMessage(`{"kind":"deployment","namespace":"shop","name":"web"}`)}

func noCanary(t *testing.T, what string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	for _, c := range lineageCanaries {
		if strings.Contains(string(b), c) {
			t.Fatalf("%s carries the lineage (%s): %s", what, c, b)
		}
	}
}

// get_alerts records its read's lineage; once the lineage no longer holds,
// the next send ends the conversation, and nothing more is sent. The
// token never enters a request.
func TestAnAlertsReadEndsTheConversationWhenItsTermsMove(t *testing.T) {
	lt := withLineageCheck(t)
	f := newToolsFixture(t, calls(alertsCall), answer("web is down"), answer("never"))
	f.useConn(alertsLineageChat())
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "is web alerting?", nil)
	if ex := st.Exchanges[0]; ex.Outcome != "answered" || ex.Reads[0].Outcome != "completed" {
		t.Fatalf("%+v", ex)
	}
	if got := f.lineagesOf(st.ConversationID); len(got) != 1 || got[0] != canaryLineage {
		t.Fatalf("recorded: %+v", got)
	}
	lt.move(canaryLineage, movedWhy)
	st = f.svc.Ask("x", st.ConversationID, "and now?", nil)
	if !strings.Contains(st.Ended, movedWhy+"; start a new conversation") || len(f.prov.reqs) != 2 {
		t.Fatalf("a moved lineage ends the conversation before any send: %q (%d requests)", st.Ended, len(f.prov.reqs))
	}
	noCanary(t, "a model request", f.prov.reqs)
	noCanary(t, "the conversation's state", st)
	// recovery is new work under the new terms
	lt.restore(canaryLineage)
	if again := f.svc.Start("x"); again.Error != "" || again.ConversationID == st.ConversationID {
		t.Fatalf("a new conversation: %+v", again)
	}
}

// Rows read under terms that have already moved are refused, never
// recorded: the conversation goes on, and they are read again shortly.
func TestAlertsAlreadyStaleAreRefusedNotRecorded(t *testing.T) {
	lt := withLineageCheck(t)
	lt.move(canaryLineage, movedWhy)
	f := newToolsFixture(t, calls(alertsCall), answer("could not read them"), answer("second"))
	f.useConn(alertsLineageChat())
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "is web alerting?", nil)
	ex := st.Exchanges[0]
	if ex.Outcome != "answered" || ex.Reads[0].Outcome == "completed" || !strings.Contains(ex.Reads[0].Detail, movedWhy) {
		t.Fatalf("the read is refused: %+v", ex.Reads)
	}
	result := f.prov.reqs[1].Messages[2].Results[0].Content
	if !strings.Contains(result, movedWhy) || !strings.Contains(result, "Klyx reads them again within 30s") || strings.Contains(result, "WebDown") {
		t.Fatalf("the model is told, without the rows: %s", result)
	}
	if got := f.lineagesOf(st.ConversationID); len(got) != 0 {
		t.Fatalf("a refused read recorded its lineage: %+v", got)
	}
	if st = f.svc.Ask("x", st.ConversationID, "q2", nil); st.Ended != "" || st.Exchanges[1].Outcome != "answered" {
		t.Fatalf("the conversation goes on: %+v", st)
	}
}

// A conversation keeps every token it recorded: an exchange whose reads
// were trimmed away still informed what followed (S3a §4.2).
func TestLineagesOutliveTheirExchanges(t *testing.T) {
	lt := withLineageCheck(t)
	turns := []func(ai.ConvRequest) (ai.Turn, error){calls(alertsCall), answer("first")}
	for i := 0; i < chatRetainExchange+1; i++ {
		turns = append(turns, answer("a"))
	}
	f := newToolsFixture(t, append(turns, answer("never"))...)
	f.useConn(alertsLineageChat())
	st := f.svc.Start("x")
	for i := 0; i < chatRetainExchange+2; i++ {
		st = f.svc.Ask("x", st.ConversationID, "q", nil)
	}
	if st.Removed == 0 || st.Ended != "" {
		t.Fatalf("fixture: the first exchange is removed: %+v", st)
	}
	sent := len(f.prov.reqs)
	lt.move(canaryLineage, movedWhy)
	st = f.svc.Ask("x", st.ConversationID, "q", nil)
	if !strings.Contains(st.Ended, movedWhy) || len(f.prov.reqs) != sent {
		t.Fatalf("a removed exchange's lineage still ends the conversation: %q", st.Ended)
	}
}

// A summary's alert counts come from one capture of the alerts rows, whose
// lineage is recorded with them; counts that cannot be tied to their terms
// are left out, and say so (S3a §4.2).
func TestSummaryAlertCountsCarryTheirLineage(t *testing.T) {
	for _, tool := range []string{toolOverview, toolAttention} {
		t.Run(tool, func(t *testing.T) {
			conn := alertsLineageChat()
			tools := newChatTools(conn, fixedNow)
			var recorded []alertmanager.Lineage
			var refuse error
			tools.record = func(l alertmanager.Lineage) error {
				if refuse != nil {
					return refuse
				}
				recorded = append(recorded, l)
				return nil
			}
			read := func() (string, chatEnvelope) {
				res := tools.Run(context.Background(), tool, json.RawMessage(`{}`))
				var env chatEnvelope
				_ = json.Unmarshal(res.JSON, &env)
				return string(res.JSON), env
			}
			out, env := read()
			if len(recorded) != 1 || recorded[0] != canaryLineage {
				t.Fatalf("recorded: %+v", recorded)
			}
			// the rows' own counts (2), never the summary's (4) read earlier
			if !strings.Contains(out, `"counted":2`) && !strings.Contains(out, `"alerts":2`) || strings.Contains(out, `"counted":4`) || strings.Contains(out, `"alerts":4`) {
				t.Fatalf("counts: %s", out)
			}
			noCanary(t, tool, env)
			refuse = fmt.Errorf("%s", movedWhy)
			out, env = read()
			if strings.Contains(out, `"counted":`) || strings.Contains(out, `"alerts":2`) || !strings.Contains(strings.Join(env.Errors, "\n"), movedWhy+"; their counts are left out") {
				t.Fatalf("moved: %s", out)
			}
			refuse = nil
			delete(conn.rows, healthsrc.TaskAlerts+"/alerts")
			out, env = read()
			if strings.Contains(out, `"counted":`) || !strings.Contains(strings.Join(env.Errors, "\n"), "could not be read with their source's terms") {
				t.Fatalf("unreadable: %s", out)
			}
		})
	}
	// outside a turn nothing is sent later, so the summary is shown as read
	out, _ := run(t, alertsLineageChat(), toolOverview, `{}`)
	if b, _ := json.Marshal(out.Data); !strings.Contains(string(b), `"counted":4`) {
		t.Fatalf("outside a turn: %s", b)
	}
}

// The Claude Code CLI's snapshot carries the summary's alert counts: it
// records their lineage, and the next release is checked.
func TestASnapshotRecordsItsAlertsLineage(t *testing.T) {
	lt := withLineageCheck(t)
	p := &fakeConverser{}
	f := newChatFixture(t, snapshotOnly{p})
	f.useConn(alertsLineageChat())
	st := f.svc.Start("x")
	if st.Mode != "snapshot" {
		t.Fatalf("fixture: %+v", st)
	}
	st = f.svc.Ask("x", st.ConversationID, "how is it?", nil)
	if st.Exchanges[0].Outcome != "answered" || len(f.lineagesOf(st.ConversationID)) != 1 {
		t.Fatalf("%+v %+v", st.Exchanges[0], f.lineagesOf(st.ConversationID))
	}
	noCanary(t, "the snapshot", p.creqs)
	lt.move(canaryLineage, movedWhy)
	st = f.svc.Ask("x", st.ConversationID, "again?", nil)
	if !strings.Contains(st.Ended, movedWhy) || len(p.creqs) != 1 {
		t.Fatalf("the release is refused: %q (%d)", st.Ended, len(p.creqs))
	}
}

// A3 ruling 2: a plan offered by a conversation whose alerts lineage no
// longer holds is withdrawn, for good.
func TestAPlanRestingOnMovedAlertsIsWithdrawn(t *testing.T) {
	lt := withLineageCheck(t)
	sf := newStepsFixture(t, pauseWeb)
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	c.lineages = append(c.lineages, canaryLineage)
	sf.svc.mu.Unlock()
	if rv := sf.review(1); !rv.Offered {
		t.Fatalf("while its lineage holds: %+v", rv)
	}
	lt.move(canaryLineage, movedWhy)
	plan := sf.svc.InvestigationState("x").Exchanges[0].Plan
	if plan == nil || plan.Status != planStatusWithdrawn || !strings.Contains(plan.StatusWhy, movedWhy) {
		t.Fatalf("withdrawn: %+v", plan)
	}
	lt.restore(canaryLineage)
	if rv := sf.review(1); rv.Offered {
		t.Fatalf("a withdrawn plan came back: %+v", rv)
	}
	if len(sf.sc.recorded()) != 0 {
		t.Fatal("written")
	}
}

// recordingFenced honours its call's fence and keeps what it was sent.
type recordingFenced struct {
	fencedProvider
	users []string
}

func (p *recordingFenced) Complete(ctx context.Context, req ai.Request) (ai.Result, error) {
	if err := readfence.Check(ctx); err != nil {
		return ai.Result{}, err
	}
	p.users = append(p.users, req.User)
	return p.fencedProvider.Complete(ctx, req)
}

// Explain records its alerts' lineage on the call: one that moves before
// the send refuses the call, one already moved at the read leaves the
// alerts out, and the token is never sent.
func TestExplainFencesItsAlertsLineage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		set      func(lt *lineageTerms)
		ok       bool
		inPrompt bool
	}{
		{"holds", func(*lineageTerms) {}, true, true},
		{"moves before the send", func(lt *lineageTerms) { lt.holdFor = 1; lt.move(canaryLineage, movedWhy) }, false, false},
		{"moved at the read", func(lt *lineageTerms) { lt.move(canaryLineage, movedWhy) }, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lt := withLineageCheck(t)
			tc.set(lt)
			rows := healthsrc.AlertRows{Lineage: canaryLineage, Alerts: []healthsrc.AlertRow{alertRow("Mine", "active", true, "p-uid", healthsrc.SubjectFound)}}
			conn := &fakeExplainConn{
				details: map[string]crd.InstanceDetail{"/pods/ns/p": {Kind: "Pod", Namespace: "ns", Name: "p", UID: "p-uid"}},
				rows:    map[string]observe.Rows{healthsrc.TaskAlerts + "/alerts": {Task: healthsrc.TaskAlerts, Part: observe.Part{ID: "alerts"}, Rows: rows}},
			}
			p := &recordingFenced{}
			s := NewAIService(func(string) (AIConn, bool) { return conn, true }, &AIState{Provider: p, ProviderID: ai.ProviderAnthropic, Origin: ai.DefaultAnthropicOrigin},
				func(string) bool { return false }, nil)
			r := s.Explain("c", ExplainTargetDTO{Kind: "object", Version: "v1", Plural: "pods", Namespace: "ns", Name: "p", UID: "p-uid"})
			if r.OK != tc.ok || !tc.ok && (len(p.users) != 0 || !strings.Contains(r.Error, movedWhy)) {
				t.Fatalf("%+v (sent %d)", r, len(p.users))
			}
			if tc.ok && strings.Contains(p.users[0], `"name":"Mine"`) != tc.inPrompt {
				t.Fatalf("alerts in the prompt: %v: %s", tc.inPrompt, p.users[0])
			}
			noCanary(t, "Explain's request", p.users)
		})
	}
}
