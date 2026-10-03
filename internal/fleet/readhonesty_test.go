package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// Review 2026-10-03 (K): the fleet-ops lenses silently dropped failed reads,
// so a missing input read as "clean". Absence (NotFound: the operator is not
// installed) stays silent; any OTHER failure is surfaced.

func forbidden(resource string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("RBAC: access denied"))
}

func listKinds(gvrs ...schema.GroupVersionResource) map[schema.GroupVersionResource]string {
	m := map[schema.GroupVersionResource]string{}
	for _, g := range gvrs {
		m[g] = strings.ToUpper(g.Resource[:1]) + g.Resource[1:] + "List"
	}
	return m
}

func TestExpiryScan_FailedCertManagerReadIsAWarning(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(cmCertGVRScan))
	dyn.PrependReactor("list", "certificates", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden("certificates")
	})
	c := &ClusterConn{typed: k8sfake.NewSimpleClientset(), dyn: dyn, clk: clock.NewFake(time.Unix(0, 0))}
	r, err := c.ExpiryScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(r.Warnings, "cert-manager certificates could not be read") {
		t.Fatalf("want a named warning, got %v", r.Warnings)
	}

	// Not installed (NotFound) stays silent.
	dyn2 := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(cmCertGVRScan))
	dyn2.PrependReactor("list", "certificates", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "")
	})
	c2 := &ClusterConn{typed: k8sfake.NewSimpleClientset(), dyn: dyn2, clk: clock.NewFake(time.Unix(0, 0))}
	r2, _ := c2.ExpiryScan(context.Background())
	if hasWarning(r2.Warnings, "cert-manager") {
		t.Fatalf("an absent cert-manager is not a warning: %v", r2.Warnings)
	}
}

func TestConnectivity_FailedCiliumCountMakesVerdictPartial(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(
		connPod("team-a", "web-1", map[string]string{"app": "web"}),
		connPod("team-b", "db-1", map[string]string{"app": "db"}),
		connNS("team-a"), connNS("team-b"),
	)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(connCNPGVR, connCCNPGVR))
	dyn.PrependReactor("list", "ciliumnetworkpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden("ciliumnetworkpolicies")
	})
	c := &ClusterConn{typed: cs, dyn: dyn, clk: clock.NewFake(time.Unix(0, 0))}
	r, err := c.ConnectivityCanI(context.Background(), "team-a", "web-1", "team-b", "db-1", 5432, "TCP")
	if err != nil {
		t.Fatal(err)
	}
	if r.CiliumUnknown == "" {
		t.Fatal("a failed Cilium count must leave the verdict partial with the reason, not read as 0 policies")
	}
}

func TestUpgradeReadiness_UnreadableNodesIsAWarning(t *testing.T) {
	cs := k8sfake.NewSimpleClientset()
	cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden("nodes")
	})
	fd := cs.Discovery().(*discoveryfake.FakeDiscovery)
	fd.FakedServerVersion = &version.Info{GitVersion: "v1.31.4"}
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}
	r, err := c.UpgradeReadiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Skew) != 0 || !hasWarning(r.Warnings, "kubelet skew unknown") {
		t.Fatalf("no skew findings must say the node list was unreadable: skew=%v warnings=%v", r.Skew, r.Warnings)
	}
}

func TestMeshMember_UnreadablePeersIsANote(t *testing.T) {
	cfg := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cilium-config", Namespace: "kube-system"},
		Data: map[string]string{"cluster-name": "prd-weu"}}
	typed := k8sfake.NewSimpleClientset(cfg)
	typed.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden("secrets")
	})
	c := NewClusterConn("prd-weu", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	m, st := c.MeshMember(context.Background())
	if !strings.Contains(m.ReadNote, "peers could not be read") || st.Note != m.ReadNote {
		t.Fatalf("an RBAC-denied peers Secret means peering UNKNOWN: note=%q", m.ReadNote)
	}

	// An absent Secret (no peering configured) carries no note.
	plain := NewClusterConn("lab", k8sfake.NewSimpleClientset(cfg), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	if m2, _ := plain.MeshMember(context.Background()); m2.ReadNote != "" {
		t.Fatalf("absence is not a read failure: %q", m2.ReadNote)
	}
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
