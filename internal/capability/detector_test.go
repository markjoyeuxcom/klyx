package capability

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// reloaderControllerDeploy builds a Reloader controller Deployment carrying the
// name label detectReloader selects on, in an arbitrary namespace/name (both vary
// by install layout).
func reloaderControllerDeploy(namespace, name string, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/name": "reloader"}},
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
		"Reloader":        set.Reloader.Base,
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

func TestDetectReloaderAbsent(t *testing.T) {
	// A non-Reloader Deployment must not be mistaken for the controller.
	other := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(1))},
	}
	cs := newFake(nil, other)
	set := NewDetector(cs).Detect(context.Background())
	if set.Reloader.Tier != Absent || set.Reloader.Present {
		t.Fatalf("want Absent+not present, got %+v", set.Reloader)
	}
}

func TestDetectReloaderPresentButUnhealthy(t *testing.T) {
	// Chart-style name; controller not ready.
	cs := newFake(nil, reloaderControllerDeploy("reloader", "reloader-reloader", 0))
	set := NewDetector(cs).Detect(context.Background())
	if set.Reloader.Tier != Degraded {
		t.Fatalf("want Degraded, got %v (%s)", set.Reloader.Tier, set.Reloader.Reason)
	}
	if !set.Reloader.Present || set.Reloader.Reason == "" {
		t.Fatalf("want present with a reason, got %+v", set.Reloader)
	}
}

func TestDetectReloaderHealthy(t *testing.T) {
	// Namespace/name independent: label match anywhere counts.
	cs := newFake(nil, reloaderControllerDeploy("tooling", "reloader", 1))
	set := NewDetector(cs).Detect(context.Background())
	if set.Reloader.Tier != Healthy || !set.Reloader.Present || !set.Reloader.Healthy {
		t.Fatalf("want present+healthy, got %+v", set.Reloader)
	}
}

func TestDetectReloaderListError(t *testing.T) {
	// Error honesty: a Deployments list failure reads Absent WITH a reason, never
	// a silent "not installed".
	cs := newFake(nil)
	cs.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("deployments is forbidden")
	})
	set := NewDetector(cs).Detect(context.Background())
	if set.Reloader.Tier != Absent {
		t.Fatalf("want Absent on list error, got %v", set.Reloader.Tier)
	}
	if set.Reloader.Reason == "" {
		t.Fatal("want a failure reason on list error (error honesty)")
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

// Review 2026-10-03 F: the Network tier is degraded only by a FAULT, and the
// data plane is detected through the implementation-agnostic GatewayClass
// contract - not by assuming Envoy Gateway.
func TestDetectNetworkTier(t *testing.T) {
	gw := []*metav1.APIResourceList{{GroupVersion: "gateway.networking.k8s.io/v1"}}
	cilium := []*metav1.APIResourceList{{GroupVersion: "cilium.io/v2"}}
	cases := []struct {
		name           string
		groups         []*metav1.APIResourceList
		probe          func(context.Context, string) (gatewayClassCounts, error)
		wantTier       Tier
		wantUnverified bool
	}{
		{"non-Envoy data plane accepted its class", gw, func(context.Context, string) (gatewayClassCounts, error) { return gatewayClassCounts{1, 1, 0}, nil }, Healthy, false},
		{"classes exist, every one rejected", gw, func(context.Context, string) (gatewayClassCounts, error) { return gatewayClassCounts{2, 0, 2}, nil }, Degraded, false},
		// Codex 2c review P1: pending or unreported acceptance proves nothing.
		{"classes exist, none accepted yet", gw, func(context.Context, string) (gatewayClassCounts, error) { return gatewayClassCounts{2, 0, 1}, nil }, Degraded, true},
		{"Gateway API CRDs but no classes (not in use)", gw, func(context.Context, string) (gatewayClassCounts, error) { return gatewayClassCounts{}, nil }, Healthy, false},
		{"classes unreadable, no recognised data plane", gw, func(context.Context, string) (gatewayClassCounts, error) {
			return gatewayClassCounts{}, errors.New("forbidden")
		}, Degraded, true},
		// Unreadable acceptance is unknown: CRD presence cannot establish it (R4).
		{"classes unreadable, Cilium present", append(append([]*metav1.APIResourceList{}, gw...), cilium...), func(context.Context, string) (gatewayClassCounts, error) {
			return gatewayClassCounts{}, errors.New("forbidden")
		}, Degraded, true},
		{"Cilium without Gateway API is not a fault", cilium, nil, Healthy, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDetector(newFake(tc.groups))
			var gotVersion string
			if tc.probe != nil {
				d.gatewayClasses = func(ctx context.Context, v string) (gatewayClassCounts, error) {
					gotVersion = v
					return tc.probe(ctx, v)
				}
			}
			set := d.Detect(context.Background())
			if set.Network.Tier != tc.wantTier {
				t.Fatalf("tier = %v (%s), want %v", set.Network.Tier, set.Network.Reason, tc.wantTier)
			}
			if tc.probe != nil && gotVersion != "v1" {
				t.Fatalf("probe must use the served version, got %q", gotVersion)
			}
			if tc.wantTier == Degraded && set.Network.Reason == "" {
				t.Fatal("a degraded tier must say why")
			}
			if set.Network.GatewayUnverified != tc.wantUnverified {
				t.Fatalf("unverified = %v (%s), want %v", set.Network.GatewayUnverified, set.Network.Reason, tc.wantUnverified)
			}
		})
	}
}

// Codex batch-2 review R4: an Accepted=True that lags the spec is not current.
func TestCountAcceptedGatewayClasses_StaleAcceptanceDoesNotCount(t *testing.T) {
	raw := []byte(`{"items":[
		{"metadata":{"generation":2},"status":{"conditions":[{"type":"Accepted","status":"True","observedGeneration":1}]}},
		{"metadata":{"generation":3},"status":{"conditions":[{"type":"Accepted","status":"True","observedGeneration":3}]}}]}`)
	gc, err := countAcceptedGatewayClasses(raw)
	if err != nil || gc.Total != 2 || gc.Accepted != 1 {
		t.Fatalf("got %+v err=%v, want 2/1", gc, err)
	}
}

func TestCountAcceptedGatewayClasses(t *testing.T) {
	raw := []byte(`{"items":[
		{"status":{"conditions":[{"type":"Accepted","status":"True"}]}},
		{"status":{"conditions":[{"type":"Accepted","status":"False"}]}},
		{"status":{}}]}`)
	gc, err := countAcceptedGatewayClasses(raw)
	if err != nil || gc != (gatewayClassCounts{Total: 3, Accepted: 1, Rejected: 1}) {
		t.Fatalf("got %+v err=%v", gc, err)
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

func TestDetectHubbleInstalled(t *testing.T) {
	// hubble-relay Deployment -> Hubble installed.
	relay := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "hubble-relay", Namespace: "kube-system"},
	})
	if !NewDetector(relay).hubbleInstalled(context.Background()) {
		t.Fatal("hubble-relay deployment should mark Hubble installed")
	}
	// hubble-peer Service alone (Hubble on, relay off) -> installed.
	peer := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "hubble-peer", Namespace: "kube-system"},
	})
	if !NewDetector(peer).hubbleInstalled(context.Background()) {
		t.Fatal("hubble-peer service should mark Hubble installed")
	}
	// Nothing -> false.
	if NewDetector(fake.NewSimpleClientset()).hubbleInstalled(context.Background()) {
		t.Fatal("no relay/peer -> not installed")
	}

	// And end-to-end: Cilium served + relay present -> HasHubble true.
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "hubble-relay", Namespace: "kube-system"},
	})
	cs.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{{GroupVersion: "cilium.io/v2"}}
	set := NewDetector(cs).Detect(context.Background())
	if !set.Network.HasHubble {
		t.Fatalf("HasHubble must be detected: %+v", set.Network)
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

// Codex 2c review P1: only a controller workload actually read is evidence.
// Forbidden, a transport failure and a missing conventional workload are
// unverified, never "not ready"; a workload read adverse is not ready.
func TestControllerHealthIsClaimedOnlyFromAWorkloadRead(t *testing.T) {
	cm := []*metav1.APIResourceList{{GroupVersion: "cert-manager.io/v1"}, {GroupVersion: "karpenter.sh/v1"}}
	forbidden := func(cs *fake.Clientset) {
		cs.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "cert-manager", errors.New("rbac"))
		})
	}
	transport := func(cs *fake.Clientset) {
		cs.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("dial tcp 10.0.0.1:443: connect: connection refused")
		})
	}
	cases := []struct {
		name           string
		objs           []runtime.Object
		react          func(*fake.Clientset)
		healthy, known bool
		reason         string
	}{
		{"read and ready", []runtime.Object{certManagerControllerDeploy("cert-manager", 1)}, nil, true, true, ""},
		{"read and not ready", []runtime.Object{certManagerControllerDeploy("cert-manager", 0)}, nil, false, true, "cert-manager installed but cert-manager is not ready"},
		{"forbidden", nil, forbidden, false, false, "health is unverified"},
		{"transport failure", nil, transport, false, false, "connection refused"},
		{"no workload under the conventional name", nil, nil, false, false, "no Deployment cert-manager/cert-manager"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newFake(cm, tc.objs...)
			if tc.react != nil {
				tc.react(cs)
			}
			set := NewDetector(cs).Detect(context.Background())
			c := set.CertManager
			if c.Healthy != tc.healthy || c.Verified != tc.known || !strings.Contains(c.Reason, tc.reason) {
				t.Fatalf("healthy=%v verified=%v reason=%q", c.Healthy, c.Verified, c.Reason)
			}
			if !tc.known && strings.Contains(c.Reason, "is not ready") {
				t.Fatalf("an unverified controller is never called not ready: %q", c.Reason)
			}
			// Karpenter: no deployment in either conventional namespace is
			// unverified, as the Karpenter lens says.
			if tc.name == "no workload under the conventional name" {
				k := set.Karpenter
				if k.Verified || !strings.Contains(k.Reason, "no Deployment kube-system/karpenter; no Deployment karpenter/karpenter") {
					t.Fatalf("karpenter: verified=%v reason=%q", k.Verified, k.Reason)
				}
			}
		})
	}
}

// Codex 2c re-review: a condition without a current observedGeneration is no
// verdict - neither Accepted nor Rejected - on a class with a generation.
func TestCountAcceptedGatewayClassesNeedsACurrentObservation(t *testing.T) {
	raw := []byte(`{"items":[
		{"metadata":{"generation":2},"status":{"conditions":[{"type":"Accepted","status":"True"}]}},
		{"metadata":{"generation":2},"status":{"conditions":[{"type":"Accepted","status":"False"}]}},
		{"metadata":{"generation":3},"status":{"conditions":[{"type":"Accepted","status":"False","observedGeneration":2}]}},
		{"metadata":{"generation":3},"status":{"conditions":[{"type":"Accepted","status":"False","observedGeneration":3}]}}]}`)
	gc, err := countAcceptedGatewayClasses(raw)
	if err != nil || gc != (gatewayClassCounts{Total: 4, Accepted: 0, Rejected: 1}) {
		t.Fatalf("only the current False is a verdict: %+v err=%v", gc, err)
	}
}

// Coverage S7b §7: the experimental group's served kinds are recognised,
// deduplicated across versions and sorted, without subresources; a version
// whose resources cannot be read is named as a gap.
func TestExperimentalGatewayKinds(t *testing.T) {
	groups := []*metav1.APIResourceList{
		{GroupVersion: "gateway.networking.k8s.io/v1"},
		{GroupVersion: "gateway.networking.x-k8s.io/v1alpha1", APIResources: []metav1.APIResource{
			{Name: "xmeshes", Kind: "XMesh"}, {Name: "xmeshes/status", Kind: "XMesh"},
			{Name: "xlistenersets", Kind: "XListenerSet"}, {Name: "xbackendtrafficpolicies", Kind: "XBackendTrafficPolicy"}}},
		{GroupVersion: "gateway.networking.x-k8s.io/v1alpha2", APIResources: []metav1.APIResource{{Name: "xmeshes", Kind: "XMesh"}}},
	}
	set := NewDetector(newFake(groups)).Detect(context.Background())
	if got := strings.Join(set.Network.GatewayExperimentalKinds, ","); got != "XBackendTrafficPolicy,XListenerSet,XMesh" || set.Network.GatewayExperimentalGap != "" {
		t.Fatalf("kinds = %q gap = %q", got, set.Network.GatewayExperimentalGap)
	}

	cs := newFake(groups)
	cs.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	gap := NewDetector(cs).Detect(context.Background())
	if len(gap.Network.GatewayExperimentalKinds) != 0 || !strings.Contains(gap.Network.GatewayExperimentalGap, "x-k8s.io resources at v1alpha1, v1alpha2 could not be read") {
		t.Fatalf("an unreadable version is a gap: %v %q", gap.Network.GatewayExperimentalKinds, gap.Network.GatewayExperimentalGap)
	}

	none := NewDetector(newFake(groups[:1])).Detect(context.Background())
	if none.Network.GatewayExperimentalKinds != nil || none.Network.GatewayExperimentalGap != "" {
		t.Fatalf("no experimental group, nothing to say: %+v", none.Network)
	}
}
