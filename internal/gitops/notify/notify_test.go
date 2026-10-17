package notify

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

const needle = "PATHneedle6M"

func obj(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("%v: %s", err, doc)
	}
	var norm func(v any) any
	norm = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				x[k] = norm(e)
			}
		case []any:
			for i, e := range x {
				x[i] = norm(e)
			}
		case float64:
			return int64(x)
		}
		return v
	}
	norm(m)
	return &unstructured.Unstructured{Object: m}
}

func receiver(t *testing.T, spec, status string, gen int) Receiver {
	t.Helper()
	g, _ := json.Marshal(gen)
	u := obj(t, `{"apiVersion":"notification.toolkit.fluxcd.io/v1","kind":"Receiver","metadata":{"name":"gh","namespace":"flux-system","uid":"u-r","generation":`+string(g)+`},
		"spec":`+spec+`,"status":`+status+`}`)
	v, err := ProjectReceiver(u)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*Object)
	b, _ := json.Marshal(o.Object)
	if strings.Contains(string(b), needle) {
		t.Fatalf("the webhook path crossed into the projection: %s", b)
	}
	return ParseReceiver(o)
}

const genericSpec = `{"type":"generic","secretRef":{"name":"token"},"events":["ping"],"resources":[{"kind":"GitRepository","name":"fleet","filter":"true"}]}`

func ready(status, reason string, gen int) string {
	g, _ := json.Marshal(gen)
	return `{"observedGeneration":` + string(g) + `,"webhookPath":"/hook/` + needle + `","conditions":[{"type":"Ready","status":"` + status + `","reason":"` + reason +
		`","message":"Receiver initialized for path: /hook/` + needle + `","lastTransitionTime":"2026-10-17T09:00:00Z","observedGeneration":` + string(g) + `}]}`
}

// Coverage S1c §4: a Receiver's verdict, with Klyx's own wording.
func TestReceiverRule(t *testing.T) {
	r := receiver(t, genericSpec, ready("True", "Succeeded", 1), 1)
	if e := ReceiverRule(r); e.Role != health.OK || ReadyMeaning(r) != "configuration accepted, token read" || r.FilterCount != 1 || r.ResourceCount != 1 {
		t.Fatalf("ready: %+v %+v", e, r)
	}
	oidc := receiver(t, `{"type":"generic-oidc","oidcProviders":[{"issuerURL":"https://issuer.example"}]}`, ready("True", "Succeeded", 1), 1)
	if ReadyMeaning(oidc) != "configuration accepted (OIDC, no token Secret)" || oidc.OIDCProviders != 1 {
		t.Fatalf("generic-oidc without a Secret: %+v", oidc)
	}
	tnf := receiver(t, genericSpec, ready("False", ReasonTokenNF, 1), 1)
	if e := ReceiverRule(tnf); e.Role != health.Fault || !strings.Contains(e.Reason, TokenUnavailable) || strings.Contains(e.Reason, needle) {
		t.Fatalf("token unavailable: %+v", e)
	}
	stalled := receiver(t, genericSpec, `{"observedGeneration":1,"conditions":[{"type":"Ready","status":"False","reason":"InvalidCELExpression","message":"bad","observedGeneration":1},
		{"type":"Stalled","status":"True","reason":"InvalidCELExpression","message":"bad"}]}`, 1)
	if e := ReceiverRule(stalled); e.Label != "Stalled" || e.Role != health.Fault {
		t.Fatalf("stalled: %+v", e)
	}
	// S1b §4.1: a failure of the current generation is a fault while
	// status lags; an older Ready is Syncing
	current := receiver(t, genericSpec, `{"observedGeneration":1,"conditions":[{"type":"Ready","status":"False","reason":"TokenNotFound","message":"x","observedGeneration":2}]}`, 2)
	if ReceiverRule(current).Role != health.Fault {
		t.Fatal("a current failure is a fault")
	}
	if ReceiverRule(receiver(t, genericSpec, ready("True", "Succeeded", 1), 2)).Label != LabelSyncing {
		t.Fatal("an older Ready is Syncing")
	}
	if ReceiverRule(receiver(t, `{"type":"generic","suspend":true}`, ready("True", "Succeeded", 1), 1)).Role != health.Paused {
		t.Fatal("suspended")
	}
}

// S1c §2: Providers and Alerts are facts; the address and proxy are never
// read, only whether they are set.
func TestProviderAndAlertFacts(t *testing.T) {
	p := ParseProvider(obj(t, `{"apiVersion":"notification.toolkit.fluxcd.io/v1beta3","kind":"Provider","metadata":{"name":"slack","namespace":"flux-system"},
		"spec":{"type":"slack","channel":"alerts","address":"https://hooks.slack.com/services/T/B/`+needle+`","proxy":"http://u:`+needle+`@p:3128","secretRef":{"name":"slack-url"},"suspend":true}}`))
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), needle) || !p.HasAddress || !p.HasProxy || p.SecretRef != "slack-url" || !p.Suspended {
		t.Fatalf("provider: %s", b)
	}
	a := ParseAlert(obj(t, `{"apiVersion":"notification.toolkit.fluxcd.io/v1beta3","kind":"Alert","metadata":{"name":"on-call","namespace":"flux-system"},
		"spec":{"providerRef":{"name":"slack"},"eventSeverity":"error","eventSources":[{"kind":"Kustomization","name":"*"},{"kind":"ArtifactGenerator","name":"apps","matchLabels":{"a":"b"}}],
			"eventMetadata":{"cluster":"`+needle+`"},"exclusionList":["^.*retry.*$"]}}`))
	b, _ = json.Marshal(a)
	if strings.Contains(string(b), needle) || len(a.EventSources) != 2 || a.EventSources[1].MatchLabels != 1 || a.MetadataKeys[0] != "cluster" {
		t.Fatalf("alert: %s", b)
	}
	if l := AlertProvider(a, []Provider{p}, true); l.State != ProviderFound || !l.Suspended {
		t.Fatalf("a suspended provider: %+v", l)
	}
	if l := AlertProvider(a, nil, true); l.State != ProviderMissing {
		t.Fatalf("a missing provider: %+v", l)
	}
	if l := AlertProvider(a, nil, false); l.State != ProviderIncomplete {
		t.Fatalf("a capped read: %+v", l)
	}
	if s := SummaryOf(Dispatch([]string{"NotificationDispatchFailed", "NotificationDispatchFailed", "Migration"})); !strings.HasPrefix(s, "2 NotificationDispatchFailed") {
		t.Fatalf("dispatch evidence: %q", s)
	}
}
