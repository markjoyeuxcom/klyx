package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

// Review 2026-10-03 #12: the new entries land at the actual end of the
// top-level clusters list wherever it sits, every original line survives in
// order, and the result loads with exactly the new clusters added.
func TestAppendClustersInsertsIntoTheListWhereverItIs(t *testing.T) {
	cases := []struct {
		name, original string
		want           []string // cluster names after the append, in order
		contains       string   // a substring proving the layout of the insertion
	}{
		{
			name: "field after the list (the original failure)",
			original: `clusters:
  - name: a
    context: a
# AI settings
ai:
  backend: "off"
`,
			want:     []string{"a", "new"},
			contains: "    context: a\n  - name: new\n    context: new\n# AI settings\nai:",
		},
		{
			name: "fields before and after, trailing comments",
			original: `ai:
  backend: "off"
clusters:
  - name: a
    context: a # inline

# trailing note about the list

`,
			want: []string{"a", "new"},
			// Blank lines may be a |+ scalar's kept newlines, so they are never
			// stepped back over (batch-4 review R1).
			contains: "# trailing note about the list\n\n  - name: new\n",
		},
		{
			name:     "zero-indent list keeps its indentation",
			original: "clusters:\n- name: a\n  context: a\nai:\n  backend: \"off\"\n",
			want:     []string{"a", "new"},
			contains: "  context: a\n- name: new\n  context: new\nai:",
		},
		{
			name:     "clusters with no value",
			original: "ai:\n  backend: \"off\"\nclusters:\n",
			want:     []string{"new"},
			contains: "clusters:\n  - name: new\n",
		},
		{
			name:     "empty inline list",
			original: "clusters: [] # none yet\nai:\n  backend: \"off\"\n",
			want:     []string{"new"},
			contains: "clusters: # none yet\n  - name: new\n    context: new\nai:",
		},
		{
			name:     "a comment mentioning clusters: is not the field",
			original: "# clusters: are added by Klyx\nai:\n  backend: \"off\"\n",
			want:     []string{"new"},
			contains: "ai:\n  backend: \"off\"\nclusters:\n  - name: new\n",
		},
		{
			name:     "no trailing newline",
			original: "clusters:\n  - name: a\n    context: a",
			want:     []string{"a", "new"},
			contains: "    context: a\n  - name: new\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fleet.yaml")
			if err := os.WriteFile(path, []byte(tc.original), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := AppendClusters(path, []string{"new"}); err != nil {
				t.Fatalf("append: %v", err)
			}
			b, _ := os.ReadFile(path)
			got := string(b)
			if !strings.Contains(got, tc.contains) {
				t.Fatalf("insertion layout wrong, want %q in:\n%s", tc.contains, got)
			}
			// Every original line survives, in order (the empty inline list's
			// line is the one deliberate edit).
			rest := got
			for _, l := range strings.Split(strings.TrimRight(tc.original, "\n"), "\n") {
				if strings.Contains(l, "[]") {
					continue
				}
				i := strings.Index(rest, l)
				if i < 0 {
					t.Fatalf("original line %q lost or reordered:\n%s", l, got)
				}
				rest = rest[i+len(l):]
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("result must load: %v\n%s", err, got)
			}
			var names []string
			for _, c := range cfg.Clusters {
				names = append(names, c.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("clusters = %v, want %v", names, tc.want)
			}
		})
	}
}

// A non-empty inline list cannot be extended without reformatting the user's
// file: refused with how to fix it, and the file is untouched.
func TestAppendClustersRefusesANonEmptyInlineList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	original := "clusters: [{name: a, context: a}]\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	err := AppendClusters(path, []string{"new"})
	if err == nil || !strings.Contains(err.Error(), "block list") {
		t.Fatalf("want an actionable refusal, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != original {
		t.Fatalf("file must be unchanged:\n%s", b)
	}
}

// Codex batch-4 review R1, adopted: block-scalar content that looks like a
// comment or a blank line is part of the value. The append must leave every
// existing value exactly as it was - or refuse with the file untouched.
func TestAppendClustersPreservesBlockScalarContent(t *testing.T) {
	for name, raw := range map[string]string{
		"strip scalar with a #-line":         "clusters:\n  - name: dev\n    tags:\n      note: |-\n        first line\n        # this is scalar content\n",
		"keep scalar with trailing newlines": "clusters:\n  - name: dev\n    tags:\n      note: |+\n        first line\n\n\n",
		"scalar then a field":                "clusters:\n  - name: dev\n    tags:\n      note: |\n        a\n        # b\nai:\n  backend: \"off\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "fleet.yaml")
			if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := AppendClusters(p, []string{"new"}); err != nil {
				if b, _ := os.ReadFile(p); string(b) != raw {
					t.Fatal("a refusal must leave the file untouched")
				}
				t.Logf("refused safely: %v", err)
				return
			}
			after, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Clusters[0], after.Clusters[0]) {
				t.Fatalf("existing cluster changed: note %q -> %q", before.Clusters[0].Tags["note"], after.Clusters[0].Tags["note"])
			}
			if len(after.Clusters) != 2 || after.Clusters[1].Name != "new" {
				t.Fatalf("clusters = %+v", after.Clusters)
			}
		})
	}
}

// The decoded-document guard: any splice that alters an existing value is
// refused, whatever the textual heuristic did.
func TestOnlyClustersAdded(t *testing.T) {
	orig := []byte("clusters:\n  - name: a\n    tags:\n      note: x\nai:\n  backend: \"off\"\n")
	ok := []byte("clusters:\n  - name: a\n    tags:\n      note: x\n  - name: b\nai:\n  backend: \"off\"\n")
	if err := onlyClustersAdded(orig, ok, 1); err != nil {
		t.Fatalf("a pure append must pass: %v", err)
	}
	for name, bad := range map[string]string{
		"value changed": "clusters:\n  - name: a\n    tags:\n      note: y\n  - name: b\nai:\n  backend: \"off\"\n",
		"other field":   "clusters:\n  - name: a\n    tags:\n      note: x\n  - name: b\nai:\n  backend: local\n",
		"wrong count":   "clusters:\n  - name: a\n    tags:\n      note: x\nai:\n  backend: \"off\"\n",
	} {
		if onlyClustersAdded(orig, []byte(bad), 1) == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// Spec 2026-10-13 §2.3: the atomic rename stops a torn file, not a lost
// update. An edit made on disk after AppendClusters read the file is refused,
// and the edit survives.
func TestAppendClustersRefusesAnEditMadeMeanwhile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited := "clusters:\n  - name: a\n# edited in another tool\nai:\n  provider: anthropic\n"
	beforeReplace = func() { _ = os.WriteFile(path, []byte(edited), 0o644) }
	defer func() { beforeReplace = nil }()

	err := AppendClusters(path, []string{"b"})
	if !errors.Is(err, ErrFleetChangedOnDisk) {
		t.Fatalf("want ErrFleetChangedOnDisk, got %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != edited {
		t.Fatalf("the on-disk edit was overwritten:\n%s", got)
	}

	// A file created meanwhile on the create-new path is refused too.
	fresh := filepath.Join(dir, "new.yaml")
	beforeReplace = func() { _ = os.WriteFile(fresh, []byte("clusters:\n  - name: z\n"), 0o644) }
	if err := AppendClusters(fresh, []string{"b"}); !errors.Is(err, ErrFleetChangedOnDisk) {
		t.Fatalf("create-new: want ErrFleetChangedOnDisk, got %v", err)
	}
}

// Concurrent writers in this process are serialised: every add lands.
func TestAppendClustersSerialisesWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- AppendClusters(path, []string{fmt.Sprintf("ctx-%d", i)})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a serialised append failed: %v", err)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clusters) != 9 {
		t.Fatalf("want 9 clusters (no lost update), got %d", len(cfg.Clusters))
	}
}
