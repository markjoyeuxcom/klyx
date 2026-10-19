package fleet

import (
	"strings"
	"testing"
)

func TestExecCommand(t *testing.T) {
	fakeKubectl(t, true)
	conn := &ClusterConn{kubeContext: "prod-aks"}

	t.Run("with container", func(t *testing.T) {
		got, err := conn.ExecCommand("default", "web-abc", "web")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{
			"/fake/kubectl",
			"--context", "prod-aks",
			"-n", "default",
			"exec", "-it", "web-abc",
			"-c", "web",
			"--",
			"/bin/sh", "-c",
			"command -v bash >/dev/null && exec bash || exec sh",
		}
		if len(got) != len(want) {
			t.Fatalf("argv length mismatch: got %d want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("argv[%d]: got %q want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("without container", func(t *testing.T) {
		got, err := conn.ExecCommand("monitoring", "grafana-xyz", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{
			"/fake/kubectl",
			"--context", "prod-aks",
			"-n", "monitoring",
			"exec", "-it", "grafana-xyz",
			"--",
			"/bin/sh", "-c",
			"command -v bash >/dev/null && exec bash || exec sh",
		}
		if len(got) != len(want) {
			t.Fatalf("argv length mismatch: got %d want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("argv[%d]: got %q want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("empty kubeContext error", func(t *testing.T) {
		empty := &ClusterConn{kubeContext: ""}
		_, err := empty.ExecCommand("default", "pod-xyz", "app")
		if err == nil {
			t.Fatal("expected error for empty kubeContext, got nil")
		}
		const want = "cluster has no kubeconfig context"
		if err.Error() != want {
			t.Errorf("error message: got %q want %q", err.Error(), want)
		}
	})
}

func TestDebugCommand(t *testing.T) {
	fakeKubectl(t, true)
	c := &ClusterConn{kubeContext: "kubernetes-admin@homelab-nelli"}
	argv, err := c.DebugCommand("cert-manager", "cert-manager-7d65955cff-c8s5r", "cert-manager-controller", DebugOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/fake/kubectl", "--context", "kubernetes-admin@homelab-nelli",
		"-n", "cert-manager", "debug", "-it", "cert-manager-7d65955cff-c8s5r",
		"--image=busybox:1.36", "--profile=general", "--target=cert-manager-controller", "--", "sh",
	}
	if len(argv) != len(want) {
		t.Fatalf("argv: got %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d]: got %q, want %q", i, argv[i], want[i])
		}
	}
}

func TestDebugCommandNoTarget(t *testing.T) {
	c := &ClusterConn{kubeContext: "ctx"}
	argv, _ := c.DebugCommand("ns", "pod", "", DebugOptions{})
	for _, a := range argv {
		if strings.HasPrefix(a, "--target") {
			t.Fatalf("empty container must omit --target: %v", argv)
		}
	}
}

func TestDebugCommandNoContext(t *testing.T) {
	c := &ClusterConn{}
	if _, err := c.DebugCommand("ns", "pod", "c", DebugOptions{}); err == nil {
		t.Fatal("want error without kube context")
	}
}

func TestExecDebugCommandRefuseInvalidNames(t *testing.T) {
	c := &ClusterConn{kubeContext: "ctx"}
	// Flag-shaped or control-character names must never reach the argv (or the
	// shell-quoting layer above it, which trusts its inputs are names).
	bad := []struct{ ns, pod, container string }{
		{"--all-namespaces", "pod", ""},
		{"ns", "--all", ""},
		{"ns", "pod\nreboot", ""},
		{"ns", "pod", "-c evil"},
		{"ns", "", ""},
	}
	for _, b := range bad {
		if _, err := c.ExecCommand(b.ns, b.pod, b.container); err == nil {
			t.Errorf("ExecCommand(%q,%q,%q): want error", b.ns, b.pod, b.container)
		}
		if _, err := c.DebugCommand(b.ns, b.pod, b.container, DebugOptions{}); err == nil {
			t.Errorf("DebugCommand(%q,%q,%q): want error", b.ns, b.pod, b.container)
		}
	}
	// Real names still pass.
	if _, err := c.ExecCommand("kube-system", "coredns-5d78c9869d-x2m4p", "coredns"); err != nil {
		t.Fatalf("valid names refused: %v", err)
	}
}
