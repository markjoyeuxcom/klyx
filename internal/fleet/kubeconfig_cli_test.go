package fleet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/clibin"
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

	t.Run("no recorded selection (conn built outside the factory) is plain kubectl", func(t *testing.T) {
		c := &ClusterConn{kubeContext: "ctx"}
		argv, err := c.DebugCommand("ns", "pod", "", DebugOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if argv[0] != "/fake/kubectl" || argv[1] != "--context" {
			t.Errorf("want bare kubectl when no kubeconfig is configured, got %v", argv)
		}
	})

	t.Run("single file uses --kubeconfig", func(t *testing.T) {
		c := (&ClusterConn{kubeContext: "ctx"}).WithKubeconfig("/k/dev.yaml")
		argv, err := c.DebugCommand("ns", "pod", "", DebugOptions{})
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

	// No recorded selection (a conn built outside the factory): inherit the
	// environment unchanged (nil Env).
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

// Codex's batch-1 review (R1/R2): the selection must be the EFFECTIVE one,
// absolute. With no `kubeconfig:` configured, client-go used Klyx's inherited
// KUBECONFIG - a terminal's own shell may export a different one. A relative
// selection was resolved against Klyx's working directory - a terminal starts
// in another.

func TestEffectiveSelection_InheritedKUBECONFIGIsPinned(t *testing.T) {
	fakeKubectl(t, true)
	selected := writeKubeconfig(t, "selected-dev", "https://dev.synthetic.invalid")
	t.Setenv("KUBECONFIG", selected)

	conn, err := DefaultConnFactory(clock.Real{})(config.ClusterConfig{Name: "dev", Context: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*ClusterConn)
	if c.restConfig.Host != "https://dev.synthetic.invalid" {
		t.Fatalf("fixture: client-go resolved %q", c.restConfig.Host)
	}
	argv, err := c.ExecCommand("ns", "pod", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/fake/kubectl", "--kubeconfig", selected}; !reflect.DeepEqual(argv[:3], want) {
		t.Errorf("an inherited selection must still be pinned: got %v, want prefix %v", argv, want)
	}
	drain, err := c.DrainNodeCmd("node-1", DrainOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !envHas(drain.Env, "KUBECONFIG="+selected) {
		t.Error("drain must be pinned to the inherited selection too")
	}
}

func TestEffectiveSelection_RelativeSelectionIsMadeAbsolute(t *testing.T) {
	fakeKubectl(t, true)
	appDir := t.TempDir()
	dev := writeKubeconfig(t, "dev", "https://dev.synthetic.invalid")
	b, err := os.ReadFile(dev)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "selection"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(appDir)
	wd, _ := os.Getwd()

	conn, err := DefaultConnFactory(clock.Real{})(config.ClusterConfig{Name: "dev", Context: "shared", Kubeconfig: "selection"})
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*ClusterConn)
	argv, err := c.ExecCommand("ns", "pod", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := argv[2], filepath.Join(wd, "selection"); got != want {
		t.Errorf("relative selection must travel absolute: got %q, want %q", got, want)
	}
}

func TestTerminalArgv_RelativeKubectlOverrideIsMadeAbsolute(t *testing.T) {
	old := kubectlBin
	t.Cleanup(func() { kubectlBin = old })
	kubectlBin = clibin.Binary{Name: "kubectl", LookPath: func(string) (string, error) { return "bin/kubectl", nil }}
	wd, _ := os.Getwd()

	argv, err := (&ClusterConn{kubeContext: "ctx"}).ExecCommand("ns", "pod", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(wd, "bin", "kubectl"); argv[0] != want {
		t.Errorf("a relative kubectl must travel absolute: got %q, want %q", argv[0], want)
	}
}

// The end-to-end probes Codex used: run the generated kubectl prefix with a
// read-only `config view --minify` from a terminal-like environment that has a
// DIFFERENT KUBECONFIG and working directory, and require it to resolve the
// same server client-go did. Synthetic files only; no API call is made.
func TestTerminalArgv_RealKubectlResolvesTheSameCluster(t *testing.T) {
	if _, ok := kubectlBin.Resolve(); !ok {
		t.Skip("kubectl unavailable for the synthetic config-view probe")
	}
	server := func(t *testing.T, argv []string, env []string, dir string) string {
		t.Helper()
		end := 0
		for i, a := range argv {
			if a == "exec" {
				end = i
				break
			}
		}
		if end == 0 {
			t.Fatalf("no exec verb in %v", argv)
		}
		args := append(append([]string{}, argv[1:end]...), "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
		cmd := exec.Command(argv[0], args...)
		cmd.Env, cmd.Dir = env, dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("config view: %v: %s", err, out)
		}
		return string(out)
	}
	terminalEnv := func(kubeconfig string) []string {
		var env []string
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "KUBECONFIG=") {
				env = append(env, e)
			}
		}
		return append(env, "KUBECONFIG="+kubeconfig)
	}

	t.Run("inherited selection vs a terminal's own KUBECONFIG", func(t *testing.T) {
		selected := writeKubeconfig(t, "selected-dev", "https://dev.synthetic.invalid")
		terminal := writeKubeconfig(t, "terminal-prd", "https://prd.synthetic.invalid")
		t.Setenv("KUBECONFIG", selected)
		conn, err := DefaultConnFactory(clock.Real{})(config.ClusterConfig{Name: "dev", Context: "shared"})
		if err != nil {
			t.Fatal(err)
		}
		c := conn.(*ClusterConn)
		argv, err := c.ExecCommand("ns", "pod", "")
		if err != nil {
			t.Fatal(err)
		}
		if got := server(t, argv, terminalEnv(terminal), ""); got != c.restConfig.Host {
			t.Errorf("client-go=%s but the terminal command resolved %s (argv %v)", c.restConfig.Host, got, argv)
		}
	})

	t.Run("relative selection vs a terminal's own working directory", func(t *testing.T) {
		appDir, termDir := t.TempDir(), t.TempDir()
		for _, pair := range [][2]string{
			{writeKubeconfig(t, "dev", "https://dev.synthetic.invalid"), appDir},
			{writeKubeconfig(t, "prd", "https://prd.synthetic.invalid"), termDir},
		} {
			b, err := os.ReadFile(pair[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pair[1], "selection"), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Chdir(appDir)
		conn, err := DefaultConnFactory(clock.Real{})(config.ClusterConfig{Name: "dev", Context: "shared", Kubeconfig: "selection"})
		if err != nil {
			t.Fatal(err)
		}
		c := conn.(*ClusterConn)
		argv, err := c.ExecCommand("ns", "pod", "")
		if err != nil {
			t.Fatal(err)
		}
		if got := server(t, argv, terminalEnv(filepath.Join(termDir, "selection")), termDir); got != c.restConfig.Host {
			t.Errorf("client-go=%s but the terminal command resolved %s (argv %v)", c.restConfig.Host, got, argv)
		}
	})
}
