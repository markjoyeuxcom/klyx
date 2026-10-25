//go:build eval

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/envread"
	"github.com/moomora/klyx/internal/evalrun"
	"github.com/moomora/klyx/internal/evalrun/record"
)

// The AI eval (eval design §1, §3): built only with `-tags eval`, run with
// KLYX_CONFIG (the eval directory's fleet.yaml, never the owner's),
// KLYX_EVAL_LAB (scripts/lab/eval), KLYX_EVAL_OUT (the report directory),
// KLYX_EVAL_SCENARIOS (comma-separated, default all), KLYX_EVAL_RUNS
// (default 3, at most 10) and KLYX_EVAL_KIND (the kind cluster, default
// klyx). The preflight runs here, before main starts any connection.
//
// KLYX_EVAL_A3 names one of AI A3 §7's lab cases instead (plan, undo-only,
// resume-between): it runs on E1 alone, through the real ChatService, and
// asks the lab script (klyx-eval.sh a3-write) for the lab's own writes.

func init() {
	// the confined build re-runs this binary as a child with an empty
	// environment: the eval stands aside, as the smoke does
	if len(os.Args) == 2 && os.Args[1] == cachediff.ChildFlag {
		return
	}
	out := envread.Get(envread.KlyxEvalOut)
	if out == "" {
		log.Fatal("the eval build needs KLYX_EVAL_OUT")
	}
	var names []string
	if s := strings.TrimSpace(envread.Get(envread.KlyxEvalScenarios)); s != "" {
		names = strings.Split(s, ",")
	}
	a3 := strings.TrimSpace(envread.Get(envread.KlyxEvalA3))
	if a3 != "" {
		if !slices.Contains(evalrun.A3Cases, a3) {
			log.Fatalf("eval: no A3 case %q (%s)", a3, strings.Join(evalrun.A3Cases, ", "))
		}
		names = []string{"e1-flux-crashloop"}
	}
	scenarios, err := evalrun.LoadScenarios(envread.Get(envread.KlyxEvalLab), names)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	var repos []string
	for _, s := range scenarios {
		repos = append(repos, s.Repos...)
	}
	kind := envread.Get(envread.KlyxEvalKind)
	if kind == "" {
		kind = "klyx"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lab, err := evalrun.Preflight{ConfigPath: envread.Get(envread.KlyxConfig), KindName: kind, Repos: repos}.Run(ctx)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	runs := 3
	if n, err := strconv.Atoi(envread.Get(envread.KlyxEvalRuns)); err == nil && n > 0 {
		runs = min(n, evalrun.MaxRuns)
	}
	log.Printf("eval: lab %s at %s; %d scenarios × %d runs: at most %d model calls", lab.Cluster.Name, lab.Server, len(scenarios), runs, len(scenarios)*runs*12)
	evalRun = &labEval{lab: lab, scenarios: scenarios, runs: runs, out: out, a3: a3}
}

type labEval struct {
	lab       evalrun.Lab
	scenarios []*evalrun.Scenario
	runs      int
	out       string
	a3        string // an A3 lab case, or ""
}

func (e *labEval) Config() *config.Config { return e.lab.Config }

func (e *labEval) Resolve(cc config.ClusterConfig) (*rest.Config, error) {
	if cc.Name != e.lab.Cluster.Name || cc.Context != e.lab.Cluster.Context || cc.Kubeconfig != e.lab.Cluster.Kubeconfig {
		return nil, fmt.Errorf("refusing: the eval connects only to the lab cluster its preflight checked")
	}
	return rest.CopyConfig(e.lab.REST), nil
}

// evalRunBound is the longest one repetition may take: the readiness
// allowance, the plant checks and an investigation's turn deadline, with a
// margin. Past runs × scenarios of it, the eval prints every goroutine's
// stack and exits: a stall explains itself instead of hanging its caller.
const evalRunBound = 8 * time.Minute

// a3Bound is the longest one A3 case may take: readiness, the turn, two
// verifications, the incident's ten minutes and the restore, with a margin.
const a3Bound = 40 * time.Minute

// stall prints every goroutine's stack and exits once bound passes: a
// stall explains itself instead of hanging its caller.
func stall(bound time.Duration) *time.Timer {
	return time.AfterFunc(bound, func() {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		log.Printf("eval: passed its %s bound; every goroutine:\n%s", bound, buf[:n])
		os.Exit(3)
	})
}

// checker is the runner's plant checker: its own reads are bounded, so a
// stalled request ends.
func (e *labEval) checker() (evalrun.Checker, error) {
	plant := rest.CopyConfig(e.lab.REST)
	plant.Timeout = 30 * time.Second
	dyn, err := dynamic.NewForConfig(plant)
	if err != nil {
		return evalrun.Checker{}, err
	}
	kube, err := kubernetes.NewForConfig(plant)
	if err != nil {
		return evalrun.Checker{}, err
	}
	return evalrun.Checker{Dyn: dyn, Kube: kube}, nil
}

// ready is Klyx's readiness for a scenario's anchor, through the app's own
// connection.
func (e *labEval) ready(d evalDeps) func(ctx context.Context, s *evalrun.Scenario, uid string, since time.Time) (bool, string) {
	name := e.lab.Cluster.Name
	return func(ctx context.Context, s *evalrun.Scenario, uid string, since time.Time) (bool, string) {
		c, why := d.conn(name)
		if why != "" {
			return false, why
		}
		return appbridge.EvalAnchorReady(ctx, c, s.Anchor.Group, s.Anchor.Resource, uid, since)
	}
}

func (e *labEval) Run(ctx context.Context, d evalDeps) int {
	if e.a3 != "" {
		return e.runA3(ctx, d)
	}
	bound := time.Duration(e.runs*len(e.scenarios))*evalRunBound + 2*time.Minute
	defer stall(bound).Stop()
	checker, err := e.checker()
	if err != nil {
		log.Printf("eval: %v", err)
		return 2
	}
	name := e.lab.Cluster.Name
	r := &evalrun.Runner{
		Chat:    d.chat,
		Cluster: name,
		Plant:   checker,
		Ready:   e.ready(d),
		Record:  func(rec *record.Recorder) { appbridge.SetEvalRecorder(d.chat, rec) },
		// the files the preflight checked must not change while it runs
		Boundary:       e.lab.Unchanged,
		Runs:           e.runs,
		FirstAllowance: 120 * time.Second,
		Allowance:      60 * time.Second,
		Log:            log.Printf,
	}
	rep := r.Run(ctx, e.scenarios)
	if err := rep.Write(e.out); err != nil {
		log.Printf("eval: the report: %v", err)
		return 2
	}
	log.Printf("eval: report written to %s", e.out)
	return 0
}

// runA3 runs one A3 lab case (§7) and writes its record. It exits 1 when
// the case's expectation did not hold.
func (e *labEval) runA3(ctx context.Context, d evalDeps) int {
	defer stall(a3Bound).Stop()
	checker, err := e.checker()
	if err != nil {
		log.Printf("eval: %v", err)
		return 2
	}
	script := filepath.Join(envread.Get(envread.KlyxEvalLab), "..", "klyx-eval.sh")
	lab := &evalrun.A3Lab{
		Chat:    d.chat,
		Cluster: e.lab.Cluster.Name,
		Plant:   checker,
		Ready:   e.ready(d),
		// the lab's own writes are the script's, owner-checked
		Write: func(ctx context.Context, action string) error {
			// a restore waits for the resume's reconcile, then plants E1
			// again: steady, then crash, each awaited
			bound := 3 * time.Minute
			if action == "restore" {
				bound = 15 * time.Minute
			}
			wctx, cancel := context.WithTimeout(ctx, bound)
			defer cancel()
			out, err := exec.CommandContext(wctx, script, "a3-write", action).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
			}
			log.Printf("eval: A3 lab write %s: %s", action, strings.TrimSpace(string(out)))
			return nil
		},
		Inspect: func(ctx context.Context, r evalrun.Ref, uid string) string {
			c, why := d.conn(e.lab.Cluster.Name)
			if why != "" {
				return why
			}
			return appbridge.EvalIncidentProbe(ctx, d.chat, e.lab.Cluster.Name, c, r.Group, r.Version, r.Resource, r.Namespace, r.Name, uid)
		},
		ReadyWait:    2 * time.Minute,
		EffectWait:   11 * time.Minute,
		IncidentWait: 11 * time.Minute,
		RestoreWait:  8 * time.Minute,
		Log:          log.Printf,
	}
	rec := lab.Run(ctx, e.scenarios[0], e.a3)
	if err := rec.Write(e.out); err != nil {
		log.Printf("eval: the record: %v", err)
		return 2
	}
	log.Printf("eval: A3 %s: %s; the expectation held: %v %s", rec.Case, rec.Status, rec.Held, rec.Why)
	if !rec.Held {
		return 1
	}
	return 0
}
