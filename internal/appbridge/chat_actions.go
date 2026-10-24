package appbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/moomora/klyx/internal/fleet"
)

// AI A3 (docs/superpowers/specs/2026-10-24-klyx-ai-a3-guarded-execution.md):
// an investigation can end with an action proposal - at most four existing
// day-2 verbs, each on an object the investigation read by UID. Klyx decides
// whether each step is eligible to run, never whether it is the right
// remedy; a step runs only when the operator confirms it (stage 3). The
// model names no cluster and no UID: the UID is Klyx's, from its own reads.

const (
	toolActions         = "propose_actions"
	actionsArgsMax      = 8 << 10
	actionStepsMax      = 4
	actionWhyMax        = 300
	actionCitesMax      = 10
	actionsAttemptsTurn = 2 // one invalid call may be retried once
	stepOffered         = "offered"
	stepRefused         = "refused"
	stepUnsupported     = "unsupported"
	stepNotOffered      = "not offered"
	stepWithdrawn       = "withdrawn"
)

// matrixVerb is one verb A3 offers (§1): the kinds it acts on, by kind and
// API group, its parameter, the verb that undoes it on the same target,
// and whether it carries the danger treatment.
type matrixVerb struct {
	kinds    map[string]string
	param    string // "", "replicas", "revision"
	opposite string
	danger   bool
}

var (
	fluxUnits    = map[string]string{"Kustomization": "kustomize.toolkit.fluxcd.io", "HelmRelease": "helm.toolkit.fluxcd.io"}
	workloadsAll = map[string]string{"Deployment": "apps", "StatefulSet": "apps", "DaemonSet": "apps"}
	kedaScalers  = map[string]string{"ScaledObject": "keda.sh", "ScaledJob": "keda.sh"}
)

// actionMatrix is A3's verb matrix (§1). Anything else is never offered.
var actionMatrix = map[string]matrixVerb{
	"flux-suspend":    {kinds: fluxUnits, opposite: "flux-resume", danger: true},
	"flux-resume":     {kinds: fluxUnits, opposite: "flux-suspend"},
	"flux-reconcile":  {kinds: fluxUnits},
	"rollout-restart": {kinds: workloadsAll, danger: true},
	"scale":           {kinds: map[string]string{"Deployment": "apps", "StatefulSet": "apps"}, param: "replicas"},
	"rollout-undo":    {kinds: workloadsAll, param: "revision", danger: true},
	"rollout-pause":   {kinds: map[string]string{"Deployment": "apps"}, opposite: "rollout-resume"},
	"rollout-resume":  {kinds: map[string]string{"Deployment": "apps"}, opposite: "rollout-pause"},
	"delete-pod":      {kinds: map[string]string{"Pod": ""}, danger: true},
	"cordon":          {kinds: map[string]string{"Node": ""}, opposite: "uncordon", danger: true},
	"uncordon":        {kinds: map[string]string{"Node": ""}, opposite: "cordon"},
	"keda-pause":      {kinds: kedaScalers, opposite: "keda-resume", danger: true},
	"keda-resume":     {kinds: kedaScalers, opposite: "keda-pause"},
}

func matrixVerbs() []string {
	var out []string
	for v := range actionMatrix {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func proposeActionsToolDef() ChatToolDef {
	target := obj(map[string]any{"kind": strProp("Kind."), "namespace": strProp("Namespace, when namespaced."), "name": strProp("Name.")}, "kind", "name")
	step := obj(map[string]any{
		"verb":   map[string]any{"type": "string", "enum": matrixVerbs(), "description": "One of Klyx's existing day-2 verbs."},
		"target": target,
		"params": obj(map[string]any{
			"replicas": intProp("scale only: the replica count.", 0, maxScaleReplicas),
			"revision": map[string]any{"type": "integer", "minimum": 1, "description": "rollout-undo only: the partner revision rollout_history established."},
		}),
		"cites": map[string]any{"type": "array", "minItems": 1, "maxItems": actionCitesMax, "items": map[string]any{"type": "string"}, "description": "Read and check IDs the step rests on, at least one about its target."},
		"why":   map[string]any{"type": "string", "maxLength": actionWhyMax, "description": "Why this step, plainly."},
	}, "verb", "target", "cites", "why")
	return ChatToolDef{toolActions, "Propose a mitigation as an ordered plan of at most 4 of Klyx's existing day-2 verbs, each on an object you read by UID in this investigation. Klyx checks that each step is eligible; the operator reviews and confirms each one; you never run anything.", obj(map[string]any{
		"steps": map[string]any{"type": "array", "minItems": 1, "maxItems": actionStepsMax, "items": step},
	}, "steps")}
}

// actionsInput is propose_actions' arguments, or a snapshot's actions block.
type actionsInput struct {
	Steps []actionStepInput `json:"steps"`
}

type actionStepInput struct {
	Verb   string `json:"verb"`
	Target struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"target"`
	Params *struct {
		Replicas *int   `json:"replicas"`
		Revision *int64 `json:"revision"`
	} `json:"params"`
	Cites []string `json:"cites"`
	Why   string   `json:"why"`
}

var citeIDRe = regexp.MustCompile(`^r\d{1,5}(\.c\d{1,3})?$`)

// parseActions is the proposal's shape, strictly; a step's eligibility is
// validatePlan's.
func parseActions(raw json.RawMessage) (actionsInput, error) {
	var in actionsInput
	if err := strictArgsWithin(raw, &in, actionsArgsMax); err != nil {
		return in, err
	}
	if len(in.Steps) == 0 || len(in.Steps) > actionStepsMax {
		return in, fmt.Errorf("a plan has 1 to %d steps", actionStepsMax)
	}
	for i, s := range in.Steps {
		switch {
		case s.Verb == "":
			return in, fmt.Errorf("step %d: a verb", i+1)
		case len(s.Cites) == 0 || len(s.Cites) > actionCitesMax:
			return in, fmt.Errorf("step %d: 1 to %d cites", i+1, actionCitesMax)
		case strings.TrimSpace(s.Why) == "" || utf8.RuneCountInString(s.Why) > actionWhyMax:
			return in, fmt.Errorf("step %d: a why of 1 to %d characters", i+1, actionWhyMax)
		}
		for _, c := range s.Cites {
			if !citeIDRe.MatchString(c) {
				return in, fmt.Errorf("step %d: %q is not a read or check ID", i+1, label(c))
			}
		}
		if err := firstErr(checkKind(s.Target.Kind), checkName("namespace", s.Target.Namespace, false), checkName("name", s.Target.Name, true)); err != nil {
			return in, fmt.Errorf("step %d: target: %w", i+1, err)
		}
	}
	return in, nil
}

// ActionStepDTO is one step as Klyx decided it. Target carries the UID
// Klyx holds; State is offered, refused, unsupported or not offered.
type ActionStepDTO struct {
	N        int              `json:"n"`
	Verb     string           `json:"verb"`
	Target   ChatCheckSubject `json:"target"`
	Replicas *int             `json:"replicas,omitempty"`
	Revision *int64           `json:"revision,omitempty"`
	Why      string           `json:"why"`
	Cites    []string         `json:"cites"`
	State    string           `json:"state"`
	Reason   string           `json:"reason,omitempty"`
	Danger   bool             `json:"danger,omitempty"`
	// Run is the step's run, once it ran (AI A3 §4, §5)
	Run *StepRunStateDTO `json:"run,omitempty"`
	// FactsRead and FactsAt are the read the step's eligibility rests on
	FactsRead string `json:"factsRead,omitempty"`
	FactsAt   string `json:"factsAt,omitempty"`
}

// ActionPlanDTO is a turn's proposal, as Klyx decided it.
type ActionPlanDTO struct {
	Steps []ActionStepDTO `json:"steps"`
	Note  string          `json:"note,omitempty"`
	// Status is "" for the current plan, or superseded, withdrawn,
	// declined or held, with StatusWhy (AI A3 §2.4, §4.6)
	Status    string `json:"status,omitempty"`
	StatusWhy string `json:"statusWhy,omitempty"`
}

// actionPlan is a proposal with what Klyx pinned for each offered step:
// its target's facts and, for an undo, the partner revision it writes.
type actionPlan struct {
	dto     ActionPlanDTO
	facts   []actionFact
	partner []*fleet.RevisionExpect
	// runs are the steps' reviews and runs; running is the step whose
	// write or verification is in progress (0: none); declined, dismissed
	runs     []*stepRun
	running  int
	declined bool
}

// validatePlan is Klyx's decision on every step (§2.3), in order. A step
// refused or unsupported holds every later one: each needs the ones before.
func validatePlan(in actionsInput, facts *actionFacts, ledger []ChatRead, a anchorFacts) actionPlan {
	reads := map[string]ChatRead{}
	checks := map[string]ChatCheck{}
	for _, r := range ledger {
		if r.ID == "" {
			continue
		}
		reads[r.ID] = r
		for _, c := range r.Checks {
			checks[c.ID] = c
		}
	}
	var plan actionPlan
	n := 0
	held := 0 // the first step refused or unsupported
	seen := map[string]bool{}
	verbsOn := map[string]map[string]bool{} // target UID -> earlier verbs
	suspendedBy := map[string]int{}         // unit UID -> the step suspending it
	for i, s := range in.Steps {
		st := ActionStepDTO{N: i + 1, Verb: s.Verb, Why: chatScrubWithin(s.Why, actionWhyMax, &n), Cites: append([]string{}, s.Cites...),
			Target: ChatCheckSubject{Kind: s.Target.Kind, Namespace: s.Target.Namespace, Name: s.Target.Name}}
		if s.Params != nil {
			st.Replicas, st.Revision = s.Params.Replicas, s.Params.Revision
		}
		var fact actionFact
		var partner *fleet.RevisionExpect
		state, reason := stepOffered, ""
		if held > 0 {
			state, reason = stepNotOffered, fmt.Sprintf("step %d was refused", held)
		} else {
			fact, partner, reason = eligible(s, st, facts)
			// the plan's own rules come first: a pair that undoes itself is
			// a contradiction, whatever the facts say of its second step
			key := s.Verb + "|" + fact.UID
			opp := actionMatrix[s.Verb].opposite
			switch {
			case fact.UID != "" && seen[key]:
				state, reason = stepRefused, "the same step is already in this plan"
			case fact.UID != "" && opp != "" && verbsOn[fact.UID][opp]:
				state, reason = stepRefused, fmt.Sprintf("it contradicts an earlier step's %s on the same object", opp)
			case reason != "":
				state = stepRefused
			case s.Verb == "flux-reconcile" && suspendedBy[fact.UID] > 0:
				state, reason = stepRefused, fmt.Sprintf("step %d suspends it: Flux skips a reconcile request while suspended, so its handshake never completes", suspendedBy[fact.UID])
			default:
				st.Target.UID, st.Target.Identity = fact.UID, identityUID
				st.Danger = actionMatrix[s.Verb].danger || (s.Verb == "scale" && fact.Object.Replicas != nil && int64(*st.Replicas) < *fact.Object.Replicas)
				st.FactsRead, st.FactsAt = fact.Object.ReadID, fact.Object.ReadAt
				if r := citationProblem(s.Cites, reads, checks, fact.UID, a.subject.UID); r != "" {
					state, reason = stepUnsupported, r
				}
			}
		}
		st.State, st.Reason = state, reason
		if state != stepNotOffered && state != stepOffered && held == 0 {
			held = st.N
		}
		if state == stepOffered {
			seen[s.Verb+"|"+fact.UID] = true
			if verbsOn[fact.UID] == nil {
				verbsOn[fact.UID] = map[string]bool{}
			}
			verbsOn[fact.UID][s.Verb] = true
			if s.Verb == "flux-suspend" {
				suspendedBy[fact.UID] = st.N
			}
		} else {
			fact, partner = actionFact{}, nil
		}
		plan.dto.Steps = append(plan.dto.Steps, st)
		plan.facts = append(plan.facts, fact)
		plan.partner = append(plan.partner, partner)
		plan.runs = append(plan.runs, &stepRun{})
	}
	return plan
}

// eligible is a step's matrix, identity and parameter rules (§1, §2.3).
// reason is empty when the step may be offered.
func eligible(s actionStepInput, st ActionStepDTO, facts *actionFacts) (actionFact, *fleet.RevisionExpect, string) {
	mv, ok := actionMatrix[s.Verb]
	if !ok {
		return actionFact{}, nil, fmt.Sprintf("%s is not a verb Klyx offers an investigation (%s)", label(s.Verb), strings.Join(matrixVerbs(), ", "))
	}
	group, ok := mv.kinds[s.Target.Kind]
	if !ok {
		return actionFact{}, nil, fmt.Sprintf("%s does not act on a %s", s.Verb, label(s.Target.Kind))
	}
	switch {
	case mv.param == "replicas" && (st.Replicas == nil || st.Revision != nil):
		return actionFact{}, nil, "scale takes replicas, and only replicas"
	case mv.param == "revision" && (st.Revision == nil || st.Replicas != nil):
		return actionFact{}, nil, "rollout-undo takes revision, and only revision"
	case mv.param == "" && (st.Replicas != nil || st.Revision != nil):
		return actionFact{}, nil, s.Verb + " takes no parameters"
	case st.Replicas != nil && (*st.Replicas < 0 || *st.Replicas > maxScaleReplicas):
		return actionFact{}, nil, fmt.Sprintf("replicas must be 0 to %d", maxScaleReplicas)
	}
	who := fmt.Sprintf("%s %s", s.Target.Kind, strings.TrimPrefix(s.Target.Namespace+"/"+s.Target.Name, "/"))
	held := facts.named(s.Target.Kind, s.Target.Namespace, s.Target.Name)
	switch {
	case len(held) == 0:
		return actionFact{}, nil, fmt.Sprintf("Klyx has not read %s by UID in this investigation, or no longer holds what it read; read it first", who)
	case len(held) > 1:
		return actionFact{}, nil, fmt.Sprintf("%s has been more than one object in this investigation (its UID changed); read it again", who)
	}
	f := held[0]
	if f.Group != group {
		return actionFact{}, nil, fmt.Sprintf("%s was read from API group %q, not %q", who, f.Group, group)
	}
	if f.Object == nil {
		return actionFact{}, nil, fmt.Sprintf("Klyx holds no read of %s itself; read it first", who)
	}
	o := f.Object
	switch s.Verb {
	case "flux-suspend":
		if o.Suspended {
			return f, nil, "no change: it is already suspended"
		}
	case "flux-resume":
		if !o.Suspended {
			return f, nil, "no change: it is not suspended"
		}
	case "flux-reconcile":
		if o.Suspended {
			return f, nil, "it is suspended: Flux skips a reconcile request while suspended, so its handshake never completes"
		}
	case "rollout-restart":
		switch {
		case f.Kind == "Deployment" && o.Paused:
			return f, nil, "it is paused: a restart would not roll out"
		case o.Strategy == "OnDelete":
			return f, nil, "its update strategy is OnDelete: pods are replaced only when deleted"
		case f.Kind == "StatefulSet" && o.Partition > 0:
			return f, nil, fmt.Sprintf("its rolling update has partition %d: not every pod would be replaced", o.Partition)
		case o.Desired == nil:
			return f, nil, "Klyx did not read how many pods it wants"
		case *o.Desired == 0:
			return f, nil, "it wants no pods: there is nothing to replace"
		}
	case "scale":
		if o.Replicas != nil && *o.Replicas == int64(*st.Replicas) {
			return f, nil, fmt.Sprintf("no change: it is already at %d replicas", *st.Replicas)
		}
	case "rollout-undo":
		h := f.History
		switch {
		case f.Kind == "Deployment" && o.Paused:
			return f, nil, "it is paused: the reverted template would not roll out"
		case h == nil:
			return f, nil, "rollout_history has not been read for it in this investigation"
		case h.PartnerRevision == 0 || h.PartnerObjectUID == "" || h.PartnerDigest == "":
			return f, nil, "rollout_history established no partner revision for it"
		case *st.Revision != h.PartnerRevision:
			return f, nil, fmt.Sprintf("only the partner revision rollout_history established, %d, can be the target", h.PartnerRevision)
		}
		return f, &fleet.RevisionExpect{ObjectUID: h.PartnerObjectUID, TemplateDigest: h.PartnerDigest}, ""
	case "rollout-pause":
		if o.Paused {
			return f, nil, "no change: it is already paused"
		}
	case "rollout-resume":
		if !o.Paused {
			return f, nil, "no change: it is not paused"
		}
	case "cordon":
		if o.Unschedulable {
			return f, nil, "no change: it is already cordoned"
		}
	case "uncordon":
		if !o.Unschedulable {
			return f, nil, "no change: it is not cordoned"
		}
	case "keda-pause":
		// only a true value pauses on every version; a present "false" is a
		// real change on 2.13 and later, which the verb's version gate checks
		if o.kedaPausedTrue() {
			return f, nil, "no change: its pause annotation is already true"
		}
	case "keda-resume":
		switch {
		case !o.KEDAPauseSet:
			return f, nil, "no change: it carries no pause annotation"
		case o.KEDAOtherPause != "":
			return f, nil, fmt.Sprintf("it is paused by %s, set outside Klyx: Klyx resumes only its own pause", o.KEDAOtherPause)
		}
	}
	return f, nil, ""
}

// citationProblem is A-C1 for a step: every citation is in the ledger, and
// one is about the target, or the investigated object, by UID.
func citationProblem(cites []string, reads map[string]ChatRead, checks map[string]ChatCheck, target, anchor string) string {
	about := false
	for _, id := range cites {
		if c, ok := checks[id]; ok {
			about = about || (c.Subject.UID != "" && (c.Subject.UID == target || c.Subject.UID == anchor))
			continue
		}
		r, ok := reads[id]
		if !ok {
			return fmt.Sprintf("it cites %s, which is not in this investigation's ledger", id)
		}
		for _, ref := range r.Refs {
			about = about || (ref.UID != "" && (ref.UID == target || ref.UID == anchor))
		}
		for _, c := range r.Checks {
			about = about || (c.Subject.UID != "" && (c.Subject.UID == target || c.Subject.UID == anchor))
		}
	}
	if !about {
		return "none of its citations is about its target or the investigated object by UID"
	}
	return ""
}

// actionsResult is what the model reads back: each step's state and
// reason, and that nothing runs.
func actionsResult(dto ActionPlanDTO) string {
	type st struct {
		Step   int    `json:"step"`
		State  string `json:"state"`
		Reason string `json:"reason,omitempty"`
	}
	var steps []st
	for _, s := range dto.Steps {
		steps = append(steps, st{s.N, s.State, s.Reason})
	}
	b, _ := json.Marshal(map[string]any{"steps": steps,
		"note": "Nothing runs: the operator reviews and confirms each offered step, in order. You never run anything. Answer the operator briefly now."})
	return string(b)
}

var errActionsOff = errors.New("action proposals are off in Settings > AI")
