package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newCordonTestConn(nodeName string, initialUnschedulable bool) *ClusterConn {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, UID: types.UID("uid-" + nodeName)},
		Spec:       corev1.NodeSpec{Unschedulable: initialUnschedulable},
	}
	cs := fake.NewClientset(node)
	return &ClusterConn{name: "test", kubeContext: "test-ctx", typed: cs}
}

func unschedulable(t *testing.T, conn *ClusterConn, name string) bool {
	t.Helper()
	n, err := conn.typed.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	return n.Spec.Unschedulable
}

func TestSetCordon_Cordon(t *testing.T) {
	conn := newCordonTestConn("node-1", false)
	if err := conn.SetCordon(context.Background(), "node-1", "uid-node-1", true); err != nil {
		t.Fatalf("SetCordon(true) error: %v", err)
	}
	if !unschedulable(t, conn, "node-1") {
		t.Errorf("expected Unschedulable=true, got false")
	}
}

func TestSetCordon_Uncordon(t *testing.T) {
	conn := newCordonTestConn("node-2", true)
	if err := conn.SetCordon(context.Background(), "node-2", "uid-node-2", false); err != nil {
		t.Fatalf("SetCordon(false) error: %v", err)
	}
	if unschedulable(t, conn, "node-2") {
		t.Errorf("expected Unschedulable=false, got true")
	}
}

// The patch carries the armed UID as metadata.uid: the API server refuses
// it for a same-name replacement that lands after the pre-check.
func TestSetCordon_PatchCarriesTheArmedUID(t *testing.T) {
	conn := newCordonTestConn("node-1", false)
	var body string
	conn.typed.(*fake.Clientset).PrependReactor("patch", "nodes", func(a k8stesting.Action) (bool, runtime.Object, error) {
		body = string(a.(k8stesting.PatchAction).GetPatch())
		return false, nil, nil
	})
	if err := conn.SetCordon(context.Background(), "node-1", "uid-node-1", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"uid":"uid-node-1"`) || !strings.Contains(body, `"unschedulable":true`) {
		t.Fatalf("patch must name the armed UID: %s", body)
	}
}

// A verb armed against another object, or a node that is gone, writes
// nothing; an unarmed verb is refused.
func TestSetCordon_RefusesAnUnarmedOrReplacedNode(t *testing.T) {
	for _, tc := range []struct {
		name, node, uid, want string
		replaced              bool
	}{
		{"replaced", "node-1", "uid-old", "nothing was written", true},
		{"gone", "node-x", "uid-node-x", "nothing was written", true},
		{"unarmed", "node-1", "", "no object identity was armed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newCordonTestConn("node-1", false)
			patched := false
			conn.typed.(*fake.Clientset).PrependReactor("patch", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
				patched = true
				return false, nil, nil
			})
			err := conn.SetCordon(context.Background(), tc.node, tc.uid, true)
			if err == nil || errors.Is(err, ErrReplaced) != tc.replaced || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal (%s), got %v", tc.want, err)
			}
			if patched || unschedulable(t, conn, "node-1") {
				t.Fatal("a refused cordon must not write")
			}
		})
	}
}

// A replacement after the pre-check is refused by the server for the UID:
// ErrReplaced, honestly "refused", never "nothing was written" by guess.
// Other failures say whether the write may have landed.
func TestSetCordon_ReportsTheServersAnswer(t *testing.T) {
	uidRefusal := apierrors.NewInvalid(schema.GroupKind{Kind: "Node"}, "node-1",
		field.ErrorList{field.Invalid(field.NewPath("metadata", "uid"), "uid-node-1", "field is immutable")})
	for _, tc := range []struct {
		name     string
		err      error
		replaced bool
		want     string
	}{
		{"uid refused", uidRefusal, true, "its write was refused"},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "node-1", errors.New("rbac")), false, "nothing was written"},
		{"transport", errors.New("connection reset"), false, "the outcome is unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newCordonTestConn("node-1", false)
			conn.typed.(*fake.Clientset).PrependReactor("patch", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, tc.err
			})
			err := conn.SetCordon(context.Background(), "node-1", "uid-node-1", true)
			if err == nil || errors.Is(err, ErrReplaced) != tc.replaced || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q (replaced=%v), got %v", tc.want, tc.replaced, err)
			}
		})
	}
}

func TestCheckNodeArmed(t *testing.T) {
	conn := newCordonTestConn("node-1", false)
	if err := conn.CheckNodeArmed(context.Background(), "node-1", "uid-node-1"); err != nil {
		t.Fatalf("the armed node must pass: %v", err)
	}
	for _, c := range [][2]string{{"node-1", "uid-old"}, {"node-x", "uid-node-x"}} {
		if err := conn.CheckNodeArmed(context.Background(), c[0], c[1]); !errors.Is(err, ErrReplaced) {
			t.Fatalf("%v must be ErrReplaced, got %v", c, err)
		}
	}
	conn.typed.(*fake.Clientset).PrependReactor("get", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection reset")
	})
	if err := conn.CheckNodeArmed(context.Background(), "node-1", "uid-node-1"); err == nil || errors.Is(err, ErrReplaced) {
		t.Fatalf("a failed read is not a replacement: %v", err)
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
