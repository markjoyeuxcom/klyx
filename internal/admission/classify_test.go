package admission

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The classifier's oracle (S3b §8): testdata/status holds every shape,
// built with the upstream code at k8s.io/apiserver v0.34.12, v0.35.9,
// v0.36.5 and v0.37.1 (MODULES records each module's hash; the generator
// lives in the session scratchpad, s3b-golden) and serialized as the API
// server sends it. The four versions build identical shapes, local ports
// normalised.

type shape struct {
	Name   string        `json:"name"`
	Status metav1.Status `json:"status"`
}

const lab = "deny.lab.klyx.dev"

var expected = map[string]Finding{
	"webhook-denied-message":  {Envelope: EnvWebhookDenied, Name: lab, Detail: "widgets labelled deny are refused"},
	"webhook-denied-reason":   {Envelope: EnvWebhookDenied, Name: lab, Detail: "WidgetPolicy"},
	"webhook-denied-noexpl":   {Envelope: EnvWebhookDenied, Name: lab},
	"webhook-denied-uid-text": {Envelope: EnvWebhookDenied, Name: lab, Detail: "see the guide: the uuid and fluid labels are required"},
	"dryrun-unsupported":      {Envelope: EnvDryRunUnsupported, Name: lab},
	"call-refused":            {Envelope: EnvWebhookCallFailed, Name: lab, Cause: CauseRefused},
	"call-x509":               {Envelope: EnvWebhookCallFailed, Name: lab, Cause: CauseTLS},
	"call-deadline":           {Envelope: EnvWebhookCallFailed, Name: lab, Cause: CauseDeadline},
	"call-client-timeout":     {Envelope: EnvWebhookCallFailed, Name: lab, Cause: CauseDeadline},
	"call-no-endpoints":       {Envelope: EnvWebhookCallFailed, Name: lab, Cause: CauseNoEndpoints, Detail: `no endpoints available for service "lab-hook"`},
	"call-rest-client":        {Envelope: EnvWebhookCallFailed, Name: lab, Cause: CauseOther, Detail: "could not get REST client: no client config"},
	"call-invalid-response":   {Envelope: EnvWebhookCallFailed, Name: lab, Cause: CauseOther, Detail: `received invalid webhook response: expected response.uid="a", got "b"`},
	"vap-binding":             {Envelope: EnvVAPDenied, Name: "deny-labelled", Binding: "deny-labelled-binding", Detail: "widgets labelled deny are refused"},
	"vap-nobinding":           {Envelope: EnvVAPDenied, Name: "deny-labelled", Detail: "widgets labelled deny are refused"},
	"vap-forbidden":           {Envelope: EnvVAPDenied, Name: "deny-labelled", Binding: "b", Detail: "refused"},
	"vap-unnamed":             {Envelope: EnvVAPDenied, Name: "deny-labelled", Binding: "b", Detail: "refused"},
	"vap-uid-text":            {Envelope: EnvVAPDenied, Name: "needs-uuid", Binding: "b", Detail: "the uuid label is required, see the guide"},
	"generic-binding":         {Envelope: EnvPolicyDenied, Name: "set-owner", Binding: "set-owner-b", Detail: "owner label missing"},
	"generic-nobinding":       {Envelope: EnvPolicyDenied, Name: "set-owner", Detail: "owner label missing"},
	"generic-several":         {Envelope: EnvPolicyDenied, Name: "first", Binding: "first-b", Detail: "one"},
	// a webhook's own cause naming a policy never re-attributes it
	"neg-webhook-vap-cause": {Envelope: EnvWebhookDenied, Name: lab, Detail: "refused"},
}

// the UID guards' refusals; every other shape is not one
var uidRefusals = map[string]bool{"uid-precondition-update": true, "uid-precondition-delete": true, "uid-immutable": true, "uid-not-found": true}

func TestClassifyAgreesWithTheServersShapes(t *testing.T) {
	versions := []string{"v0.34.12", "v0.35.9", "v0.36.5", "v0.37.1"}
	for _, v := range versions {
		b, err := os.ReadFile("testdata/status/shapes-" + v + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var shapes []shape
		if err := json.Unmarshal(b, &shapes); err != nil {
			t.Fatal(err)
		}
		if len(shapes) != 34 {
			t.Fatalf("%s: %d shapes", v, len(shapes))
		}
		for _, s := range shapes {
			got := Classify(s.Status)
			want := expected[s.Name]
			want.Code, want.Reason = s.Status.Code, string(s.Status.Reason)
			if strings.HasPrefix(s.Name, "call-") && want.Detail == "" {
				want.Detail = got.Detail // the cause's own text, checked below
			}
			if got != want {
				t.Errorf("%s %s: %+v, want %+v", v, s.Name, got, want)
			}
			if strings.Contains(got.Detail, "timeout=10s") || strings.Contains(got.Detail, "lab-hook.lab-s3b.svc") {
				t.Errorf("%s %s: a call failure's detail carries its URL: %q", v, s.Name, got.Detail)
			}
			if r := UIDRefusal(s.Status); r != uidRefusals[s.Name] {
				t.Errorf("%s %s: UIDRefusal %v", v, s.Name, r)
			}
		}
	}
	// every expectation names a shape the corpus has
	b, _ := os.ReadFile("testdata/status/shapes-v0.37.1.json")
	for name := range expected {
		if !strings.Contains(string(b), `"name": "`+name+`"`) {
			t.Errorf("no shape %s", name)
		}
	}
}

// What the old regex misread (S3b §1.2, defect 1): a refused connection
// behind the ?timeout= query reads refused, never a timeout.
func TestACallFailureIsReadAfterItsURL(t *testing.T) {
	st := metav1.Status{Code: 500, Reason: metav1.StatusReasonInternalError,
		Message: `Internal error occurred: failed calling webhook "h": failed to call webhook: Post "https://h.ns.svc:443/x?timeout=30s": dial tcp 10.0.0.1:443: connect: connection refused`}
	if f := Classify(st); f.Cause != CauseRefused || f.Detail != "dial tcp 10.0.0.1:443: connect: connection refused" {
		t.Fatalf("%+v", f)
	}
	// a quoted name with an escaped quote stays one name
	st.Message = `Internal error occurred: failed calling webhook "a\"b": failed to call webhook: Post "u": x509: bad`
	if f := Classify(st); f.Name != `a"b` || f.Cause != CauseTLS {
		t.Fatalf("%+v", f)
	}
	// the envelope's code and reason are part of its shape
	st.Code = 503
	if f := Classify(st); f.Attributed() {
		t.Fatalf("a 503 is not the call-failure envelope: %+v", f)
	}
}

// The envelopes' exact shapes (S3b §2.1): a policy's cause is attributed
// only behind NewForbidden's prefix and only as the message's whole tail;
// a webhook denying with NotFound is never the guarded object gone.
func TestTheEnvelopesAreExact(t *testing.T) {
	cause := "ValidatingAdmissionPolicy 'p' with binding 'b' denied request: no"
	vap := func(msg string) metav1.Status {
		return metav1.Status{Code: 422, Reason: metav1.StatusReasonInvalid, Message: msg, Details: &metav1.StatusDetails{Kind: "widgets", Causes: []metav1.StatusCause{{Message: cause}}}}
	}
	if f := Classify(vap(`widgets.lab "w" is forbidden: ` + cause)); f.Envelope != EnvVAPDenied {
		t.Fatalf("the NewForbidden form: %+v", f)
	}
	for _, msg := range []string{
		"note: " + cause,                                // not NewForbidden's prefix
		`widgets.lab "w" is forbidden: ` + cause + " x", // not the whole tail
		`widgets.lab is invalid: ` + cause,              // another prefix
	} {
		if f := Classify(vap(msg)); f.Attributed() {
			t.Errorf("%q attributed: %+v", msg, f)
		}
	}
	gone := metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound, Message: `admission webhook "h" denied the request: not found here`}
	if Classify(gone).Envelope != EnvWebhookDenied || UIDRefusal(gone) {
		t.Fatal("a webhook's NotFound denial is a denial, never the object gone")
	}
	if !UIDRefusal(metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound, Message: `deployments.apps "w" not found`}) {
		t.Fatal("the object gone")
	}
}
