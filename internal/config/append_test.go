package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendClustersPreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	original := `# my hand-written header comment
clusters:
  - name: homelab-nelli
    context: kubernetes-admin@homelab-nelli
    protected: false # inline comment survives
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AppendClusters(path, []string{"kubernetes-admin@homelab-blue"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, _ := os.ReadFile(path)
	s := string(got)
	if !strings.HasPrefix(s, original) {
		t.Fatalf("original bytes (incl. comments) must be preserved verbatim:\n%s", s)
	}
	if !strings.Contains(s, "- name: kubernetes-admin@homelab-blue") {
		t.Fatalf("new entry missing:\n%s", s)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("result must load: %v", err)
	}
	if len(cfg.Clusters) != 2 || cfg.Clusters[1].Context != "kubernetes-admin@homelab-blue" {
		t.Fatalf("parsed clusters wrong: %+v", cfg.Clusters)
	}
}

func TestAppendClustersCreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "fleet.yaml")

	if err := AppendClusters(path, []string{"ctx-a", "ctx-b"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("created file must load: %v", err)
	}
	if len(cfg.Clusters) != 2 || cfg.Clusters[0].Name != "ctx-a" {
		t.Fatalf("clusters wrong: %+v", cfg.Clusters)
	}
}

func TestAppendClustersEnrichesEKSContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	ctx := "arn:aws:eks:us-east-1:934692410245:cluster/eks-tooling"

	if err := AppendClusters(path, []string{ctx}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, _ := os.ReadFile(path)
	s := string(got)
	for _, want := range []string{
		"- name: eks-tooling",
		`context: "arn:aws:eks:us-east-1:934692410245:cluster/eks-tooling"`,
		"provider: eks",
		"region: us-east-1",
		`account: "934692410245"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("appended EKS config missing %q:\n%s", want, s)
		}
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("result must load: %v", err)
	}
	if cfg.Clusters[0].Name != "eks-tooling" || cfg.Clusters[0].Context != ctx {
		t.Fatalf("cluster identity wrong: %+v", cfg.Clusters[0])
	}
	if cfg.Clusters[0].Tags["account"] != "934692410245" {
		t.Fatalf("account tag wrong: %+v", cfg.Clusters[0].Tags)
	}
}

func TestAppendClustersRefusesDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	os.WriteFile(path, []byte("clusters:\n  - name: a\n    context: ctx-a\n"), 0o644)

	// Duplicate by context AND by name are both refused.
	if err := AppendClusters(path, []string{"ctx-a"}); err == nil {
		t.Fatal("want duplicate-context error")
	}
	if err := AppendClusters(path, []string{"a"}); err == nil {
		t.Fatal("want duplicate-name error")
	}
	if err := AppendClusters(path, []string{"ctx-new", "ctx-new"}); err == nil {
		t.Fatal("want duplicate-within-request error")
	}
	got, _ := os.ReadFile(path)
	if strings.Count(string(got), "- name:") != 1 {
		t.Fatalf("file must be unchanged on refusal:\n%s", got)
	}
}

func TestAppendClustersLeavesInvalidExistingAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	broken := "clusters:\n  - name: a\n  - name: a\n" // duplicate names: invalid
	os.WriteFile(path, []byte(broken), 0o644)

	if err := AppendClusters(path, []string{"ctx-new"}); err == nil {
		t.Fatal("want error when existing config is invalid")
	}
	got, _ := os.ReadFile(path)
	if string(got) != broken {
		t.Fatalf("file must be untouched:\n%s", got)
	}
}

func TestKubeContexts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	kubeconfig := `apiVersion: v1
kind: Config
contexts:
  - name: kubernetes-admin@homelab-nelli
    context: {cluster: nelli, user: admin}
  - name: kubernetes-admin@homelab-blue
    context: {cluster: blue, user: admin}
clusters: []
users: []
`
	os.WriteFile(path, []byte(kubeconfig), 0o644)

	got, err := KubeContexts(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kubernetes-admin@homelab-blue", "kubernetes-admin@homelab-nelli"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("contexts: got %v, want %v (sorted)", got, want)
	}
}

func TestKubeContextsPathListMergesFiles(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one")
	two := filepath.Join(dir, "two")
	if err := os.WriteFile(one, []byte(`apiVersion: v1
kind: Config
contexts:
  - name: dev
    context: {cluster: dev, user: dev}
clusters: []
users: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(two, []byte(`apiVersion: v1
kind: Config
contexts:
  - name: prd
    context: {cluster: prd, user: prd}
clusters: []
users: []
`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := KubeContexts(strings.Join([]string{one, two}, string(os.PathListSeparator)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "dev" || got[1] != "prd" {
		t.Fatalf("contexts: got %v, want [dev prd]", got)
	}
}

func TestKubeContextsMissingFileIsEmptyNotError(t *testing.T) {
	got, err := KubeContexts(filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != nil {
		t.Fatalf("missing kubeconfig: want nil/nil, got %v/%v", got, err)
	}
}

func TestAppendClustersPersistsProtectedForProd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	// A prod-looking context persists protected: true; a dev one does not.
	if err := AppendClusters(path, []string{"prod-eu", "dev-eu"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	s := string(mustRead(t, path))
	if !strings.Contains(s, "- name: prod-eu\n    context: prod-eu\n    protected: true") {
		t.Fatalf("prod cluster must persist protected: true:\n%s", s)
	}
	if strings.Contains(s, "- name: dev-eu\n    context: dev-eu\n    protected: true") {
		t.Fatalf("dev cluster must not be marked protected:\n%s", s)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("result must load: %v", err)
	}
	for _, c := range cfg.Clusters {
		switch c.Name {
		case "prod-eu":
			if !c.Protected {
				t.Fatalf("prod-eu must reload protected: %+v", c)
			}
		case "dev-eu":
			if c.Protected {
				t.Fatalf("dev-eu must reload unprotected: %+v", c)
			}
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return b
}
