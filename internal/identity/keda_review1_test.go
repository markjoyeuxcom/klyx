package identity

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/keda"
)

// Implementation review 1, finding 10 (Codex's probe, kept): an explicit
// Azure tenant is part of the credential KEDA builds, so two tenants are
// two subjects; none is the operator's default, said; a value outside the
// tenant shape is never shown.
func TestReview1AzureTenantChangesTheRequestedSubject(t *testing.T) {
	subject := func(tenant string) Subject {
		pi := map[string]any{"provider": "azure-workload", "identityId": "11111111-1111-1111-1111-111111111111"}
		if tenant != "" {
			pi["identityTenantId"] = tenant
		}
		a := keda.ProjectAuth(&unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "TriggerAuthentication",
			"metadata": map[string]any{"namespace": "team", "name": "azure"}, "spec": map[string]any{"podIdentity": pi}}}, false)
		return TriggerAuthSubject(a.PodIdentity, &SARef{Namespace: "keda", Name: "operator"}, nil, nil)
	}
	enc := func(s Subject) string { b, _ := json.Marshal(s); return string(b) }
	a, b := subject("22222222-2222-2222-2222-222222222222"), subject("33333333-3333-3333-3333-333333333333")
	if enc(a) == enc(b) {
		t.Fatalf("two distinct requested Azure tenants have identical subjects: %s", enc(a))
	}
	if strings.Join(a.Requested, "; ") != "client 11111111-1111-1111-1111-111111111111; tenant 22222222-2222-2222-2222-222222222222" {
		t.Fatalf("explicit tenant: %v", a.Requested)
	}
	if d := subject(""); len(d.Requested) != 1 || !strings.Contains(strings.Join(d.Notes, "; "), "the operator's default tenant") {
		t.Fatalf("default tenant: %+v", d)
	}
	if odd := subject("not a tenant!"); strings.Contains(enc(odd), "not a tenant!") {
		t.Fatalf("an unexpected tenant value was shown: %s", enc(odd))
	}
}
