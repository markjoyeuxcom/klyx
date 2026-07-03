package fleet

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newCordonTestConn(nodeName string, initialUnschedulable bool) *ClusterConn {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Spec:       corev1.NodeSpec{Unschedulable: initialUnschedulable},
	}
	cs := fake.NewSimpleClientset(node)
	return &ClusterConn{name: "test", kubeContext: "test-ctx", typed: cs}
}

func TestSetCordon_Cordon(t *testing.T) {
	conn := newCordonTestConn("node-1", false)
	if err := conn.SetCordon(context.Background(), "node-1", true); err != nil {
		t.Fatalf("SetCordon(true) error: %v", err)
	}
	n, err := conn.typed.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if !n.Spec.Unschedulable {
		t.Errorf("expected Unschedulable=true, got false")
	}
}

func TestSetCordon_Uncordon(t *testing.T) {
	conn := newCordonTestConn("node-2", true)
	if err := conn.SetCordon(context.Background(), "node-2", false); err != nil {
		t.Fatalf("SetCordon(false) error: %v", err)
	}
	n, err := conn.typed.CoreV1().Nodes().Get(context.Background(), "node-2", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.Spec.Unschedulable {
		t.Errorf("expected Unschedulable=false, got true")
	}
}

func TestDrainNodeCmd_BuildsArgs(t *testing.T) {
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	cmd, err := conn.DrainNodeCmd("node-3", DrainOptions{IgnoreDaemonSets: true, DeleteEmptyDirData: true, Force: true, TimeoutSeconds: 300})
	if err != nil {
		// If kubectl is not in PATH in the test environment, skip gracefully.
		t.Skipf("kubectl not in PATH: %v", err)
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	args := cmd.Args
	// args[0] is the binary path; rest are flags
	checkArg := func(want string) {
		for _, a := range args {
			if a == want {
				return
			}
		}
		t.Errorf("expected arg %q in %v", want, args)
	}
	checkArg("--context")
	checkArg("my-ctx")
	checkArg("drain")
	checkArg("node-3")
	checkArg("--ignore-daemonsets")
	checkArg("--delete-emptydir-data")
	checkArg("--force")
	checkArg("--timeout=300s")
	// The node name must come after the "--" flag terminator so it can never
	// be parsed as a flag.
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
		}
	}
	if sep == -1 || args[len(args)-1] != "node-3" || sep != len(args)-2 {
		t.Errorf("expected trailing \"-- node-3\", got %v", args)
	}
}

func TestDrainNodeCmd_RefusesEmptyContext(t *testing.T) {
	// Empty kubeContext would make kubectl fall back to current-context - a
	// drain aimed at whatever cluster the user's terminal happens to point at.
	conn := &ClusterConn{name: "test", kubeContext: ""}
	if _, err := conn.DrainNodeCmd("node-3", DrainOptions{}); err == nil {
		t.Fatal("expected error for empty kubeContext")
	}
}

func TestDrainNodeCmd_RefusesInvalidNames(t *testing.T) {
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	// Flag-shaped and control-character names must be refused before they can
	// reach an argv ("--all" would drain every node in the cluster).
	for _, name := range []string{"--all", "-l", "node 3", "node\nreboot", "Node3", ""} {
		if _, err := conn.DrainNodeCmd(name, DrainOptions{}); err == nil {
			t.Errorf("expected error for node name %q", name)
		}
	}
}

func TestDrainNodeCmd_SafeDefaults(t *testing.T) {
	// The zero-value options are the SAFE drain: no emptyDir destruction
	// (flipped from hardcoded-on 2026-07-03), no force, no daemonset toleration,
	// 120s timeout.
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	cmd, err := conn.DrainNodeCmd("node-3", DrainOptions{})
	if err != nil {
		t.Skipf("kubectl not in PATH: %v", err)
	}
	for _, a := range cmd.Args {
		if a == "--delete-emptydir-data" || a == "--force" || a == "--ignore-daemonsets" {
			t.Fatalf("safe default must not include %q: %v", a, cmd.Args)
		}
	}
	found := false
	for _, a := range cmd.Args {
		if a == "--timeout=120s" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want default --timeout=120s: %v", cmd.Args)
	}
}
