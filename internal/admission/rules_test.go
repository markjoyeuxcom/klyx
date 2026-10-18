package admission

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/health"
)

func ptr[T any](v T) *T { return &v }

func svc(name string, mut ...func(*corev1.Service)) *corev1.Service {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "sys", Name: name}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP,
		Selector: map[string]string{"": ""}, Ports: []corev1.ServicePort{{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP}}}}
	for _, m := range mut {
		m(s)
	}
	return s
}

type ep struct {
	ready, serving, terminating *bool
	addr                        bool
}

func eslice(svcName string, portName *string, port *int32, proto *corev1.Protocol, eps ...ep) *discoveryv1.EndpointSlice {
	s := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "sys", Name: svcName + "-x", Labels: map[string]string{discoveryv1.LabelServiceName: svcName}},
		Ports: []discoveryv1.EndpointPort{{Name: portName, Port: port, Protocol: proto}}}
	for _, e := range eps {
		de := discoveryv1.Endpoint{Conditions: discoveryv1.EndpointConditions{Ready: e.ready, Serving: e.serving, Terminating: e.terminating}}
		if e.addr {
			de.Addresses = []string{"(address)"}
		}
		s.Endpoints = append(s.Endpoints, de)
	}
	return s
}

var (
	readyEP = ep{addr: true}
	tcp     = ptr(corev1.ProtocolTCP)
)

func backends(svcs []*corev1.Service, slices ...*discoveryv1.EndpointSlice) Backends {
	b := Backends{Services: map[string]*corev1.Service{}, Slices: map[string][]*discoveryv1.EndpointSlice{}, SvcRead: ReadComplete, SliceRead: ReadComplete}
	for _, s := range svcs {
		b.Services[s.Namespace+"/"+s.Name] = s
	}
	for _, s := range slices {
		k := s.Namespace + "/" + s.Labels[discoveryv1.LabelServiceName]
		b.Slices[k] = append(b.Slices[k], s)
	}
	return b
}

func hook(fail bool, service string) *Webhook {
	w := &Webhook{Name: "hook.example.com", Service: &ServiceRef{Namespace: "sys", Name: service, Port: 443}}
	if !fail {
		w.FailurePolicy = "Ignore"
	}
	return w
}

// Every row of S3a §5, at both failure policies.
func TestBackendRows(t *testing.T) {
	ok := []*discoveryv1.EndpointSlice{eslice("hook", ptr("https"), ptr(int32(8443)), tcp, readyEP)}
	cases := []struct {
		name  string
		w     *Webhook
		b     Backends
		state string
		want  string
	}{
		{"url", &Webhook{Name: "u", URL: true, URLOrigin: "https://x"}, backends(nil), BackendUnknown, "URL backend"},
		{"services unread", hook(true, "hook"), Backends{}, BackendUnknown, "Services could not be read"},
		{"missing from a complete read", hook(true, "gone"), backends(nil), BackendUnavailable, "not found"},
		{"missing from a stale read", hook(true, "gone"), func() Backends { b := backends(nil); b.SvcRead = ReadStale; return b }(), BackendUnknown, "last good read"},
		{"headless", hook(true, "hook"), backends([]*corev1.Service{svc("hook", func(s *corev1.Service) { s.Spec.ClusterIP = corev1.ClusterIPNone })}, ok...), BackendUnknown, "headless"},
		{"external name", hook(true, "hook"), backends([]*corev1.Service{svc("hook", func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeExternalName })}), BackendUnknown, "ExternalName"},
		{"no port", hook(true, "hook"), backends([]*corev1.Service{svc("hook", func(s *corev1.Service) { s.Spec.Ports[0].Port = 8443 })}, ok...), BackendUnavailable, "no Service port 443"},
		{"udp service port", hook(true, "hook"), backends([]*corev1.Service{svc("hook", func(s *corev1.Service) { s.Spec.Ports[0].Protocol = corev1.ProtocolUDP })}, ok...), BackendUnknown, "over TCP"},
		{"slices unread", hook(true, "hook"), func() Backends { b := backends([]*corev1.Service{svc("hook")}); b.SliceRead = ReadNone; return b }(), BackendUnknown, "EndpointSlices could not be read"},
		{"ready", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}, ok...), BackendReady, "1 usable"},
		{"no slices", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}), BackendUnavailable, "no EndpointSlices"},
		{"no ready endpoint", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}, eslice("hook", ptr("https"), ptr(int32(8443)), tcp, ep{ready: ptr(false), addr: true})), BackendUnavailable, "no ready endpoint"},
		{"port named but no number", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}, eslice("hook", ptr("https"), nil, tcp, readyEP)), BackendUnavailable, "no number"},
		{"no port by name", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}, eslice("hook", ptr("http"), ptr(int32(80)), tcp, readyEP)), BackendUnavailable, "no slice carries"},
		{"nil port name", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}, eslice("hook", nil, ptr(int32(8443)), tcp, readyEP)), BackendUnavailable, "no slice carries"},
		// Codex's re-review of 2827d4b: endpoint routing ignores the slice
		// port's protocol, so a protocol-only mismatch is routing-dependent
		{"slice protocol mismatch", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}, eslice("hook", ptr("https"), ptr(int32(8443)), ptr(corev1.ProtocolUDP), readyEP)), BackendUnknown, "endpoint routing only"},
		{"only terminating", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}, eslice("hook", ptr("https"), ptr(int32(8443)), tcp, ep{ready: ptr(false), serving: ptr(true), terminating: ptr(true), addr: true})), BackendTerminating, "only terminating"},
		{"no address", hook(true, "hook"), backends([]*corev1.Service{svc("hook")}, eslice("hook", ptr("https"), ptr(int32(8443)), tcp, ep{})), BackendUnavailable, ""},
		{"stale slices without endpoints", hook(true, "hook"), func() Backends { b := backends([]*corev1.Service{svc("hook")}); b.SliceRead = ReadStale; return b }(), BackendUnknown, "last good read"},
	}
	for _, c := range cases {
		got := BackendOf(c.w, c.b)
		if got.State != c.state || !strings.Contains(got.Label+" "+got.Reason, c.want) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	// a selectorless Service is counted from its own slices, and said
	b := BackendOf(hook(true, "hook"), backends([]*corev1.Service{svc("hook", func(s *corev1.Service) { s.Spec.Selector = nil })}, ok...))
	if b.State != BackendReady || !b.Selectorless {
		t.Fatalf("selectorless: %+v", b)
	}
}

func TestWebhookVerdictByFailurePolicy(t *testing.T) {
	down := Backend{State: BackendUnavailable, Label: "Backend unavailable", Reason: "Service sys/x not found"}
	if r, _, why := WebhookVerdict(hook(true, "x"), down); r != health.Fault || !strings.Contains(why, "may fail") {
		t.Fatalf("fail-closed: %v %s", r, why)
	}
	if r, _, why := WebhookVerdict(hook(false, "x"), down); r != health.Degraded || !strings.Contains(why, "skip this webhook") {
		t.Fatalf("fail-open: %v %s", r, why)
	}
	if r, _, _ := WebhookVerdict(hook(true, "x"), Backend{State: BackendTerminating}); r != health.Degraded {
		t.Fatal("terminating is degraded")
	}
	if r, _, _ := WebhookVerdict(hook(true, "x"), Backend{State: BackendUnknown}); r != health.Unknown {
		t.Fatal("unknown")
	}
}

// The configuration is the counted entity: its worst webhook decides, and
// a cut never reads ok (S3a §4, §5).
func TestConfigVerdict(t *testing.T) {
	good, down := Backend{State: BackendReady, Label: "Backend ready"}, Backend{State: BackendUnavailable, Label: "Backend unavailable", Reason: "no ready endpoint"}
	c := &Config{Webhooks: []Webhook{*hook(true, "a"), *hook(true, "b")}, WebhookCount: 2}
	c.Webhooks[1].Name = "second.example.com"
	ev := ConfigVerdict(c, []Backend{good, down})
	if ev.Role != health.Fault || !ev.Included || !strings.Contains(ev.Reason, "second.example.com: no ready endpoint") {
		t.Fatalf("two webhooks, one faulty: %+v", ev)
	}
	if ev := ConfigVerdict(c, []Backend{good, good}); ev.Role != health.OK {
		t.Fatalf("all ready: %+v", ev)
	}
	// the tail was cut: never ok, and a proven fault among the read stands
	cut := &Config{Webhooks: c.Webhooks, WebhookCount: 5, Cut: true}
	if ev := ConfigVerdict(cut, []Backend{good, good}); ev.Role != health.Unknown || !strings.Contains(ev.Reason, "3 webhooks not read") {
		t.Fatalf("cut: %+v", ev)
	}
	if ev := ConfigVerdict(cut, []Backend{down, good}); ev.Role != health.Fault || !strings.Contains(ev.Reason, "3 webhooks not read") {
		t.Fatalf("cut with a fault: %+v", ev)
	}
	// two faults: the first names "+1 more"
	if ev := ConfigVerdict(c, []Backend{down, down}); !strings.Contains(ev.Reason, "(+1 more)") {
		t.Fatalf("+N more: %+v", ev)
	}
}

func pol(mut ...func(*Policy)) *Policy {
	p := &Policy{Meta: Meta{Name: "p", Generation: 2}, Kind: Validating, HasStatus: true, ObservedGeneration: 2}
	for _, m := range mut {
		m(p)
	}
	return p
}

func bnd(actions ...string) *Binding {
	return &Binding{Meta: Meta{Name: "b"}, PolicyName: "p", Actions: actions}
}

// Every row of S3a §6.
func TestPolicyRows(t *testing.T) {
	param := func(p *Policy) { p.ParamKind = &ParamKind{APIVersion: "example.com/v1", Kind: "Limits"} }
	ignore := func(p *Policy) { p.FailurePolicy = "Ignore" }
	served := ParamKinds{KeyOf("example.com", "v1", "Limits"): {State: ParamServed, Namespaced: true}}
	gone := ParamKinds{KeyOf("example.com", "v1", "Limits"): {State: ParamNotServed}}
	cluster := ParamKinds{KeyOf("example.com", "v1", "Limits"): {State: ParamServed}}
	ns := &Binding{Meta: Meta{Name: "b"}, PolicyName: "p", ParamRef: &ParamRef{Name: "x", Namespace: "team"}}
	cases := []struct {
		name  string
		in    PolicyInput
		role  health.Kind
		label string
		why   string
	}{
		{"bindings unread", PolicyInput{P: pol()}, health.Unknown, "Bindings unverified", ""},
		{"unbound", PolicyInput{P: pol(), BindingsRead: ReadComplete}, health.Paused, "Not bound", "no effect"},
		{"param kind unknown", PolicyInput{P: pol(param), Bindings: []*Binding{bnd("Deny")}, BindingsRead: ReadComplete}, health.Unknown, "paramKind unverified", ""},
		{"param kind not served, fail-closed", PolicyInput{P: pol(param), Bindings: []*Binding{bnd("Warn")}, BindingsRead: ReadComplete, ParamKinds: gone}, health.Fault, "paramKind not served", "matching requests are denied"},
		{"param kind not served, ignore", PolicyInput{P: pol(param, ignore), Bindings: []*Binding{bnd("Deny")}, BindingsRead: ReadComplete, ParamKinds: gone}, health.Degraded, "paramKind not served", "skipped"},
		{"namespace for a cluster-scoped kind", PolicyInput{P: pol(param), Bindings: []*Binding{ns}, BindingsRead: ReadComplete, ParamKinds: cluster}, health.Fault, "Binding misconfigured", "denied"},
		{"namespaced kind, namespace fine", PolicyInput{P: pol(param), Bindings: []*Binding{ns}, BindingsRead: ReadComplete, ParamKinds: served}, health.OK, "Bound", ""},
		{"status behind, historical warnings", PolicyInput{P: pol(func(p *Policy) { p.Generation, p.WarningCount = 3, 2 }), Bindings: []*Binding{bnd("Deny")}, BindingsRead: ReadComplete}, health.Progressing, "Type checking pending", ""},
		{"warnings under Deny", PolicyInput{P: pol(func(p *Policy) { p.WarningCount = 1 }), Bindings: []*Binding{bnd("Deny")}, BindingsRead: ReadComplete}, health.Degraded, "Type-checking warnings", "denies requests matched by Deny bindings"},
		{"warnings under Warn and Audit", PolicyInput{P: pol(func(p *Policy) { p.WarningCount = 1 }), Bindings: []*Binding{bnd("Warn", "Audit")}, BindingsRead: ReadComplete}, health.Degraded, "Type-checking warnings", "warned or audited, not denied"},
		{"warnings under Ignore", PolicyInput{P: pol(ignore, func(p *Policy) { p.WarningCount = 1 }), Bindings: []*Binding{bnd("Deny")}, BindingsRead: ReadComplete}, health.Degraded, "Type-checking warnings", "ignored"},
		{"mutating, no status", PolicyInput{P: &Policy{Kind: Mutating}, Bindings: []*Binding{bnd()}, BindingsRead: ReadComplete}, health.OK, "Bound", ""},
		{"bound", PolicyInput{P: pol(), Bindings: []*Binding{bnd("Deny"), bnd("Audit")}, BindingsRead: ReadComplete}, health.OK, "Bound", "bound by 2"},
	}
	for _, c := range cases {
		ev := PolicyVerdict(c.in)
		if ev.Role != c.role || ev.Label != c.label || !strings.Contains(ev.Reason, c.why) || ev.Included != c.role.Attention() {
			t.Errorf("%s: %+v", c.name, ev)
		}
	}
}

// Only a complete bindings read proves a policy unbound (Codex's S3a
// implementation review, finding 3); a binding found in a stale read
// still names it.
func TestPolicyBindingsReadStates(t *testing.T) {
	if ev := PolicyVerdict(PolicyInput{P: pol(), BindingsRead: ReadStale}); ev.Role != health.Unknown || ev.Label != "Bindings unverified" {
		t.Fatalf("stale empty: %+v", ev)
	}
	if ev := PolicyVerdict(PolicyInput{P: pol(), Bindings: []*Binding{bnd("Deny")}, BindingsRead: ReadStale}); ev.Role != health.OK {
		t.Fatalf("stale with a binding: %+v", ev)
	}
	if ev := PolicyVerdict(PolicyInput{P: pol(), BindingsRead: ReadComplete}); ev.Role != health.Paused {
		t.Fatalf("complete empty: %+v", ev)
	}
}
