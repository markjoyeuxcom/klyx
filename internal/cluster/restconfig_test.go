package cluster

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/moomora/klyx/internal/config"
)

func TestRESTConfigResolvesContext(t *testing.T) {
	cc := config.ClusterConfig{
		Name:       "plt-sea-prd-we-aks-01",
		Context:    "prd-we",
		Kubeconfig: filepath.Join("testdata", "kubeconfig.yaml"),
	}
	rc, err := RESTConfig(cc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Host != "https://prd-we.example:6443" {
		t.Fatalf("want resolved host, got %q", rc.Host)
	}
}

func TestRESTConfigErrorsOnMissingContext(t *testing.T) {
	cc := config.ClusterConfig{
		Name:       "x",
		Context:    "does-not-exist",
		Kubeconfig: filepath.Join("testdata", "kubeconfig.yaml"),
	}
	if _, err := RESTConfig(cc); err == nil {
		t.Fatal("expected error for unknown context")
	}
}

func TestRESTConfigFailsClosedOnEmptyContext(t *testing.T) {
	// An empty context must be refused before building the config, never fall
	// back to the kubeconfig's current-context (possibly a protected cluster).
	cc := config.ClusterConfig{
		Name:       "plt-sea-prd-we-aks-01",
		Kubeconfig: filepath.Join("testdata", "kubeconfig.yaml"),
	}
	_, err := RESTConfig(cc)
	if err == nil {
		t.Fatal("expected error for empty context")
	}
	if !strings.Contains(err.Error(), "no kubeconfig context") {
		t.Fatalf("want no-context error, got %v", err)
	}
}

func TestRESTConfigMergesKUBECONFIGPathList(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.yaml")
	second := filepath.Join(dir, "two.yaml")
	if err := os.WriteFile(first, []byte(`apiVersion: v1
kind: Config
clusters:
  - name: c-dev
    cluster:
      server: https://dev.example:6443
      insecure-skip-tls-verify: true
contexts:
  - name: dev
    context:
      cluster: c-dev
      user: u-dev
users:
  - name: u-dev
    user:
      token: dev-token
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(`apiVersion: v1
kind: Config
clusters:
  - name: c-prd
    cluster:
      server: https://prd.example:6443
      insecure-skip-tls-verify: true
contexts:
  - name: prd
    context:
      cluster: c-prd
      user: u-prd
users:
  - name: u-prd
    user:
      token: prd-token
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", strings.Join([]string{first, second}, string(os.PathListSeparator)))

	rc, err := RESTConfig(config.ClusterConfig{Name: "prd", Context: "prd"})
	if err != nil {
		t.Fatalf("RESTConfig: %v", err)
	}
	if rc.Host != "https://prd.example:6443" {
		t.Fatalf("want host from second kubeconfig, got %q", rc.Host)
	}
}

// EffectiveKubeconfigPaths must name exactly the files client-go's loading
// rules read for the same input - the parity the subprocess pinning relies on -
// only absolute.
func TestEffectiveKubeconfigPaths_MatchesLoadingRules(t *testing.T) {
	sep := string(os.PathListSeparator)
	dir := t.TempDir()
	t.Chdir(dir)

	cases := []struct {
		name, configured, envKubeconfig string
	}{
		{"configured single file", "/k/dev.yaml", "/ignored"},
		{"configured path list", "/k/a.yaml" + sep + sep + "/k/b.yaml", ""},
		{"configured relative file", "selection", ""},
		{"inherited KUBECONFIG list", "", "/k/x.yaml" + sep + "rel.yaml" + sep + "/k/x.yaml"},
		{"home default", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", tc.envKubeconfig)
			want := kubeconfigLoadingRules(tc.configured).GetLoadingPrecedence()
			if tc.configured == "" {
				// The default rules are built from the environment.
				want = clientcmd.NewDefaultClientConfigLoadingRules().GetLoadingPrecedence()
			}
			got := EffectiveKubeconfigPaths(tc.configured)

			var wantAbs []string
			seen := map[string]bool{}
			for _, p := range want {
				if p == "" {
					continue
				}
				abs, err := filepath.Abs(p)
				if err != nil {
					t.Fatal(err)
				}
				if !seen[abs] {
					seen[abs] = true
					wantAbs = append(wantAbs, abs)
				}
			}
			if !reflect.DeepEqual(got, wantAbs) {
				t.Fatalf("got %v, want %v (client-go precedence %v)", got, wantAbs, want)
			}
			for _, p := range got {
				if !filepath.IsAbs(p) {
					t.Errorf("%q is relative: a terminal with another working directory would resolve a different file", p)
				}
			}
		})
	}
	if got := EffectiveKubeconfigPaths("selection"); got[0] != filepath.Join(dir, "selection") {
		// macOS TempDir sits behind /var -> /private/var; Abs uses Getwd, which may
		// report either spelling, so compare against Getwd too.
		wd, _ := os.Getwd()
		if got[0] != filepath.Join(wd, "selection") {
			t.Errorf("relative selection must resolve against the working directory client-go used: got %q", got[0])
		}
	}
}
