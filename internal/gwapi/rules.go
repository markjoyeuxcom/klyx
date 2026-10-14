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
	gatewayDated(&e, by, current, accepted, programmed)
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

// gatewayDated fills Since and Reason (The Line §8.1). A role read from a
// current Accepted condition began at its lastTransitionTime; one read from
// Programmed was reached once Accepted was True, so it began at the later of
// the two (both must be known, or the earlier one would overstate the age).
// A last-observed condition (an older generation) keeps its reason but no
// Since: the role began when the spec changed, which nothing dates.
func gatewayDated(e *health.NativeEval, by gatewayBasis, current bool, accepted, programmed Cond) {
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
	since := c.LastTransitionTime
	if !current || since.IsZero() {
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
		carrying := map[int][]Backend{}
		listed := map[int]bool{}
		total := 0
		for _, b := range rn.Backends {
			if !listed[b.Rule] {
				listed[b.Rule] = true
				order = append(order, b.Rule)
			}
			if b.CarriesTraffic() {
				carrying[b.Rule] = append(carrying[b.Rule], b)
				total++
			}
		}
		named := total > 1
		for _, ri := range order {
			rc := carrying[ri]
			if len(rc) == 0 {
				if len(order) > 1 {
					add(health.Fault, fmt.Sprintf("rule %d: every backend has weight 0", ri+1))
				} else {
					add(health.Fault, "every backend has weight 0")
				}
				continue
			}
			var found []routeIssue
			faults := 0
			for _, b := range rc {
				role, short := BackendIssue(b)
				if role == health.OK {
					continue
				}
				if role == health.Fault {
					faults++
				}
				if named {
					short = b.Name + ": " + short
				}
				found = append(found, routeIssue{role: role, short: short})
			}
			for _, is := range found {
				if len(rc) > 1 && is.role == health.Fault && faults < len(rc) {
					is.role = health.Degraded
				}
				issues = append(issues, is)
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
