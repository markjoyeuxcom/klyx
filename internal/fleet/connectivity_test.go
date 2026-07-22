package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
)

func connPod(ns, name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels}}
}

func connNS(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"kubernetes.io/metadata.name": name}}}
}

func TestConnectivityCanI_ResolvesPodsAndEvaluates(t *testing.T) {
	denyAll := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", Name: "deny-all"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{}, // empty = all pods in team-b
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	cs := k8sfake.NewSimpleClientset(
		connPod("team-a", "web-1", map[string]string{"app": "web"}),
		connPod("team-b", "db-1", map[string]string{"app": "db"}),
		connNS("team-a"), connNS("team-b"),
		denyAll,
	)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}

	r, err := c.ConnectivityCanI(context.Background(), "team-a", "web-1", "team-b", "db-1", 5432, "TCP")
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict.Allowed || r.Verdict.Ingress.Allowed {
		t.Fatalf("deny-all on dst must deny: %+v", r.Verdict)
	}
	if len(r.Verdict.Ingress.Policies) != 1 || r.Verdict.Ingress.Policies[0].Name != "deny-all" {
		t.Fatalf("policies: %+v", r.Verdict.Ingress.Policies)
	}
	if r.SrcLabels["app"] != "web" || r.DstLabels["app"] != "db" {
		t.Fatalf("labels must resolve from live pods: %+v %+v", r.SrcLabels, r.DstLabels)
	}
	if !strings.Contains(r.IngressExplain, "DENIED") {
		t.Fatalf("explain: %q", r.IngressExplain)
	}
	// No dynamic client wired: the Cilium count degrades to 0, never errors.
	if r.CiliumPolicies != 0 {
		t.Fatalf("cilium count: %d", r.CiliumPolicies)
	}
}

func TestConnectivityCanI_MissingPodIsHonest(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(connNS("team-a"))
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}
	_, err := c.ConnectivityCanI(context.Background(), "team-a", "ghost", "team-a", "also-ghost", 80, "TCP")
	if err == nil || !strings.Contains(err.Error(), "source pod") {
		t.Fatalf("missing pod must error naming the side: %v", err)
	}
}
