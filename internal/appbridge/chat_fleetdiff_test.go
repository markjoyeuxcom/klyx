package appbridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/observe"
)

// AI A1 §3.7, §8 and A-C2: a comparison set fixed before the first question,
// each member fenced before its read and, once read, before every send.

// fleetFixture is a chat service over several clusters, each with its own
// connection; "prd" is protected.
type fleetFixture struct {
	svc    *ChatService
	prov   *fakeConverser
	mu     sync.Mutex
	conns  map[string]*fakeChatConn
	usable map[string]bool
}

func newFleetFixture(t *testing.T, turns ...func(ai.ConvRequest) (ai.Turn, error)) *fleetFixture {
	t.Helper()
	f := &fleetFixture{prov: &fakeConverser{turns: turns}, conns: map[string]*fakeChatConn{}, usable: map[string]bool{}}
	for _, c := range []string{"x", "stg", "prd", "dev"} {
		conn := newFakeChat()
		conn.summary = &observe.Summary{EvaluatedAt: fixedNow(), Connection: "connected"}
		f.conns[c], f.usable[c] = conn, true
	}
	f.conns["x"].objects[key(podsGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-p"}})
	aiSvc := NewAIService(func(string) (AIConn, bool) { return nil, false },
		&AIState{Provider: f.prov, ProviderID: ai.ProviderAnthropic, Model: "m", Origin: "https://api.example"},
		func(c string) bool { return c == "prd" }, func(string) capability.Set { return capability.Set{} })
	f.svc = NewChatService(func(c string) (ChatConn, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		conn, ok := f.conns[c]
		if !ok {
			return nil, false
		}
		return conn, f.usable[c]
	}, aiSvc, nil)
	f.svc.now = fixedNow
	return f
}

func (f *fleetFixture) setUsable(c string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usable[c] = ok
}

func compareInfo(image, hash, rev string) fleet.WorkloadCompareInfo {
	return fleet.WorkloadCompareInfo{Found: true, Kind: "Deployment", Namespace: "shop", Name: "web", DesiredReplicas: 2, ReadyReplicas: 2,
		Containers: []fleet.CompareContainer{{Name: "web", Image: image}}, SpecHash: hash, OwnerKind: "Kustomization", OwnerNamespace: "flux-system", OwnerName: "app", OwnerRevision: rev}
}

const diffArgs = `{"kind":"Deployment","namespace":"shop","name":"web"}`

func TestComparisonSetIsFixedBeforeTheFirstQuestion(t *testing.T) {
	f := newFleetFixture(t, answer("done"))
	inv := f.svc.StartInvestigation("x", podAnchor)
	for _, tc := range []struct {
		members []string
		want    string
	}{
		{[]string{"x"}, "the investigated cluster is not a comparison member"},
		{[]string{"nowhere"}, "nowhere is not connected"},
		{[]string{"prd"}, "prd: "},
		{[]string{"a", "b", "c", "d", "e", "f"}, "at most 5"},
	} {
		if st := f.svc.SetComparison("x", inv.ConversationID, tc.members); !strings.Contains(st.Notice, tc.want) || len(st.Comparison) != 0 {
			t.Fatalf("%v: %+v", tc.members, st)
		}
	}
	st := f.svc.SetComparison("x", inv.ConversationID, []string{"stg", "dev", "stg"})
	if st.Notice != "" || strings.Join(st.Comparison, ",") != "stg,dev" {
		t.Fatalf("%+v", st)
	}
	f.svc.Ask("x", inv.ConversationID, "", nil)
	if st := f.svc.SetComparison("x", inv.ConversationID, []string{"dev"}); !strings.Contains(st.Notice, "fixed once the investigation has begun") ||
		strings.Join(st.Comparison, ",") != "stg,dev" {
		t.Fatalf("%+v", st)
	}
	chat := f.svc.Start("x")
	if st := f.svc.SetComparison("x", chat.ConversationID, []string{"stg"}); !strings.Contains(st.Notice, "only an investigation") {
		t.Fatalf("%+v", st)
	}
}

func TestFleetDifferenceComparesTheMembersWithHome(t *testing.T) {
	f := newFleetFixture(t, calls(ai.ToolCall{ID: "d", Name: toolFleetDiff, Args: json.RawMessage(diffArgs)}), answer("compared"))
	f.conns["x"].compare = compareInfo("web:1.1", "h2", "main@sha1:bbb")
	f.conns["stg"].compare = compareInfo("web:1.0", "h1", "main@sha1:aaa")
	inv := f.svc.StartInvestigation("x", podAnchor)
	f.svc.SetComparison("x", inv.ConversationID, []string{"stg"})
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	if !hasTool(f.prov.reqs[0].Tools, toolFleetDiff) || st.Exchanges[0].Outcome != "answered" {
		t.Fatalf("%+v", st.Exchanges[0])
	}
	res := f.prov.reqs[1].Messages[len(f.prov.reqs[1].Messages)-1].Results[0].Content
	var env chatEnvelope
	_ = json.Unmarshal([]byte(res), &env)
	b, _ := json.Marshal(env.Data)
	var d chatFleetDiffData
	_ = json.Unmarshal(b, &d)
	if len(d.Clusters) != 2 || d.Clusters[0].Role != "home" || d.Clusters[1].Cluster != "stg" ||
		strings.Join(d.Clusters[1].Differences, "; ") != "container web image: home web:1.1, here web:1.0; owner's applied revision: home main@sha1:bbb, here main@sha1:aaa; the pod specs differ (spec hash)" {
		t.Fatalf("%s", b)
	}
	if !f.svc.convs[invKey("x")].membersRead {
		t.Fatal("a member's data was read")
	}
}

func TestAMemberLostAfterItsReadEndsTheInvestigation(t *testing.T) {
	f := newFleetFixture(t, calls(ai.ToolCall{ID: "d", Name: toolFleetDiff, Args: json.RawMessage(diffArgs)}), answer("never sent"))
	f.conns["stg"].compare = compareInfo("web:1.0", "h1", "")
	inv := f.svc.StartInvestigation("x", podAnchor)
	f.svc.SetComparison("x", inv.ConversationID, []string{"stg"})
	// stg disconnects right after its read: the next send carries its data
	f.conns["stg"].compareHook = func() { f.setUsable("stg", false) }
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	if len(f.prov.reqs) != 1 || !strings.Contains(st.Ended, "the comparison cluster stg disconnected") || st.Exchanges[0].Outcome != "failed" {
		t.Fatalf("the next send is refused and the investigation ends: %d sends, %+v", len(f.prov.reqs), st)
	}
}

func TestFleetDifferenceRefusesAClusterOutsideTheSet(t *testing.T) {
	home := newFakeChat()
	reads := 0
	tools := newChatTools(home, fixedNow)
	tools.members = []chatMember{{cluster: "stg", compare: func(context.Context, string, string, string) (fleet.WorkloadCompareInfo, error) {
		reads++
		return fleet.WorkloadCompareInfo{}, nil
	}}}
	res := tools.Run(context.Background(), toolFleetDiff, json.RawMessage(`{"kind":"Deployment","namespace":"shop","name":"web","clusters":["prd"]}`))
	if res.Read.Outcome != "failed" || !strings.Contains(res.Read.Detail, "prd is not in this investigation's comparison set") || reads != 0 || home.compareCalls != 0 {
		t.Fatalf("refused before any read: %+v %d %d", res.Read, reads, home.compareCalls)
	}
	tools.members = nil
	if res := tools.Run(context.Background(), toolFleetDiff, json.RawMessage(diffArgs)); !strings.Contains(res.Read.Detail, "no comparison set") || home.compareCalls != 0 {
		t.Fatalf("%+v", res.Read)
	}
}

func TestSnapshotInvestigationComparesTheAnchorsWorkload(t *testing.T) {
	f := newFleetFixture(t)
	p := &fakeConverser{}
	SetAIState(f.svc.ai, &AIState{Provider: snapshotOnly{p}, ProviderID: ai.ProviderClaudeCLI, Model: "m"})
	home := f.conns["x"]
	src := alertsChat(observe.KindCoverage{})
	src.summary, src.compare = home.summary, compareInfo("web:1.1", "h", "")
	f.conns["x"] = src
	f.conns["stg"].compare = compareInfo("web:1.0", "h", "")
	anchor := ChatSeedDTO{Kind: "object", Group: "apps", Version: "v1", Plural: "deployments", Namespace: "shop", Name: "web", UID: "uid-d"}
	inv := f.svc.StartInvestigation("x", anchor)
	f.svc.SetComparison("x", inv.ConversationID, []string{"stg"})
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	last := st.Exchanges[0].Reads[len(st.Exchanges[0].Reads)-1]
	if last.Tool != toolFleetDiff || last.Outcome != "completed" || f.conns["stg"].compareCalls != 1 {
		t.Fatalf("%+v", st.Exchanges[0].Reads)
	}
}

func TestAMemberLostBeforeItsReadIsNeverRead(t *testing.T) {
	f := newFleetFixture(t, calls(ai.ToolCall{ID: "d", Name: toolFleetDiff, Args: json.RawMessage(diffArgs)}), answer("never"))
	inv := f.svc.StartInvestigation("x", podAnchor)
	f.svc.SetComparison("x", inv.ConversationID, []string{"stg"})
	f.setUsable("stg", false)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	if f.conns["stg"].compareCalls != 0 || !strings.Contains(st.Ended, "the comparison cluster stg disconnected") {
		t.Fatalf("%d reads, %+v", f.conns["stg"].compareCalls, st)
	}
}
