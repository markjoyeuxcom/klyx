package appbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/loki"
	"github.com/moomora/klyx/internal/observe"
)

// Settings S3b §4: every AI read of logs or metrics records its lineage,
// and every later send of what it informed checks the lineage still holds.

var metricsCanary = alertmanager.Lineage{Kind: "metrics", Mode: "subscribed", Identity: "metrics|source|lgtm-we-metrics|r3|s4|aCANARY-M-ACCESS|tenant=|match=cluster=x",
	Installation: "lgtm-we-metrics", InstRev: 3, SubRev: 4, Host: "prd", HostConn: 55501, Access: "CANARY-M-ACCESS", Match: "cluster=x"}

const metricsMovedWhy = "lgtm-we-metrics changed since its metrics were read"

var metricsCall = ai.ToolCall{ID: "m", Name: toolMetrics, Args: json.RawMessage(`{"template":"workload_cpu","kind":"Deployment","namespace":"shop","name":"web"}`)}

func TestAMetricsReadEndsTheConversationWhenItsTermsMove(t *testing.T) {
	lt := withLineageCheck(t)
	f := newToolsFixture(t, calls(metricsCall), answer("cpu is fine"), answer("never"))
	conn := alertsChat(observe.KindCoverage{})
	conn.metricsLineage = metricsCanary
	f.useConn(conn)
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "how is web's cpu?", nil)
	if ex := st.Exchanges[0]; ex.Outcome != "answered" || ex.Reads[0].Outcome != "completed" {
		t.Fatalf("%+v", ex)
	}
	if got := f.lineagesOf(st.ConversationID); len(got) != 1 || got[0] != metricsCanary {
		t.Fatalf("recorded: %+v", got)
	}
	// a plan's affected work names what the conversation holds
	if work := f.svc.AlertWork([]string{"x"}); len(work) != 1 || !strings.Contains(work[0].What, "holding metrics read under the old terms") {
		t.Fatalf("work: %+v", work)
	}
	lt.move(metricsCanary, metricsMovedWhy)
	st = f.svc.Ask("x", st.ConversationID, "and now?", nil)
	if !strings.Contains(st.Ended, metricsMovedWhy+"; start a new conversation") || len(f.prov.reqs) != 2 {
		t.Fatalf("a moved lineage ends the conversation before any send: %q (%d requests)", st.Ended, len(f.prov.reqs))
	}
	for _, c := range []string{"CANARY-M-ACCESS", "55501"} {
		b, _ := json.Marshal(f.prov.reqs)
		if strings.Contains(string(b), c) {
			t.Fatalf("a model request carries the lineage: %s", c)
		}
	}
}

// Series read under terms that already moved are refused, never recorded;
// the conversation goes on.
func TestMetricsAlreadyStaleAreRefusedNotRecorded(t *testing.T) {
	lt := withLineageCheck(t)
	lt.move(metricsCanary, metricsMovedWhy)
	f := newToolsFixture(t, calls(metricsCall), answer("could not read them"), answer("second"))
	conn := alertsChat(observe.KindCoverage{})
	conn.metricsLineage = metricsCanary
	f.useConn(conn)
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "how is web's cpu?", nil)
	if r := st.Exchanges[0].Reads[0]; r.Outcome == "completed" || !strings.Contains(r.Detail, metricsMovedWhy) {
		t.Fatalf("the read is refused: %+v", r)
	}
	if !strings.Contains(f.prov.reqs[1].Messages[2].Results[0].Content, "ask again to read it under the current terms") {
		t.Fatalf("the model is told: %s", f.prov.reqs[1].Messages[2].Results[0].Content)
	}
	if got := f.lineagesOf(st.ConversationID); len(got) != 0 {
		t.Fatalf("a refused read recorded its lineage: %+v", got)
	}
	if st = f.svc.Ask("x", st.ConversationID, "q2", nil); st.Ended != "" {
		t.Fatalf("the conversation goes on: %+v", st)
	}
}

func TestALogHistoryReadEndsTheConversationWhenItsTermsMove(t *testing.T) {
	lt := withLineageCheck(t)
	f := newToolsFixture(t, calls(ai.ToolCall{ID: "h", Name: toolLogHistory, Args: json.RawMessage(`{"namespace":"team","pod":"api-1"}`)}), answer("history"), answer("never"))
	h := otelFake()
	h.entries[q(podBranch, loki.ModeName, "")] = lines(3, tNow.Add(-time.Minute), "", "x")
	SetChatLogHistory(f.svc, histSvc(h))
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "its history", nil)
	if r := st.Exchanges[0].Reads[0]; r.Outcome != "completed" {
		t.Fatalf("%+v", r)
	}
	got := f.lineagesOf(st.ConversationID)
	if len(got) != 1 || got[0] != h.Lineage() {
		t.Fatalf("recorded: %+v", got)
	}
	lt.move(got[0], "the cluster's log history source changed since its logs were read")
	if st = f.svc.Ask("x", st.ConversationID, "again", nil); !strings.Contains(st.Ended, "since its logs were read; start a new conversation") {
		t.Fatalf("%q", st.Ended)
	}
}

// A plan's affected work names what its conversations hold.
func TestHeldNounsSayWhatAConversationRead(t *testing.T) {
	for want, ls := range map[string][]alertmanager.Lineage{
		"alerts":                   {canaryLineage},
		"metrics":                  {metricsCanary},
		"alerts and logs":          {canaryLineage, {Kind: "loki", Mode: "service", Identity: "l"}},
		"alerts, logs and metrics": {metricsCanary, {Kind: "loki", Mode: "service", Identity: "l"}, canaryLineage},
	} {
		if got := heldNouns(ls); got != want {
			t.Errorf("%v: %q", ls, got)
		}
	}
}

// A route's per-rule read records each Gateway's read lineage; one read
// under terms that already moved is left out, said, never recorded.
func TestARouteReadRecordsEachGatewaysLineage(t *testing.T) {
	call := ai.ToolCall{ID: "r", Name: toolMetrics, Args: json.RawMessage(`{"template":"route_5xx_ratio","namespace":"shop","name":"web"}`)}
	setup := func() *chatFixture {
		f := newToolsFixture(t, calls(call), answer("fine"))
		conn := routeChat("u-r")
		gr := conn.rules["infra/gw"]
		gr.Status.Provenance = metricsCanary
		conn.rules["infra/gw"] = gr
		f.useConn(conn)
		return f
	}
	t.Run("recorded", func(t *testing.T) {
		withLineageCheck(t)
		f := setup()
		st := f.svc.Start("x")
		st = f.svc.Ask("x", st.ConversationID, "web's errors?", nil)
		if got := f.lineagesOf(st.ConversationID); len(got) != 1 || got[0] != metricsCanary {
			t.Fatalf("recorded: %+v", got)
		}
	})
	t.Run("already moved", func(t *testing.T) {
		lt := withLineageCheck(t)
		lt.move(metricsCanary, metricsMovedWhy)
		f := setup()
		st := f.svc.Start("x")
		st = f.svc.Ask("x", st.ConversationID, "web's errors?", nil)
		if got := f.lineagesOf(st.ConversationID); len(got) != 0 {
			t.Fatalf("a moved read was recorded: %+v", got)
		}
		if res := f.prov.reqs[1].Messages[2].Results[0].Content; !strings.Contains(res, "Gateway infra/gw: "+metricsMovedWhy) {
			t.Fatalf("the model is told: %s", res)
		}
	})
}
