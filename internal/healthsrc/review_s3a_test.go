package healthsrc

import (
	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
	"testing"
)

func TestReviewS3aStaleEmptyBindingsAreUnknown(t *testing.T) {
	p := &admission.Policy{Meta: admission.Meta{Name: "new-policy", UID: "p", Generation: 1}, Kind: admission.Validating, HasStatus: true, ObservedGeneration: 1}
	c := observe.Capture{Rows: true, Objects: map[string][]any{KindValidatingPolicies: {p}, KindValidatingBindings: {}}, Complete: map[string]bool{KindValidatingPolicies: true, KindValidatingBindings: false, KindMutatingBindings: true}}
	r := evalAdmissionPolicies("c")(c)
	got := r.Rows.(RowSet[AdmissionPolicyRow]).Rows[0].Eval
	if got.Role != health.Unknown {
		t.Fatalf("stale empty bindings cannot prove this live policy has no effect: %+v", got)
	}
}
