package appbridge

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A1 §1.1 and §8: a snapshot investigation's first turn runs Klyx's
// fixed plan from the anchor and the links its own results establish,
// within 96 KiB and the read budget; the model's findings block is labelled
// like report_findings.

var ksAnchor = ChatSeedDTO{Kind: "object", Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Plural: "kustomizations", Namespace: "flux-system", Name: "app", UID: "uid-ks"}

// snapshotInvestigation is a no-tools provider whose answer is reply.
func snapshotInvestigation(t *testing.T, reply string) (*chatFixture, *fakeConverser) {
	t.Helper()
	p := &fakeConverser{complete: func(ai.Request) (ai.Result, error) { return ai.Result{Text: reply, ModelUsed: "m"}, nil }}
	f := newChatFixture(t, snapshotOnly{p})
	f.prov = p
	return f, p
}

// planCluster is a Kustomization whose health check names Deployment
// shop/web (joined) and Widget shop/w (not in the inventory), with one
// failing pod of the Deployment's own.
func planCluster(f *chatFixture) {
	msg := "health check failed after 5m0s: timeout waiting for: [Deployment/shop/web status: 'InProgress', Widget/shop/w status: 'InProgress']"
	ks := unitObj(msg, "shop_web_apps_Deployment")
	src := alertsChat(observe.KindCoverage{})
	src.flux = map[string]*unstructured.Unstructured{"Kustomization|flux-system|app": ks}
	src.kinds["kustomization"] = []fleet.ChatKind{{GVR: ksGVR, Kind: "Kustomization", Namespaced: true}}
	src.objects[key(ksGVR, "flux-system", "app")] = ks
	src.objects[key(deployGVR, "shop", "web")].SetCreationTimestamp(ks.GetCreationTimestamp())
	src.objects[key(podsGVR, "shop", "web-1-a")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-1-a", "namespace": "shop", "uid": "uid-p1"}})
	src.rows = map[string]observe.Rows{healthsrc.TaskPods + "/pods": podVerdictRows("uid-p1", 1, observe.Live)}
	src.summary = f.conn.summary
	f.conn, f.cur = src, src
}

func TestSnapshotInvestigationRunsKlyxsPlanFromItsOwnLinks(t *testing.T) {
	reply := "The web pods crash.\n\n```findings\n" + `{"findings":[{"type":"cause","statement":"the pod crash loops","subject":{"kind":"Pod","namespace":"shop","name":"web-1-a"},"cites":["r3"]}],"missing":["the registry"]}` + "\n```\n"
	f, p := snapshotInvestigation(t, reply)
	planCluster(f)
	inv := f.svc.StartInvestigation("x", ksAnchor)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	ex := st.Exchanges[0]
	var steps []string
	for _, r := range ex.Reads {
		steps = append(steps, r.ID+":"+r.Tool)
	}
	// AI A1.1 §7: the joined workload's rollout history after git_changes
	want := "r1:get_resource,r2:gitops_unit,r3:get_resource,r4:get_pod_logs,r5:get_events,r6:get_events,r7:git_changes,r8:rollout_history,r9:get_alerts,r10:get_dependencies"
	if strings.Join(steps, ",") != want {
		t.Fatalf("the plan, in order, following only the joined entry and its own failing pod:\n%s\n%s", strings.Join(steps, ","), want)
	}
	if ex.Reads[2].Target != "Pod shop/web-1-a" || !strings.Contains(ex.Reads[3].Target, "shop/web-1-a") {
		t.Fatalf("%+v %+v", ex.Reads[2], ex.Reads[3])
	}
	req := p.creqs[0]
	if !strings.Contains(req.System, "Klyx read a fixed set of evidence") || !strings.HasPrefix(req.User, "<cluster-data>\n{") || !strings.Contains(req.User, `"readId":"r1"`) {
		t.Fatalf("%.300s", req.User)
	}
	fd := ex.Findings
	if ex.Answer != "The web pods crash." || fd == nil || fd.Scope != invSnapshotNote || len(fd.Findings) != 1 || fd.Findings[0].Label != labelSuspected {
		t.Fatalf("the block is taken out of the answer and labelled: %q %+v", ex.Answer, fd)
	}
	var missing []string
	for _, m := range fd.Missing {
		missing = append(missing, m.From+":"+m.Read)
	}
	// the anchor's two reads' gap (the fixture serves no verdicts), the
	// unjoined Widget entry, the alerts no snapshot answered, and the
	// model's own note
	if strings.Join(missing, ",") != "klyx:r1,klyx:r2,klyx:r2.c3,klyx:r9,model:" {
		t.Fatalf("%v %+v", missing, fd.Missing)
	}
}

// AI A1 implementation review, finding 9: a provider that rejects tools on
// the first call gets the investigation's own snapshot - its plan, its
// system text and its findings block - not the chat's.
func TestARejectionOfToolsRunsTheInvestigationsPlan(t *testing.T) {
	reply := "The web pods crash.\n\n```findings\n" + `{"findings":[{"type":"cause","statement":"the pod crash loops","subject":{"kind":"Pod","namespace":"shop","name":"web-1-a"},"cites":["r4"]}]}` + "\n```\n"
	rejected := func(ai.ConvRequest) (ai.Turn, error) { return ai.Turn{}, ai.ErrToolsUnsupported }
	f := newToolsFixture(t, rejected)
	f.prov.complete = func(ai.Request) (ai.Result, error) { return ai.Result{Text: reply, ModelUsed: "m"}, nil }
	planCluster(f)
	inv := f.svc.StartInvestigation("x", ksAnchor)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	ex := st.Exchanges[0]
	var steps []string
	for _, r := range ex.Reads {
		steps = append(steps, r.Tool)
	}
	p := f.prov
	if st.Mode != "snapshot" || !contains(steps, "mode") || !contains(steps, toolUnit) || !contains(steps, toolChanges) || contains(steps, "snapshot_index") ||
		len(p.creqs) != 1 || !strings.Contains(p.creqs[0].System, invSnapshotAddendum) {
		t.Fatalf("the investigation's plan after the rejection: %v %+v", steps, st)
	}
	if ex.Findings == nil || ex.Findings.Scope != invSnapshotNote || len(ex.Findings.Findings) != 1 {
		t.Fatalf("its findings block is read: %+v", ex.Findings)
	}

	// the control: a chat that is refused tools gets the chat's snapshot
	c := newToolsFixture(t, rejected)
	cs := c.svc.Start("x")
	st = c.svc.Ask("x", cs.ConversationID, "how is the cluster?", nil)
	steps = nil
	for _, r := range st.Exchanges[0].Reads {
		steps = append(steps, r.Tool)
	}
	if cp := c.prov; !contains(steps, "snapshot_index") || len(cp.creqs) != 1 || strings.Contains(cp.creqs[0].System, invSnapshotAddendum) {
		t.Fatalf("%v", steps)
	}
}

func TestSnapshotInvestigationFindingsBlock(t *testing.T) {
	for _, tc := range []struct{ reply, note string }{
		{"no block here", "the answer carried no findings block"},
		{"x\n```findings\n{\"findings\":[{\"type\":\"proof\"}]}\n```", "the findings could not be read"},
	} {
		f, _ := snapshotInvestigation(t, tc.reply)
		planCluster(f)
		inv := f.svc.StartInvestigation("x", ksAnchor)
		st := f.svc.Ask("x", inv.ConversationID, "", nil)
		if fd := st.Exchanges[0].Findings; fd == nil || !strings.HasPrefix(fd.Note, tc.note) || len(fd.Findings) != 0 {
			t.Fatalf("%q: %+v", tc.reply, fd)
		}
	}
	prose, block, ok := takeFindings("a\n```findings\n{\"a\":1}\n```\nb\n```findings\n{\"b\":2}\n```")
	if !ok || block != `{"b":2}` || !strings.Contains(prose, `{"a":1}`) {
		t.Fatalf("the last block is the findings: %q %q", prose, block)
	}
}

func TestSnapshotInvestigationDocAndBudget(t *testing.T) {
	big := `{"readId":"r9","data":"` + strings.Repeat("x", invSnapshotMax) + `"}`
	doc, dropped := invSnapshotDoc([]string{`{"readId":"r1"}`, big, `{"readId":"r10"}`}, "q")
	if len(doc) > invSnapshotMax || len(dropped) != 1 || dropped[0] != "r9" || !strings.Contains(doc, `"readId":"r10"`) || !strings.Contains(doc, "left out to fit the snapshot: r9") {
		t.Fatalf("filled in order, what does not fit named: %d %v", len(doc), dropped)
	}
	f := anchoredFixture(t)
	r := &turnRunner{s: f.svc, c: &conversation{}, ex: &exchange{}, turn: &activeTurn{}}
	p := &invPlan{r: r, ctx: context.Background(), exec: newChatTools(f.conn, fixedNow), reads: invMaxToolCalls}
	if _, ok, err := p.step(toolOverview, `{}`, "one more"); ok || err != nil || len(p.skipped) != 1 {
		t.Fatalf("a step past the read budget is skipped and said: %v %v %v", ok, err, p.skipped)
	}
}

func TestSnapshotInvestigationOfAWorkloadFollowsItsFluxOwnerByLabels(t *testing.T) {
	f, _ := snapshotInvestigation(t, "ok")
	planCluster(f)
	dep := f.conn.objects[key(deployGVR, "shop", "web")]
	dep.SetLabels(map[string]string{"kustomize.toolkit.fluxcd.io/name": "app", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"})
	anchor := ChatSeedDTO{Kind: "object", Group: "apps", Version: "v1", Plural: "deployments", Namespace: "shop", Name: "web", UID: "uid-d"}
	inv := f.svc.StartInvestigation("x", anchor)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	var steps []string
	for _, r := range st.Exchanges[0].Reads {
		steps = append(steps, r.Tool+" "+r.Target)
	}
	got := strings.Join(steps, "; ")
	if !strings.Contains(got, "git_changes Kustomization flux-system/app") || !strings.Contains(got, "get_pod_logs shop/web-1-a") ||
		!strings.Contains(got, "rollout_history Deployment shop/web") {
		t.Fatalf("%s", got)
	}
	var notes []string
	for _, m := range st.Exchanges[0].Findings.Missing {
		notes = append(notes, m.Text)
	}
	// AI A1.1 §7: the plan reads the rollout history now
	if strings.Contains(strings.Join(notes, ";"), "rollout history") {
		t.Fatalf("%v", notes)
	}
}

func TestSnapshotInvestigationFollowUpRevisesFindings(t *testing.T) {
	n := 0
	p := &fakeConverser{complete: func(ai.Request) (ai.Result, error) {
		n++
		if n == 1 {
			return ai.Result{Text: "first"}, nil
		}
		return ai.Result{Text: "revised\n```findings\n" + `{"findings":[{"type":"observation","statement":"seen","subject":{"kind":"Pod","namespace":"shop","name":"web"},"cites":["r1"]}]}` + "\n```"}, nil
	}}
	f := newChatFixture(t, snapshotOnly{p})
	f.conn.objects[key(podsGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-p"}})
	inv := f.svc.StartInvestigation("x", podAnchor)
	f.svc.Ask("x", inv.ConversationID, "", nil)
	st := f.svc.Ask("x", inv.ConversationID, "and the pod?", nil)
	ex := st.Exchanges[1]
	if ex.Answer != "revised" || ex.Findings == nil || len(ex.Findings.Findings) != 1 || ex.Findings.Findings[0].Label != labelReading ||
		!strings.Contains(p.creqs[1].System, "fixed set of evidence") {
		t.Fatalf("%+v", ex)
	}
	// a follow-up reads the chat's snapshot; the plan ran once, on the
	// first turn
	var steps []string
	for _, r := range ex.Reads {
		steps = append(steps, r.Tool)
	}
	if !contains(steps, "snapshot_index") {
		t.Fatalf("a follow-up does not run the plan again: %v", steps)
	}
}

func TestSnapshotInvestigationFollowsOnlyFailingPodsAndHistoryForAGoneOne(t *testing.T) {
	f, _ := snapshotInvestigation(t, "ok")
	planCluster(f)
	// a healthy pod of the same workload is not followed
	pods := f.conn.lists[podsGVR.String()+"|shop"]
	healthy := owned("v1", "Pod", "shop", "web-1-b", "uid-p2", "uid-rs1")
	pods.Items = append(pods.Items, healthy)
	rows := podVerdictRows("uid-p1", 1, observe.Live)
	set := rows.Rows.(healthsrc.RowSet[workloads.PodSummary])
	ok := set.Rows[0]
	ok.Ref.UID, ok.Ref.Name, ok.Data.Health = "uid-p2", "web-1-b", health.Verdict{Kind: health.OK}
	set.Rows = append(set.Rows, ok)
	rows.Rows = set
	f.conn.rows[healthsrc.TaskPods+"/pods"] = rows
	// and the failing one is gone by the time it is read
	delete(f.conn.objects, key(podsGVR, "shop", "web-1-a"))
	inv := f.svc.StartInvestigation("x", ksAnchor)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	var steps []string
	for _, r := range st.Exchanges[0].Reads {
		steps = append(steps, r.Tool+" "+r.Target)
	}
	got := strings.Join(steps, "; ")
	if strings.Contains(got, "web-1-b") || !strings.Contains(got, "get_resource pods shop/web-1-a") || !strings.Contains(got, "get_log_history shop/web-1-a") ||
		strings.Contains(got, "get_pod_logs") {
		t.Fatalf("%s", got)
	}
}
