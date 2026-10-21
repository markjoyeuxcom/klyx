package appbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/ai"
)

// AI A1 §1: an investigation is a conversation of its own kind beside a
// cluster's chat, pinned to one object by UID, with larger budgets; its
// first turn reads the anchor and is the investigation.

var podAnchor = ChatSeedDTO{Kind: "object", Version: "v1", Plural: "pods", Namespace: "shop", Name: "web", UID: "uid-p"}

func anchoredFixture(t *testing.T, turns ...func(ai.ConvRequest) (ai.Turn, error)) *chatFixture {
	t.Helper()
	f := newToolsFixture(t, turns...)
	f.conn.objects[key(podsGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-p"}})
	return f
}

func TestInvestigationSitsBesideChatAndReadsItsAnchorFirst(t *testing.T) {
	f := anchoredFixture(t, answer("chat answer"), answer("investigated"))
	chat := f.svc.Start("x")
	inv := f.svc.StartInvestigation("x", podAnchor)
	if inv.Error != "" || inv.Kind != kindInvestigation || inv.Anchor == nil || inv.Anchor.UID != "uid-p" || inv.ConversationID == chat.ConversationID || chat.Kind != kindChat {
		t.Fatalf("an investigation is its own conversation beside the chat: %+v %+v", inv, chat)
	}
	if again := f.svc.StartInvestigation("x", podAnchor); again.ConversationID != inv.ConversationID {
		t.Fatal("the same anchor returns the open investigation")
	}
	other := podAnchor
	other.Name, other.UID = "api", "uid-api"
	if r := f.svc.StartInvestigation("x", other); !strings.Contains(r.Error, "an investigation of web is open") {
		t.Fatalf("one investigation per cluster: %+v", r)
	}
	f.svc.Ask("x", chat.ConversationID, "hello", nil)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	ex := st.Exchanges[0]
	if st.Kind != kindInvestigation || ex.Outcome != "answered" || !strings.HasPrefix(ex.Question, "Investigate why this object is failing") ||
		ex.Reads[0].Tool != toolGet || ex.Reads[0].Refs[0].UID != "uid-p" {
		t.Fatalf("the first turn reads the anchor by UID: %+v", ex)
	}
	req := f.prov.reqs[1]
	if !strings.Contains(req.System, "This conversation is an investigation") || !strings.Contains(req.Messages[0].Text, "<seeded-object>") {
		t.Fatalf("the investigation's framing and its anchor travel: %s", req.System)
	}
	if !strings.Contains(f.prov.reqs[0].System, "only to answer") || strings.Contains(f.prov.reqs[0].System, "investigation") {
		t.Fatal("the chat keeps its own framing")
	}
	if cs := f.svc.State("x"); len(cs.Exchanges) != 1 || cs.Exchanges[0].Answer != "chat answer" {
		t.Fatalf("the chat is untouched: %+v", cs)
	}
	if is := f.svc.InvestigationState("x"); is.ConversationID != inv.ConversationID || len(is.Exchanges) != 1 {
		t.Fatalf("%+v", is)
	}
	f.svc.Clear("x", inv.ConversationID)
	if is := f.svc.InvestigationState("x"); is.ConversationID != "" || f.svc.State("x").ConversationID != chat.ConversationID {
		t.Fatal("clearing the investigation leaves the chat")
	}
}

func TestInvestigationRefusals(t *testing.T) {
	f := anchoredFixture(t)
	for _, a := range []ChatSeedDTO{
		{Kind: "helm", Namespace: "shop", Name: "web"},
		{Kind: "object", Version: "v1", Plural: "pods", Namespace: "shop", Name: "web"},
		{Kind: "object", Version: "v1", Plural: "pods", Namespace: "shop", Name: "bad name", UID: "u"},
	} {
		if r := f.svc.StartInvestigation("x", a); r.Error == "" || r.ConversationID != "" {
			t.Fatalf("%+v: %+v", a, r)
		}
	}
	// an investigation counts in the conversation limit
	for i := 0; i < chatMaxConvs; i++ {
		f.svc.convs[fmt.Sprintf("c%d", i)] = &conversation{id: fmt.Sprintf("id%d", i), key: fmt.Sprintf("c%d", i)}
	}
	if r := f.svc.StartInvestigation("x", podAnchor); !strings.Contains(r.Error, "at most") {
		t.Fatalf("%+v", r)
	}
}

func TestInvestigationHasTheLargerBudgets(t *testing.T) {
	var batch []ai.ToolCall
	for i := 0; i < 20; i++ {
		batch = append(batch, ai.ToolCall{ID: fmt.Sprintf("c%d", i), Name: toolOverview, Args: json.RawMessage(`{}`)})
	}
	f := anchoredFixture(t, calls(batch...), answer("done"))
	inv := f.svc.StartInvestigation("x", podAnchor)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	refused := 0
	for _, r := range st.Exchanges[0].Reads {
		if r.Detail == "tool budget used up" {
			refused++
		}
	}
	if refused != 0 || len(st.Exchanges[0].Reads) != 21 {
		t.Fatalf("20 calls fit an investigation's 24: %d refused of %d", refused, len(st.Exchanges[0].Reads))
	}
	c := f.svc.convs[invKey("x")]
	if lim := c.limits(); lim.models != 12 || lim.tools != 24 || lim.workers != 6 || lim.deadline.Seconds() != 300 {
		t.Fatalf("%+v", lim)
	}
	if st := f.svc.Ask("x", inv.ConversationID, "and?", &podAnchor); st.Notice != "" {
		// a later turn may name a follow-up target like chat
		t.Fatalf("%+v", st.Notice)
	}
}
