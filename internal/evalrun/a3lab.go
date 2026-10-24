package evalrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/appbridge"
)

// AI A3 §7: the lab on E1's fixture. A case starts an investigation of
// E1's anchor with a scripted model that proposes a plan, then reviews and
// runs each offered step through the real ChatService - Klyx's own writes,
// the subject under test - waits for each effect and for the incident, and
// records what the card shows. The lab's own writes (a resume from
// outside, the restore) are the lab script's, owner-checked, through
// Write: this code never writes to the cluster itself.

// The cases (§7).
const (
	A3Plan          = "plan"           // suspend a1-web, then roll web back: both established, the incident resolved
	A3UndoOnly      = "undo-only"      // the roll back alone: Flux reapplies, so the incident is never resolved
	A3ResumeBetween = "resume-between" // a1-web resumed from outside after step 1: step 2's review refuses
)

// A3Cases are the cases in the order the lab runs them.
var A3Cases = []string{A3Plan, A3UndoOnly, A3ResumeBetween}

// A3Question is the roadmap's §6.1 sentence, the lab's first question.
const A3Question = "Investigate this failed Kustomization, trace the health check that timed out to the Deployment and its crashing pod's logs, show the commit that changed it, propose a remedy, let me approve the mitigation, and check whether it worked."

// A3Chat is the part of the real ChatService the lab drives.
type A3Chat interface {
	Chat
	InvestigationState(cluster string) appbridge.ChatStateDTO
	ReviewStep(cluster, conversationID, exchangeID string, n int) appbridge.StepReviewDTO
	RunStep(cluster, conversationID, exchangeID string, n int, token string) appbridge.StepRunDTO
}

// Planter checks a scenario's plant (Checker).
type Planter interface {
	Check(ctx context.Context, s *Scenario) Plant
}

// A3Lab runs one case.
type A3Lab struct {
	Chat    A3Chat
	Cluster string
	Plant   Planter
	// Ready is the Runner's: Klyx's Observer row for the anchor, live and
	// evaluated after since.
	Ready func(ctx context.Context, s *Scenario, uid string, since time.Time) (bool, string)
	// Write asks the lab script for one of its owner-checked writes:
	// "resume" resumes a1-web and requests a reconcile; "restore" does that,
	// then plants E1 again.
	Write func(ctx context.Context, action string) error
	// Inspect, when set, reads a subject as the incident does, for the
	// record (the eval build's appbridge.EvalIncidentProbe)
	Inspect func(ctx context.Context, r Ref, uid string) string
	// ReadyWait bounds the Observer's readiness; EffectWait one step's
	// verification; IncidentWait the incident's reading; RestoreWait the
	// plant's return after the restore.
	ReadyWait, EffectWait, IncidentWait, RestoreWait time.Duration
	Poll                                             time.Duration
	Now                                              func() time.Time
	Log                                              func(format string, a ...any)
	sleep                                            func(time.Duration)
}

// A3StepRecord is one step as Klyx decided, reviewed and ran it.
type A3StepRecord struct {
	N      int    `json:"n"`
	Verb   string `json:"verb"`
	Target string `json:"target"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// the review
	Reviewed     bool     `json:"reviewed"`
	ReviewReason string   `json:"reviewReason,omitempty"`
	Change       string   `json:"change,omitempty"`
	Consequence  []string `json:"consequence,omitempty"`
	Expected     string   `json:"expected,omitempty"`
	// the run
	Ran       bool   `json:"ran"`
	RunReason string `json:"runReason,omitempty"`
	Request   string `json:"request,omitempty"`
	NotSent   bool   `json:"notSent,omitempty"`
	Error     string `json:"error,omitempty"`
	Effect    string `json:"effect,omitempty"`
	EffectWhy string `json:"effectWhy,omitempty"`
}

// A3Record is one case's run.
type A3Record struct {
	Case          string                 `json:"case"`
	Status        string                 `json:"status"` // completed | plant-failed | not-ready | no-plan | failed
	Detail        string                 `json:"detail,omitempty"`
	Before        Plant                  `json:"before"`
	After         Plant                  `json:"after"`
	Restored      bool                   `json:"restored"`
	Steps         []A3StepRecord         `json:"steps"`
	PlanStatus    string                 `json:"planStatus,omitempty"`
	PlanStatusWhy string                 `json:"planStatusWhy,omitempty"`
	Incident      *appbridge.IncidentDTO `json:"incident,omitempty"`
	Duration      string                 `json:"duration"`
	// Held says whether the case's expectation held, and Why what did not
	Held bool   `json:"held"`
	Why  string `json:"why,omitempty"`
	// Timeline is what happened when; Probes are Klyx's Observer rows for
	// the incident's subjects when it settled, as the readiness check reads
	// them (live, or why not)
	Timeline []string  `json:"timeline,omitempty"`
	Probes   []A3Probe `json:"probes,omitempty"`
}

// A3Probe is one subject's Observer row when the incident settled.
type A3Probe struct {
	Subject string `json:"subject"`
	Live    bool   `json:"live"`
	Why     string `json:"why,omitempty"`
}

// Run runs one case on E1's scenario s.
func (l *A3Lab) Run(ctx context.Context, s *Scenario, c string) (rec A3Record) {
	start := l.now()
	rec = A3Record{Case: c, Steps: []A3StepRecord{}}
	defer func() { rec.Duration = l.now().Sub(start).Round(time.Second).String() }()
	rec.Before = l.plant(ctx, s)
	if !rec.Before.OK {
		rec.Status, rec.Detail = "plant-failed", rec.Before.Failed
		rec.Held, rec.Why = rec.expect()
		return rec
	}
	if ok, why := l.ready(ctx, s, rec.Before); !ok {
		rec.Status, rec.Detail = "not-ready", why
		rec.Held, rec.Why = rec.expect()
		return rec
	}
	seed := appbridge.ChatSeedDTO{Kind: "object", Group: s.Anchor.Group, Version: s.Anchor.Version, Plural: s.Anchor.Resource,
		Namespace: s.Anchor.Namespace, Name: s.Anchor.Name, UID: rec.Before.AnchorUID}
	st := l.Chat.StartInvestigation(l.Cluster, seed)
	if st.Error != "" {
		rec.Status, rec.Detail = "failed", "start: "+st.Error
		rec.Held, rec.Why = rec.expect()
		return rec
	}
	conv := st.ConversationID
	defer l.Chat.Clear(l.Cluster, conv)
	st = l.Chat.Ask(l.Cluster, conv, A3Question, nil)
	l.mark(&rec, start, "the turn answered")
	ex, ok := lastExchange(st)
	if !ok || ex.Plan == nil || len(ex.Plan.Steps) == 0 {
		rec.Status, rec.Detail = "no-plan", orElse(st.Error, ex.Error, "the turn proposed no plan")
		l.restore(ctx, s, &rec)
		rec.Held, rec.Why = rec.expect()
		return rec
	}
	for _, step := range ex.Plan.Steps {
		rec.Steps = append(rec.Steps, A3StepRecord{N: step.N, Verb: step.Verb, Target: targetText(step.Target), State: step.State, Reason: step.Reason})
	}
	for i := range rec.Steps {
		sr := &rec.Steps[i]
		if sr.State != "offered" {
			break
		}
		if c == A3ResumeBetween && sr.N == 2 {
			// the counterexample: a1-web resumed from outside between the
			// steps, before step 2's review
			if err := l.Write(ctx, "resume"); err != nil {
				rec.Status, rec.Detail = "failed", "the outside resume: "+err.Error()
				break
			}
		}
		rv := l.Chat.ReviewStep(l.Cluster, conv, ex.ID, sr.N)
		sr.Reviewed, sr.ReviewReason, sr.Change, sr.Consequence, sr.Expected = rv.Offered, rv.Reason, rv.Change, rv.Consequence, rv.Expected
		if !rv.Offered {
			break
		}
		run := l.Chat.RunStep(l.Cluster, conv, ex.ID, sr.N, rv.Token)
		sr.Ran, sr.RunReason = run.Ran, run.Reason
		if run.Result != nil {
			sr.Request, sr.NotSent, sr.Error = run.Result.Outcome, run.Result.NotSent, run.Result.Error
		}
		if !run.Ran {
			break
		}
		l.mark(&rec, start, fmt.Sprintf("step %d ran: %s", sr.N, sr.Request))
		got := l.waitStep(ctx, ex.ID, sr.N)
		if got.Run != nil {
			sr.Effect, sr.EffectWhy = got.Run.Effect, got.Run.EffectWhy
		}
		l.mark(&rec, start, fmt.Sprintf("step %d effect: %s", sr.N, sr.Effect))
		if sr.Effect != "established" {
			break
		}
	}
	if p := l.waitIncident(ctx, ex.ID, s, &rec, start); p != nil {
		rec.PlanStatus, rec.PlanStatusWhy, rec.Incident = p.Status, p.StatusWhy, p.Incident
		if p.Incident != nil {
			l.mark(&rec, start, "the incident: "+p.Incident.State)
			rec.Probes = l.probe(ctx, s, p.Incident)
		}
	}
	if rec.Status == "" {
		rec.Status = "completed"
	}
	l.restore(ctx, s, &rec)
	l.mark(&rec, start, fmt.Sprintf("restored: %v", rec.Restored))
	rec.Held, rec.Why = rec.expect()
	return rec
}

func (l *A3Lab) mark(rec *A3Record, start time.Time, what string) {
	rec.Timeline = append(rec.Timeline, fmt.Sprintf("+%s %s", l.now().Sub(start).Round(time.Second), what))
}

// probe reads the Observer's row for each incident subject the scenario
// names (its anchor and subjects), as the readiness check does.
func (l *A3Lab) probe(ctx context.Context, s *Scenario, inc *appbridge.IncidentDTO) []A3Probe {
	var out []A3Probe
	refs := append([]Ref{s.Anchor}, s.Subjects...)
	for _, sub := range inc.Subjects {
		for _, r := range refs {
			if r.Kind != sub.Kind || r.Namespace != sub.Namespace || r.Name != sub.Name {
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			live, why := l.Ready(pctx, &Scenario{Anchor: r}, sub.UID, time.Time{})
			if l.Inspect != nil {
				why = strings.TrimSpace(why + " | " + l.Inspect(pctx, r, sub.UID))
			}
			cancel()
			out = append(out, A3Probe{Subject: targetText(appbridge.ChatCheckSubject{Kind: sub.Kind, Namespace: sub.Namespace, Name: sub.Name}), Live: live, Why: why})
		}
	}
	return out
}

// expect is the case's expectation (§7).
func (r A3Record) expect() (bool, string) {
	if r.Status != "completed" {
		return false, fmt.Sprintf("the case did not complete: %s %s", r.Status, r.Detail)
	}
	if !r.Restored {
		return false, "the lab was not restored: E1's plant did not return"
	}
	step := func(n int) A3StepRecord {
		for _, s := range r.Steps {
			if s.N == n {
				return s
			}
		}
		return A3StepRecord{}
	}
	incident := ""
	if r.Incident != nil {
		incident = r.Incident.State
	}
	switch r.Case {
	case A3Plan:
		s1, s2 := step(1), step(2)
		switch {
		case s1.Verb != "flux-suspend" || s2.Verb != "rollout-undo":
			return false, "the plan is not suspend, then roll back"
		case s1.Request != "written" || s2.Request != "written":
			return false, fmt.Sprintf("a request was not written: step 1 %s, step 2 %s", orElse(s1.Request, "not run"), orElse(s2.Request, "not run"))
		case s1.Effect != "established" || s2.Effect != "established":
			return false, fmt.Sprintf("an effect was not established: step 1 %s (%s), step 2 %s (%s)", s1.Effect, s1.EffectWhy, s2.Effect, s2.EffectWhy)
		case incident != "resolved":
			return false, "the incident is " + orElse(incident, "not read")
		}
		return true, ""
	case A3UndoOnly:
		s1 := step(1)
		switch {
		// the counterexample is the roll back taking its effect and Flux
		// reapplying the failing revision, so a fault in force at the
		// bound. §7 also allows the effect not established, which this
		// record cannot tell from a failed verification: not held, for the
		// owner to read (stage 6 review, finding 3)
		case s1.Verb != "rollout-undo" || s1.Request != "written":
			return false, "the roll back was not written: " + orElse(s1.Request, "not run")
		case s1.Effect != "established":
			return false, fmt.Sprintf("the roll back's effect is %s (%s): not the counterexample this record can show", orElse(s1.Effect, "not read"), s1.EffectWhy)
		case incident == "resolved":
			return false, "the incident was resolved although Flux reapplies the failing revision"
		case incident != "not resolved":
			return false, "the incident is " + orElse(incident, "not read") + ", not a fault in force at the bound"
		}
		return true, ""
	case A3ResumeBetween:
		s1, s2 := step(1), step(2)
		switch {
		case s1.Verb != "flux-suspend" || s1.Request != "written" || s1.Effect != "established":
			return false, fmt.Sprintf("step 1's suspension was not written and established: %s %s", orElse(s1.Request, "not run"), orElse(s1.Effect, "not read"))
		case s2.Verb != "rollout-undo":
			return false, "step 2 is not the roll back"
		case s2.Reviewed || s2.Ran:
			return false, "step 2 was offered after a1-web was resumed"
		case !strings.Contains(s2.ReviewReason, "step 1's effect no longer holds"):
			return false, "step 2's review did not name step 1's prerequisite: " + s2.ReviewReason
		}
		return true, ""
	}
	return false, "no such case " + r.Case
}

// restore asks the lab script to resume a1-web and plant E1 again, then
// waits for the plant check to see it.
func (l *A3Lab) restore(ctx context.Context, s *Scenario, rec *A3Record) {
	if err := l.Write(ctx, "restore"); err != nil {
		rec.After = Plant{Failed: "the restore: " + err.Error()}
		return
	}
	end := l.now().Add(l.RestoreWait)
	for {
		rec.After = l.plant(ctx, s)
		if rec.After.OK {
			rec.Restored = true
			return
		}
		if ctx.Err() != nil || !l.now().Add(l.poll()).Before(end) {
			return
		}
		l.pause(l.poll())
	}
}

// waitStep reads the plan back until step n's effect is no longer pending.
func (l *A3Lab) waitStep(ctx context.Context, exchangeID string, n int) appbridge.ActionStepDTO {
	end := l.now().Add(l.EffectWait)
	for {
		p := l.plan(exchangeID)
		var st appbridge.ActionStepDTO
		if p != nil && n-1 < len(p.Steps) {
			st = p.Steps[n-1]
		}
		if st.Run != nil && st.Run.Effect != "pending" {
			return st
		}
		if ctx.Err() != nil || !l.now().Before(end) {
			return st
		}
		l.pause(l.poll())
	}
}

// waitIncident reads the plan back until its incident, if one started,
// settles. While it is pending, every 30 s it records in the timeline what
// the incident's own lookup reads for each subject (Inspect).
func (l *A3Lab) waitIncident(ctx context.Context, exchangeID string, s *Scenario, rec *A3Record, start time.Time) *appbridge.ActionPlanDTO {
	end := l.now().Add(l.IncidentWait)
	next := l.now()
	for {
		p := l.plan(exchangeID)
		if p == nil || p.Incident == nil || p.Incident.State != "pending" {
			return p
		}
		if l.Inspect != nil && !l.now().Before(next) {
			next = l.now().Add(30 * time.Second)
			for _, pr := range l.probe(ctx, s, p.Incident) {
				l.mark(rec, start, fmt.Sprintf("pending: %s: %s", pr.Subject, pr.Why))
			}
		}
		if ctx.Err() != nil || !l.now().Before(end) {
			return p
		}
		l.pause(l.poll())
	}
}

func (l *A3Lab) plan(exchangeID string) *appbridge.ActionPlanDTO {
	for _, ex := range l.Chat.InvestigationState(l.Cluster).Exchanges {
		if ex.ID == exchangeID {
			return ex.Plan
		}
	}
	return nil
}

func (l *A3Lab) plant(ctx context.Context, s *Scenario) Plant {
	pctx, cancel := context.WithTimeout(ctx, plantBound)
	defer cancel()
	return l.Plant.Check(pctx, s)
}

func (l *A3Lab) ready(ctx context.Context, s *Scenario, p Plant) (bool, string) {
	r := &Runner{Ready: l.Ready, Now: l.Now, sleep: l.sleep}
	return r.waitReady(ctx, s, p, l.ReadyWait)
}

func (l *A3Lab) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *A3Lab) poll() time.Duration {
	if l.Poll > 0 {
		return l.Poll
	}
	return 2 * time.Second
}

func (l *A3Lab) pause(d time.Duration) {
	if l.sleep != nil {
		l.sleep(d)
		return
	}
	time.Sleep(d)
}

func lastExchange(st appbridge.ChatStateDTO) (appbridge.ChatExchangeDTO, bool) {
	if n := len(st.Exchanges); n > 0 {
		return st.Exchanges[n-1], true
	}
	return appbridge.ChatExchangeDTO{}, false
}

func targetText(t appbridge.ChatCheckSubject) string {
	return strings.TrimSpace(t.Kind + " " + strings.TrimPrefix(t.Namespace+"/"+t.Name, "/"))
}

func orElse(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// Write writes the case's record as a3-<case>.json and a3-<case>.md into
// dir, private to the owner.
func (r A3Record) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "a3-"+r.Case+".json"), b); err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, "a3-"+r.Case+".md"), []byte(r.Markdown()))
}

// Markdown is the case for reading.
func (r A3Record) Markdown() string {
	var b strings.Builder
	held := "held"
	if !r.Held {
		held = "did not hold: " + r.Why
	}
	fmt.Fprintf(&b, "# AI A3 lab: %s\n\n%s, in %s. The expectation %s.\n\n", r.Case, r.Status, r.Duration, held)
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "- step %d, %s %s: %s", s.N, s.Verb, s.Target, s.State)
		if s.Reason != "" {
			fmt.Fprintf(&b, " (%s)", s.Reason)
		}
		switch {
		case s.Ran:
			fmt.Fprintf(&b, "; request %s; effect %s", s.Request, orElse(s.Effect, "not decided"))
			if s.EffectWhy != "" {
				fmt.Fprintf(&b, " (%s)", s.EffectWhy)
			}
		case s.ReviewReason != "":
			fmt.Fprintf(&b, "; not offered at review: %s", s.ReviewReason)
		case s.RunReason != "":
			fmt.Fprintf(&b, "; not run: %s", s.RunReason)
		}
		b.WriteString("\n")
	}
	if r.Incident != nil {
		fmt.Fprintf(&b, "\nIncident: %s", r.Incident.State)
		if r.Incident.Why != "" {
			fmt.Fprintf(&b, " (%s)", r.Incident.Why)
		}
		b.WriteString("\n")
		for _, s := range r.Incident.Subjects {
			fmt.Fprintf(&b, "- %s %s/%s: %s\n", s.Kind, s.Namespace, s.Name, s.Reading)
		}
	}
	for _, p := range r.Probes {
		fmt.Fprintf(&b, "- Observer row of %s when it settled: live %v %s\n", p.Subject, p.Live, p.Why)
	}
	fmt.Fprintf(&b, "\nRestored: %v\n", r.Restored)
	if len(r.Timeline) > 0 {
		fmt.Fprintf(&b, "\nTimeline:\n- %s\n", strings.Join(r.Timeline, "\n- "))
	}
	return b.String()
}
