package appbridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A1 stage 1: read IDs on every ledger entry and envelope, checks that
// Klyx evaluates itself (never the model's words, never a message), and the
// identity each check holds.

func TestChecksReadFailuresNeverMessages(t *testing.T) {
	const needle = "NeedleConditionMessage7x"
	ks := uobj(map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "app", "namespace": "flux-system", "uid": "uid-k"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "HealthCheckFailed", "message": needle, "lastTransitionTime": "2026-10-21T12:05:00Z"},
			map[string]any{"type": "Reconciling", "status": "True", "reason": "Progressing", "message": needle},
			map[string]any{"type": "Healthy", "status": "True", "reason": "Fine"},
		}}})
	subj := ChatCheckSubject{Kind: "Kustomization", Namespace: "flux-system", Name: "app", UID: "uid-k", Identity: identityUID}
	cs := conditionChecks(ks, subj, "2026-10-21T12:06:00Z", &projector{})
	if len(cs) != 1 || cs[0].Statement != "Kustomization flux-system/app: Ready=False, reason HealthCheckFailed" ||
		cs[0].Onset != "2026-10-21T12:05:00Z" || cs[0].Observed != "2026-10-21T12:06:00Z" || cs[0].Class != checkCondition {
		t.Fatalf("%+v", cs)
	}
	b, _ := json.Marshal(cs)
	if strings.Contains(string(b), needle) {
		t.Fatalf("a check never carries a condition's message: %s", b)
	}
	if c := (conditionChecks(uobj(map[string]any{"status": map[string]any{"conditions": []any{
		map[string]any{"type": "Stalled", "status": "True", "reason": "BuildFailed"}}}}), subj, "", &projector{})); len(c) != 1 || !strings.Contains(c[0].Statement, "Stalled=True") {
		t.Fatalf("a negative condition that is True is a failure: %+v", c)
	}
}

func TestChecksReadAPodsContainers(t *testing.T) {
	pod := uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-7d9", "namespace": "shop", "uid": "uid-p"},
		"status": map[string]any{"containerStatuses": []any{
			map[string]any{"name": "web", "restartCount": int64(6), "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "NeedleWaiting"}},
				"lastState": map[string]any{"terminated": map[string]any{"exitCode": int64(1), "reason": "Error"}}},
			map[string]any{"name": "sidecar", "restartCount": int64(0), "state": map[string]any{"running": map[string]any{}}},
		}}})
	subj := ChatCheckSubject{Kind: "Pod", Namespace: "shop", Name: "web-7d9", UID: "uid-p", Identity: identityUID}
	cs := podStatusChecks(pod, subj, "t", &projector{})
	if len(cs) != 1 || cs[0].Statement != "container web of Pod shop/web-7d9: waiting CrashLoopBackOff; restarted 6 times; last terminated with exit code 1 (Error)" {
		t.Fatalf("a healthy container makes no check; a failing one says its reason, restarts and exit: %+v", cs)
	}
	if strings.Contains(cs[0].Statement, "NeedleWaiting") {
		t.Fatal("never the waiting message")
	}
}

func TestEventChecksHoldTheirIdentity(t *testing.T) {
	cs := eventChecks([]chatEvent{
		{Type: "Warning", Reason: "BackOff", Count: 4, Object: "Pod/web-7d9", ObjectUID: "uid-p", Namespace: "shop", LastSeen: "2026-10-21T12:06:00Z"},
		{Type: "Warning", Reason: "BackOff", Count: 10, Object: "Pod/web-7d9", ObjectUID: "uid-p", Namespace: "shop", LastSeen: "2026-10-21T12:07:00Z"},
		{Type: "Warning", Reason: "FailedMount", Object: "Pod/gone", Namespace: "shop"},
		{Type: "Normal", Reason: "Pulled", Object: "Pod/web-7d9", ObjectUID: "uid-p", Namespace: "shop"},
	})
	if len(cs) != 2 || cs[0].Statement != "Warning BackOff on Pod shop/web-7d9 (14 times)" || cs[0].Subject.Identity != identityUID || cs[0].Observed != "2026-10-21T12:07:00Z" {
		t.Fatalf("%+v", cs)
	}
	if cs[1].Subject.Identity != identityName || cs[1].Statement != "Warning FailedMount on Pod shop/gone (by name) (1 time)" {
		t.Fatalf("an event without its object's UID is by name: %+v", cs[1])
	}
}

func TestGetCarriesChecksWithTheReadsID(t *testing.T) {
	f := newFakeChat()
	f.objects[key(podsGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-new"},
		"status":   map[string]any{"containerStatuses": []any{map[string]any{"name": "web", "restartCount": int64(2), "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}}}}})
	f.rows = map[string]observe.Rows{"pods/pods": podVerdictRows("uid-new", 1, observe.Live)}
	f.events = []workloads.EventSummary{{Type: "Warning", Reason: "BackOff", Kind: "Pod", Name: "web", UID: "uid-new", Namespace: "shop", LastSeenUnix: 5}}
	res := newChatTools(f, fixedNow).as("r7").Run(t.Context(), toolGet, json.RawMessage(`{"kind":"pod","namespace":"shop","name":"web"}`))
	var env chatEnvelope
	if err := json.Unmarshal(res.JSON, &env); err != nil {
		t.Fatal(err)
	}
	if env.ReadID != "r7" || res.Read.ID != "r7" || len(env.Checks) != 3 || len(res.Read.Checks) != 3 {
		t.Fatalf("the envelope and the ledger share the read's ID and checks: %s %+v", res.JSON, res.Read)
	}
	classes := []string{}
	for i, c := range env.Checks {
		if c.ID != "r7.c"+string(rune('1'+i)) || c.Subject.UID != "uid-new" || c.Subject.Identity != identityUID {
			t.Fatalf("check %d: %+v", i, c)
		}
		classes = append(classes, c.Class)
	}
	if strings.Join(classes, ",") != "status,event,verdict" {
		t.Fatalf("failures before the verdict: %v", classes)
	}
}

func TestChecksSurviveAShrunkResult(t *testing.T) {
	f := newFakeChat()
	big := map[string]any{}
	for i := 0; i < 400; i++ {
		big[strings.Repeat("k", 8)+string(rune('a'+i%26))+strings.Repeat("x", i%7)] = strings.Repeat("v", 120)
	}
	f.objects[key(podsGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-w", "labels": big},
		"status":   map[string]any{"containerStatuses": []any{map[string]any{"name": "web", "restartCount": int64(3)}}}})
	res := newChatTools(f, fixedNow).as("r2").Run(t.Context(), toolGet, json.RawMessage(`{"kind":"pod","namespace":"shop","name":"web"}`))
	var env chatEnvelope
	_ = json.Unmarshal(res.JSON, &env)
	if len(res.JSON) > chatResultMax || len(env.Checks) != 1 || env.Checks[0].ID != "r2.c1" {
		t.Fatalf("checks stay when the data is shrunk: %d bytes, %+v", len(res.JSON), env.Checks)
	}
}

func TestEveryLedgerEntryHasAnIDThatNumbersOnAcrossTurns(t *testing.T) {
	var batch []ai.ToolCall
	for i := 0; i < chatMaxToolCalls+2; i++ {
		batch = append(batch, ai.ToolCall{ID: string(rune('a' + i)), Name: toolOverview, Args: json.RawMessage(`{}`)})
	}
	f := newToolsFixture(t, calls(batch...), answer("one"), calls(ai.ToolCall{ID: "z", Name: toolOverview, Args: json.RawMessage(`{}`)}), answer("two"))
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "q1", nil)
	seen := map[string]bool{}
	for _, r := range st.Exchanges[0].Reads {
		if r.ID == "" || seen[r.ID] {
			t.Fatalf("every entry, run or refused by the budget, has its own ID: %+v", st.Exchanges[0].Reads)
		}
		seen[r.ID] = true
	}
	if len(seen) != chatMaxToolCalls+2 {
		t.Fatalf("%d IDs", len(seen))
	}
	// the envelope the model read carries the same ID as the ledger
	res := f.prov.reqs[1].Messages[2].Results[0]
	var env chatEnvelope
	_ = json.Unmarshal([]byte(res.Content), &env)
	if !seen[env.ReadID] {
		t.Fatalf("the model's envelope cites a ledger ID: %q", env.ReadID)
	}
	st = f.svc.Ask("x", st.ConversationID, "q2", nil)
	if got := st.Exchanges[1].Reads[0].ID; got != "r11" {
		t.Fatalf("IDs number on across the conversation's turns: %q", got)
	}
	if !strings.Contains(f.prov.reqs[0].System, "[r1.c1]") {
		t.Fatal("the system prompt says how to cite")
	}
}
