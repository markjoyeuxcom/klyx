package appbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/policy"
)

// Settings S2 §4 and §6: fleet plans, by class.

const fleetDoc = `# the fleet
clusters:
  - name: prd
    context: prd-ctx
    group: platform
    tags:
      region: westeurope
  - name: dev
    context: dev-ctx
`

type fleetRig struct {
	t    *testing.T
	ctx  context.Context
	path string
	reg  *fleet.Registry
	pol  *policy.Registry
	life *Lifecycle
	cs   *ConfigService

	mu        sync.Mutex
	records   map[string][]PlanRecordDTO
	deleted   []string
	ended     []string
	changed   int
	releases  int
	onPrepare func(config.ClusterConfig)
}

// newFleetRig runs a fleet over fleetDoc; run applies usability as it
// comes - without it, a held cluster stays held.
func newFleetRig(t *testing.T, run bool) *fleetRig { t.Helper(); return newFleetRigWith(t, run, "") }

// newFleetRigWith is newFleetRig whose factory fails failFirst's first
// build.
func newFleetRigWith(t *testing.T, run bool, failFirst string) *fleetRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(fleetDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r := &fleetRig{t: t, ctx: ctx, path: path, pol: policy.New(), records: map[string][]PlanRecordDTO{}}
	SetProtectGuard(NewProtectGuard(r.pol, time.Now))
	t.Cleanup(func() { SetProtectGuard(defaultTestGuard) })
	failed := false
	r.reg = fleet.NewRegistry(cfg, func(cc config.ClusterConfig) (fleet.Conn, error) {
		r.mu.Lock()
		fail := cc.Name == failFirst && !failed
		failed = failed || fail
		r.mu.Unlock()
		if fail {
			return nil, errors.New("exec plugin not found")
		}
		return lifeFactory(cc)
	})
	ix := fleetindex.New(fleetindex.Settings{})
	t.Cleanup(ix.Stop)
	r.reg.SetIndex(ix)
	r.life = NewLifecycle(r.reg, r.pol, NewHealthSink(&fakeEmitter{}), ix)
	if run {
		go r.life.Run(ctx)
	}
	r.reg.Start(ctx)
	r.cs = WithPolicy(NewConfigService(path, cfg, nil), r.pol)
	r.cs.kubeconfigPath = func() string { return "/nonexistent/kubeconfig" } // never the owner's
	r.cs.kubeContexts = func(string) ([]string, error) { return nil, nil }
	HoldBuildFailures(r.reg, r.pol)
	if run { // every initial connection published before the test begins
		for _, cc := range cfg.Clusters {
			if c, ok := r.reg.Conn(cc.Name); ok && !waitUntil(t, 5*time.Second, func() bool { return c.Snapshot().State.Usable() }) {
				t.Fatalf("%s never connected", cc.Name)
			}
		}
	}
	r.life.OnForget(func(name string) { ForgetRunningCluster(r.cs, name) })
	r.life.OnReleased(func(string, fleet.ConnID) { r.mu.Lock(); r.releases++; r.mu.Unlock() })
	WireFleetPlans(r.cs, FleetPlanDeps{
		Ctx: ctx, Life: r.life, Install: r.reg.Install,
		Prepare: func(cc config.ClusterConfig) (*fleet.Prepared, error) {
			if r.onPrepare != nil {
				r.onPrepare(cc)
			}
			return r.reg.Prepare(cc)
		},
		Resolve: func(cc config.ClusterConfig) error {
			if cc.Context == "missing" {
				return errors.New(`context "missing" does not exist`)
			}
			return nil
		},
		Records: func(c string) []PlanRecordDTO {
			r.mu.Lock()
			defer r.mu.Unlock()
			return append([]PlanRecordDTO(nil), r.records[c]...)
		},
		Delete: func(c, kind string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.deleted = append(r.deleted, c+"/"+kind)
			if kind == config.EndpointLoki {
				return errors.New("the keychain refused")
			}
			return nil
		},
		EndEpochWork: func(c, why string) { r.mu.Lock(); r.ended = append(r.ended, c); r.mu.Unlock() },
		Changed:      func() { r.mu.Lock(); r.changed++; r.mu.Unlock() },
	})
	return r
}

func (r *fleetRig) released() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.releases
}

func (r *fleetRig) file() string {
	r.t.Helper()
	b, err := os.ReadFile(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *fleetRig) handEdit(old, new string) {
	r.t.Helper()
	doc := r.file()
	if !strings.Contains(doc, old) {
		r.t.Fatalf("no %q in the file", old)
	}
	if err := os.WriteFile(r.path, []byte(strings.Replace(doc, old, new, 1)), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fleetRig) snap(name string) policy.Snapshot {
	r.t.Helper()
	s, _ := r.pol.Snapshot(name)
	return s
}

func (r *fleetRig) running(name string) config.ClusterConfig {
	r.t.Helper()
	c, ok := r.cs.runningEntry(name)
	if !ok {
		r.t.Fatalf("%s is not running", name)
	}
	return c
}

func draftOf(c config.ClusterConfig) ClusterDraftDTO {
	return ClusterDraftDTO{Context: c.Context, Kubeconfig: c.Kubeconfig, Group: c.Group, Environment: c.Environment, Tags: copyTags(c.Tags), Protected: c.Protected}
}

// the dev entry as written: its context, nothing else
func devDraft() ClusterDraftDTO { return ClusterDraftDTO{Context: "dev-ctx"} }

func (r *fleetRig) apply(p PlanDTO, typed string) ApplyResultDTO {
	r.t.Helper()
	if p.Refused != "" {
		r.t.Fatalf("refused: %s", p.Refused)
	}
	return r.cs.ApplyClusterPlan(p.Digest, typed)
}

func TestAPlacementEditRelabelsAndKeepsTheEpoch(t *testing.T) {
	r := newFleetRig(t, true)
	before := r.snap("dev")
	d := devDraft()
	d.Group = "web"
	p := r.cs.PlanClusterEdit("dev", d)
	if p.Class != classPlacement || !p.Writes || p.Interlock != nil || len(p.Ends) != 0 || len(p.Changes) != 1 || p.Changes[0].To != "web" {
		t.Fatalf("plan: %+v", p)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	if !strings.Contains(r.file(), "  - name: dev\n    context: dev-ctx\n    group: web\n") || r.running("dev").Group != "web" {
		t.Fatalf("file:\n%s", r.file())
	}
	if s := r.snap("dev"); s.Epoch != before.Epoch || s.Reserved {
		t.Fatalf("a placement moved the epoch or kept its reservation: %+v", s)
	}
	if r.changed != 1 {
		t.Fatal("the line was not told")
	}
	if res := r.cs.ApplyClusterPlan(p.Digest, ""); res.OK {
		t.Fatal("a plan applied twice")
	}
	if p := r.cs.PlanClusterRetry("dev"); p.Refused == "" {
		t.Fatal("Retry offered on a connected cluster")
	}
}

func TestTighteningTakesNoInterlockAndMovesTheEpochOnce(t *testing.T) {
	r := newFleetRig(t, true)
	tk, err := guardFor("dev") // admitted before the change
	if err != nil {
		t.Fatal(err)
	}
	before := r.snap("dev")
	d := devDraft()
	d.Protected = true
	p := r.cs.PlanClusterEdit("dev", d)
	if p.Class != classProtection || p.Interlock != nil {
		t.Fatalf("plan: %+v", p)
	}
	if !hasChange(p, "fleet.protection", "off", "on, set in fleet.yaml") {
		t.Fatalf("protection's effect line: %+v", p.Changes)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	s := r.snap("dev")
	if !s.Protected || s.Epoch.String() == before.Epoch.String() || s.Reserved {
		t.Fatalf("after: %+v", s)
	}
	if moved, _ := s.Epoch.Moved(before.Epoch); !moved {
		t.Fatal("the policy epoch did not move")
	}
	if tk.Valid() == nil {
		t.Fatal("a ticket from before the change is still valid") // R1: tightened after a guard
	}
	if len(r.ended) != 1 || r.ended[0] != "dev" {
		t.Fatalf("conversations not ended: %v", r.ended)
	}
}

func hasChange(p PlanDTO, id, from, to string) bool {
	for _, c := range p.Changes {
		if c.ID == id && c.From == from && c.To == to {
			return true
		}
	}
	return false
}

func TestLooseningTakesTheInterlock(t *testing.T) {
	r := newFleetRig(t, true)
	d := devDraft()
	d.Environment = "production"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	if !r.snap("dev").Protected {
		t.Fatal("rig: protected by its environment")
	}
	p := r.cs.PlanClusterEdit("dev", devDraft())
	if p.Interlock == nil || p.Interlock.Phrase != "dev" || !hasChange(p, "fleet.protection", "on, inferred from the environment production", "off") {
		t.Fatalf("plan: %+v %+v", p.Interlock, p.Changes)
	}
	before, file := r.snap("dev"), r.file()
	if res := r.cs.ApplyClusterPlan(p.Digest, "prd"); res.OK || !strings.Contains(res.Error, `Type "dev"`) {
		t.Fatalf("applied without the interlock: %+v", res)
	}
	if r.snap("dev") != before || r.file() != file {
		t.Fatal("a refused interlock changed something")
	}
	if res := r.cs.ApplyClusterPlan(p.Digest, "dev"); !res.OK {
		t.Fatal(res.Error)
	}
	if r.snap("dev").Protected {
		t.Fatal("not loosened")
	}
	// protection inferred from the name stays, whatever else changes
	pd := draftOf(config.ClusterConfig{Context: "prd-ctx", Group: "platform", Tags: map[string]string{"region": "westeurope"}})
	pd.Group = "ops"
	p = r.cs.PlanClusterEdit("prd", pd)
	if p.Interlock != nil || p.Class != classPlacement {
		t.Fatalf("the name's protection: %+v", p)
	}
}

func TestAContextThatCannotResolveIsRefusedAtPlanTime(t *testing.T) {
	r := newFleetRig(t, true)
	file := r.file()
	d := devDraft()
	d.Context = "missing"
	p := r.cs.PlanClusterEdit("dev", d)
	if !strings.Contains(p.Refused, "cannot connect") || r.file() != file {
		t.Fatalf("plan: %+v", p)
	}
	if res := r.cs.ApplyClusterPlan(p.Digest, ""); res.OK {
		t.Fatal("a refused plan applied")
	}
}

func TestAReconnectIsHeldUntilItsOwnConnectionIsUsable(t *testing.T) {
	r := newFleetRig(t, false)
	le, err := r.life.Acquire("dev", Work{Kind: "logs", What: "logs of shop/web"})
	if err != nil {
		t.Fatal(err)
	}
	d := devDraft()
	d.Context = "dev-2"
	p := r.cs.PlanClusterEdit("dev", d)
	if p.Class != classReconnect || len(p.Ends) != 1 || p.Ends[0].Kind != "logs" {
		t.Fatalf("plan: %+v", p)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	s := r.snap("dev")
	if s.State != policy.Held || s.Attempt == 0 || !strings.Contains(s.Reason, "connecting with context dev-2") || s.Reserved {
		t.Fatalf("held: %+v", s)
	}
	if le.Ctx.Err() == nil || !strings.Contains(le.EndedWhy(), "dev's connection settings changed") {
		t.Fatal("the listed work did not end")
	}
	if _, err := guardFor("dev"); err == nil {
		t.Fatal("a held cluster admitted a guard")
	}
	if !strings.Contains(r.file(), "context: dev-2") || r.running("dev").Context != "dev-2" {
		t.Fatalf("file:\n%s", r.file())
	}
	go r.life.Run(r.ctx)
	if !waitUntil(t, 5*time.Second, func() bool { return r.snap("dev").State == policy.Active }) {
		t.Fatal("never released")
	}
	if n := r.released(); n != 1 {
		t.Fatalf("released %d times", n)
	}
	if t2 := r.cs.PlanClusterRevert("dev"); t2.Refused == "" {
		t.Fatal("a connected cluster offered Revert")
	}
}

// "saved, not connected yet", with Revert (the redesign's R1): back to
// the last terms that connected, held for its own attempt.
func TestRevertGoesBackToTheLastTermsThatConnected(t *testing.T) {
	r := newFleetRig(t, false)
	connectNow(t, r, "dev") // the startup terms really connected
	d := devDraft()
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	held := r.snap("dev")
	p := r.cs.PlanClusterRevert("dev")
	if p.Class != classReconnect || !p.Writes || !hasChange(p, "fleet.context", "dev-2", "dev-ctx") {
		t.Fatalf("revert: %+v", p)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	s := r.snap("dev")
	if s.State != policy.Held || s.Attempt == held.Attempt || !strings.Contains(s.Reason, "dev-ctx") {
		t.Fatalf("reverted: %+v", s)
	}
	if r.file() != fleetDoc {
		t.Fatalf("not written back:\n%s", r.file())
	}
}

// Held, then a Retry or Revert refused before its write: still Held, with
// its reason, epoch and attempt.
func TestARefusedRetryLeavesTheClusterHeld(t *testing.T) {
	r := newFleetRig(t, false)
	d := devDraft()
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	held := r.snap("dev")
	p := r.cs.PlanClusterRetry("dev")
	if p.Class != classReconnect || p.Writes {
		t.Fatalf("retry: %+v", p)
	}
	r.handEdit("group: platform", "group: platform2") // a hand edit before Apply
	res := r.cs.ApplyClusterPlan(p.Digest, "")
	if res.OK || res.Plan == nil {
		t.Fatalf("applied over a hand edit: %+v", res)
	}
	if r.snap("dev") != held {
		t.Fatalf("a refused retry changed the hold: %+v, was %+v", r.snap("dev"), held)
	}
}

// Codex's design probe: a Retry whose held attempt becomes usable during
// its preparation is refused with a fresh plan; the file is unchanged, the
// recovered attempt stays current, and its release is recorded once.
func TestAReleaseDuringARetrysPreparationRefusesIt(t *testing.T) {
	r := newFleetRig(t, false)
	d := devDraft()
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	a := fleet.ConnID(r.snap("dev").Attempt)
	var evA fleet.UsabilityEvent
	if !waitUntil(t, 5*time.Second, func() bool {
		r.life.qmu.Lock()
		defer r.life.qmu.Unlock()
		for i, e := range r.life.queue {
			if e.ID == a {
				evA = e
				r.life.queue = append(r.life.queue[:i], r.life.queue[i+1:]...)
				return true
			}
		}
		return false
	}) {
		t.Fatal("no usability for the attempt")
	}
	p := r.cs.PlanClusterRetry("dev")
	file := r.file()
	r.onPrepare = func(config.ClusterConfig) { r.life.applyUsable(evA) } // the attempt connects meanwhile
	res := r.cs.ApplyClusterPlan(p.Digest, "")
	if res.OK || res.Error != ErrMoved.Error() || res.Plan == nil {
		t.Fatalf("got %+v", res)
	}
	if r.file() != file || r.snap("dev").State != policy.Active || !r.reg.IsCurrent("dev", a) || r.released() != 1 {
		t.Fatalf("after: %+v, current %v, releases %d", r.snap("dev"), r.reg.IsCurrent("dev", a), r.released())
	}
}

func TestARemovalTakesItsInterlockAndDeletesOnlyChosenRecords(t *testing.T) {
	r := newFleetRig(t, true)
	r.records["prd"] = []PlanRecordDTO{{Kind: config.EndpointMetrics, Revision: "1"}, {Kind: config.EndpointLoki, Revision: "4"}}
	p := r.cs.PlanClusterRemove("prd", []string{config.EndpointMetrics})
	chosen := map[string]bool{}
	for _, rec := range p.Records {
		chosen[rec.Kind] = rec.Chosen
	}
	if p.Class != classRemoval || p.Interlock == nil || p.Interlock.Phrase != "prd" || !p.Writes || len(p.Records) != 2 || !chosen["metrics"] || chosen["loki"] {
		t.Fatalf("plan: %+v", p)
	}
	if res := r.cs.ApplyClusterPlan(p.Digest, "dev"); res.OK {
		t.Fatal("removed without the interlock")
	}
	if res := r.cs.ApplyClusterPlan(p.Digest, "prd"); !res.OK || res.Note != "" {
		t.Fatalf("got %+v", res)
	}
	if strings.Contains(r.file(), "name: prd") || !strings.Contains(r.file(), "# the fleet\nclusters:\n  - name: dev") {
		t.Fatalf("file:\n%s", r.file())
	}
	if _, ok := r.pol.Snapshot("prd"); ok {
		t.Fatal("not forgotten")
	}
	if _, ok := r.cs.runningEntry("prd"); ok {
		t.Fatal("still running")
	}
	if len(r.deleted) != 1 || r.deleted[0] != "prd/metrics" {
		t.Fatalf("deleted %v", r.deleted)
	}
	if p := r.cs.PlanClusterRemove("dev", nil); !strings.Contains(p.Refused, "at least one cluster") {
		t.Fatalf("the last cluster: %+v", p)
	}
}

func TestAFailedRecordDeletionIsSaidAndTheClusterStaysRemoved(t *testing.T) {
	r := newFleetRig(t, true)
	r.records["dev"] = []PlanRecordDTO{{Kind: config.EndpointLoki, Revision: "2"}}
	res := r.apply(r.cs.PlanClusterRemove("dev", []string{config.EndpointLoki}), "")
	if !res.OK || !strings.Contains(res.Note, "still holds the loki credential") {
		t.Fatalf("got %+v", res)
	}
	if _, ok := r.cs.runningEntry("dev"); ok {
		t.Fatal("not removed")
	}
}

func TestAKeychainChoiceIsBoundToItsRevision(t *testing.T) {
	r := newFleetRig(t, true)
	r.records["dev"] = []PlanRecordDTO{{Kind: config.EndpointMetrics, Revision: "1"}}
	p := r.cs.PlanClusterRemove("dev", []string{config.EndpointMetrics})
	r.mu.Lock()
	r.records["dev"][0].Revision = "2" // the credential changed meanwhile
	r.mu.Unlock()
	if res := r.cs.ApplyClusterPlan(p.Digest, ""); res.OK || res.Plan == nil {
		t.Fatalf("applied over a changed credential: %+v", res)
	}
	if len(r.deleted) != 0 {
		t.Fatal("deleted")
	}
	if p := r.cs.PlanClusterRemove("dev", []string{config.EndpointLoki}); p.Refused == "" {
		t.Fatal("a record not present was offered")
	}
}

func TestASaveIsRefusedWhileTheFileDiffers(t *testing.T) {
	r := newFleetRig(t, true)
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    group: by-hand\n")
	d := devDraft()
	d.Environment = "dev"
	if p := r.cs.PlanClusterEdit("dev", d); !strings.Contains(p.Refused, "use the file's values first") {
		t.Fatalf("plan: %+v", p)
	}
}

// "Use the file's values" (S2 §4.6), each kind of drift, adopting S2's
// fields only and writing nothing.
func TestUseTheFilesValues(t *testing.T) {
	r := newFleetRig(t, true)
	SetRunningEndpoint(r.cs, "dev", config.EndpointMetrics, &config.MetricsConfig{Endpoint: "https://m.example"}, nil)
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    group: by-hand\n    metrics:\n      endpoint: https://other.example\n")
	file := r.file()
	p := r.cs.PlanClusterFromFile("dev")
	if p.Kind != planFile || p.Class != classPlacement || p.Writes {
		t.Fatalf("plan: %+v", p)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	run := r.running("dev")
	if run.Group != "by-hand" || run.Metrics == nil || run.Metrics.Endpoint != "https://m.example" || r.file() != file {
		t.Fatalf("adopted %+v (metrics %+v)", run, run.Metrics)
	}
	// a cluster in the file and not running: Connect
	r.handEdit("clusters:\n", "clusters:\n  - name: lab\n    context: lab-ctx\n")
	p = r.cs.PlanClusterFromFile("lab")
	if p.Kind != planConnect || p.Class != classAddition || p.Writes {
		t.Fatalf("connect: %+v", p)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	if s, ok := r.pol.Snapshot("lab"); !ok || s.State != policy.Active || s.Reserved {
		t.Fatalf("lab: %+v %v", s, ok)
	}
	if _, ok := r.reg.Conn("lab"); !ok {
		t.Fatal("lab is not connected")
	}
	// a running cluster gone from the file: removed, writing nothing
	r.handEdit("  - name: lab\n    context: lab-ctx\n", "")
	file = r.file()
	p = r.cs.PlanClusterFromFile("lab")
	if p.Class != classRemoval || p.Writes {
		t.Fatalf("removal: %+v", p)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	if _, ok := r.cs.runningEntry("lab"); ok || r.file() != file {
		t.Fatal("not removed, or written")
	}
}

// One fleet Apply at a time: two clusters' plans, neither overwriting the
// other in the running list; the second, planned from the file before the
// first wrote it, is refused with its new plan.
func TestTwoClustersAppliesKeepEachOther(t *testing.T) {
	r := newFleetRig(t, true)
	d := devDraft()
	d.Group = "web"
	pd := r.cs.PlanClusterEdit("dev", d)
	pp := r.cs.PlanClusterEdit("prd", ClusterDraftDTO{Context: "prd-ctx", Group: "ops", Tags: map[string]string{"region": "westeurope"}})
	if res := r.apply(pd, ""); !res.OK {
		t.Fatal(res.Error)
	}
	res := r.cs.ApplyClusterPlan(pp.Digest, "")
	if res.OK || res.Plan == nil {
		t.Fatalf("the second applied over a moved file: %+v", res)
	}
	if res := r.apply(*res.Plan, ""); !res.OK {
		t.Fatal(res.Error)
	}
	if r.running("dev").Group != "web" || r.running("prd").Group != "ops" {
		t.Fatal("one apply overwrote the other")
	}
}

// The digest moves when work starts: a plan whose Ends no longer list what
// its Apply would end is refused with its new plan.
func TestTheDigestMovesWhenWorkStarts(t *testing.T) {
	r := newFleetRig(t, true)
	d := devDraft()
	d.Context = "dev-2"
	p := r.cs.PlanClusterEdit("dev", d)
	le, err := r.life.Acquire("dev", Work{Kind: "forward", What: "a port-forward"})
	if err != nil {
		t.Fatal(err)
	}
	defer le.Release()
	res := r.cs.ApplyClusterPlan(p.Digest, "")
	if res.OK || res.Plan == nil || len(res.Plan.Ends) != 1 {
		t.Fatalf("got %+v", res)
	}
}

// A stale arm after a reconnect (the redesign's R1): a grant armed under
// the old epoch admits nothing once the cluster reconnected.
func TestAnArmDoesNotSurviveAReconnect(t *testing.T) {
	r := newFleetRig(t, true)
	g := activeProtectGuard.Load()
	if err := g.Arm("prd", "prd"); err != nil {
		t.Fatal(err)
	}
	p := r.cs.PlanClusterEdit("prd", ClusterDraftDTO{Context: "prd-2", Group: "platform", Tags: map[string]string{"region": "westeurope"}})
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	if !waitUntil(t, 5*time.Second, func() bool { return r.snap("prd").State == policy.Active }) {
		t.Fatal("never released")
	}
	if _, err := guardFor("prd"); err == nil {
		t.Fatal("an arm from before the reconnect admitted a verb")
	}
}

// A cluster whose connection failed to build starts held, and Retry
// connects it from its saved terms, writing nothing.
func TestRetryConnectsAClusterWhoseBuildFailed(t *testing.T) {
	r := newFleetRigWith(t, true, "dev")
	if s := r.snap("dev"); s.State != policy.Held || !strings.Contains(s.Reason, "exec plugin not found") {
		t.Fatalf("at start: %+v", s)
	}
	if p := r.cs.PlanClusterRevert("dev"); p.Refused == "" {
		t.Fatal("Revert offered for terms that never connected")
	}
	file := r.file()
	p := r.cs.PlanClusterRetry("dev")
	if p.Class != classReconnect || p.Writes {
		t.Fatalf("retry: %+v", p)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	if !waitUntil(t, 5*time.Second, func() bool { return r.snap("dev").State == policy.Active }) {
		t.Fatalf("never connected: %+v", r.snap("dev"))
	}
	if _, ok := r.reg.Conn("dev"); !ok || r.file() != file {
		t.Fatal("not connected, or written")
	}
}

// The last terms that connected follow each release: a second reconnect's
// Revert goes back to the first reconnect's terms, not the startup ones.
func TestRevertFollowsTheLastRelease(t *testing.T) {
	r := newFleetRig(t, false)
	reconnect := func(context string) fleet.ConnID {
		d := devDraft()
		d.Context = context
		if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
			t.Fatal(res.Error)
		}
		return fleet.ConnID(r.snap("dev").Attempt)
	}
	a := reconnect("dev-2")
	var evA fleet.UsabilityEvent
	if !waitUntil(t, 5*time.Second, func() bool {
		r.life.qmu.Lock()
		defer r.life.qmu.Unlock()
		for _, e := range r.life.queue {
			if e.ID == a {
				evA = e
				return true
			}
		}
		return false
	}) {
		t.Fatal("no usability for the attempt")
	}
	r.life.applyUsable(evA) // dev-2 connects: its terms are the last connected
	if r.snap("dev").State != policy.Active {
		t.Fatal("not released")
	}
	reconnect("dev-3")
	p := r.cs.PlanClusterRevert("dev")
	if !hasChange(p, "fleet.context", "dev-3", "dev-2") {
		t.Fatalf("revert: %+v", p.Changes)
	}
}

// A removal refused at its decision - its activation moved after the
// re-plan - deletes no record and removes nothing.
func TestARemovalRefusedAtItsDecisionDeletesNothing(t *testing.T) {
	r := newFleetRig(t, true)
	r.records["dev"] = []PlanRecordDTO{{Kind: config.EndpointMetrics, Revision: "1"}}
	p := r.cs.PlanClusterRemove("dev", []string{config.EndpointMetrics})
	file := r.file()
	r.cs.fp.beforeDecide = func() { r.pol.ConnUp("dev") } // a recovery, just before the gate
	res := r.cs.ApplyClusterPlan(p.Digest, "")
	if res.OK || res.Error != ErrMoved.Error() || res.Plan == nil {
		t.Fatalf("got %+v", res)
	}
	if len(r.deleted) != 0 || r.file() != file {
		t.Fatalf("deleted %v, or wrote", r.deleted)
	}
	if _, ok := r.cs.runningEntry("dev"); !ok {
		t.Fatal("removed")
	}
}

// Settings S2 §5: the page's rows say a held cluster's state and the
// plans it offers, and the editor opens on the raw entry.
func TestTheFleetRowsSayAHeldClustersPlans(t *testing.T) {
	r := newFleetRig(t, false)
	connectNow(t, r, "dev") // its startup terms connected: Revert has them
	row := func(name string) FleetClusterDTO {
		for _, c := range r.cs.GetFleetConfig().Clusters {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("no row for %s", name)
		return FleetClusterDTO{}
	}
	if d := row("dev"); d.Held != "" || d.CanRetry || d.CanRevert {
		t.Fatalf("an active cluster offers plans: %+v", d)
	}
	if d := row("prd"); d.ProtectedBy != "the name prd" {
		t.Fatalf("prd's protection: %q", d.ProtectedBy)
	}
	d := devDraft()
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	if got := row("dev"); !strings.Contains(got.Held, "dev-2") || !got.CanRetry || !got.CanRevert {
		t.Fatalf("held: %+v", got)
	}
}

func TestTheEditorOpensOnTheRawEntry(t *testing.T) {
	r := newFleetRig(t, true)
	r.cs.kubeContexts = func(string) ([]string, error) { return []string{"dev-ctx", "dev-2"}, nil }
	e := r.cs.GetClusterEntry("prd")
	if !e.InFile || e.Draft.Context != "prd-ctx" || e.Draft.Group != "platform" || e.Draft.Tags["region"] != "westeurope" || len(e.Contexts) != 2 {
		t.Fatalf("entry: %+v", e)
	}
	if e.ProtectedBy != "the name prd" || !strings.Contains(e.Locked, "the name prd") {
		t.Fatalf("protection: %q, locked %q", e.ProtectedBy, e.Locked)
	}
	if e := r.cs.GetClusterEntry("dev"); e.Locked != "" || e.Draft.Tags == nil {
		t.Fatalf("dev: %+v", e)
	}
	if e := r.cs.GetClusterEntry("nowhere"); e.Error == "" {
		t.Fatal("a cluster not running opened")
	}
	// protection set in the file is the file's to loosen: never locked
	d := devDraft()
	d.Protected = true
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	if e := r.cs.GetClusterEntry("dev"); e.ProtectedBy == "" || e.Locked != "" || !e.Draft.Protected {
		t.Fatalf("dev, protected in the file: %+v", e)
	}
	// the draft is the file's raw entry: a context left out stays out,
	// never the running or the defaulted one
	r.handEdit("    context: dev-ctx\n", "")
	if e := r.cs.GetClusterEntry("dev"); e.Draft.Context != "" || !e.InFile {
		t.Fatalf("dev without a context in the file: %+v", e)
	}
}

// A held reconnect offers Revert only when other terms connected before.
func TestAHeldReconnectWithNoEarlierConnectionOffersNoRevert(t *testing.T) {
	r := newFleetRig(t, false)
	d := devDraft()
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	for _, c := range r.cs.GetFleetConfig().Clusters {
		if c.Name == "dev" && (c.Held == "" || !c.CanRetry || c.CanRevert) {
			t.Fatalf("dev: %+v", c)
		}
	}
}
