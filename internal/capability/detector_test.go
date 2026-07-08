package capability

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func newFake(groups []*metav1.APIResourceList, objs ...runtime.Object) *fake.Clientset {
	cs := fake.NewSimpleClientset(objs...)
	cs.Discovery().(*discoveryfake.FakeDiscovery).Resources = groups
	return cs
}

func fluxControllerDeploy(name string, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "flux-system"},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: ready},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(1))},
	}
}

func argoControllerStatefulSet(name string, ready int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd"},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: ready},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(1))},
	}
}

func certManagerControllerDeploy(name string, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cert-manager"},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: ready},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(1))},
	}
}

func externalSecretsControllerDeploy(name string, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "external-secrets"},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: ready},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(1))},
	}
}

func ptr[T any](v T) *T { return &v }

func TestDetectCertManagerAbsent(t *testing.T) {
	cs := newFake(nil)
	set := NewDetector(cs).Detect(context.Background())
	if set.CertManager.Tier != Absent {
		t.Fatalf("want Absent, got %v", set.CertManager.Tier)
	}
	if set.CertManager.Present {
		t.Fatal("want not present")
	}
}

func TestDetectCertManagerPresentButUnhealthy(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "cert-manager.io/v1"}}
	cs := newFake(groups, certManagerControllerDeploy("cert-manager", 0))
	set := NewDetector(cs).Detect(context.Background())
	if set.CertManager.Tier != Degraded {
		t.Fatalf("want Degraded, got %v (%s)", set.CertManager.Tier, set.CertManager.Reason)
	}
	if set.CertManager.Reason == "" {
		t.Fatal("expected a reason for degraded cert-manager")
	}
	if !set.CertManager.Present {
		t.Fatal("want present")
	}
}

func TestDetectCertManagerHealthyWithACME(t *testing.T) {
	groups := []*metav1.APIResourceList{
		{GroupVersion: "cert-manager.io/v1"},
		{GroupVersion: "acme.cert-manager.io/v1"},
	}
	cs := newFake(groups, certManagerControllerDeploy("cert-manager", 1))
	set := NewDetector(cs).Detect(context.Background())
	if set.CertManager.Tier != Healthy {
		t.Fatalf("want Healthy, got %v (%s)", set.CertManager.Tier, set.CertManager.Reason)
	}
	if !set.CertManager.Present || !set.CertManager.Healthy {
		t.Fatalf("want present+healthy, got %+v", set.CertManager)
	}
	if !set.CertManager.HasACME {
		t.Fatal("want HasACME true when acme.cert-manager.io served")
	}
}

func TestDetectCertManagerHealthyWithoutACME(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "cert-manager.io/v1"}}
	cs := newFake(groups, certManagerControllerDeploy("cert-manager", 1))
	set := NewDetector(cs).Detect(context.Background())
	if set.CertManager.Tier != Healthy {
		t.Fatalf("want Healthy, got %v", set.CertManager.Tier)
	}
	if set.CertManager.HasACME {
		t.Fatal("want HasACME false when only cert-manager.io served")
	}
}

func TestDetectExternalSecretsAbsent(t *testing.T) {
	cs := newFake(nil)
	set := NewDetector(cs).Detect(context.Background())
	if set.ExternalSecrets.Tier != Absent {
		t.Fatalf("want Absent, got %v", set.ExternalSecrets.Tier)
	}
	if set.ExternalSecrets.Present {
		t.Fatal("want not present")
	}
}

func TestDetectExternalSecretsPresentButUnhealthy(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "external-secrets.io/v1"}}
	cs := newFake(groups, externalSecretsControllerDeploy("external-secrets", 0))
	set := NewDetector(cs).Detect(context.Background())
	if set.ExternalSecrets.Tier != Degraded {
		t.Fatalf("want Degraded, got %v (%s)", set.ExternalSecrets.Tier, set.ExternalSecrets.Reason)
	}
	if set.ExternalSecrets.Reason == "" {
		t.Fatal("expected a reason for degraded external-secrets")
	}
	if !set.ExternalSecrets.Present {
		t.Fatal("want present")
	}
}

func TestDetectExternalSecretsHealthy(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "external-secrets.io/v1beta1"}}
	cs := newFake(groups, externalSecretsControllerDeploy("external-secrets", 1))
	set := NewDetector(cs).Detect(context.Background())
	if set.ExternalSecrets.Tier != Healthy {
		t.Fatalf("want Healthy, got %v (%s)", set.ExternalSecrets.Tier, set.ExternalSecrets.Reason)
	}
	if !set.ExternalSecrets.Present || !set.ExternalSecrets.Healthy {
		t.Fatalf("want present+healthy, got %+v", set.ExternalSecrets)
	}
}

// TestDetectDiscoveryError proves the error-honesty branch: when ServerGroups()
// fails, every capability reads Absent with the discovery failure reason -- a
// discovery-degraded cluster must never render as "nothing installed".
func TestDetectDiscoveryError(t *testing.T) {
	cs := newFake(nil)
	cs.Discovery().(*discoveryfake.FakeDiscovery).PrependReactor("get", "group",
		func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("discovery unreachable")
		})

	set := NewDetector(cs).Detect(context.Background())

	caps := map[string]Base{
		"GitOps":          set.GitOps.Base,
		"Network":         set.Network.Base,
		"CertManager":     set.CertManager.Base,
		"ExternalSecrets": set.ExternalSecrets.Base,
	}
	for name, b := range caps {
		if b.Tier != Absent {
			t.Errorf("%s: want Absent on discovery error, got %v", name, b.Tier)
		}
		if b.Reason == "" {
			t.Errorf("%s: want a failure reason on discovery error, got empty", name)
		}
	}
}

func TestDetectFluxAbsent(t *testing.T) {
	cs := newFake(nil)
	d := NewDetector(cs)
	set := d.Detect(context.Background())
	if set.GitOps.Tier != Absent {
		t.Fatalf("want Absent, got %v", set.GitOps.Tier)
	}
}

func TestDetectFluxPresentButUnhealthy(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "kustomize.toolkit.fluxcd.io/v1"}}
	cs := newFake(groups, fluxControllerDeploy("kustomize-controller", 0))
	d := NewDetector(cs)
	set := d.Detect(context.Background())
	if set.GitOps.Tier != Degraded {
		t.Fatalf("want Degraded, got %v (%s)", set.GitOps.Tier, set.GitOps.Reason)
	}
	if set.GitOps.Reason == "" {
		t.Fatal("expected a reason for degraded flux")
	}
}

func TestDetectFluxHealthy(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "kustomize.toolkit.fluxcd.io/v1"}}
	cs := newFake(groups, fluxControllerDeploy("kustomize-controller", 1))
	d := NewDetector(cs)
	set := d.Detect(context.Background())
	if set.GitOps.Tier != Healthy {
		t.Fatalf("want Healthy, got %v", set.GitOps.Tier)
	}
	if !set.GitOps.Flux.Present || !set.GitOps.Flux.Healthy {
		t.Fatalf("want flux present+healthy, got %+v", set.GitOps.Flux)
	}
}

func TestDetectArgoHealthy(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "argoproj.io/v1alpha1"}}
	cs := newFake(groups, argoControllerStatefulSet("argocd-application-controller", 1))
	d := NewDetector(cs)
	set := d.Detect(context.Background())
	if set.GitOps.Tier != Healthy {
		t.Fatalf("want Healthy, got %v (%s)", set.GitOps.Tier, set.GitOps.Reason)
	}
	if !set.GitOps.Argo.Present || !set.GitOps.Argo.Healthy {
		t.Fatalf("want argo present+healthy, got %+v", set.GitOps.Argo)
	}
}

func TestDetectArgoPresentButUnhealthy(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "argoproj.io/v1alpha1"}}
	cs := newFake(groups, argoControllerStatefulSet("argocd-application-controller", 0))
	d := NewDetector(cs)
	set := d.Detect(context.Background())
	if set.GitOps.Tier != Degraded {
		t.Fatalf("want Degraded, got %v (%s)", set.GitOps.Tier, set.GitOps.Reason)
	}
	if set.GitOps.Reason == "" {
		t.Fatal("expected a non-empty reason for degraded argo")
	}
}

func TestDetectGatewayPresentWithoutEnvoyProxyIsDegraded(t *testing.T) {
	groups := []*metav1.APIResourceList{{GroupVersion: "gateway.networking.k8s.io/v1"}}
	cs := newFake(groups)
	d := NewDetector(cs)
	set := d.Detect(context.Background())
	if set.Network.Tier != Degraded {
		t.Fatalf("want Degraded (no EnvoyProxy), got %v", set.Network.Tier)
	}
	if set.Network.GatewayAPIVersion != "v1" {
		t.Fatalf("want pinned version v1, got %q", set.Network.GatewayAPIVersion)
	}
}

func TestDetectClusterMeshInstalled(t *testing.T) {
	// clustermesh-apiserver Deployment present -> ClusterMesh true.
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "clustermesh-apiserver", Namespace: "kube-system"},
	})
	d := NewDetector(cs)
	if !d.clusterMeshInstalled(context.Background()) {
		t.Fatal("clustermesh-apiserver deployment should mark ClusterMesh installed")
	}
	// Nothing present -> false.
	if NewDetector(fake.NewSimpleClientset()).clusterMeshInstalled(context.Background()) {
		t.Fatal("no apiserver/secret -> not installed")
	}
}

func TestDeploymentReady(t *testing.T) {
	cases := []struct {
		avail    int32
		replicas *int32
		want     bool
	}{
		{avail: 1, replicas: ptr(int32(1)), want: true},
		{avail: 0, replicas: ptr(int32(1)), want: false},
		{avail: 2, replicas: ptr(int32(3)), want: false},
		{avail: 1, replicas: nil, want: true}, // nil replicas defaults to 1
		{avail: 0, replicas: nil, want: false},
	}
	for _, tc := range cases {
		d := &appsv1.Deployment{
			Spec:   appsv1.DeploymentSpec{Replicas: tc.replicas},
			Status: appsv1.DeploymentStatus{AvailableReplicas: tc.avail},
		}
		if got := DeploymentReady(d); got != tc.want {
			t.Errorf("DeploymentReady(avail=%d, repl=%v)=%v want %v", tc.avail, tc.replicas, got, tc.want)
		}
	}
}

func TestStatefulSetReady(t *testing.T) {
	d := &appsv1.StatefulSet{
		Spec:   appsv1.StatefulSetSpec{Replicas: ptr(int32(1))},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	if !StatefulSetReady(d) {
		t.Fatal("want ready")
	}
	d.Status.ReadyReplicas = 0
	if StatefulSetReady(d) {
		t.Fatal("want not ready")
	}
}

func TestGitOpsTier(t *testing.T) {
	tier, reason := gitOpsTier(FluxInfo{Present: true, Healthy: true}, ArgoInfo{})
	if tier != Healthy || reason != "" {
		t.Fatalf("want Healthy/empty, got %v/%q", tier, reason)
	}
	tier, reason = gitOpsTier(FluxInfo{Present: true, Healthy: false}, ArgoInfo{})
	if tier != Degraded || reason == "" {
		t.Fatalf("want Degraded/reason, got %v/%q", tier, reason)
	}
}
