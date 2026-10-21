package fleet

import (
	"context"
	"testing"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/identity"
	"github.com/moomora/klyx/internal/keda"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Implementation review 2 (Codex's probes, kept): the workload half of the
// inverse join holds only with the forward join's proof - a proven handler
// that sees the authentication - and a cut trigger list or an unread
// operator inventory makes the read partial.
func review2WorkloadObserver(triggers []any) *fakeKEDAObs {
	f := newFakeKEDAObs()
	for _, kind := range []string{healthsrc.KindTriggerAuths, healthsrc.KindClusterTriggerAuths} {
		f.states[kind] = f.states[healthsrc.KindScaledObjects]
		f.complete[kind] = true
	}
	a := keda.ProjectAuth(kedaObj("TriggerAuthentication", "team", "workload", "ta-1", map[string]any{"podIdentity": map[string]any{"provider": "aws", "identityOwner": "workload"}}, nil), false)
	f.objs[healthsrc.KindTriggerAuths] = []any{a}
	s := keda.ProjectScaledObject(kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": triggers}, nil))
	f.objs[healthsrc.KindScaledObjects] = []any{s}
	return f
}

func TestReview2WorkloadIdentityNeedsTheForwardProof(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []corev1.EnvVar
	}{
		{"no-handler", []corev1.EnvVar{{Name: keda.EnvWatchNamespace, Value: "elsewhere"}}},
		{"excluded-authentication", []corev1.EnvVar{{Name: keda.EnvWatchAuthSelector, Value: "selected=yes"}}},
		{"unverified-handler", []corev1.EnvVar{{Name: keda.EnvWatchNamespace, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", tc.env, nil)
			f := review2WorkloadObserver([]any{authRef("workload", "")})
			got, read := c.kedaAuthsFor(context.Background(), f, "team", "wl")
			if len(got) > 0 {
				t.Errorf("inverse join claims a proven role assumption without the forward proof: %+v (%s)", got, read)
			}
			if tc.name == "unverified-handler" && read == identity.ReadComplete {
				t.Error("unverified handler certified a complete inverse read")
			}
		})
	}
}

func TestReview2WorkloadInverseTriggerCutIsPartial(t *testing.T) {
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, nil)
	var triggers []any
	for i := 0; i < 16; i++ {
		triggers = append(triggers, map[string]any{"type": "cpu"})
	}
	triggers = append(triggers, authRef("workload", ""))
	f := review2WorkloadObserver(triggers)
	if !f.objs[healthsrc.KindScaledObjects][0].(*keda.ScaledObject).TriggersCut {
		t.Fatal("control: no trigger cut")
	}
	got, read := c.kedaAuthsFor(context.Background(), f, "team", "wl")
	if len(got) != 0 {
		t.Fatal("control: cut reference somehow retained")
	}
	if read != identity.ReadPartial {
		t.Fatalf("a workload authentication beyond the trigger cap disappeared from a supposedly complete inverse join: read=%s", read)
	}
}

func TestReview2UnreadOperatorsDoNotProveAnEmptyInverse(t *testing.T) {
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, nil)
	// Detection could not LIST Deployments; the known-good previous pods
	// are not proof of the current candidate inventory.
	c.caps.KEDA.OperatorsRead = false
	c.caps.KEDA.Operators = nil
	f := review2WorkloadObserver([]any{authRef("workload", "")})
	// This one uses its operator's base credential, rather than a workload role.
	a := f.objs[healthsrc.KindTriggerAuths][0].(*keda.Auth)
	a.PodIdentity.IdentityOwner = "keda"
	got, read := c.kedaAuthsFor(context.Background(), f, "keda", "keda-operator")
	if len(got) != 0 {
		t.Fatal("control: unexpectedly found a known handler")
	}
	if read != identity.ReadPartial {
		t.Fatalf("unread operator Deployments produced a complete empty inverse report: %s", read)
	}
}

func TestReview2WorkloadInversePositiveControl(t *testing.T) {
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, nil)
	f := review2WorkloadObserver([]any{authRef("workload", "")})
	got, read := c.kedaAuthsFor(context.Background(), f, "team", "wl")
	if len(got) != 1 || read != identity.ReadComplete {
		t.Fatalf("valid current handler and visible authentication should link: %+v (%s)", got, read)
	}
}

// The workload half with the operator inventory unread: no proof, partial.
func TestReview2WorkloadInverseWithUnreadOperators(t *testing.T) {
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, nil)
	c.caps.KEDA.OperatorsRead = false
	c.caps.KEDA.Operators = nil
	f := review2WorkloadObserver([]any{authRef("workload", "")})
	if got, read := c.kedaAuthsFor(context.Background(), f, "team", "wl"); len(got) != 0 || read != identity.ReadPartial {
		t.Fatalf("a workload link without an operator read: %+v (%s)", got, read)
	}
}

// The forward proof's edges: no operator read proves nothing; no handler
// is a proven absence (no link, complete); a ScaledObject in another
// namespace never lends this namespace's ServiceAccount.
func TestReview2WorkloadProofEdges(t *testing.T) {
	a := keda.ProjectAuth(kedaObj("TriggerAuthentication", "team", "workload", "ta-1", map[string]any{"podIdentity": map[string]any{"provider": "aws", "identityOwner": "workload"}}, nil), false)
	s := keda.ProjectScaledObject(kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}}, nil))
	if hd, vis := (kedaHandling{}).sees(&s.Scaler, a); hd != nil || vis != keda.Unknown {
		t.Fatalf("no operator read: %v %v", hd, vis)
	}
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", []corev1.EnvVar{{Name: keda.EnvWatchNamespace, Value: "elsewhere"}}, nil)
	if got, read := c.kedaAuthsFor(context.Background(), review2WorkloadObserver([]any{authRef("workload", "")}), "team", "wl"); len(got) != 0 || read != identity.ReadComplete {
		t.Fatalf("no handler: %+v (%s)", got, read)
	}
	c = kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, nil)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "web"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: "wl"}}}}
	if _, err := c.typed.AppsV1().Deployments("other").Create(context.Background(), dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f := newFakeKEDAObs()
	for _, kind := range []string{healthsrc.KindTriggerAuths, healthsrc.KindClusterTriggerAuths} {
		f.states[kind] = f.states[healthsrc.KindScaledObjects]
		f.complete[kind] = true
	}
	f.objs[healthsrc.KindTriggerAuths] = []any{keda.ProjectAuth(kedaObj("TriggerAuthentication", "other", "workload", "ta-2", map[string]any{"podIdentity": map[string]any{"provider": "aws", "identityOwner": "workload"}}, nil), false)}
	f.objs[healthsrc.KindScaledObjects] = []any{keda.ProjectScaledObject(kedaObj("ScaledObject", "other", "web", "so-2", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": []any{authRef("workload", "")}}, nil))}
	if got, read := c.kedaAuthsFor(context.Background(), f, "team", "wl"); len(got) != 0 || read != identity.ReadComplete {
		t.Fatalf("another namespace's ScaledObject: %+v (%s)", got, read)
	}
	if got, _ := c.kedaAuthsFor(context.Background(), f, "other", "wl"); len(got) != 1 {
		t.Fatalf("control: the ScaledObject's own namespace: %+v", got)
	}
}
