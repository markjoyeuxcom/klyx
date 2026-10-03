package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/helmcli"
)

// Review 2026-10-03 #1: client-go honours a cluster's `kubeconfig:` selection,
// so every kubectl/helm/flux subprocess must resolve its context through the
// SAME files - otherwise a context name that also exists in the default
// kubeconfig (k3s's "default") silently targets another cluster.

// writeKubeconfig writes a minimal kubeconfig whose context "shared" points at
// server, and returns its path.
func writeKubeconfig(t *testing.T, name, server string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	raw := "apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster:\n    server: " + server +
		"\ncontexts:\n- name: shared\n  context:\n    cluster: c\n    user: u\nusers:\n- name: u\n  user:\n    token: synthetic\ncurrent-context: shared\n"
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func envHas(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// TestDefaultConnFactory_KubeconfigReachesCLI is Codex's repro: the factory
// resolved the custom file for client-go, and the exec argv must name it too.
func TestDefaultConnFactory_KubeconfigReachesCLI(t *testing.T) {
	fakeKubectl(t, true)
	p := writeKubeconfig(t, "custom", "https://dev.synthetic.invalid")
	conn, err := DefaultConnFactory(clock.Real{})(config.ClusterConfig{Name: "dev", Context: "shared", Kubeconfig: p})
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*ClusterConn)
	if c.restConfig.Host != "https://dev.synthetic.invalid" {
		t.Fatalf("fixture did not resolve the custom kubeconfig: %q", c.restConfig.Host)
	}
	argv, err := c.ExecCommand("default", "synthetic", "app")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/fake/kubectl", "--kubeconfig", p, "--context", "shared"}
	if !reflect.DeepEqual(argv[:5], want) {
		t.Errorf("exec argv lost the cluster's kubeconfig:\n got %v\nwant prefix %v", argv, want)
	}
}

func TestTerminalArgv_KubeconfigForms(t *testing.T) {
	fakeKubectl(t, true)
	sep := string(os.PathListSeparator)

	t.Run("none configured keeps default resolution", func(t *testing.T) {
		c := &ClusterConn{kubeContext: "ctx"}
		argv, err := c.DebugCommand("ns", "pod", "")
		if err != nil {
			t.Fatal(err)
		}
		if argv[0] != "/fake/kubectl" || argv[1] != "--context" {
			t.Errorf("want bare kubectl when no kubeconfig is configured, got %v", argv)
		}
	})

	t.Run("single file uses --kubeconfig", func(t *testing.T) {
		c := (&ClusterConn{kubeContext: "ctx"}).WithKubeconfig("/k/dev.yaml")
		argv, err := c.DebugCommand("ns", "pod", "")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"/fake/kubectl", "--kubeconfig", "/k/dev.yaml", "--context", "ctx"}; !reflect.DeepEqual(argv[:5], want) {
			t.Errorf("got %v, want prefix %v", argv, want)
		}
	})

	t.Run("path list uses env KUBECONFIG", func(t *testing.T) {
		prev := terminalGOOS
		t.Cleanup(func() { terminalGOOS = prev })
		terminalGOOS = "darwin"
		c := (&ClusterConn{kubeContext: "ctx"}).WithKubeconfig("/k/dev.yaml" + sep + sep + "/k/shared.yaml")
		argv, err := c.ExecCommand("ns", "pod", "")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"env", "KUBECONFIG=/k/dev.yaml" + sep + "/k/shared.yaml", "/fake/kubectl", "--context", "ctx"}
		if !reflect.DeepEqual(argv[:5], want) {
			t.Errorf("got %v, want prefix %v", argv, want)
		}
	})

	t.Run("path list refused on windows", func(t *testing.T) {
		prev := terminalGOOS
		t.Cleanup(func() { terminalGOOS = prev })
		terminalGOOS = "windows"
		c := (&ClusterConn{kubeContext: "ctx"}).WithKubeconfig("/k/a" + sep + "/k/b")
		if _, err := c.ExecCommand("ns", "pod", ""); err == nil || !strings.Contains(err.Error(), "path list") {
			t.Fatalf("want a path-list refusal on windows, got %v", err)
		}
	})

	t.Run("kubectl not found leaves the bare name for the terminal PATH", func(t *testing.T) {
		fakeKubectl(t, false)
		c := &ClusterConn{kubeContext: "ctx"}
		argv, err := c.ExecCommand("ns", "pod", "")
		if err != nil {
			t.Fatal(err)
		}
		if argv[0] != "kubectl" {
			t.Errorf("want bare kubectl fallback, got %q", argv[0])
		}
	})
}

func TestDrainAndDescribe_CarryKubeconfigEnv(t *testing.T) {
	fakeKubectl(t, true)
	c := (&ClusterConn{kubeContext: "ctx"}).WithKubeconfig("/k/dev.yaml")

	drain, err := c.DrainNodeCmd("node-1", DrainOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !envHas(drain.Env, "KUBECONFIG=/k/dev.yaml") {
		t.Error("drain child must run with the cluster's KUBECONFIG")
	}
	desc, err := c.DescribeCmd(context.Background(), "pod", "ns", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !envHas(desc.Env, "KUBECONFIG=/k/dev.yaml") {
		t.Error("describe child must run with the cluster's KUBECONFIG")
	}

	// No kubeconfig configured: inherit the environment unchanged (nil Env), so
	// the child resolves exactly as client-go's default loading rules did.
	plain := &ClusterConn{kubeContext: "ctx"}
	drain2, err := plain.DrainNodeCmd("node-1", DrainOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if drain2.Env != nil {
		t.Errorf("no kubeconfig: want inherited env (nil), got %d entries", len(drain2.Env))
	}
}

func TestHelm_CarriesKubeconfigEnv(t *testing.T) {
	r := &captureRunner{out: []byte(`[]`)}
	defer withHelmRunner(r)()
	c := (&ClusterConn{name: "test", kubeContext: "ctx"}).WithKubeconfig("/k/dev.yaml")

	if _, err := c.HelmReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.HelmRollback(context.Background(), "ns", "rel", 2, helmcli.RollbackOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(r.envs) != 2 {
		t.Fatalf("want 2 runner builds, got %d", len(r.envs))
	}
	for i, env := range r.envs {
		if !envHas(env, "KUBECONFIG=/k/dev.yaml") {
			t.Errorf("helm call %d built without the cluster's KUBECONFIG: %v", i, env)
		}
	}
}

func TestFluxDiff_CarriesKubeconfigEnv(t *testing.T) {
	prev := newFluxRunner
	t.Cleanup(func() { newFluxRunner = prev })
	var got []string
	newFluxRunner = func(env []string) fluxcli.Runner {
		got = env
		return &fakeFluxRunner{stdout: "± drift", err: errors.New("exit 1")}
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), ksListKinds(), seedKs("app", true, "True", "./apps"))
	c := newActionConn(dyn).WithKubeconfig("/k/dev.yaml")
	if _, err := c.FluxDiffKustomization(context.Background(), "flux-system", "app", ""); err != nil {
		t.Fatal(err)
	}
	if !envHas(got, "KUBECONFIG=/k/dev.yaml") {
		t.Errorf("flux diff built without the cluster's KUBECONFIG: %v", got)
	}
}
