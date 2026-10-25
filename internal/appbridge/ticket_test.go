package appbridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/policy"
	"github.com/moomora/klyx/internal/readfence"
)

// Settings slice F §1-§2: the guard reads one published snapshot, every
// ticket captures its epoch, and every write context checks it.

func installRegistry(t *testing.T, protected bool, clusters ...string) *policy.Registry {
	t.Helper()
	reg := policy.New()
	for _, c := range clusters {
		reg.Seed(c, protected)
	}
	SetProtectGuard(NewProtectGuard(reg, time.Now))
	t.Cleanup(func() { SetProtectGuard(defaultTestGuard) })
	return reg
}

func tighten(t *testing.T, reg *policy.Registry, cluster string) {
	t.Helper()
	if _, err := reg.Begin(cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Publish(cluster, true); err != nil {
		t.Fatal(err)
	}
}

func TestTheGuardBindsTheEpoch(t *testing.T) {
	reg := installRegistry(t, true, "prd")
	g := activeProtectGuard.Load()
	// an arm under one epoch is refused under another
	if err := g.Arm("prd", "prd"); err != nil {
		t.Fatal(err)
	}
	reg.ConnUp("prd") // a Failed recovery
	if _, err := g.Guard("prd"); err == nil || !strings.Contains(err.Error(), "connection restarted") {
		t.Fatalf("a stale arm after a reconnect: %v", err)
	}
	// a fresh arm passes, and its ticket fails once the policy moves
	if err := g.Arm("prd", "prd"); err != nil {
		t.Fatal(err)
	}
	tk, err := g.Guard("prd")
	if err != nil || tk.Valid() != nil {
		t.Fatalf("%v %v", err, tk.Valid())
	}
	tighten(t, reg, "prd")
	if err := tk.Valid(); err == nil || !strings.Contains(err.Error(), "policy changed") {
		t.Fatalf("a moved ticket: %v", err)
	}
	// a mismatched arm clears the grant
	_ = g.Arm("prd", "prd")
	if err := g.Arm("prd", "wrong"); err == nil {
		t.Fatal("a mismatch")
	}
	if _, err := g.Guard("prd"); err == nil {
		t.Fatal("a mismatched arm left the grant")
	}
}

// R1's probe: while a change is applied nothing is admitted, so no guard
// can pair the old protection with the new epoch; an unprotected pass is
// refused once protection tightens; Release revives nothing.
func TestAChangeInProgressAdmitsNothing(t *testing.T) {
	reg := installRegistry(t, false, "a")
	before, err := guardFor("a") // an unprotected pass
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Begin("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := guardFor("a"); err == nil || !strings.Contains(err.Error(), "being applied") {
		t.Fatalf("a guard during Applying: %v", err)
	}
	if before.Valid() == nil {
		t.Fatal("a ticket from before the change survived Begin")
	}
	if _, err := reg.Publish("a", true); err != nil {
		t.Fatal(err)
	}
	if before.Valid() == nil {
		t.Fatal("an unprotected pass survived the tightening")
	}
	if _, err := guardFor("a"); err == nil || !strings.Contains(err.Error(), "not armed") {
		t.Fatalf("now protected, an unarmed guard: %v", err)
	}
	_ = activeProtectGuard.Load().Arm("a", "a")
	held, _ := guardFor("a")
	if _, err := reg.Hold("a", "the reconnect failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := guardFor("a"); err == nil || !strings.Contains(err.Error(), "saved but not active") {
		t.Fatalf("a held cluster: %v", err)
	}
	if _, err := reg.Release("a", true); err != nil {
		t.Fatal(err)
	}
	if held.Valid() == nil {
		t.Fatal("Release revived a ticket")
	}
}

func TestZeroTicketsAndUnknownClustersFailClosed(t *testing.T) {
	installRegistry(t, false, "known")
	if (Ticket{}).Valid() == nil {
		t.Fatal("a zero ticket")
	}
	if _, err := guardFor("unknown"); err == nil {
		t.Fatal("an unknown cluster")
	}
	SetProtectGuard(nil)
	if _, err := guardFor("known"); err == nil {
		t.Fatal("no guard")
	}
}

// Publication never takes the guard's lock: concurrent guards, arms and
// publications under -race.
func TestPublicationAndTheGuardRace(t *testing.T) {
	reg := installRegistry(t, true, "p")
	g := activeProtectGuard.Load()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = g.Arm("p", "p"); _, _ = g.Guard("p") }()
		go func() { defer wg.Done(); reg.ConnUp("p") }()
		go func() {
			defer wg.Done()
			if _, err := reg.Begin("p"); err == nil {
				_, _ = reg.Publish("p", true)
			}
		}()
	}
	wg.Wait()
}

// --- every guarded method: its write context carries the ticket ---

type okRT struct{}

func (okRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

// wire stands in for the cluster transport: a write's hook (the move) runs
// first, then one PATCH goes through readfence.Transport with the write's
// context, so it is refused exactly as a real request would be.
type wire struct {
	move func()
	sent int
}

func (w *wire) write(ctx context.Context) error {
	if w.move != nil {
		w.move()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "http://cluster.invalid/x", nil)
	resp, err := readfence.Transport(okRT{}).RoundTrip(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	w.sent++
	return nil
}

// check is a CLI's start: the context's fence, after the move.
func (w *wire) check(ctx context.Context) error {
	if w.move != nil {
		w.move()
	}
	return readfence.Check(ctx)
}

type wlFake struct {
	WorkloadsConn
	w *wire
}

func (f wlFake) RolloutRestart(ctx context.Context, _, _, _, _ string) error { return f.w.write(ctx) }
func (f wlFake) RolloutUndo(ctx context.Context, _, _, _, _ string, _ int64, _ *fleet.RevisionExpect) error {
	return f.w.write(ctx)
}
func (f wlFake) RolloutPause(ctx context.Context, _, _, _ string, _ bool) error {
	return f.w.write(ctx)
}
func (f wlFake) ScaleWorkload(ctx context.Context, _, _, _, _ string, _ int32) error {
	return f.w.write(ctx)
}

type podFake struct {
	PodsConn
	w *wire
}

func (f podFake) DeletePod(ctx context.Context, _, _, _ string) error { return f.w.write(ctx) }
func (f podFake) LaunchDiagnosticPod(ctx context.Context, _, _ string, _ int64) fleet.AdhocResult {
	if err := f.w.write(ctx); err != nil {
		return fleet.AdhocResult{Error: err.Error()}
	}
	return fleet.AdhocResult{OK: true}
}

type nodeFake struct {
	NodeOpsConn
	w *wire
}

func (f nodeFake) SetCordon(ctx context.Context, _, _ string, _ bool) error { return f.w.write(ctx) }
func (f nodeFake) CheckNodeArmed(context.Context, string, string) error {
	if f.w.move != nil {
		f.w.move() // the move lands between the drain's preparation and its start
	}
	return nil
}
func (f nodeFake) DrainNodeCmd(string, fleet.DrainOptions) (*exec.Cmd, error) {
	return exec.Command("/bin/sh", "-c", "exit 0"), nil
}

type gitopsFake struct {
	GitOpsConn
	w *wire
}

func (f gitopsFake) Reconcile(ctx context.Context, _, _, _, _ string) error { return f.w.write(ctx) }
func (f gitopsFake) SetSuspend(ctx context.Context, _, _, _, _ string, _ bool) error {
	return f.w.write(ctx)
}
func (f gitopsFake) FluxOpAct(ctx context.Context, _, _, _, _, _ string) error { return f.w.write(ctx) }
func (f gitopsFake) ReconcileWithSourceSteps(ctx context.Context, _, _, _, _ string) ([]fleet.Step, error) {
	err := f.w.write(readfence.WithWriteTracker(ctx))
	return []fleet.Step{{Verb: "reconcile", Subject: "x", Written: err == nil, Err: err}}, err
}

type argoFake struct {
	ArgoConn
	w *wire
}

func (f argoFake) RefreshArgoApp(ctx context.Context, _, _, _ string) error { return f.w.write(ctx) }
func (f argoFake) SyncArgoApp(ctx context.Context, _, _, _ string, _ argo.SyncOptions) error {
	return f.w.write(ctx)
}

type helmFake struct {
	HelmConn
	w *wire
}

func (f helmFake) HelmRollback(ctx context.Context, _, _ string, _ int, _ helmcli.RollbackOptions) error {
	return f.w.check(ctx) // the runner's check at the child's start
}

type crdFake struct {
	CRDConn
	w *wire
}

func (f crdFake) ApplyResourceYAML(ctx context.Context, _, _, _, _, _, _, _, _, _ string, _ bool) error {
	return f.w.write(ctx)
}
func (f crdFake) CreateResourceYAML(ctx context.Context, _ string, _ bool) []fleet.AdhocResult {
	if err := f.w.write(readfence.WithWriteTracker(ctx)); err != nil {
		return []fleet.AdhocResult{{Error: err.Error()}}
	}
	return []fleet.AdhocResult{{OK: true}}
}
func (f crdFake) DeleteAdhocObject(ctx context.Context, _, _, _, _, _, _ string) error {
	return f.w.write(ctx)
}

type kedaFake struct {
	KEDAConn
	w *wire
}

func (f kedaFake) KEDASetPaused(ctx context.Context, _, _, _, _ string, _ bool) error {
	return f.w.write(ctx)
}

type execFake struct {
	ExecConn
	w *wire
}

func (f execFake) DebugCommand(string, string, string, fleet.DebugOptions) ([]string, error) {
	return []string{"kubectl", "debug"}, nil
}
func (f execFake) DebugKubectlCheck(context.Context) fleet.DebugKubectl {
	return fleet.DebugKubectl{OK: true}
}
func (f execFake) CheckPodArmed(context.Context, string, string, string) error {
	if f.w.move != nil {
		f.w.move()
	}
	return nil
}

// refused reports whether a method's reply refused the write.
func refusedReply(v any) bool {
	switch r := v.(type) {
	case ActionResultDTO:
		return !r.OK && r.Error != ""
	case AdhocResultDTO:
		return !r.OK && r.Error != ""
	case []AdhocResultDTO:
		return len(r) == 1 && !r[0].OK && r[0].Error != ""
	case OpenLogStreamResultDTO:
		return r.StreamID == "" && r.Error != ""
	}
	return false
}

func TestEveryGuardedMethodFencesItsWrite(t *testing.T) {
	const c = "c"
	cases := []struct {
		name string
		cli  bool // the check is a child's start: refusal only
		call func(w *wire) any
	}{
		{"ScaleWorkload", false, func(w *wire) any {
			return NewWorkloadsService(func(string) (WorkloadsConn, bool) { return wlFake{w: w}, true }).ScaleWorkload(c, "Deployment", "ns", "n", "u", 2)
		}},
		{"RolloutUndo", false, func(w *wire) any {
			return NewWorkloadsService(func(string) (WorkloadsConn, bool) { return wlFake{w: w}, true }).RolloutUndo(c, "Deployment", "ns", "n", "u", 1, "u", "d")
		}},
		{"RolloutPause", false, func(w *wire) any {
			return NewWorkloadsService(func(string) (WorkloadsConn, bool) { return wlFake{w: w}, true }).RolloutPause(c, "ns", "n", "u", true)
		}},
		{"RolloutRestart", false, func(w *wire) any {
			return NewWorkloadsService(func(string) (WorkloadsConn, bool) { return wlFake{w: w}, true }).RolloutRestart(c, "Deployment", "ns", "n", "u")
		}},
		{"DeletePod", false, func(w *wire) any {
			return NewPodsService(func(string) (PodsConn, bool) { return podFake{w: w}, true }).DeletePod(c, "ns", "n", "u")
		}},
		{"LaunchDiagnosticPod", false, func(w *wire) any {
			return NewPodsService(func(string) (PodsConn, bool) { return podFake{w: w}, true }).LaunchDiagnosticPod(c, "n", "busybox", 60)
		}},
		{"Cordon", false, func(w *wire) any {
			return NewNodeOpsService(func(string) (NodeOpsConn, bool) { return nodeFake{w: w}, true }, nil).Cordon(c, "node", "u", true)
		}},
		{"StartDrain", true, func(w *wire) any {
			return NewNodeOpsService(func(string) (NodeOpsConn, bool) { return nodeFake{w: w}, true }, nil).StartDrain(c, "node", "u", DrainOptionsDTO{}, "drain-1")
		}},
		{"Reconcile", false, func(w *wire) any {
			return NewGitOpsService(func(string) (GitOpsConn, bool) { return gitopsFake{w: w}, true }, time.Now).Reconcile(c, "Kustomization", "ns", "n", "u")
		}},
		{"ReconcileWithSource", false, func(w *wire) any {
			return NewGitOpsService(func(string) (GitOpsConn, bool) { return gitopsFake{w: w}, true }, time.Now).ReconcileWithSource(c, "Kustomization", "ns", "n", "u")
		}},
		{"SetSuspend", false, func(w *wire) any {
			return NewGitOpsService(func(string) (GitOpsConn, bool) { return gitopsFake{w: w}, true }, time.Now).SetSuspend(c, "Kustomization", "ns", "n", "u", true)
		}},
		{"FluxOpAct", false, func(w *wire) any {
			return NewGitOpsService(func(string) (GitOpsConn, bool) { return gitopsFake{w: w}, true }, time.Now).FluxOpAct(c, "ResourceSet", "ns", "n", "u", "reconcile")
		}},
		{"RefreshApp", false, func(w *wire) any {
			return NewArgoService(func(string) (ArgoConn, bool) { return argoFake{w: w}, true }).RefreshApp(c, "ns", "n", "u")
		}},
		{"SyncApp", false, func(w *wire) any {
			return NewArgoService(func(string) (ArgoConn, bool) { return argoFake{w: w}, true }).SyncApp(c, "ns", "n", "u", ArgoSyncOptionsDTO{})
		}},
		{"HelmRollback", true, func(w *wire) any {
			return NewHelmService(func(string) (HelmConn, bool) { return helmFake{w: w}, true }).HelmRollback(c, "ns", "r", 1, RollbackOptionsDTO{})
		}},
		{"ApplyResourceYAML", false, func(w *wire) any {
			return NewCRDService(func(string) (CRDConn, bool) { return crdFake{w: w}, true }).ApplyResourceYAML(c, "apps", "v1", "deployments", "Deployment", "Namespaced", "ns", "n", "u", "y", false)
		}},
		{"CreateResourceYAML", false, func(w *wire) any {
			return NewCRDService(func(string) (CRDConn, bool) { return crdFake{w: w}, true }).CreateResourceYAML(c, "y", false)
		}},
		{"DeleteAdhocObject", false, func(w *wire) any {
			return NewCRDService(func(string) (CRDConn, bool) { return crdFake{w: w}, true }).DeleteAdhocObject(c, "", "v1", "pods", "klyx-scratch", "n", "u")
		}},
		{"SetPaused", false, func(w *wire) any {
			return NewKEDAService(func(string) (KEDAConn, bool) { return kedaFake{w: w}, true }).SetPaused(c, "scaledobjects", "ns", "n", "u", true)
		}},
		{"OpenDebugTerminal", true, func(w *wire) any {
			return NewExecService(func(string) (ExecConn, bool) { return execFake{w: w}, true }).OpenDebugTerminal(c, "ns", "pod", "u", "app", "busybox", "general")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := installRegistry(t, false, c)
			// a tightening between the guard and the write: refused, not sent
			w := &wire{move: func() { tighten(t, reg, c) }}
			if r := tc.call(w); !refusedReply(r) || w.sent != 0 {
				t.Fatalf("after a move: %+v, sent %d", r, w.sent)
			}
			if r, ok := tc.call(&wire{}).(ActionResultDTO); ok && r.NotSent != (r.Outcome == fleet.OutcomeRefused) && r.Outcome != "" {
				t.Fatalf("an outcome without its not-sent: %+v", r)
			}
			if tc.cli {
				return // a child's start: the unmoved path would start it
			}
			// without a move the same path writes
			reg2 := installRegistry(t, false, c)
			_ = reg2
			w2 := &wire{}
			if r := tc.call(w2); refusedReply(r) || w2.sent != 1 {
				t.Fatalf("without a move: %+v, sent %d", r, w2.sent)
			}
		})
	}
}

// A matrix verb's fenced write says it was not sent; a lost answer after
// forwarding is unverified (§2, R3).
func TestAFencedWriteIsNotSentAndALostAnswerUnverified(t *testing.T) {
	reg := installRegistry(t, false, "c")
	w := &wire{move: func() { tighten(t, reg, "c") }}
	r := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return wlFake{w: w}, true }).RolloutRestart("c", "Deployment", "ns", "n", "u")
	if r.Outcome != fleet.OutcomeRefused || !r.NotSent {
		t.Fatalf("%+v", r)
	}
	// the same write's retry after its first attempt was forwarded
	reg = installRegistry(t, false, "c")
	tk, err := guardFor("c")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tk.Write(context.Background())
	if err := (&wire{}).write(ctx); err != nil {
		t.Fatal(err)
	}
	tighten(t, reg, "c")
	err = (&wire{}).write(ctx)
	if !readfence.Failed(err) || errors.Is(err, readfence.ErrNotForwarded) {
		t.Fatalf("a refused retry after forwarding must not say not sent: %v", err)
	}
	if out, notSent := fleet.WriteOutcome(err); out != fleet.OutcomeUnverified || notSent {
		t.Fatalf("%s %v", out, notSent)
	}
}

// Two logical writes under one ticket keep their own outcomes: the first
// written, the second, fenced, not sent.
func TestTwoWritesUnderOneTicketKeepTheirOwnOutcomes(t *testing.T) {
	reg := installRegistry(t, false, "c")
	tk, _ := guardFor("c")
	base := tk.Write(context.Background())
	first := readfence.WithWriteTracker(base)
	if err := (&wire{}).write(first); err != nil {
		t.Fatal(err)
	}
	tighten(t, reg, "c")
	second := readfence.WithWriteTracker(base)
	err := (&wire{}).write(second)
	if !errors.Is(err, readfence.ErrNotForwarded) {
		t.Fatalf("the second write: %v", err)
	}
}
