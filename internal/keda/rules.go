package keda

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/hpa"
)

// Finding is one independent finding (§3.2): the verdict is the worst.
type Finding struct {
	Role  health.Kind `json:"role"`
	Label string      `json:"label"`
	Text  string      `json:"text,omitempty"`
	// Note: an informational finding (ok), never the headline.
	Note bool `json:"note,omitempty"`
	// next and boundary are the finding's time edges: a pending deadline
	// (re-evaluated through Next) or one crossed.
	next     *time.Time
	boundary *time.Time
	cond     *health.CondSince
}

// HPAEvidence is what the HPA kind says about one ScaledObject's join (§3).
type HPAEvidence struct {
	// Joined is the retained HPA whose controller reference UID is the
	// ScaledObject's; nil when none is retained.
	Joined *hpa.HPA
	// Current: the HPA kind's watch is live, so a retained HPA is the
	// latest the API delivered. Complete: current, uncut and not failed,
	// so absence is provable. They are independent (§3).
	Current, Complete bool
	// AsOf is when the kind was last current, for stale evidence.
	AsOf time.Time
	// Others are the other autoscalers on the same target, each with its
	// owner, for AmbiguousSelector.
	Others []string
}

// SOInputs is what the ScaledObject rule reads beside the object.
type SOInputs struct {
	Now      time.Time
	Version  Version
	HPA      HPAEvidence
	Episodes map[EpisodeKind]Episode
	// MissingAuths are authentication references ("Kind/name") absent from
	// a complete, uncut read of their kind.
	MissingAuths []string
	// TargetReplicas is the target's actual replicas when readable.
	TargetReplicas *int32
}

// Verdict is a scaler's verdict and every finding behind it.
type Verdict struct {
	Eval      health.NativeEval `json:"-"`
	Findings  []Finding         `json:"findings"`
	Intent    Intent            `json:"-"`
	Completed bool              `json:"completed,omitempty"` // a confirmed completed full pause
}

// KEDA's own pause-step errors (2.21's handlePause): only these are "pause
// failed" (§3.2).
var pauseStepErrors = []string{
	"failed to update paused status",
	"failed to stop the scale loop for paused ScaledObject",
	"failed to delete HPA for paused ScaledObject",
	"failed to scale target to paused replica count",
	"failed to update paused replica count status",
}

const pausedBeingScaled = "ScaledObject paused replicas are being scaled"

// The 2.20.0 and 2.20.1 Ready reasons that report HPA trouble (§1.8).
var hpaReadyReasons = map[string]bool{"HPAMetricsUnavailable": true, "ScalingDegraded": true}

// States are the episode states a ScaledObject is in now (§3.1), for the
// tracker. hpaAbsent: no controlled HPA in a current, complete read.
func States(so *ScaledObject, v Version, hpaAbsent bool) EpisodeKind {
	in := ScaledObjectIntent(so.Pause, v)
	var st EpisodeKind
	if in.Kind == PauseFull && !PauseCompleted(so, in, hpaAbsent) {
		st |= EpPausing
	}
	if so.CondIs(CondPaused, "True") && !so.Pause.Any() {
		st |= EpResuming
	}
	if in.Kind != PauseFull && so.CondIs(CondReady, "True") && hpaAbsent {
		st |= EpAwaitingHPA
	}
	return st
}

// JobStates are a ScaledJob's episode states.
func JobStates(j *ScaledJob, v Version) EpisodeKind {
	in := ScaledJobIntent(j.Pause, v)
	var st EpisodeKind
	if in.Kind == PauseFull && !JobPauseCompleted(j, in) {
		st |= EpPausing
	}
	if j.CondIs(CondPaused, "True") && !j.Pause.Any() {
		st |= EpResuming
	}
	return st
}

// ScaledObjectRule judges a ScaledObject (S9 §3.2).
func ScaledObjectRule(so *ScaledObject, in SOInputs) Verdict {
	intent := ScaledObjectIntent(so.Pause, in.Version)
	v := Verdict{Intent: intent}
	if len(so.Conds) == 0 {
		v.Findings = []Finding{noConditions(so.Created, in.Now, "ScaledObject")}
		v.Eval = evalOf(v.Findings)
		return v
	}
	hpaAbsent := in.HPA.Joined == nil && in.HPA.Complete
	if PauseCompleted(so, intent, hpaAbsent) {
		v.Completed = true
		v.Findings = []Finding{{Role: health.Paused, Label: pausedLabel(so, intent, in.TargetReplicas)}}
		v.Eval = evalOf(v.Findings)
		return v
	}
	ready := so.Cond(CondReady)
	var f []Finding
	if ready != nil && ready.Status == "False" && !hpaReadyReasons[ready.Reason] {
		f = append(f, readyFalse(so, *ready, intent, in))
	}
	f = append(f, hpaResult(so, intent, in)...)
	if ready != nil && ready.Status == "Unknown" {
		if ready.Reason == "PartialTriggerError" {
			f = append(f, Finding{Role: health.Degraded, Label: "Some triggers failing", Text: failingTriggers(so)})
		} else {
			f = append(f, Finding{Role: health.Progressing, Label: "Waiting for the first poll"})
		}
	}
	if so.CondIs(CondFallback, "True") {
		f = append(f, fallbackFinding(so))
	}
	if j := in.HPA.Joined; j != nil && so.HPAName != "" && so.HPAName != j.Name {
		f = append(f, Finding{Role: health.Degraded, Label: "HPA name mismatch",
			Text: fmt.Sprintf("status names HPA %s; the HPA KEDA controls is %s", so.HPAName, j.Name)})
	}
	f = append(f, pauseFindings(so, intent, in)...)
	f = append(f, healthy(so, intent, in))
	v.Findings = f
	v.Eval = evalOf(f)
	return v
}

// noConditions: creation dates it - a reconciled object keeps its
// conditions (§3.2).
func noConditions(created, now time.Time, kind string) Finding {
	deadline := created.Add(Grace)
	if now.Before(deadline) {
		return Finding{Role: health.Progressing, Label: "Waiting for KEDA", next: health.At(deadline)}
	}
	return Finding{Role: health.Unknown, Label: "Not reconciled", boundary: health.At(deadline),
		Text: "KEDA has not reconciled this " + kind}
}

func readyFalse(so *ScaledObject, c Cond, intent Intent, in SOInputs) Finding {
	if len(in.MissingAuths) > 0 {
		missing := make([]string, 0, len(in.MissingAuths))
		for _, m := range in.MissingAuths {
			kind, name, _ := strings.Cut(m, "/")
			missing = append(missing, kind+" "+name)
		}
		return Finding{Role: health.Fault, Label: missing[0] + " does not exist",
			Text: strings.Join(missing, ", ") + " does not exist; KEDA: " + c.Reason + ": " + c.Message}
	}
	if intent.Kind == PauseFull {
		for _, p := range pauseStepErrors {
			if strings.HasPrefix(c.Message, p) {
				return Finding{Role: health.Fault, Label: "Pause failed", Text: "pause failed: " + c.Message}
			}
		}
		if strings.HasPrefix(c.Message, pausedBeingScaled) {
			ep := episode(in, EpPausing)
			if in.Now.Before(ep.Deadline()) {
				return Finding{Role: health.Progressing, Label: "Pausing", Text: "pausing: scaling to " + countText(intent.Requested),
					next: health.At(ep.Deadline())}
			}
			return Finding{Role: health.Fault, Label: c.Reason, Text: c.Message + " (while a pause is requested)", boundary: health.At(ep.Deadline())}
		}
		return Finding{Role: health.Fault, Label: c.Reason, Text: c.Message + " (while a pause is requested)"}
	}
	return Finding{Role: health.Fault, Label: c.Reason, Text: c.Message}
}

// mirror is KEDA's cached copy of the HPA's health: HPAActive, or the
// 2.20.0/2.20.1 Ready reasons read the same way (§3).
func mirror(so *ScaledObject) *Cond {
	if c := so.Cond(CondHPAActive); c != nil {
		return c
	}
	if r := so.Cond(CondReady); r != nil && r.Status == "False" && hpaReadyReasons[r.Reason] {
		return &Cond{Type: CondHPAActive, Status: "False", Reason: r.Reason, Message: r.Message}
	}
	return nil
}

// hpaResult is §3's HPA result table.
func hpaResult(so *ScaledObject, intent Intent, in SOInputs) []Finding {
	ev := in.HPA
	m := mirror(so)
	mirrorFalse := m != nil && m.Status == "False"
	if j := ev.Joined; j != nil {
		c, verb, failed := j.Failure()
		text := ""
		if failed {
			text = hpa.FailureText(c, verb)
			if c.Reason == hpa.ReasonAmbiguous && len(ev.Others) > 0 {
				text += "; also scaling this target: " + strings.Join(ev.Others, ", ")
			}
		}
		if ev.Current {
			if failed {
				return []Finding{{Role: health.Fault, Label: hpaLabel(verb), Text: text, cond: health.CondAt(c.Type, c.Status, c.At)}}
			}
			if mirrorFalse {
				return []Finding{{Role: health.OK, Note: true, Label: "KEDA's copy disagrees",
					Text: fmt.Sprintf("KEDA last reported HPAActive=False (%s); the HPA itself is healthy now", m.Reason)}}
			}
			return nil
		}
		asOf := ev.AsOf.UTC().Format(time.RFC3339)
		if failed {
			return []Finding{{Role: health.Fault, Label: hpaLabel(verb), Text: text + " (as of " + asOf + ")", cond: health.CondAt(c.Type, c.Status, c.At)}}
		}
		if mirrorFalse {
			return []Finding{{Role: health.Unknown, Label: "HPA health not established",
				Text: fmt.Sprintf("KEDA reports HPAActive=False (%s); Klyx's last HPA read (as of %s) was healthy", m.Reason, asOf)}}
		}
		return []Finding{{Role: health.Unknown, Label: "HPA health not current", Text: "last read healthy as of " + asOf}}
	}
	if intent.Kind == PauseFull {
		return nil // a full pause deletes the HPA: the pause findings judge it
	}
	if ev.Complete {
		if !so.CondIs(CondReady, "True") {
			return nil
		}
		ep := episode(in, EpAwaitingHPA)
		if in.Now.Before(ep.Deadline()) {
			return []Finding{{Role: health.Progressing, Label: "Awaiting HPA", next: health.At(ep.Deadline())}}
		}
		return []Finding{{Role: health.Fault, Label: "No HPA", boundary: health.At(ep.Deadline()),
			Text: "no HPA controlled by this ScaledObject (KEDA reported ready)" + since(ep)}}
	}
	if mirrorFalse {
		return []Finding{{Role: health.Fault, Label: "HPA not active",
			Text: fmt.Sprintf("the HPA cannot compute replicas: %s: %s (as KEDA last saw it)", m.Reason, m.Message)}}
	}
	return []Finding{{Role: health.Unknown, Label: "HPA join not established",
		Text: "the HPA read is incomplete, so Klyx cannot establish which HPA scales this target"}}
}

func hpaLabel(verb string) string {
	if verb == "compute replicas" {
		return "HPA cannot compute replicas"
	}
	return "HPA cannot scale"
}

func failingTriggers(so *ScaledObject) string {
	var names []string
	for k, e := range so.Health {
		if e.Status == "Failing" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "some triggers are failing"
	}
	return "some triggers are failing: " + strings.Join(names, ", ")
}

// fallbackFinding says fallback is active, how it is configured, and the
// failures KEDA counted - never a held replica count (§3.2).
func fallbackFinding(so *ScaledObject) Finding {
	behavior := "static"
	threshold := int32(0)
	if so.Fallback != nil {
		if so.Fallback.Behavior != "" {
			behavior = so.Fallback.Behavior
		}
		threshold = so.Fallback.FailureThreshold
	}
	keys := make([]string, 0, len(so.Health))
	for k := range so.Health {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if e := so.Health[k]; e.Status == "Failing" {
			parts = append(parts, fmt.Sprintf("%s failed %d times", k, e.Failures))
		}
	}
	text := "a trigger is failing"
	if len(parts) > 0 {
		text = strings.Join(parts, "; ")
	}
	return Finding{Role: health.Degraded, Label: "Fallback active (" + behavior + ")",
		Text: fmt.Sprintf("fallback active (%s): %s (threshold %d)", behavior, text, threshold)}
}

// pauseFindings: a full pause not yet completed, a resume not yet
// established, and the version notes (§3.2). Intent is context: it never
// clears another finding.
func pauseFindings(so *ScaledObject, intent Intent, in SOInputs) []Finding {
	var f []Finding
	if intent.NotApplied != "" {
		f = append(f, Finding{Role: health.Unknown, Label: "Pause not applied by this version",
			Text: fmt.Sprintf("pause requested; KEDA %s does not apply %s", in.Version, intent.NotApplied)})
	}
	if intent.Kind == PauseFull && !intent.Malformed {
		ep := episode(in, EpPausing)
		if in.Now.Before(ep.Deadline()) {
			f = append(f, Finding{Role: health.Progressing, Label: "Pause requested", next: health.At(ep.Deadline())})
		} else {
			f = append(f, Finding{Role: health.Unknown, Label: "Pause not completed", boundary: health.At(ep.Deadline()),
				Text: pauseNotCompleted(so, intent, in) + since(ep)})
		}
	} else if intent.Kind != PauseFull && intent.Unverified == AnnPaused {
		f = append(f, Finding{Role: health.OK, Note: true, Label: "Pause annotation",
			Text: "autoscaling.keda.sh/paused is present; the KEDA version is unknown, so whether it pauses is unverified"})
	}
	if so.CondIs(CondPaused, "True") && !so.Pause.Any() {
		ep := episode(in, EpResuming)
		if in.Now.Before(ep.Deadline()) {
			f = append(f, Finding{Role: health.Progressing, Label: "Resuming", next: health.At(ep.Deadline())})
		} else {
			f = append(f, Finding{Role: health.Unknown, Label: "Resume not established", boundary: health.At(ep.Deadline()),
				Text: "resume not established" + since(ep)})
		}
	}
	return f
}

func pauseNotCompleted(so *ScaledObject, intent Intent, in SOInputs) string {
	switch {
	case intent.Unverified != "" && !in.Version.Known:
		return "pause requested; whether this KEDA applies " + intent.Annotation + " is unverified (version unknown)"
	case in.HPA.Joined != nil && in.HPA.Current:
		return "pause requested; KEDA still has its HPA " + in.HPA.Joined.Name
	case !in.HPA.Complete:
		return "pause requested; completion not established (the HPA read is incomplete)"
	}
	return "pause requested; completion not established"
}

// healthy is the ok finding, in KEDA's vocabulary (§3.2).
func healthy(so *ScaledObject, intent Intent, in SOInputs) Finding {
	label := "Ready"
	if a := so.Cond(CondActive); a != nil {
		switch {
		case a.Status == "True":
			label = "Active"
			if j := in.HPA.Joined; j != nil {
				label = fmt.Sprintf("Active (%d replicas, wants %d)", j.Current, j.Desired)
			}
		case a.Reason == "ScalerCooldown":
			label = "Cooling down"
		case a.Status == "False":
			label = "Idle"
		}
	}
	if len(intent.Directions) > 0 {
		label += "; " + strings.Join(intent.Directions, " and ") + " paused"
	}
	if j := in.HPA.Joined; j != nil && in.HPA.Current {
		if c := j.Cond(hpa.CondScalingLimited); c != nil && c.Status == "True" && c.Reason == hpa.ReasonTooMany {
			label += "; wants more than maxReplicaCount"
		}
	}
	return Finding{Role: health.OK, Label: label}
}

func pausedLabel(so *ScaledObject, intent Intent, target *int32) string {
	if intent.Annotation != AnnPausedReplicas {
		return "Paused: frozen at the current replicas"
	}
	applied, requested := so.PausedCount, intent.Requested
	if applied != nil && requested != nil && *applied != *requested {
		return fmt.Sprintf("Paused at %d; %d requested", *applied, *requested)
	}
	n := requested
	if n == nil {
		n = applied
	}
	switch {
	case n == nil:
		return "Paused"
	case target == nil:
		return fmt.Sprintf("Paused at %d (the target's replicas were not read)", *n)
	case *target == *n:
		return fmt.Sprintf("Paused: held at %d replicas", *n)
	}
	return fmt.Sprintf("Paused at %d; the target is at %d", *n, *target)
}

func episode(in SOInputs, k EpisodeKind) Episode {
	if ep, ok := in.Episodes[k]; ok {
		return ep
	}
	return Episode{Start: in.Now} // not tracked yet: a lower bound from now
}

func since(ep Episode) string {
	if ep.Observed {
		return " (since " + ep.Start.UTC().Format(time.RFC3339) + ")"
	}
	return " (at least since " + ep.Start.UTC().Format(time.RFC3339) + ")"
}

func countText(n *int32) string {
	if n == nil {
		return "the requested count"
	}
	return fmt.Sprint(*n)
}

// evalOf reduces findings to the verdict: the worst in the engine's triage
// order, ties by order (§3.2). Next is the earliest pending deadline.
func evalOf(f []Finding) health.NativeEval {
	if len(f) == 0 {
		return health.NativeEval{Role: health.Unknown, Label: "Not evaluated"}
	}
	w := -1
	for i := range f {
		if f[i].Note {
			continue
		}
		if w < 0 || f[i].Role < f[w].Role {
			w = i
		}
	}
	if w < 0 {
		w = 0
	}
	ev := health.NativeEval{Role: f[w].Role, Label: f[w].Label, Included: f[w].Role.Attention(),
		Boundary: f[w].boundary, Cond: f[w].cond}
	if ev.Role != health.OK {
		text := f[w].Text
		if text == "" {
			text = f[w].Label
		}
		ev.Reason = health.CapReason(f[w].Label, text)
	}
	for i := range f {
		if n := f[i].next; n != nil && (ev.Next == nil || n.Before(*ev.Next)) {
			ev.Next = n
		}
	}
	return ev
}

// JobInputs is what the ScaledJob rule reads beside the object.
type JobInputs struct {
	Now          time.Time
	Version      Version
	Episodes     map[EpisodeKind]Episode
	MissingAuths []string
}

// ScaledJobRule judges a ScaledJob (S9 §5): §3.2's structure without an
// HPA. Fallback is never written for ScaledJobs and is ignored.
func ScaledJobRule(j *ScaledJob, in JobInputs) Verdict {
	intent := ScaledJobIntent(j.Pause, in.Version)
	v := Verdict{Intent: intent}
	if len(j.Conds) == 0 {
		v.Findings = []Finding{noConditions(j.Created, in.Now, "ScaledJob")}
		v.Eval = evalOf(v.Findings)
		return v
	}
	if JobPauseCompleted(j, intent) {
		v.Completed = true
		v.Findings = []Finding{{Role: health.Paused, Label: "Paused: no new Jobs; running Jobs continue"}}
		v.Eval = evalOf(v.Findings)
		return v
	}
	// a ScaledJob has no HPA: absence is trivially complete, so the pause
	// reasons never cite an HPA read
	soIn := SOInputs{Now: in.Now, Version: in.Version, Episodes: in.Episodes, MissingAuths: in.MissingAuths, HPA: HPAEvidence{Current: true, Complete: true}}
	var f []Finding
	if r := j.Cond(CondReady); r != nil {
		switch {
		case r.Status == "False":
			f = append(f, readyFalse(&ScaledObject{Scaler: j.Scaler}, *r, intent, soIn))
		case r.Status == "Unknown" && r.Reason == "PartialTriggerError":
			f = append(f, Finding{Role: health.Degraded, Label: "Some triggers failing", Text: "some triggers are failing"})
		case r.Status == "Unknown":
			f = append(f, Finding{Role: health.Progressing, Label: "Waiting for the first poll"})
		}
	}
	f = append(f, pauseFindings(&ScaledObject{Scaler: j.Scaler}, intent, soIn)...)
	label := "Ready"
	if a := j.Cond(CondActive); a != nil {
		if a.Status == "True" {
			label = "Active"
		} else if a.Status == "False" {
			label = "Idle"
		}
	}
	f = append(f, Finding{Role: health.OK, Label: label})
	v.Findings = f
	v.Eval = evalOf(f)
	return v
}
