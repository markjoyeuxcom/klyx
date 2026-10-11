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

	several := len(rn.Backends) > 1
	primary := func(role health.Kind) health.Kind {
		if several && role == health.Fault {
			return health.Degraded // other backends may still carry traffic
		}
		return role
	}
	switch {
	case len(rn.Backends) == 0:
		add(health.Unknown, "no backendRefs")
	default:
		b := rn.Backends[0]
		switch {
		case b.State == BackendUnsupported:
			// (The lens's undefined-state branch only serves pre-state
			// payloads; the bridge always sends state, "" included.)
			add(health.Unknown, b.Kind+" backend not evaluated")
		case b.State == BackendNotFound:
			if several {
				add(primary(health.Fault), "primary backend not found")
			} else {
				add(primary(health.Fault), "backend not found")
			}
		case b.State == BackendUnreadable:
			add(health.Unknown, "backend unreadable")
		case b.State == BackendResolved:
			ep := b.Endpoints
			switch {
			case ep == nil || ep.Error != "":
				add(health.Unknown, "endpoints unreadable")
			case ep.Total == 0:
				add(primary(health.Fault), "no endpoints")
			case ep.Ready == 0:
				add(primary(health.Fault), "no ready endpoints")
			case ep.Ready < ep.Total:
				add(health.Degraded, fmt.Sprintf("%d/%d endpoints ready", ep.Ready, ep.Total))
			}
		default:
			add(health.Unknown, "backend not evaluated")
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
			if e.Conditions.Ready != nil && *e.Conditions.Ready {
				out.Ready++
			}
		}
	}
	return out
}
