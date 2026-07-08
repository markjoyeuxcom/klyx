package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
