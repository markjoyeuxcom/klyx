package evalrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/evalrun/record"
)

// fakeChat stands in for ChatService: it records what the runner asked and
// answers with a scripted exchange. Through the record it reports the
// anchor's read as delivered and the report as accepted.
type fakeChat struct {
	startErr string
	ex       appbridge.ChatExchangeDTO
	rec      *record.Recorder
	started  []appbridge.ChatSeedDTO
	cleared  int
	onAsk    func()
}

func (f *fakeChat) StartInvestigation(cluster string, anchor appbridge.ChatSeedDTO) appbridge.ChatStateDTO {
	f.started = append(f.started, anchor)
	if f.startErr != "" {
		return appbridge.ChatStateDTO{Error: f.startErr}
	}
	return appbridge.ChatStateDTO{ConversationID: "inv-1"}
}

func (f *fakeChat) Ask(cluster, conv, question string, seed *appbridge.ChatSeedDTO) appbridge.ChatStateDTO {
	if f.onAsk != nil {
		f.onAsk()
	}
	if f.rec != nil {
		f.rec.ToolsReport(conv, f.ex.Findings != nil)
	}
	return appbridge.ChatStateDTO{ConversationID: conv, Provider: "openaiCompatible", Model: "scripted", Mode: "tools", Exchanges: []appbridge.ChatExchangeDTO{f.ex}}
}

func (f *fakeChat) Clear(string, string) { f.cleared++ }

func controlRunner(chat *fakeChat, ready bool) *Runner {
	kube := crashLab("CrashLoopBackOff")
	c := checker(kube, depObj())
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	r := &Runner{Chat: chat, Cluster: "lab", Plant: c, Runs: 2,
		Ready: func(context.Context, *Scenario, string, time.Time) (bool, string) {
			if ready {
				return true, ""
			}
			return false, "no row for the anchor"
		},
		Record:         func(rec *record.Recorder) { chat.rec = rec },
		FirstAllowance: 4 * time.Second, Allowance: 2 * time.Second,
		Now: func() time.Time { return clock },
	}
	r.sleep = func(d time.Duration) { clock = clock.Add(d) }
	return r
}

// controlScenario: the anchor is the E1 fixture's Deployment, its plant the
// Deployment's existence; a control plants no evidence.
func controlScenario() *Scenario {
	dep := Ref{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespace: "klyx-a1-app", Name: "web"}
	return &Scenario{Name: "e2-healthy-control", Control: true, Anchor: dep,
		Invariants: []Invariant{{Object: dep, Path: "metadata.name", Matches: "^web$"}},
		Witnesses:  []Witness{{Object: dep, Path: "metadata.uid"}}}
}

func anchoredExchange(window string, fs ...appbridge.FindingDTO) appbridge.ChatExchangeDTO {
	return appbridge.ChatExchangeDTO{Outcome: "answered", Findings: &appbridge.FindingsDTO{Window: window, Findings: fs},
		Reads: []appbridge.ChatRead{{ID: "r1", Tool: "get_resource", Outcome: "completed", Refs: []appbridge.ChatRef{{UID: "uid-dep"}}}},
		Calls: []appbridge.ChatModelCallDTO{{}}}
}

func TestAControlRunCompletesAndPasses(t *testing.T) {
	chat := &fakeChat{ex: anchoredExchange(windowNoFailure)}
	rep := controlRunner(chat, true).Run(context.Background(), []*Scenario{controlScenario()})
	if len(rep.Runs) != 2 || chat.cleared != 2 {
		t.Fatalf("%+v", rep.Runs)
	}
	run := rep.Runs[0]
	if run.Status != StatusCompleted || run.Marks == nil || run.Marks.Control == nil || !*run.Marks.Control {
		t.Fatalf("%+v", run)
	}
	if chat.started[0].UID != "uid-dep" || chat.started[0].Plural != "deployments" {
		t.Fatalf("the anchor is pinned by the plant's UID: %+v", chat.started[0])
	}
	s := rep.Summary[0]
	if s.Completed != 2 || s.ControlPasses == nil || *s.ControlPasses != 2 || s.Statuses[StatusCompleted] != 2 {
		t.Fatalf("%+v", s)
	}
}

func TestKlyxNotReadyIsKlyxsAndNeverStarts(t *testing.T) {
	chat := &fakeChat{ex: anchoredExchange(windowNoFailure)}
	rep := controlRunner(chat, false).Run(context.Background(), []*Scenario{controlScenario()})
	run := rep.Runs[0]
	if run.Status != StatusSUTNotReady || run.Detail != "no row for the anchor" || len(chat.started) != 0 {
		t.Fatalf("%+v %d", run, len(chat.started))
	}
	if s := rep.Summary[0]; s.Completed != 0 || !strings.HasPrefix(s.Result, "no result") || s.Statuses[StatusSUTNotReady] != 2 {
		t.Fatalf("zero completed runs are no result, and Klyx's failure stays counted: %+v", s)
	}
}

func TestABrokenPlantIsNotReady(t *testing.T) {
	chat := &fakeChat{ex: anchoredExchange(windowNoFailure)}
	r := controlRunner(chat, true)
	s := controlScenario()
	s.Invariants[0].Matches = "^api$"
	rep := r.Run(context.Background(), []*Scenario{s})
	if rep.Runs[0].Status != StatusNotReady || len(chat.started) != 0 {
		t.Fatalf("%+v", rep.Runs[0])
	}
}

func TestAWitnessChangedMidRunIsDrift(t *testing.T) {
	chat := &fakeChat{ex: anchoredExchange(windowNoFailure)}
	r := controlRunner(chat, true)
	r.Runs = 1
	// the Deployment is replaced while the model answers
	chat.onAsk = func() {
		replaced := depObj()
		replaced.SetUID("uid-dep-2")
		r.Plant = checker(crashLab("CrashLoopBackOff"), replaced)
	}
	rep := r.Run(context.Background(), []*Scenario{controlScenario()})
	if rep.Runs[0].Status != StatusDrift {
		t.Fatalf("%+v", rep.Runs[0])
	}
}

func TestARefusedStartIsKlyxs(t *testing.T) {
	chat := &fakeChat{startErr: "the object to investigate is not valid"}
	rep := controlRunner(chat, true).Run(context.Background(), []*Scenario{controlScenario()})
	if rep.Runs[0].Status != StatusSUTFailure {
		t.Fatalf("%+v", rep.Runs[0])
	}
}

func TestAnAnswerWithoutAReportIsAReportFailure(t *testing.T) {
	ex := anchoredExchange(windowNoFailure)
	ex.Findings = nil
	chat := &fakeChat{ex: ex}
	rep := controlRunner(chat, true).Run(context.Background(), []*Scenario{controlScenario()})
	if rep.Runs[0].Status != StatusReportFailure || rep.Runs[0].Marks != nil {
		t.Fatalf("%+v", rep.Runs[0])
	}
}

func TestTheReportIsPrivateBoundedAndHoldsNoBodies(t *testing.T) {
	long := strings.Repeat("x", 5000)
	chat := &fakeChat{ex: anchoredExchange(windowNoFailure, appbridge.FindingDTO{Type: "cause", Label: "unsupported", Statement: long, Cites: []string{"r1"}})}
	rep := controlRunner(chat, true).Run(context.Background(), []*Scenario{controlScenario()})
	dir := filepath.Join(t.TempDir(), "out")
	if err := rep.Write(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report.json", "report.md"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", name, err, st)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "report.json"))
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(back.Runs[0].Findings[0].Statement)); got > statementMax+1 {
		t.Fatalf("a statement is clipped: %d", got)
	}
	if back.Runs[0].Marks == nil || *back.Runs[0].Marks.Control {
		t.Fatal("an unsupported cause fails the control")
	}
	md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	if !strings.Contains(string(md), "- [ ] cause, unsupported") || !strings.Contains(string(md), "not diagnoses") {
		t.Fatalf("%s", md)
	}
}

func TestAnOversizedReportSaysWhatItLeftOut(t *testing.T) {
	rep := &Report{}
	for i := 0; i < 40; i++ {
		run := RunRecord{Scenario: "s", Repetition: i}
		for j := 0; j < 60; j++ {
			run.Findings = append(run.Findings, FindingRow{Statement: strings.Repeat("y", statementMax)})
			run.Missing = append(run.Missing, MissingRow{Text: strings.Repeat("z", missingTextMax)})
		}
		rep.Runs = append(rep.Runs, run)
	}
	b, err := rep.encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxReportBytes || len(rep.Omitted) == 0 || rep.Omitted[0] != "every run's missing evidence" {
		t.Fatalf("%d bytes, omitted %v", len(b), rep.Omitted)
	}
}

func TestAnAnchorReadUnderAnotherUIDIsNotTheAnchor(t *testing.T) {
	ex := anchoredExchange(windowNoFailure)
	ex.Reads[0].Refs[0].UID = "uid-someone-else"
	chat := &fakeChat{ex: ex}
	rep := controlRunner(chat, true).Run(context.Background(), []*Scenario{controlScenario()})
	if rep.Runs[0].Status != StatusSUTFailure {
		t.Fatalf("%+v", rep.Runs[0])
	}
}

// Codex's implementation review, finding 9: every readiness attempt is
// bounded by the allowance, so a stalled Observer read ends with
// sut-not-ready.
func TestAStalledReadinessReadEndsAtTheAllowance(t *testing.T) {
	chat := &fakeChat{ex: anchoredExchange(windowNoFailure)}
	r := controlRunner(chat, true)
	r.Runs, r.Now, r.FirstAllowance = 1, time.Now, 50*time.Millisecond
	r.Ready = func(ctx context.Context, _ *Scenario, _ string, _ time.Time) (bool, string) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("a readiness attempt without a deadline")
			return false, "no deadline"
		}
		<-ctx.Done() // the Observer never answers
		return false, ""
	}
	start := time.Now()
	rep := r.Run(context.Background(), []*Scenario{controlScenario()})
	if rep.Runs[0].Status != StatusSUTNotReady || !strings.Contains(rep.Runs[0].Detail, "did not answer") || time.Since(start) > 5*time.Second {
		t.Fatalf("%+v after %s", rep.Runs[0], time.Since(start))
	}
}

// Codex's implementation review, finding 1: a change to the files the
// preflight checked stops the eval.
func TestAChangedBoundaryStopsTheEval(t *testing.T) {
	chat := &fakeChat{ex: anchoredExchange(windowNoFailure)}
	r := controlRunner(chat, true)
	r.Runs = 3
	n := 0
	r.Boundary = func() error {
		if n++; n == 2 {
			return errors.New("fleet.yaml changed after the preflight")
		}
		return nil
	}
	rep := r.Run(context.Background(), []*Scenario{controlScenario()})
	if len(rep.Runs) != 2 || rep.Runs[1].Status != StatusNotReady || !strings.Contains(rep.Stopped, "changed after the preflight") || len(chat.started) != 1 {
		t.Fatalf("%+v %q", rep.Runs, rep.Stopped)
	}
	if !strings.Contains(rep.Markdown(), "The runs after it were not made") {
		t.Fatal("the report says the eval stopped")
	}
}

func TestTheLabsFilesAreDigested(t *testing.T) {
	l := newLab(t, oneCluster, "")
	lab, err := l.preflight().Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lab.Unchanged(); err != nil {
		t.Fatal(err)
	}
	kc := filepath.Join(l.dir, "kubeconfig")
	if err := os.WriteFile(kc, []byte(strings.Replace(kubeconfigFmt, "%s", "https://api.production.example.invalid:6443", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lab.Unchanged(); err == nil || !strings.Contains(err.Error(), "changed after the preflight") {
		t.Fatalf("%v", err)
	}
}
