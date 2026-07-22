package fleet

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	discoveryfake "k8s.io/client-go/discovery/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
)

func upgradeNode(name, ver string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: ver}},
	}
}

func TestUpgradeReadiness(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(
		upgradeNode("fresh", "v1.31.4"),
		upgradeNode("ancient", "v1.27.1"),
	)
	fd := cs.Discovery().(*discoveryfake.FakeDiscovery)
	fd.FakedServerVersion = &version.Info{GitVersion: "v1.31.4"}
	fd.Resources = []*metav1.APIResourceList{
		// A still-served deprecated GV from the curated table.
		{GroupVersion: "flowcontrol.apiserver.k8s.io/v1beta3", APIResources: []metav1.APIResource{{Name: "flowschemas", Kind: "FlowSchema"}}},
		// A modern GV that must NOT be reported.
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment"}}},
	}

	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}
	report, err := c.UpgradeReadiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.ServerVersion != "v1.31.4" || report.ServerMinor != 31 {
		t.Fatalf("version: %+v", report)
	}
	if report.EOLDate != "2025-10-28" {
		t.Fatalf("eol: %+v", report)
	}
	if len(report.Skew) != 1 || report.Skew[0].Node != "ancient" || report.Skew[0].Level != "danger" {
		t.Fatalf("skew: %+v", report.Skew)
	}
	// Only the SERVED deprecated entries appear (both flowcontrol v1beta3 rows
	// of the table match the served GV).
	if len(report.Deprecated) != 2 {
		t.Fatalf("deprecated: %+v", report.Deprecated)
	}
	for _, d := range report.Deprecated {
		if d.Group != "flowcontrol.apiserver.k8s.io" || d.Version != "v1beta3" || d.RemovedInMinor != 32 {
			t.Fatalf("entry: %+v", d)
		}
		// meta client absent in this conn: the count degrades honestly.
		if d.CountKnown {
			t.Fatalf("count must be unknown without a metadata client: %+v", d)
		}
	}
}
