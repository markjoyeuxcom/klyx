package appbridge

// Codex's review of S2b (2026-10-26): its probes, R1-R8, as regression
// tests. connectNow applies a connection's own queued usability: the
// connection really connected.

import (
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/policy"
)

func connectNow(t *testing.T, r *fleetRig, name string) {
	t.Helper()
	c, ok := r.reg.Conn(name)
	if !ok {
		t.Fatal("no connection")
	}
	id, _ := connID(c)
	var ev fleet.UsabilityEvent
	if !waitUntil(t, 5*time.Second, func() bool {
		r.life.qmu.Lock()
		defer r.life.qmu.Unlock()
		for i, e := range r.life.queue {
			if e.ID == id {
				ev = e
				r.life.queue = append(r.life.queue[:i], r.life.queue[i+1:]...)
				return true
			}
		}
		return false
	}) {
		t.Fatal("no usability event")
	}
	r.life.applyUsable(ev)
}

func TestAFileAdoptionRechecksTheFinalFile(t *testing.T) {
	r := newFleetRig(t, true)
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    group: reviewed\n")
	p := r.cs.PlanClusterFromFile("dev")
	if p.Refused != "" || p.Writes {
		t.Fatalf("fixture plan: %+v", p)
	}
	before := r.running("dev")
	r.cs.fp.beforeDecide = func() { r.handEdit("group: reviewed", "group: later-edit") }
	res := r.cs.ApplyClusterPlan(p.Digest, "")
	if res.OK || r.running("dev").Group != before.Group {
		t.Fatalf("stale file adoption: result=%+v, running=%q, file has later-edit=%v", res, r.running("dev").Group, strings.Contains(r.file(), "later-edit"))
	}
}

func TestATighteningWhileHeldDoesNotWriteThenRefuse(t *testing.T) {
	r := newFleetRig(t, false)
	d := devDraft()
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	d.Protected = true
	p := r.cs.PlanClusterEdit("dev", d)
	if p.Refused != "" {
		return // a pre-write refusal is safe too
	}
	before := r.file()
	res := r.cs.ApplyClusterPlan(p.Digest, "")
	if !res.OK && r.file() != before {
		t.Fatalf("write then refusal: result=%+v, file changed=%v, run protected=%v, published=%+v", res, r.file() != before, r.running("dev").IsProtected(), r.snap("dev"))
	}
	if res.OK {
		if r.snap("dev").State == policy.Held {
			connectNow(t, r, "dev")
		}
		if !r.snap("dev").Protected {
			t.Fatal("tightening succeeded but its activation did not publish protection")
		}
	}
}

func TestARemovalUsesThePublishedProtection(t *testing.T) {
	r := newFleetRig(t, false)
	d := devDraft()
	d.Environment = "production"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	d.Environment, d.Context = "", "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), "dev"); !res.OK {
		t.Fatal(res.Error)
	}
	if snap := r.snap("dev"); !snap.Protected || snap.State != policy.Held {
		t.Fatalf("fixture not published-protected and held: %+v", snap)
	}
	p := r.cs.PlanClusterRemove("dev", nil)
	if p.Refused == "" && p.Interlock == nil {
		res := r.cs.ApplyClusterPlan(p.Digest, "")
		t.Fatalf("published-protected removal had no interlock: class=%s, result without phrase=%+v", p.Class, res)
	}
}

func TestAFileReconnectRevertsToTheRunningTerms(t *testing.T) {
	r := newFleetRig(t, false)
	connectNow(t, r, "dev") // startup terms really connected
	r.handEdit("context: dev-ctx", "context: new-context")
	if res := r.apply(r.cs.PlanClusterFromFile("dev"), ""); !res.OK {
		t.Fatal(res.Error)
	}
	p := r.cs.PlanClusterRevert("dev")
	if p.Refused != "" || !hasChange(p, "fleet.context", "new-context", "dev-ctx") {
		last, _ := r.cs.fp.lastConnected("dev")
		t.Fatalf("Revert lost the actual connected terms: last=%+v, plan=%+v", last, p)
	}
}

func TestRetryDoesNotRecordAnUnbuiltFileContext(t *testing.T) {
	r := newFleetRig(t, false)
	connectNow(t, r, "dev")
	d := devDraft()
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	r.handEdit("context: dev-2", "context: never-built")
	p := r.cs.PlanClusterRetry("dev")
	if p.Refused != "" {
		return // refusing drift before Retry is safe
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	connectNow(t, r, "dev")
	last, _ := r.cs.fp.lastConnected("dev")
	if last.Context != r.running("dev").Context {
		t.Fatalf("recorded unbuilt file terms as connected: running=%q, last=%q", r.running("dev").Context, last.Context)
	}
}

func TestAWriterDiagnosticWithholdsTheAnchor(t *testing.T) {
	r := newFleetRig(t, true)
	needle := "Bearer_super_secret_do_not_display_1234567890"
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    group: &"+needle+" platform\n")
	if res := r.apply(r.cs.PlanClusterFromFile("dev"), ""); !res.OK {
		t.Fatal(res.Error)
	}
	d := devDraft()
	d.Group = "new-group"
	p := r.cs.PlanClusterEdit("dev", d)
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	res := r.cs.ApplyClusterPlan(p.Digest, "")
	if strings.Contains(res.Error, needle) {
		t.Fatalf("writer error exposed file content: %s", res.Error)
	}
}

func TestAddPreparesBeforeWriting(t *testing.T) {
	r := newFleetRigWith(t, false, "missing")
	r.cs.connect = func(cc config.ClusterConfig) error { return r.reg.Add(r.ctx, cc) }
	before := r.file()
	res := r.cs.AddClusters([]string{"missing"})
	if !res.OK && r.file() != before {
		t.Fatalf("known build failure changed the fleet file: result=%+v", res)
	}
}

func TestAConnectNamesTheConfiguredEndpointKinds(t *testing.T) {
	r := newFleetRig(t, false)
	r.handEdit("clusters:\n", "clusters:\n  - name: lab\n    context: lab-ctx\n    metrics:\n      endpoint: https://metrics.example\n    loki:\n      endpoint: https://loki.example\n")
	p := r.cs.PlanClusterFromFile("lab")
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	var text string
	for _, change := range p.Changes {
		text += change.Label + " " + change.From + " " + change.To + "\n"
	}
	if !strings.Contains(strings.ToLower(text), "metrics") || !strings.Contains(strings.ToLower(text), "loki") {
		t.Fatalf("configured endpoint kinds absent from connect consequences: %+v", p.Changes)
	}
}

func TestACommittedRemovalFencesTheOldTicketBeforeCallbacks(t *testing.T) {
	r := newFleetRig(t, true)
	ticket, err := guardFor("dev")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.reg.Conn("dev")
	id, _ := connID(c)
	retired, resume := make(chan struct{}), make(chan struct{})
	r.life.OnEnd(func(name string, _ fleet.ConnID, _ string) {
		if name == "dev" {
			close(retired)
			<-resume
		}
	})
	p := r.cs.PlanClusterRemove("dev", nil)
	done := make(chan ApplyResultDTO, 1)
	go func() { done <- r.cs.ApplyClusterPlan(p.Digest, "") }()
	<-retired // file written and old identity retired; outside-gate cleanup
	current := r.reg.IsCurrent("dev", id)
	valid := ticket.Valid()
	close(resume)
	res := <-done
	if current || valid == nil {
		t.Fatalf("ticket survived committed retirement: current=%v, ticket error=%v, final result=%+v", current, valid, res)
	}
}

// R1, the addition: a file-only Connect whose entry changed after its
// re-plan is refused at its decision; nothing of it runs.
func TestAnAdditionRechecksTheFileAtItsDecision(t *testing.T) {
	r := newFleetRig(t, true)
	r.handEdit("clusters:\n", "clusters:\n  - name: lab\n    context: lab-ctx\n")
	p := r.cs.PlanClusterFromFile("lab")
	r.cs.fp.beforeDecide = func() { r.handEdit("context: lab-ctx", "context: lab-other") }
	if res := r.cs.ApplyClusterPlan(p.Digest, ""); res.OK || res.Plan == nil {
		t.Fatalf("connected over a changed entry: %+v", res)
	}
	if _, ok := r.pol.Snapshot("lab"); ok {
		t.Fatal("the refused addition left its name")
	}
	if _, ok := r.cs.runningEntry("lab"); ok {
		t.Fatal("the refused addition runs")
	}
}

// R4: while a reconnect to unprotected terms is held, the published
// protection decides - a placement edit is a protection change refused
// while held, and a further reconnect still takes the interlock.
func TestWhileHeldThePublishedProtectionDecides(t *testing.T) {
	r := newFleetRig(t, false)
	d := devDraft()
	d.Environment = "production"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	d.Environment, d.Context = "", "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), "dev"); !res.OK {
		t.Fatal(res.Error)
	}
	if s := r.snap("dev"); !s.Protected || s.State != policy.Held {
		t.Fatalf("rig: %+v", s)
	}
	g := d
	g.Group = "web"
	if p := r.cs.PlanClusterEdit("dev", g); !strings.Contains(p.Refused, "saved, not connected yet") {
		t.Fatalf("a placement while held under published protection: %+v", p)
	}
	n := d
	n.Context = "dev-3"
	p := r.cs.PlanClusterEdit("dev", n)
	if p.Class != classReconnect || p.Interlock == nil || p.Interlock.Phrase != "dev" {
		t.Fatalf("a further reconnect to unprotected terms: %+v", p)
	}
	if !hasChange(p, "fleet.protection", "on, until the pending connection is usable", "off") {
		t.Fatalf("protection's effect from the published value: %+v", p.Changes)
	}
}

// Codex's re-review of S2b (2026-10-26): its probes, RR1-RR4.

func TestAStartupConnectionPublishedBeforeWiringIsRemembered(t *testing.T) {
	r := newFleetRig(t, true)
	last, ok := r.cs.fp.lastConnected("dev")
	if !ok || last.Context != "dev-ctx" {
		t.Fatalf("startup usability was published before WireFleetPlans and is forgotten: known=%v last=%+v", ok, last)
	}
}

func TestRetryRecordsTheTermsItActuallyBuilds(t *testing.T) {
	r := newFleetRig(t, false)
	connectNow(t, r, "dev")
	d := devDraft()
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	d.Group = "new-platform"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	if res := r.apply(r.cs.PlanClusterRetry("dev"), ""); !res.OK {
		t.Fatal(res.Error)
	}
	connectNow(t, r, "dev")
	last, ok := r.cs.fp.lastConnected("dev")
	if !ok || last.Group != r.running("dev").Group {
		t.Fatalf("Retry's recorded terms differ from its actual terms: last=%+v running=%+v", last, r.running("dev"))
	}
}

func TestCancellingAPendingLooseningKeepsProtection(t *testing.T) {
	r := newFleetRig(t, false)
	d := devDraft()
	d.Protected = true
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	d.Context = "dev-2"
	d.Protected = false
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), "dev"); !res.OK {
		t.Fatal(res.Error)
	}
	d.Protected = true
	p := r.cs.PlanClusterEdit("dev", d)
	if p.Refused != "" {
		return
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	connectNow(t, r, "dev")
	if !r.snap("dev").Protected {
		t.Fatalf("restored protection was applied to file and running entry, yet usability loosened it: plan=%+v running=%+v published=%+v", p, r.running("dev"), r.snap("dev"))
	}
}

func TestAddRefusesARunningNameBeforeWriting(t *testing.T) {
	r := newFleetRig(t, false)
	r.handEdit("  - name: dev\n    context: dev-ctx\n", "")
	before := r.file()
	res := r.cs.AddClusters([]string{"dev"})
	if !res.OK && r.file() != before {
		t.Fatalf("a name still running was appended before the addition was refused: %+v", res)
	}
}

// RR3: an edit committed on the connection that connected keeps its last
// connected terms coherent - a placement and a protection change alike.
func TestAnEditOnTheConnectedIdentityKeepsItsTerms(t *testing.T) {
	r := newFleetRig(t, false)
	connectNow(t, r, "dev")
	d := devDraft()
	d.Group = "web"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	if last, _ := r.cs.fp.lastConnected("dev"); last.Group != "web" {
		t.Fatalf("after a placement: %+v", last)
	}
	d.Protected = true
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	if last, _ := r.cs.fp.lastConnected("dev"); !last.Protected || last.Group != "web" {
		t.Fatalf("after a protection change: %+v", last)
	}
}
