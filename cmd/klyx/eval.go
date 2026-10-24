//go:build eval

package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/evalrun"
	"github.com/moomora/klyx/internal/evalrun/record"
)

// The AI eval (eval design §1, §3): built only with `-tags eval`, run with
// KLYX_CONFIG (the eval directory's fleet.yaml, never the owner's),
// KLYX_EVAL_LAB (scripts/lab/eval), KLYX_EVAL_OUT (the report directory),
// KLYX_EVAL_SCENARIOS (comma-separated, default all), KLYX_EVAL_RUNS
// (default 3, at most 10) and KLYX_EVAL_KIND (the kind cluster, default
// klyx). The preflight runs here, before main starts any connection.

func init() {
	// the confined build re-runs this binary as a child with an empty
	// environment: the eval stands aside, as the smoke does
	if len(os.Args) == 2 && os.Args[1] == cachediff.ChildFlag {
		return
	}
	out := os.Getenv("KLYX_EVAL_OUT")
	if out == "" {
		log.Fatal("the eval build needs KLYX_EVAL_OUT")
	}
	var names []string
	if s := strings.TrimSpace(os.Getenv("KLYX_EVAL_SCENARIOS")); s != "" {
		names = strings.Split(s, ",")
	}
	scenarios, err := evalrun.LoadScenarios(os.Getenv("KLYX_EVAL_LAB"), names)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	var repos []string
	for _, s := range scenarios {
		repos = append(repos, s.Repos...)
	}
	kind := os.Getenv("KLYX_EVAL_KIND")
	if kind == "" {
		kind = "klyx"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lab, err := evalrun.Preflight{ConfigPath: os.Getenv("KLYX_CONFIG"), KindName: kind, Repos: repos}.Run(ctx)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	runs := 3
	if n, err := strconv.Atoi(os.Getenv("KLYX_EVAL_RUNS")); err == nil && n > 0 {
		runs = min(n, evalrun.MaxRuns)
	}
	log.Printf("eval: lab %s at %s; %d scenarios × %d runs: at most %d model calls", lab.Cluster.Name, lab.Server, len(scenarios), runs, len(scenarios)*runs*12)
	evalRun = &labEval{lab: lab, scenarios: scenarios, runs: runs, out: out}
}

type labEval struct {
	lab       evalrun.Lab
	scenarios []*evalrun.Scenario
	runs      int
	out       string
}

func (e *labEval) Run(ctx context.Context, d evalDeps) int {
	dyn, err := dynamic.NewForConfig(e.lab.REST)
	if err != nil {
		log.Printf("eval: %v", err)
		return 2
	}
	kube, err := kubernetes.NewForConfig(e.lab.REST)
	if err != nil {
		log.Printf("eval: %v", err)
		return 2
	}
	name := e.lab.Cluster.Name
	r := &evalrun.Runner{
		Chat:    d.chat,
		Cluster: name,
		Plant:   evalrun.Checker{Dyn: dyn, Kube: kube},
		Ready: func(ctx context.Context, s *evalrun.Scenario, uid string, since time.Time) (bool, string) {
			c, why := d.conn(name)
			if why != "" {
				return false, why
			}
			return appbridge.EvalAnchorReady(ctx, c, s.Anchor.Group, s.Anchor.Resource, uid, since)
		},
		Record:         func(rec *record.Recorder) { appbridge.SetEvalRecorder(d.chat, rec) },
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
