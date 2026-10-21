package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// Coverage S3b §2.4, §3, §5: the converter.

type fakeAdmissionRows struct {
	rows     map[string]observe.Rows
	coverage observe.Coverage
	asOf     *time.Time
}

func (f fakeAdmissionRows) HealthRowsNow(_ context.Context, _, part string) (observe.Rows, bool) {
	r, ok := f.rows[part]
	for i := range r.Part.Kinds {
		r.Part.Kinds[i].Coverage, r.Part.Kinds[i].AsOf = f.coverage, f.asOf
	}
	return r, ok
}

func admissionFixture() fakeAdmissionRows {
	rule := func(group, resource string) any {
		return map[string]any{"operations": []any{"UPDATE"}, "apiGroups": []any{group}, "apiVersions": []any{"v1"}, "resources": []any{resource}}
	}
	cfg := func(name string, hooks ...any) *admission.Config {
		return admission.ProjectConfig(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": name, "uid": "u-" + name}, "webhooks": hooks}}, admission.Validating)
	}
	a := cfg("lab-a", map[string]any{"name": "deny.lab.klyx.dev", "rules": []any{rule("apps", "deployments")}}, map[string]any{"name": "other.lab", "rules": []any{rule("apps", "statefulsets")}})
	b := cfg("lab-b", map[string]any{"name": "deny.lab.klyx.dev", "rules": []any{rule("", "configmaps")}})
	p := admission.ProjectPolicy(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "deny-labelled", "uid": "u-p"},
		"spec": map[string]any{"matchConstraints": map[string]any{"resourceRules": []any{rule("apps", "deployments")}}}}}, admission.Validating)
	kinds := []observe.KindCoverage{{Kind: "validatingwebhookconfigurations", Role: "counted"}}
	return fakeAdmissionRows{coverage: observe.Live, rows: map[string]observe.Rows{
		"webhooks": {Part: observe.Part{Kinds: kinds}, Rows: healthsrc.RowSet[healthsrc.AdmissionConfigRow]{Rows: []healthsrc.Row[healthsrc.AdmissionConfigRow]{
			{Data: healthsrc.AdmissionConfigRow{Config: a}}, {Data: healthsrc.AdmissionConfigRow{Config: b}}}}},
		"policies": {Part: observe.Part{Kinds: append([]observe.KindCoverage(nil), kinds...)}, Rows: healthsrc.RowSet[healthsrc.AdmissionPolicyRow]{Rows: []healthsrc.Row[healthsrc.AdmissionPolicyRow]{
			{Data: healthsrc.AdmissionPolicyRow{Policy: p}}}}},
	}}
}

func denial(msg string) error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{Code: 403, Message: `admission webhook "deny.lab.klyx.dev" denied the request: ` + msg}}
}

func TestTheFindingNamesTheRefusalAndItsCandidates(t *testing.T) {
	q := admission.Request{Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "lab"}
	err := fmt.Errorf("scale Deployment lab/api: %w - nothing was written", admission.Wrap(q, "Deployment", "lab", "api", denial("label deny is refused")))
	text, f := writeError(context.Background(), admissionFixture(), err)
	if text != err.Error() || f == nil {
		t.Fatalf("an unprotected subject keeps today's text and gains the finding: %q %+v", text, f)
	}
	if f.Envelope != admission.EnvWebhookDenied || f.Summary != `update Deployment lab/api was refused by admission before it was stored: denied by admission webhook "deny.lab.klyx.dev"` ||
		f.Message != "label deny is refused" || !f.NothingWritten || f.Inventory != "live" {
		t.Fatalf("%+v", f)
	}
	if f.Correlation != "a webhook named deny.lab.klyx.dev in lab-a and lab-b" {
		t.Fatalf("a name in two configurations is a correlation with both: %q", f.Correlation)
	}
	// the attributed rows first (both configurations), then the other
	// candidates: the policy; other.lab's rules name statefulsets
	var got []string
	for _, c := range f.Candidates {
		got = append(got, fmt.Sprintf("%s %s/%s attributed=%v may=%q", c.Kind, c.Config, c.Name, c.Attributed, c.MayMatch))
	}
	want := []string{
		`ValidatingWebhook lab-a/deny.lab.klyx.dev attributed=true may=""`,
		`ValidatingWebhook lab-b/deny.lab.klyx.dev attributed=true may=""`,
		`ValidatingAdmissionPolicy /deny-labelled attributed=false may=""`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") || f.Aliases != 1 {
		t.Fatalf("candidates:\n%s\naliases %d", strings.Join(got, "\n"), f.Aliases)
	}
}

func TestAProtectedSubjectKeepsStructuredFactsOnly(t *testing.T) {
	const needle = "NeedleSecretValue7x"
	for _, tc := range []struct{ group, version, resource, kind string }{
		{"", "v1", "secrets", "Secret"},
		{"helm.toolkit.fluxcd.io", "v2", "helmreleases", "HelmRelease"},
	} {
		q := admission.Request{Operation: "UPDATE", Group: tc.group, Version: tc.version, Resource: tc.resource, Namespace: "lab"}
		err := fmt.Errorf("reconcile %s lab/x: %w", tc.kind, admission.Wrap(q, tc.kind, "lab", "x", denial("value "+needle+" is not allowed")))
		text, f := writeError(context.Background(), admissionFixture(), err)
		b, _ := json.Marshal(ActionResultDTO{Error: text, Admission: f})
		if strings.Contains(string(b), needle) {
			t.Fatalf("%s: the denial's text crossed: %s", tc.kind, b)
		}
		if f == nil || f.Name != "deny.lab.klyx.dev" || f.Message != "" || !strings.Contains(f.Withheld, "withheld for a "+tc.kind) || !strings.Contains(text, `denied by admission webhook "deny.lab.klyx.dev"`) {
			t.Fatalf("%s: the webhook's name stays, the message is withheld: %q %+v", tc.kind, text, f)
		}
	}
	// an unattributed refusal of a protected subject is withheld too
	q := admission.Request{Operation: "UPDATE", Version: "v1", Resource: "secrets", Namespace: "lab"}
	err := admission.Wrap(q, "Secret", "lab", "x", apierrors.NewBadRequest("bad value "+needle))
	if text, f := writeError(context.Background(), admissionFixture(), err); strings.Contains(text, needle) || f != nil {
		t.Fatalf("%q %+v", text, f)
	}
}

func TestTheInventorysStateQualifiesTheCandidates(t *testing.T) {
	q := admission.Request{Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "lab"}
	err := admission.Wrap(q, "Deployment", "lab", "api", denial("no"))
	fx := admissionFixture()
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	fx.coverage, fx.asOf = observe.Stale, &at
	if _, f := writeError(context.Background(), fx, err); f.Inventory != "stale" || f.InventoryAsOf != at.Unix() || len(f.Candidates) == 0 {
		t.Fatalf("stale: %+v", f)
	}
	fx.coverage = observe.Coverage("unreadable")
	if _, f := writeError(context.Background(), fx, err); f.Inventory != "unread" || len(f.Candidates) != 0 || !strings.Contains(f.Correlation, "not in Klyx's admission inventory") {
		t.Fatalf("unread: %+v", f)
	}
	// no live view: the finding stands, its candidates not established
	if _, f := writeError(context.Background(), struct{}{}, err); f == nil || f.Inventory != "unread" {
		t.Fatalf("no reader: %+v", f)
	}
	// a long message is clipped after the scrub
	long := strings.Repeat("x", 600) + " password=hunter2"
	if _, f := writeError(context.Background(), admissionFixture(), admission.Wrap(q, "Deployment", "lab", "api", denial("password=hunter2 "+long))); strings.Contains(f.Message, "hunter2") || len(f.Message) > 512+len("…") || !strings.HasSuffix(f.Message, "…") {
		t.Fatalf("the message is scrubbed and clipped: %q", f.Message)
	}
	// a refusal that is not attributed keeps today's text and no finding
	if text, f := writeError(context.Background(), admissionFixture(), admission.Wrap(q, "Deployment", "lab", "api", apierrors.NewInternalError(fmt.Errorf("etcd")))); f != nil || !strings.Contains(text, "etcd") {
		t.Fatalf("%q %+v", text, f)
	}
}

// An ad-hoc document's refused CREATE carries its finding; its text stays
// today's, or the withheld form for a protected subject.
func TestAnAdhocRefusalCarriesItsFinding(t *testing.T) {
	q := admission.Request{Operation: "CREATE", Version: "v1", Resource: "configmaps", Namespace: "lab"}
	text, f := adhocFinding(context.Background(), admissionFixture(), "today's text", admission.Wrap(q, "ConfigMap", "lab", "c", denial("no")))
	if text != "today's text" || f == nil || f.Request.Operation != "CREATE" {
		t.Fatalf("%q %+v", text, f)
	}
	sq := admission.Request{Operation: "CREATE", Version: "v1", Resource: "secrets", Namespace: "lab"}
	text, f = adhocFinding(context.Background(), admissionFixture(), "raw NeedleAdhoc7x", admission.Wrap(sq, "Secret", "lab", "s", denial("NeedleAdhoc7x")))
	if strings.Contains(text, "NeedleAdhoc7x") || f == nil || f.Message != "" {
		t.Fatalf("a protected subject: %q %+v", text, f)
	}
	if text, f := adhocFinding(context.Background(), admissionFixture(), "parse error", nil); text != "parse error" || f != nil {
		t.Fatalf("no refusal: %q %+v", text, f)
	}
}
