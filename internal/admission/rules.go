package admission

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/moomora/klyx/internal/health"
)

// Read is how a dependency was read for a part (C3's per-consumer view).
type Read int

const (
	ReadNone     Read = iota // unread, checking, or its consumer cannot tell
	ReadStale                // the last good state: absence proves nothing
	ReadComplete             // every object there is
)

// Backends is what the webhook rule reads of the shared network kinds.
type Backends struct {
	Services  map[string]*corev1.Service              // namespace/name
	Slices    map[string][]*discoveryv1.EndpointSlice // namespace/service name
	SvcRead   Read
	SliceRead Read
}

// Backend states, per webhook (S3a §5).
const (
	BackendReady       = "ready"
	BackendUnavailable = "unavailable"
	BackendTerminating = "terminating"
	BackendUnknown     = "unknown"
)

// Backend is one webhook's backend as Klyx reads it.
type Backend struct {
	State  string // BackendReady | BackendUnavailable | BackendTerminating | BackendUnknown
	Label  string
	Reason string
	// Usable counts the usable endpoints under each routing mode (§5 row
	// 10); Terminating counts the serving-terminating ones ClusterIP may
	// fall back to.
	EndpointRouting, ClusterRouting, Terminating int
	// Selectorless: the Service has no selector; its slices are managed by
	// something other than the Service controller.
	Selectorless bool
}

func unknown(label, reason string) Backend {
	return Backend{State: BackendUnknown, Label: label, Reason: reason}
}

func unavailable(reason string) Backend {
	return Backend{State: BackendUnavailable, Label: "Backend unavailable", Reason: reason}
}

func protocolOf(p corev1.Protocol) corev1.Protocol {
	if p == "" {
		return corev1.ProtocolTCP
	}
	return p
}

// BackendOf is S3a §5's per-webhook state, first match wins. The API
// server's routing mode is not observable, so a state the modes disagree on
// is unknown and says so.
func BackendOf(w *Webhook, b Backends) Backend {
	if w.URL || w.Service == nil {
		return unknown("Not inspected", "a URL backend has no Service to inspect")
	}
	ref := *w.Service
	key := ref.Namespace + "/" + ref.Name
	if b.SvcRead == ReadNone {
		return unknown("Backend unverified", "the Services could not be read")
	}
	svc, ok := b.Services[key]
	if !ok {
		if b.SvcRead == ReadStale {
			return unknown("Backend unverified", "Service "+key+" is not in the last good read")
		}
		return unavailable("Service " + key + " not found")
	}
	switch {
	case svc.Spec.Type == corev1.ServiceTypeExternalName:
		return unknown("Not inspected", "ExternalName: ClusterIP routing leaves the cluster and endpoint routing rejects it")
	case svc.Spec.Type != "" && svc.Spec.Type != corev1.ServiceTypeClusterIP && svc.Spec.Type != corev1.ServiceTypeNodePort && svc.Spec.Type != corev1.ServiceTypeLoadBalancer:
		return unavailable(fmt.Sprintf("the Service type %q is not routable", string(svc.Spec.Type)))
	case svc.Spec.ClusterIP == corev1.ClusterIPNone:
		return unknown("Routing-dependent", "headless: ClusterIP routing cannot reach it, endpoint routing can; the routing mode is not observable")
	}
	var port *corev1.ServicePort
	for i := range svc.Spec.Ports {
		if svc.Spec.Ports[i].Port == ref.Port {
			port = &svc.Spec.Ports[i]
			break
		}
	}
	if port == nil {
		return unavailable(fmt.Sprintf("no Service port %d on %s", ref.Port, key))
	}
	if protocolOf(port.Protocol) != corev1.ProtocolTCP {
		return unknown("Routing-dependent", fmt.Sprintf("the Service port %d is %s; a webhook is called over TCP", ref.Port, port.Protocol))
	}
	if b.SliceRead == ReadNone {
		return unknown("Backend unverified", "the EndpointSlices could not be read")
	}
	out := usable(port, b.Slices[key])
	out.Selectorless = len(svc.Spec.Selector) == 0
	switch {
	case out.EndpointRouting > 0 && out.ClusterRouting > 0:
		noun := "endpoints"
		if out.ClusterRouting == 1 {
			noun = "endpoint"
		}
		out.State, out.Label, out.Reason = BackendReady, "Backend ready", fmt.Sprintf("%d usable %s", out.ClusterRouting, noun)
	case out.EndpointRouting > 0 || out.ClusterRouting > 0:
		mode := "endpoint"
		if out.ClusterRouting > 0 {
			mode = "ClusterIP"
		}
		out.State, out.Label, out.Reason = BackendUnknown, "Routing-dependent", "usable under "+mode+" routing only; the routing mode is not observable"
	case out.Terminating > 0:
		out.State, out.Label, out.Reason = BackendTerminating, "Only terminating endpoints", "only terminating endpoints: ClusterIP routing may fall back to them, endpoint routing does not"
	default:
		if b.SliceRead == ReadStale {
			return unknown("Backend unverified", "no usable endpoints in the last good read")
		}
		out.State, out.Label = BackendUnavailable, "Backend unavailable"
	}
	return out
}

// usable counts a Service port's usable endpoints per routing mode (§5
// row 10): endpoint routing needs a present name equal to the port's, a
// present number, and a ready endpoint with an address; ClusterIP routing
// also needs the protocol to match, and falls back to serving-terminating
// endpoints.
func usable(port *corev1.ServicePort, slices []*discoveryv1.EndpointSlice) Backend {
	var out Backend
	named, numbered, ready := false, false, false
	for _, s := range slices {
		for _, sp := range s.Ports {
			if sp.Name == nil || *sp.Name != port.Name {
				continue
			}
			named = true
			if sp.Port == nil {
				continue
			}
			numbered = true
			proto := protocolOf(ptrProtocol(sp.Protocol)) == protocolOf(port.Protocol)
			for _, e := range s.Endpoints {
				if len(e.Addresses) == 0 {
					continue
				}
				isReady := e.Conditions.Ready == nil || *e.Conditions.Ready
				serving := e.Conditions.Serving == nil || *e.Conditions.Serving
				terminating := e.Conditions.Terminating != nil && *e.Conditions.Terminating
				switch {
				case isReady:
					ready = true
					out.EndpointRouting++
					if proto {
						out.ClusterRouting++
					}
				case serving && terminating && proto:
					out.Terminating++
				}
			}
			break // one matching port per slice
		}
	}
	switch {
	case len(slices) == 0:
		out.Reason = "no EndpointSlices for the Service"
	case !named:
		out.Reason = fmt.Sprintf("no slice carries the port %q", port.Name)
	case !numbered:
		out.Reason = "the slice port has no number"
	case !ready:
		out.Reason = "no ready endpoint"
	}
	return out
}

func ptrProtocol(p *corev1.Protocol) corev1.Protocol {
	if p == nil {
		return ""
	}
	return *p
}

// WebhookVerdict is one webhook's role from its backend and failure policy.
func WebhookVerdict(w *Webhook, b Backend) (health.Kind, string, string) {
	switch b.State {
	case BackendReady:
		return health.OK, b.Label, b.Reason
	case BackendUnavailable:
		if w.FailClosed() {
			return health.Fault, b.Label, b.Reason + "; matching requests may fail"
		}
		return health.Degraded, b.Label + " (fail-open)", b.Reason + "; matching requests skip this webhook"
	case BackendTerminating:
		return health.Degraded, b.Label, b.Reason
	}
	return health.Unknown, b.Label, b.Reason
}

// severity orders roles for a configuration's worst webhook.
func severity(k health.Kind) int {
	switch k {
	case health.Fault:
		return 0
	case health.Degraded:
		return 1
	case health.Unknown:
		return 2
	}
	return 3
}

// ConfigVerdict is S3a §5's configuration verdict: its worst webhook's,
// naming that webhook and "+N more". A configuration whose webhooks were
// cut never reads ok: the cut ones are unread (§4).
func ConfigVerdict(c *Config, backends []Backend) health.NativeEval {
	var ev health.NativeEval
	set := func(role health.Kind, label, reason string) {
		ev.Role, ev.Label, ev.Included = role, label, role.Attention()
		if reason != "" {
			ev.Reason = health.CapReason(label, reason)
		}
	}
	worst, at, same := health.OK, -1, 0
	for i := range c.Webhooks {
		role, _, _ := WebhookVerdict(&c.Webhooks[i], backends[i])
		switch {
		case at < 0 || severity(role) < severity(worst):
			worst, at, same = role, i, 0
		case severity(role) == severity(worst) && role != health.OK:
			same++
		}
	}
	unread := c.WebhookCount - len(c.Webhooks)
	if at < 0 {
		if unread > 0 {
			set(health.Unknown, "Webhooks not read", fmt.Sprintf("%d webhooks not read (too many to hold)", unread))
		} else {
			set(health.OK, "No webhooks", "")
		}
		return ev
	}
	if worst == health.OK {
		if unread > 0 {
			set(health.Unknown, "Webhooks not read", fmt.Sprintf("%d webhooks not read (too many to hold); the rest are ready", unread))
			return ev
		}
		set(health.OK, "Backends ready", "")
		return ev
	}
	role, label, reason := WebhookVerdict(&c.Webhooks[at], backends[at])
	reason = c.Webhooks[at].Name + ": " + reason
	if same > 0 {
		reason += fmt.Sprintf(" (+%d more)", same)
	}
	if unread > 0 {
		reason += fmt.Sprintf("; %d webhooks not read", unread)
	}
	set(role, label, reason)
	return ev
}

// ---- policies -------------------------------------------------------------

// Param-kind states (S3a §6): from a completed discovery only.
const (
	ParamServed    = "served"
	ParamNotServed = "not-served"
	ParamUnknown   = "unknown"
)

// ParamKindState is one exact group/version/kind's discovery.
type ParamKindState struct {
	State      string    `json:"state"`
	Namespaced bool      `json:"namespaced,omitempty"`
	Resource   string    `json:"resource,omitempty"` // its plural, when served
	At         time.Time `json:"at"`
	Error      string    `json:"error,omitempty"`
}

// ParamKinds is FactParamKinds: keyed by "group/version/Kind".
type ParamKinds map[string]ParamKindState

// KeyOf is a ParamKind's key in ParamKinds.
func KeyOf(group, version, kind string) string { return group + "/" + version + "/" + kind }

// PolicyInput is everything one policy's verdict reads.
type PolicyInput struct {
	P            *Policy
	Bindings     []*Binding // the bindings that name it
	BindingsRead bool       // the bindings kind was read for this part
	ParamKinds   ParamKinds // nil: the fact is not known
}

// PolicyVerdict is S3a §6's policy rule, first match wins.
func PolicyVerdict(in PolicyInput) health.NativeEval {
	var ev health.NativeEval
	set := func(role health.Kind, label, reason string) {
		ev.Role, ev.Label, ev.Included = role, label, role.Attention()
		if reason != "" {
			ev.Reason = health.CapReason(label, reason)
		}
	}
	p := in.P
	if !in.BindingsRead {
		set(health.Unknown, "Bindings unverified", "the bindings could not be read")
		return ev
	}
	if len(in.Bindings) == 0 {
		set(health.Paused, "Not bound", "no binding names it: it has no effect")
		return ev
	}
	if p.ParamKind != nil {
		g, v, k, ok := p.ParamKind.GVK()
		st := ParamKindState{State: ParamUnknown}
		if ok && in.ParamKinds != nil {
			if s, found := in.ParamKinds[KeyOf(g, v, k)]; found {
				st = s
			}
		}
		switch st.State {
		case ParamUnknown:
			set(health.Unknown, "paramKind unverified", "whether paramKind "+p.ParamKind.Kind+" is served is not known yet")
			return ev
		case ParamNotServed:
			if p.FailClosed() {
				set(health.Fault, "paramKind not served", "paramKind "+p.ParamKind.Kind+" ("+p.ParamKind.APIVersion+") is not served: matching requests are denied")
			} else {
				set(health.Degraded, "paramKind not served", "paramKind "+p.ParamKind.Kind+" ("+p.ParamKind.APIVersion+") is not served: the policy is skipped")
			}
			return ev
		}
		if !st.Namespaced {
			for _, b := range in.Bindings {
				if b.ParamRef != nil && b.ParamRef.Namespace != "" {
					why := "binding " + b.Name + " sets a namespace for the cluster-scoped paramKind " + p.ParamKind.Kind
					if p.FailClosed() {
						set(health.Fault, "Binding misconfigured", why+": matching requests are denied")
					} else {
						set(health.Degraded, "Binding misconfigured", why+": the binding is skipped")
					}
					return ev
				}
			}
		}
	}
	if p.Behind() {
		set(health.Progressing, "Type checking pending", "the status describes an earlier generation")
		return ev
	}
	if p.WarningCount > 0 {
		set(health.Degraded, "Type-checking warnings", fmt.Sprintf("%d type-checking warnings: an expression may fail when it runs; %s", p.WarningCount, failureConsequence(p, in.Bindings)))
		return ev
	}
	set(health.OK, "Bound", fmt.Sprintf("bound by %d", len(in.Bindings)))
	return ev
}

// failureConsequence is what a failed evaluation does (§6 row 5): under
// Fail it becomes a deny decision, which the bindings' actions apply.
func failureConsequence(p *Policy, bindings []*Binding) string {
	if !p.FailClosed() {
		return "a failure is ignored"
	}
	for _, b := range bindings {
		for _, a := range b.Actions {
			if a == "Deny" {
				return "a failure denies requests matched by Deny bindings"
			}
		}
	}
	return "a failure is " + strings.ToLower(joinActions(bindings)) + ", not denied"
}

func joinActions(bindings []*Binding) string {
	seen := map[string]bool{}
	var out []string
	for _, b := range bindings {
		for _, a := range b.Actions {
			if !seen[a] {
				seen[a] = true
				out = append(out, map[string]string{"Warn": "warned", "Audit": "audited"}[a])
			}
		}
	}
	if len(out) == 0 {
		return "not acted on"
	}
	return strings.Join(out, " or ")
}
