package gwapi

import (
	"fmt"

	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/moomora/klyx/internal/health"
)

// The Gateway API lens rules, relocated verbatim from frontend
// cluster/gatewayModel.ts (1c addendum §2): condition evidence (condState),
// Gateway roles (gatewayHealth) and route-attachment roles (routeIssues /
// routeHealth). The lens's issue texts stay presentation; the verdict -
// role and short label - is computed here once.

// condEvidence is one condition as Klyx may claim it: "absent" = no
// controller has written it; "Unknown" = still deciding; "False" = an
// explicit negative. A condition observed for an older generation is
// last-observed: stale.
type condEvidence struct {
	status string // True | False | Unknown | absent
	stale  bool
}

func condState(c Cond, generation int64) condEvidence {
	status := "absent"
	switch c.Status {
	case "True", "False", "Unknown":
		status = c.Status
	}
	return condEvidence{status: status, stale: status != "absent" && generation > 0 && c.ObservedGeneration < generation}
}

// GatewayRule: an explicit, current Accepted=False is the fault; absent
// status is unverified; a pending decision is progressing; an explicit
// not-programmed is degraded; a last-observed negative is degraded "(last
// observed)". Included = fault or degraded (GatewayApiView's attention count).
func GatewayRule(accepted, programmed Cond, generation int64) health.NativeEval {
	role, label, by, current := gatewayRole(accepted, programmed, generation)
	e := health.NativeEval{Included: role == health.Fault || role == health.Degraded, Role: role, Label: label}
	gatewayDated(&e, by, current, accepted, programmed, generation)
	return e
}

// gatewayBasis is the condition a Gateway role was read from.
type gatewayBasis int

const (
	byNothing    gatewayBasis = iota // absent, pending or syncing: no condition dates the role
	byAccepted                       // Accepted decided it
	byProgrammed                     // Programmed decided it, once Accepted was True
)

// gatewayRole returns the role, its label, the condition it was read from,
// and whether that condition is current (observed for this generation).
func gatewayRole(accepted, programmed Cond, generation int64) (health.Kind, string, gatewayBasis, bool) {
	a := condState(accepted, generation)
	switch a.status {
	case "absent":
		return health.Unknown, "no status reported", byNothing, false
	case "Unknown":
		return health.Progressing, "acceptance pending", byNothing, false
	case "False":
		if a.stale {
			return health.Degraded, "not accepted (last observed)", byAccepted, false
		}
		return health.Fault, "not accepted", byAccepted, true
	}
	p := condState(programmed, generation)
	switch p.status {
	case "absent":
		return health.Unknown, "programming not reported", byNothing, false
	case "Unknown":
		return health.Progressing, "programming", byNothing, false
	case "False":
		if p.stale {
			return health.Degraded, "not programmed (last observed)", byProgrammed, false
		}
		return health.Degraded, "not programmed", byProgrammed, true
	}
	if a.stale || p.stale {
		return health.Progressing, "syncing", byNothing, false
	}
	return health.OK, "programmed", byProgrammed, true
}

// gatewayDated fills Since, Cond and Reason (The Line §8.1). The deciding
// condition (Accepted, or Programmed once Accepted is True) carries its own
// time as Cond, current or last observed. Since is set only when the role's
// start is provable: generation 1, where the spec never changed, so no
// last-observed overlay can have followed a transition and ended without
// changing its time (Gateway API controllers stamp observedGeneration on
// every condition they write, and at generation 1 that is 1). The role is
// then a function of the two conditions' statuses, so it began at the
// deciding condition's time - for Programmed, the later of Accepted's and
// Programmed's (both must be known, or the earlier one would overstate the
// age). On an edited Gateway the role is undated and Cond keeps the age.
func gatewayDated(e *health.NativeEval, by gatewayBasis, current bool, accepted, programmed Cond, generation int64) {
	var c Cond
	switch by {
	case byAccepted:
		c = accepted
	case byProgrammed:
		c = programmed
	default:
		return
	}
	if e.Role != health.OK {
		e.Reason = health.CapReason(c.Reason, c.Message)
	}
	cond := "Accepted"
	if by == byProgrammed {
		cond = "Programmed"
	}
	e.Cond = health.CondAt(cond, c.Status, c.LastTransitionTime)
	since := c.LastTransitionTime
	if !current || generation != 1 || since.IsZero() {
		return
	}
	if by == byProgrammed {
		if accepted.LastTransitionTime.IsZero() {
			return
		}
		if accepted.LastTransitionTime.After(since) {
			since = accepted.LastTransitionTime
		}
	}
	e.Since = &since
}

// lensRank is gatewayModel.RANK: progressing ranks BEFORE unknown here (the
// lens's own order), unlike the health verdict order.
var lensRank = map[health.Kind]int{health.Fault: 0, health.Degraded: 1, health.Progressing: 2, health.Unknown: 3, health.OK: 4}

type routeIssue struct {
	role  health.Kind
	short string
	cond  *Cond // the condition that raised it, nil for observed issues
}

// RouteRule is one route attachment's verdict (route x parent Gateway):
// routeIssues, then the worst issue by the lens's rank (first wins on ties),
// "resolved" when none. listeners are the parent Gateway's. Included = fault
// or degraded - the Gateway list's predicate, applied to the routes part
// (the lens has no route predicate of its own yet).
func RouteRule(rn RouteNode, listeners []Listener) health.NativeEval {
	var issues []routeIssue
	add := func(role health.Kind, short string) { issues = append(issues, routeIssue{role: role, short: short}) }
	addCond := func(role health.Kind, short string, c Cond) {
		issues = append(issues, routeIssue{role: role, short: short, cond: &c})
	}
	if !rn.ParentFound {
		add(health.Unknown, "no status for this Gateway")
	} else {
		a := condState(rn.AcceptedCond, rn.Generation)
		switch a.status {
		case "False":
			if a.stale {
				addCond(health.Degraded, "not accepted (last observed)", rn.AcceptedCond)
			} else {
				addCond(health.Fault, "not accepted", rn.AcceptedCond)
			}
		case "Unknown":
			add(health.Progressing, "acceptance pending")
		case "absent":
			add(health.Unknown, "acceptance not reported")
		}
		rr := condState(rn.ResolvedRefsCond, rn.Generation)
		switch rr.status {
		case "False":
			if rr.stale {
				addCond(health.Degraded, "unresolved refs (last observed)", rn.ResolvedRefsCond)
			} else {
				addCond(health.Fault, "unresolved refs", rn.ResolvedRefsCond)
			}
		case "Unknown":
			add(health.Progressing, "refs pending")
		case "absent":
			add(health.Unknown, "refs not reported")
		}
		if (a.status == "True" && a.stale) || (rr.status == "True" && rr.stale) {
			add(health.Progressing, "status syncing")
		}
	}
	names := map[string]bool{}
	for _, l := range listeners {
		if l.Name != "" {
			names[l.Name] = true
		}
	}
	if rn.ParentSectionName != "" && len(names) > 0 && !names[rn.ParentSectionName] {
		add(health.Fault, "listener missing")
	}

	// Every backend that carries traffic is diagnosed, not only the lane's
	// primary (Codex review 2026-10-13: an equal-weight second backend with
	// no endpoints read as "resolved"). Weights split traffic within one
	// rule's backendRefs (Gateway API BackendRef.Weight), so each rule is
	// judged on its own: a rule whose backends all have weight 0 serves
	// nothing and is a fault; within a rule a weight-0 backend is left out,
	// and a backend fault is degraded - the rule's other backends still take
	// its traffic - unless every one of them faults.
	switch {
	case len(rn.Backends) == 0:
		add(health.Unknown, "no backendRefs")
	default:
		var order []int
		listed := map[int]bool{}
		total := 0
		for _, b := range rn.Backends {
			if !listed[b.Rule] {
				listed[b.Rule] = true
				order = append(order, b.Rule)
			}
			if b.CarriesTraffic() {
				total++
			}
		}
		named := total > 1
		for _, ri := range order {
			allZero, js := judgeRule(rulesBackends(rn.Backends, ri))
			if allZero {
				if len(order) > 1 {
					add(health.Fault, fmt.Sprintf("rule %d: every backend has weight 0", ri+1))
				} else {
					add(health.Fault, "every backend has weight 0")
				}
				continue
			}
			for _, j := range js {
				if !j.carries || j.role == health.OK {
					continue
				}
				short := j.short
				if named {
					short = j.b.Name + ": " + short
				}
				issues = append(issues, routeIssue{role: j.role, short: short})
			}
		}
	}
	if len(issues) == 0 {
		return health.NativeEval{Role: health.OK, Label: "resolved"}
	}
	worst := issues[0]
	for _, is := range issues[1:] {
		if lensRank[is.role] < lensRank[worst.role] {
			worst = is
		}
	}
	e := health.NativeEval{Included: worst.role == health.Fault || worst.role == health.Degraded, Role: worst.role, Label: worst.short}
	// A route keeps Since nil: its role is the worst of several issues, some
	// observed only (endpoints, weights), so no one condition's time dates it
	// (The Line §8.1). A condition-raised worst issue still names its reason.
	if worst.cond != nil {
		e.Reason = health.CapReason(worst.cond.Reason, worst.cond.Message)
	}
	return e
}

func rulesBackends(all []Backend, rule int) []Backend {
	var out []Backend
	for _, b := range all {
		if b.Rule == rule {
			out = append(out, b)
		}
	}
	return out
}

type backendJudgment struct {
	b       Backend
	carries bool
	own     health.Kind // the backend's own verdict
	role    health.Kind // its weight in the rule's verdict: a fault softened while a sibling carries
	short   string      // "" when OK
}

// judgeRule judges one rule's backends. Weights split traffic within one
// rule (Gateway API BackendRef.Weight): a rule whose backends all have
// weight 0 serves nothing (allZero); a weight-0 backend is left out; and a
// backend fault is degraded while another carrying backend is not faulty.
// RouteRule and RuleVerdicts share it, so a route's verdict and its rules'
// never disagree.
func judgeRule(bs []Backend) (allZero bool, js []backendJudgment) {
	var carrying []int
	for i, b := range bs {
		js = append(js, backendJudgment{b: b, carries: b.CarriesTraffic()})
		if js[i].carries {
			carrying = append(carrying, i)
		}
	}
	if len(carrying) == 0 {
		return true, js
	}
	faults := 0
	for _, i := range carrying {
		js[i].role, js[i].short = BackendIssue(bs[i])
		js[i].own = js[i].role
		if js[i].role == health.Fault {
			faults++
		}
	}
	for _, i := range carrying {
		if len(carrying) > 1 && js[i].role == health.Fault && faults < len(carrying) {
			js[i].role = health.Degraded
		}
	}
	return false, js
}

// RuleVerdict is one spec.rules entry's verdict (The Line slice 8d, parent
// §5.2): its backends judged together, each backend with its own verdict, so
// the data path draws a failing backend's branch in danger.
type RuleVerdict struct {
	Index    int
	Eval     health.NativeEval
	Backends []BackendVerdict
}

// BackendVerdict is one backendRef of a rule, with its configured weight
// (unset counts as 1) and its own verdict.
type BackendVerdict struct {
	Backend
	Eval health.NativeEval
}

// RuleVerdicts judges every rule of the route, in spec order. A rule with no
// backendRefs (a direct response or a redirect answers it) is unknown here:
// Klyx does not evaluate filters.
func RuleVerdicts(rn RouteNode) []RuleVerdict {
	n := rn.RuleCount
	for _, b := range rn.Backends {
		if b.Rule+1 > n {
			n = b.Rule + 1
		}
	}
	out := make([]RuleVerdict, 0, n)
	for i := 0; i < n; i++ {
		rv := RuleVerdict{Index: i}
		bs := rulesBackends(rn.Backends, i)
		if len(bs) == 0 {
			rv.Eval = health.NativeEval{Role: health.Unknown, Label: "no backendRefs"}
			out = append(out, rv)
			continue
		}
		allZero, js := judgeRule(bs)
		carrying := 0
		for _, j := range js {
			if j.carries {
				carrying++
			}
		}
		var worst *backendJudgment
		for k := range js {
			j := js[k]
			bv := BackendVerdict{Backend: j.b}
			switch {
			case !j.carries:
				bv.Eval = health.NativeEval{Role: health.OK, Label: "no traffic (weight 0)"}
			case j.role == health.OK:
				bv.Eval = health.NativeEval{Role: health.OK, Label: "resolved"}
			default:
				// the backend keeps its own verdict (a Service with no ready
				// endpoints is a fault); the rule takes the softened one
				bv.Eval = health.NativeEval{Role: j.own, Label: j.short, Included: j.own == health.Fault || j.own == health.Degraded}
				if worst == nil || lensRank[j.role] < lensRank[worst.role] {
					worst = &js[k]
				}
			}
			rv.Backends = append(rv.Backends, bv)
		}
		switch {
		case allZero:
			rv.Eval = health.NativeEval{Role: health.Fault, Label: "every backend has weight 0", Included: true}
		case worst == nil:
			rv.Eval = health.NativeEval{Role: health.OK, Label: "resolved"}
		default:
			label := worst.short
			if carrying > 1 {
				label = worst.b.Name + ": " + label
			}
			rv.Eval = health.NativeEval{Role: worst.role, Label: label, Included: worst.role == health.Fault || worst.role == health.Degraded}
		}
		out = append(out, rv)
	}
	return out
}

// BackendIssue is one backend's own verdict: OK when Klyx found nothing
// wrong, otherwise the role and short label as if it were the only backend.
func BackendIssue(b Backend) (health.Kind, string) {
	switch b.State {
	case BackendUnsupported:
		// (The lens's undefined-state branch only serves pre-state
		// payloads; the bridge always sends state, "" included.)
		return health.Unknown, b.Kind + " backend not evaluated"
	case BackendNotFound:
		return health.Fault, "backend not found"
	case BackendUnreadable:
		return health.Unknown, "backend unreadable"
	case BackendResolved:
		ep := b.Endpoints
		switch {
		case ep == nil || ep.Error != "":
			return health.Unknown, "endpoints unreadable"
		case ep.Total == 0:
			return health.Fault, "no endpoints"
		case ep.Ready == 0:
			return health.Fault, "no ready endpoints"
		case ep.Ready < ep.Total:
			return health.Degraded, fmt.Sprintf("%d/%d endpoints ready", ep.Ready, ep.Total)
		}
		return health.OK, ""
	default:
		return health.Unknown, "backend not evaluated"
	}
}

// CountEndpoints counts a Service's EndpointSlice endpoints, deduplicated by
// TargetRef across slices (a dual-stack Service lists the same pods in its
// IPv4 and IPv6 slices). Endpoints without a pod TargetRef count
// individually and as NonPod.
func CountEndpoints(slices []discoveryv1.EndpointSlice) EndpointCount {
	out := EndpointCount{}
	seen := make(map[string]bool)
	for i := range slices {
		for j, e := range slices[i].Endpoints {
			key := fmt.Sprintf("slice%d/ep%d", i, j) // no TargetRef: treat as distinct
			if e.TargetRef != nil {
				key = string(e.TargetRef.UID)
				if key == "" {
					key = e.TargetRef.Namespace + "/" + e.TargetRef.Name
				}
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Total++
			if e.TargetRef == nil || e.TargetRef.Kind != "Pod" {
				out.NonPod++
			}
			// The EndpointSlice API: a nil Ready is an unknown state that
			// consumers should treat as ready (Codex review 2026-10-13).
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				out.Ready++
			}
		}
	}
	return out
}
