package fleet

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/clibin"
)

// fakeKubectl pins kubectlBin to a known path (or to "missing") for the
// duration of one test, so argv assertions never depend on the machine's
// real kubectl install.
func fakeKubectl(t *testing.T, found bool) {
	t.Helper()
	old := kubectlBin
	t.Cleanup(func() { kubectlBin = old })
	if found {
		kubectlBin = clibin.Binary{
			Name:     "kubectl",
			LookPath: func(string) (string, error) { return "/fake/kubectl", nil },
		}
		return
	}
	kubectlBin = clibin.Binary{
		Name:             "kubectl",
		LookPath:         func(string) (string, error) { return "", errors.New("not found") },
		ExecutableExists: func(string) bool { return false },
	}
}

func TestDescribeCmd_NamespacedArgs(t *testing.T) {
	fakeKubectl(t, true)
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	cmd, err := conn.DescribeCmd(context.Background(), "pod", "default", "web-abc")
	if err != nil {
		t.Fatalf("DescribeCmd error: %v", err)
	}
	want := []string{"/fake/kubectl", "--context", "my-ctx", "describe", "pod", "-n", "default", "--", "web-abc"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("args:\n got %v\nwant %v", cmd.Args, want)
	}
}

func TestDescribeCmd_ClusterScopedOmitsNamespace(t *testing.T) {
	fakeKubectl(t, true)
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	cmd, err := conn.DescribeCmd(context.Background(), "node", "", "node-1")
	if err != nil {
		t.Fatalf("DescribeCmd error: %v", err)
	}
	want := []string{"/fake/kubectl", "--context", "my-ctx", "describe", "node", "--", "node-1"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("args:\n got %v\nwant %v", cmd.Args, want)
	}
}

func TestDescribeCmd_PluralGroupKindArg(t *testing.T) {
	fakeKubectl(t, true)
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	cmd, err := conn.DescribeCmd(context.Background(), "kustomizations.kustomize.toolkit.fluxcd.io", "flux-system", "apps")
	if err != nil {
		t.Fatalf("DescribeCmd error: %v", err)
	}
	if cmd.Args[4] != "kustomizations.kustomize.toolkit.fluxcd.io" {
		t.Errorf("kind arg: got %q", cmd.Args[4])
	}
}

func TestDescribeCmd_RefusesEmptyContext(t *testing.T) {
	// Empty kubeContext would make kubectl fall back to current-context and
	// describe an object on whatever cluster the terminal happens to point at.
	fakeKubectl(t, true)
	conn := &ClusterConn{name: "test", kubeContext: ""}
	if _, err := conn.DescribeCmd(context.Background(), "pod", "default", "web-abc"); err == nil {
		t.Fatal("expected error for empty kubeContext")
	}
}

func TestDescribeCmd_RefusesInjection(t *testing.T) {
	fakeKubectl(t, true)
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	cases := []struct{ kindArg, ns, name string }{
		// flag-shaped or whitespace-bearing kind args
		{"--all", "default", "web"},
		{"pod extra", "default", "web"},
		{"pod\n", "default", "web"},
		{"", "default", "web"},
		// flag-shaped, whitespace-bearing, or empty names
		{"pod", "default", "--all"},
		{"pod", "default", "-oyaml"},
		{"pod", "default", "web abc"},
		{"pod", "default", "web\nabc"},
		{"pod", "default", ""},
		// bad namespaces ("" is legal: cluster-scoped)
		{"pod", "-n", "web"},
		{"pod", "kube system", "web"},
	}
	for _, tc := range cases {
		if _, err := conn.DescribeCmd(context.Background(), tc.kindArg, tc.ns, tc.name); err == nil {
			t.Errorf("expected error for kindArg=%q ns=%q name=%q", tc.kindArg, tc.ns, tc.name)
		}
	}
}

func TestDescribeCmd_AcceptsRBACColonNames(t *testing.T) {
	// RBAC names are legal path segments, not DNS-1123 subdomains -
	// system:node-proxier must describe, not bounce off the validator.
	fakeKubectl(t, true)
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	cmd, err := conn.DescribeCmd(context.Background(), "clusterroles.rbac.authorization.k8s.io", "", "system:node-proxier")
	if err != nil {
		t.Fatalf("DescribeCmd error: %v", err)
	}
	if got := cmd.Args[len(cmd.Args)-1]; got != "system:node-proxier" {
		t.Errorf("trailing name: got %q", got)
	}
}

func TestDescribeCmd_NamePlacedAfterFlagTerminator(t *testing.T) {
	fakeKubectl(t, true)
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	cmd, err := conn.DescribeCmd(context.Background(), "pod", "default", "web-abc")
	if err != nil {
		t.Fatalf("DescribeCmd error: %v", err)
	}
	n := len(cmd.Args)
	if cmd.Args[n-2] != "--" || cmd.Args[n-1] != "web-abc" {
		t.Errorf("expected trailing \"-- web-abc\", got %v", cmd.Args)
	}
}

func TestDescribeCmd_KubectlMissingIsActionable(t *testing.T) {
	fakeKubectl(t, false)
	conn := &ClusterConn{name: "test", kubeContext: "my-ctx"}
	_, err := conn.DescribeCmd(context.Background(), "pod", "default", "web-abc")
	if err == nil {
		t.Fatal("expected error when kubectl is missing")
	}
	if got := err.Error(); !strings.Contains(got, "kubectl") || !strings.Contains(got, "KLYX_KUBECTL_PATH") {
		t.Errorf("error must name the binary and the override env var, got: %s", got)
	}
}

func TestValidateObjectName(t *testing.T) {
	valid := []string{"web-abc", "system:node-proxier", "my.config_map", "Node3", "a"}
	for _, name := range valid {
		if err := validateObjectName("object", name); err != nil {
			t.Errorf("expected %q valid, got: %v", name, err)
		}
	}
	invalid := []string{"", "--all", "-l", ":lead-colon", ".lead-dot", "a b", "a\tb", "a\nb", "a/b", "a%b", "a$b", strings.Repeat("a", 254)}
	for _, name := range invalid {
		if err := validateObjectName("object", name); err == nil {
			t.Errorf("expected %q invalid", name)
		}
	}
}
