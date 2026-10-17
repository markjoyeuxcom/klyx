package capability

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func operatorDep(ns, name string, ready bool, env []corev1.EnvVar, args ...string) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app.kubernetes.io/name": "flux-operator"}},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "manager", Env: env, Args: args}}}}}}
	if ready {
		d.Status.AvailableReplicas = 1
	}
	return d
}

var fieldRefNS = []corev1.EnvVar{{Name: "RUNTIME_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}}}

func detectOps(t *testing.T, objs ...runtime.Object) FluxOperatorInfo {
	t.Helper()
	cs := fake.NewSimpleClientset(objs...)
	return (&Detector{cs: cs}).detectFluxOperator(context.Background(), map[string]bool{fluxOperatorGroup: true})
}

// Coverage S1a §2, revision 2: the runtime namespace comes from the
// manager container, web-server-only binds nothing, and absence needs a
// complete scan.
func TestFluxOperatorDetection(t *testing.T) {
	info := detectOps(t,
		operatorDep("ops", "fo-chart", true, fieldRefNS),                                                        // chart form: the Deployment's namespace
		operatorDep("ops2", "fo-literal", true, []corev1.EnvVar{{Name: "RUNTIME_NAMESPACE", Value: "tenants"}}), // literal
		operatorDep("ops3", "fo-default", true, nil),                                                            // unset: flux-system
		operatorDep("ops4", "fo-web", true, fieldRefNS, "--web-server-only"),                                    // runs no reconcilers
	)
	if !info.Verified || !info.Complete || len(info.Operators) != 4 {
		t.Fatalf("info: %+v", info)
	}
	want := map[string]Binding{"ops": BindingBound, "tenants": BindingBound, "flux-system": BindingBound, "ops2": BindingOrphan, "ops4": BindingOrphan, "elsewhere": BindingOrphan}
	for ns, b := range want {
		if got := info.Bind(ns); got != b {
			t.Errorf("Bind(%s) = %s, want %s", ns, got, b)
		}
	}
	if info.Issue() != "" {
		t.Errorf("issue: %q", info.Issue())
	}

	// two reconciling operators for one namespace are ambiguous, and an issue
	two := detectOps(t, operatorDep("a", "x", true, nil), operatorDep("b", "y", true, nil))
	if two.Bind("flux-system") != BindingAmbiguous || two.Issue() == "" {
		t.Errorf("ambiguous: %s %q", two.Bind("flux-system"), two.Issue())
	}

	// an unresolvable source makes other namespaces unverified, never orphaned
	cm := detectOps(t, operatorDep("ops", "fo", true, []corev1.EnvVar{{Name: "RUNTIME_NAMESPACE",
		ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "ns"}}}}))
	if cm.Operators[0].RuntimeNamespace != "" || cm.Bind("anywhere") != BindingUnverified {
		t.Errorf("unresolvable: %+v %s", cm.Operators[0], cm.Bind("anywhere"))
	}

	// only a web-server-only Deployment: no reconcilers, an issue
	web := detectOps(t, operatorDep("ops", "fo-web", true, nil, "--web-server-only=true"))
	if web.Bind("flux-system") != BindingOrphan || web.Issue() == "" {
		t.Errorf("web only: %s %q", web.Bind("flux-system"), web.Issue())
	}

	// not ready
	if nr := detectOps(t, operatorDep("flux-system", "fo", false, nil)); nr.Issue() == "" {
		t.Error("a not-ready operator is an issue")
	}
}

func TestFluxOperatorScanLimits(t *testing.T) {
	// an incomplete list (a continue token) never declares absence
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		l := &appsv1.DeploymentList{ListMeta: metav1.ListMeta{Continue: "more"}}
		l.Items = append(l.Items, *operatorDep("ops", "fo", true, fieldRefNS))
		return true, l, nil
	})
	info := (&Detector{cs: cs}).detectFluxOperator(context.Background(), map[string]bool{fluxOperatorGroup: true})
	if info.Complete || info.Bind("flux-system") != BindingUnverified || info.Bind("ops") != BindingBound {
		t.Errorf("incomplete: %+v %s", info, info.Bind("flux-system"))
	}
	// a forbidden list is unverified, never absent
	cs = fake.NewSimpleClientset()
	cs.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	info = (&Detector{cs: cs}).detectFluxOperator(context.Background(), map[string]bool{fluxOperatorGroup: true})
	if info.Verified || info.Bind("flux-system") != BindingUnverified || info.Issue() == "" {
		t.Errorf("forbidden: %+v", info)
	}
	// not served: nothing
	if info := (&Detector{cs: fake.NewSimpleClientset()}).detectFluxOperator(context.Background(), map[string]bool{}); info.Present || info.Issue() != "" {
		t.Errorf("absent: %+v", info)
	}
}

// Flux is present with the operator alone; its controller probe follows a
// single resolved runtime namespace.
func TestGitOpsWithFluxOperator(t *testing.T) {
	kc := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: fluxKustomizeController}}
	kc.Status.AvailableReplicas = 1
	cs := fake.NewSimpleClientset(operatorDep("ops", "fo", true, fieldRefNS), kc)
	g := (&Detector{cs: cs}).detectGitOps(context.Background(), map[string]bool{fluxOperatorGroup: true, "kustomize.toolkit.fluxcd.io": true})
	if !g.Flux.Present || !g.Flux.Healthy || g.FluxIssue() != "" {
		t.Fatalf("operator-managed: %+v issue %q", g.Flux, g.FluxIssue())
	}
	only := (&Detector{cs: fake.NewSimpleClientset(operatorDep("flux-system", "fo", true, nil))}).detectGitOps(context.Background(), map[string]bool{fluxOperatorGroup: true})
	if !only.Flux.Present || !only.Flux.Healthy || only.FluxIssue() != "" {
		t.Fatalf("operator only: %+v issue %q", only.Flux, only.FluxIssue())
	}
}
