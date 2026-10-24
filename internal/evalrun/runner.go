package evalrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"time"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/evalrun/record"
)

// MaxRuns bounds the repetitions per scenario (eval design §1, §7).
const MaxRuns = 10

// Chat is the part of the real ChatService the runner drives.
type Chat interface {
	StartInvestigation(cluster string, anchor appbridge.ChatSeedDTO) appbridge.ChatStateDTO
	Ask(cluster, conversationID, question string, seed *appbridge.ChatSeedDTO) appbridge.ChatStateDTO
	Clear(cluster, conversationID string)
}

// Runner runs each scenario's repetitions through the real ChatService
// (eval design §1). It never writes to the cluster: setup and teardown are
// the scripts' (scripts/lab/eval).
type Runner struct {
	Chat    Chat
	Cluster string
	Plant   Checker
	// Ready reports whether Klyx's Observer row for the anchor is live and
	// evaluated after since, and otherwise the reason seen.
	Ready func(ctx context.Context, s *Scenario, uid string, since time.Time) (bool, string)
	// Record points chat's eval hooks at a run's record (nil clears it).
	Record func(*record.Recorder)
	Runs   int
	// FirstAllowance and Allowance bound the wait for Klyx's readiness:
	// the first repetition after start, and every later one.
	FirstAllowance, Allowance time.Duration
	Now                       func() time.Time
	Log                       func(format string, a ...any)
	sleep                     func(time.Duration)
}

// Run runs every scenario and returns the report.
func (r *Runner) Run(ctx context.Context, scenarios []*Scenario) *Report {
	now := r.now()
	rep := &Report{Version: reportVersion, Scorer: ScorerVersion, Started: now(), Provenance: BuildProvenance()}
	runs := r.Runs
	if runs <= 0 {
		runs = 3
	}
	if runs > MaxRuns {
		runs = MaxRuns
	}
	first := true
	for _, s := range scenarios {
		sp := ScenarioProvenance{Name: s.Name, Manifest: manifestHash(s)}
		for i := 1; i <= runs; i++ {
			if ctx.Err() != nil {
				break
			}
			run := r.one(ctx, s, i, first)
			first = false
			if sp.AnchorUID == "" {
				sp.AnchorUID, sp.Witnesses = run.AnchorUID, run.Witnesses
			}
			if rep.Provenance.Provider == "" {
				rep.Provenance.Provider, rep.Provenance.Model = run.Provider, run.Model
			}
			r.log("eval: %s run %d: %s %s", s.Name, i, run.Status, run.Detail)
			rep.Runs = append(rep.Runs, run)
		}
		rep.Provenance.Scenarios = append(rep.Provenance.Scenarios, sp)
		rep.Summary = append(rep.Summary, summarize(s, runs, rep.Runs))
	}
	rep.Finished = now()
	return rep
}

func (r *Runner) one(ctx context.Context, s *Scenario, rep int, first bool) RunRecord {
	start := r.now()()
	run := RunRecord{Scenario: s.Name, Repetition: rep}
	var f RunFacts
	f.Before = r.Plant.Check(ctx, s)
	run.AnchorUID, run.Witnesses = f.Before.AnchorUID, f.Before.Witnesses
	var ex appbridge.ChatExchangeDTO
	var facts record.Facts
	if f.Before.OK {
		allowance := r.Allowance
		if first {
			allowance = r.FirstAllowance
		}
		f.Ready, f.ReadyReason = r.waitReady(ctx, s, f.Before, allowance)
	}
	if f.Before.OK && f.Ready {
		seed := appbridge.ChatSeedDTO{Kind: "object", Group: s.Anchor.Group, Version: s.Anchor.Version, Plural: s.Anchor.Resource,
			Namespace: s.Anchor.Namespace, Name: s.Anchor.Name, UID: f.Before.AnchorUID}
		st := r.Chat.StartInvestigation(r.Cluster, seed)
		if st.Error != "" {
			f.StartError = st.Error
		} else {
			rec, err := record.New(st.ConversationID, f.Before.Predicates)
			if err != nil {
				f.StartError = "the scenario's predicates: " + err.Error()
			} else {
				r.Record(rec)
				st = r.Chat.Ask(r.Cluster, st.ConversationID, "", nil)
				r.Record(nil)
				facts = rec.Facts()
				run.Record = &facts
				run.Provider, run.Model, run.Mode = st.Provider, st.Model, st.Mode
				if n := len(st.Exchanges); n > 0 {
					ex = st.Exchanges[n-1]
					f.Outcome, f.Error = ex.Outcome, ex.Error
					f.AnchorRead = anchorRead(ex, f.Before.AnchorUID)
					f.Report = facts.Report
				} else {
					f.Error = st.Error
				}
				r.Chat.Clear(r.Cluster, st.ConversationID)
			}
		}
		f.After = r.Plant.Check(ctx, s)
	} else {
		f.After = f.Before
	}
	run.Status, run.Detail = Classify(f)
	run.Outcome, run.ReadyReason = f.Outcome, f.ReadyReason
	run.Fallback = fellBack(ex)
	run.Calls, run.Reads = len(ex.Calls), len(ex.Reads)
	if run.Status == StatusCompleted {
		m := Score(s, ex, facts)
		run.Marks = &m
	}
	if ex.Findings != nil {
		run.Window = ex.Findings.Window
		run.Findings, run.Missing = findingRows(ex.Findings), missingRows(ex.Findings)
	}
	run.Duration = r.now()().Sub(start).Round(time.Millisecond).String()
	return run
}

// waitReady polls Klyx's readiness with backoff within the allowance.
func (r *Runner) waitReady(ctx context.Context, s *Scenario, p Plant, allowance time.Duration) (bool, string) {
	sleep := r.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	deadline := r.now()().Add(allowance)
	wait := time.Second
	for {
		ok, why := r.Ready(ctx, s, p.AnchorUID, p.At)
		if ok {
			return true, ""
		}
		if ctx.Err() != nil || !r.now()().Add(wait).Before(deadline) {
			return false, why
		}
		sleep(wait)
		if wait < 5*time.Second {
			wait *= 2
		}
	}
}

// anchorRead: the exchange's first read is the anchor's, completed, by the
// UID the run pinned.
func anchorRead(ex appbridge.ChatExchangeDTO, uid string) bool {
	if len(ex.Reads) == 0 {
		return false
	}
	rd := ex.Reads[0]
	if rd.Outcome == "failed" || rd.Outcome == "cancelled" {
		return false
	}
	for _, ref := range rd.Refs {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

// fellBack: the provider refused tools and the investigation ran its
// snapshot plan (the ledger's "mode" entry).
func fellBack(ex appbridge.ChatExchangeDTO) bool {
	for _, rd := range ex.Reads {
		if rd.Tool == "mode" {
			return true
		}
	}
	return false
}

func manifestHash(s *Scenario) string {
	raw, err := os.ReadFile(s.Script("scenario.yaml"))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r *Runner) now() func() time.Time {
	if r.Now != nil {
		return r.Now
	}
	return time.Now
}

func (r *Runner) log(format string, a ...any) {
	if r.Log != nil {
		r.Log(format, a...)
	}
}
