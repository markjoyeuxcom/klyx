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
	role, label := gatewayRole(accepted, programmed, generation)
	return health.NativeEval{Included: role == health.Fault || role == health.Degraded, Role: role, Label: label}
}

func gatewayRole(accepted, programmed Cond, generation int64) (health.Kind, string) {
	a := condState(accepted, generation)
	switch a.status {
	case "absent":
		return health.Unknown, "no status reported"
	case "Unknown":
		return health.Progressing, "acceptance pending"
	case "False":
		if a.stale {
			return health.Degraded, "not accepted (last observed)"
		}
		return health.Fault, "not accepted"
	}
	p := condState(programmed, generation)
	switch p.status {
	case "absent":
		return health.Unknown, "programming not reported"
	case "Unknown":
		return health.Progressing, "programming"
	case "False":
		if p.stale {
			return health.Degraded, "not programmed (last observed)"
		}
		return health.Degraded, "not programmed"
	}
	if a.stale || p.stale {
		return health.Progressing, "syncing"
	}
	return health.OK, "programmed"
}

// lensRank is gatewayModel.RANK: progressing ranks BEFORE unknown here (the
// lens's own order), unlike the health verdict order.
var lensRank = map[health.Kind]int{health.Fault: 0, health.Degraded: 1, health.Progressing: 2, health.Unknown: 3, health.OK: 4}

type routeIssue struct {
	role  health.Kind
	short string
}

// RouteRule is one route attachment's verdict (route x parent Gateway):
// routeIssues, then the worst issue by the lens's rank (first wins on ties),
// "resolved" when none. listeners are the parent Gateway's. Included = fault
// or degraded - the Gateway list's predicate, applied to the routes part
// (the lens has no route predicate of its own yet).
func RouteRule(rn RouteNode, listeners []Listener) health.NativeEval {
	var issues []routeIssue
	add := func(role health.Kind, short string) { issues = append(issues, routeIssue{role, short}) }
	if !rn.ParentFound {
		add(health.Unknown, "no status for this Gateway")
	} else {
		a := condState(rn.AcceptedCond, rn.Generation)
		switch a.status {
		case "False":
			if a.stale {
				add(health.Degraded, "not accepted (last observed)")
			} else {
				add(health.Fault, "not accepted")
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
				add(health.Degraded, "unresolved refs (last observed)")
			} else {
				add(health.Fault, "unresolved refs")
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
	// no endpoints read as "resolved"). A weight-0 backend carries none and
	// is left out. With several carrying backends a fault is degraded -
	// the others still take traffic - unless every one of them faults.
	switch {
	case len(rn.Backends) == 0:
		add(health.Unknown, "no backendRefs")
	default:
		var carrying []Backend
		for _, b := range rn.Backends {
			if b.CarriesTraffic() {
				carrying = append(carrying, b)
			}
		}
		if len(carrying) == 0 {
			add(health.Fault, "every backend has weight 0")
			break
		}
		several := len(carrying) > 1
		var found []routeIssue
		faults := 0
		for _, b := range carrying {
			role, short := BackendIssue(b)
			if role == health.OK {
				continue
			}
			if role == health.Fault {
				faults++
			}
			if several {
				short = b.Name + ": " + short
			}
			found = append(found, routeIssue{role, short})
		}
		for _, is := range found {
			if several && is.role == health.Fault && faults < len(carrying) {
				is.role = health.Degraded
			}
			issues = append(issues, is)
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
	return health.NativeEval{Included: worst.role == health.Fault || worst.role == health.Degraded, Role: worst.role, Label: worst.short}
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
