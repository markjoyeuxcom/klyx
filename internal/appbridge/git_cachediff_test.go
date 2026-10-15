//go:build !windows

package appbridge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitcache"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/workloads"
)

// The Line slice 6e: "Compute diff" from the Git cache - the fence, the
// confined build (this test binary is the child), commonMetadata, the
// dry-run's inputs and the result's words - and the flux CLI path for
// every repository that is not approved.

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == cachediff.ChildFlag {
		os.Exit(cachediff.RunChild(os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// cacheFake is the Git cache over a real repository: reads run against it;
// its content is all present, so acquisition is a no-op.
type cacheFake struct {
	runner  gitcli.Runner
	dir     string
	ensured []string
}

func (c *cacheFake) Start(context.Context) error                                { return nil }
func (c *cacheFake) RefreshHistory(context.Context, string, []gitcli.Ref) error { return nil }
func (c *cacheFake) Clear(context.Context, string) error                        { return nil }
func (c *cacheFake) Size(string) int64                                          { return 0 }
func (c *cacheFake) Status(string) gitcache.Status                              { return gitcache.Status{} }
func (c *cacheFake) AcquireIDs(context.Context, string, []string) error         { return nil }
func (c *cacheFake) Read(_ string, fn func(gitcli.Repo) error) error            { return fn(c.runner.Repo(c.dir)) }
func (c *cacheFake) EnsureCommit(_ context.Context, _ string, sha string) error {
	c.ensured = append(c.ensured, sha)
	return nil
}

// diffConn is a station: the Kustomization and its source, and a dry-run
// that records what it is asked.
type diffConn struct {
	objs    map[string]*unstructured.Unstructured
	dryRun  []*unstructured.Unstructured
	inv     []string
	prune   bool
	changes []cachediff.Change
}

func (d *diffConn) FluxObject(_ context.Context, kind, ns, name string) (*unstructured.Unstructured, error) {
	if o, ok := d.objs[kind+"/"+ns+"/"+name]; ok {
		return o.DeepCopy(), nil
	}
	return nil, errors.New("not found")
}
func (d *diffConn) FluxEvents(context.Context, string, string, string) ([]workloads.EventSummary, error) {
	return nil, nil
}
func (d *diffConn) FluxAvailable() bool { return false }
func (d *diffConn) FluxDiffKustomization(context.Context, string, string, string) (fluxcli.DiffResult, error) {
	return fluxcli.DiffResult{}, errors.New("the flux CLI path ran")
}
func (d *diffConn) ServerSideDiff(_ context.Context, objs []*unstructured.Unstructured, inv []string, prune bool) ([]cachediff.Change, error) {
	d.dryRun, d.inv, d.prune = objs, inv, prune
	return d.changes, nil
}
func (d *diffConn) Reconcile(context.Context, string, string, string, string) error { return nil }
func (d *diffConn) ReconcileWithSource(context.Context, string, string, string, string) error {
	return nil
}
func (d *diffConn) SetSuspend(context.Context, string, string, string, string, bool) error {
	return nil
}

const diffFleet = `clusters:
  - name: prd
git:
  repos:
    - url: git@github.com:acme/fleet.git
`

// newDiffFixture: a fleet repository with a kustomization tree, published
// into a Klyx cache, and the commit it is at.
func newDiffFixture(t *testing.T) (*cacheFake, string) {
	t.Helper()
	env := gitcli.OwnerEnv(os.Getenv, os.Environ())
	env.Home = t.TempDir()
	g, err := gitcli.Locate(context.Background(), env)
	if err != nil {
		t.Skipf("no usable git: %v", err)
	}
	work := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command(g.Path, args...)
		c.Dir = work
		c.Env = []string{"HOME=" + work, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.test", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.test"}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for p, c := range map[string]string{
		"clusters/prd/kustomization.yaml": "resources: [../../apps/base]\nconfigMapGenerator:\n  - name: settings\n    files: [../../config/app.ini]\n",
		"apps/base/kustomization.yaml":    "resources: [deployment.yaml]\n",
		"apps/base/deployment.yaml":       "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          image: ghcr.io/acme/web:1.0\n",
		"config/app.ini":                  "a=1\n",
		"other/never.yaml":                "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: never\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(work, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-qm", "fleet")
	sha := run("rev-parse", "HEAD")
	base := gitcli.CacheBase(t.TempDir())
	if err := gitcli.EnsureBase(base); err != nil {
		t.Fatal(err)
	}
	cfg := gitcli.GlobalConfigPath(base)
	if err := gitcli.WriteGlobalConfig(cfg, gitcli.Helpers{}, ""); err != nil {
		t.Fatal(err)
	}
	runner := gitcli.Runner{Git: g, Env: env, GlobalConfig: cfg}
	dir := gitcli.CacheDir(base, "git@github.com:acme/fleet.git")
	if err := runner.InitRepo(context.Background(), dir, "git@github.com:acme/fleet.git"); err != nil {
		t.Fatal(err)
	}
	run("push", "-q", dir, "main:refs/klyx/tracked/refs/heads/main")
	return &cacheFake{runner: runner, dir: dir}, sha
}

func newDiffService(t *testing.T, store *cacheFake) *GitService {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(diffFleet), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewGitService(context.Background(), GitDeps{ConfigPath: path,
		StartGit: func(context.Context) (gitcli.Runner, gitcli.Helpers, error) {
			return gitcli.Runner{Git: gitcli.Git{Path: "/usr/bin/git"}}, gitcli.Helpers{}, nil
		},
		NewStore: func(gitcli.Runner) gitStore { return store }})
	for end := time.Now().Add(5 * time.Second); !svc.GetGitSettings().Binary.Ready; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("Git never started")
		}
	}
	return svc
}

func fluxObjects(sha string, mod func(ks, src map[string]any)) map[string]*unstructured.Unstructured {
	ks := map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system", "generation": int64(3)},
		"spec": map[string]any{"path": "./clusters/prd", "sourceRef": map[string]any{"kind": "GitRepository", "name": "fleet"},
			"prune": true, "targetNamespace": "shop", "commonMetadata": map[string]any{"labels": map[string]any{"team": "a"}},
			"postBuild": map[string]any{"substitute": map[string]any{"x": "y"}}, "decryption": map[string]any{"provider": "sops"}},
		"status": map[string]any{"observedGeneration": int64(3),
			"inventory": map[string]any{"entries": []any{map[string]any{"id": "shop_old_apps_Deployment", "v": "v1"}}}},
	}
	src := map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "fleet", "namespace": "flux-system", "generation": int64(2)},
		"spec":     map[string]any{"url": "https://github.com/acme/fleet.git"},
		"status": map[string]any{"observedGeneration": int64(2),
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			"artifact":   map[string]any{"revision": "main@sha1:" + sha}},
	}
	if mod != nil {
		mod(ks, src)
	}
	return map[string]*unstructured.Unstructured{
		"Kustomization/flux-system/apps":  {Object: ks},
		"GitRepository/flux-system/fleet": {Object: src},
	}
}

func TestComputeDiffBuildsFromTheCache(t *testing.T) {
	store, sha := newDiffFixture(t)
	svc := newDiffService(t, store)
	conn := &diffConn{objs: fluxObjects(sha, nil), changes: []cachediff.Change{
		{Action: "configured", Kind: "Deployment", Namespace: "shop", Name: "web", Diff: "--- live\n+++ from Git (dry-run)\n-x\n+y\n"},
		{Action: "unchanged", Kind: "ConfigMap", Namespace: "shop", Name: "settings-abc"},
		{Action: cachediff.ActionPruned, Kind: "Deployment", Namespace: "shop", Name: "old"},
	}}
	d, ok := svc.cacheDiff(context.Background(), conn, "flux-system", "apps")
	if !ok || d.Error != "" || d.Via != "cache" || d.Revision != sha || !d.HasChanges {
		t.Fatalf("diff %+v", d)
	}
	if !slices.Equal(store.ensured, []string{sha}) {
		t.Fatalf("the revision was not ensured: %v", store.ensured)
	}
	// What was dry-run: the build at the revision, with Flux's overlay and
	// commonMetadata; never the file outside the input set.
	var names []string
	for _, o := range conn.dryRun {
		names = append(names, o.GetKind()+" "+o.GetNamespace()+"/"+o.GetName())
		if o.GetLabels()["team"] != "a" {
			t.Fatalf("commonMetadata not applied to %s", o.GetName())
		}
	}
	slices.Sort(names)
	if len(names) != 2 || !strings.HasPrefix(names[0], "ConfigMap shop/settings-") || names[1] != "Deployment shop/web" {
		t.Fatalf("dry-run objects %q", names)
	}
	if !conn.prune || !slices.Equal(conn.inv, []string{"shop_old_apps_Deployment"}) {
		t.Fatalf("prune %v inventory %v", conn.prune, conn.inv)
	}
	if !strings.Contains(d.Output, "► Deployment shop/web configured\n--- live") || !strings.Contains(d.Output, "► Deployment shop/old would be pruned") ||
		strings.Contains(d.Output, "settings-abc") {
		t.Fatalf("output:\n%s", d.Output)
	}
	notes := strings.Join(d.Notes, "\n")
	if !strings.Contains(notes, "never decrypts") || !strings.Contains(notes, "unsubstituted") {
		t.Fatalf("notes %q", d.Notes)
	}
}

func TestComputeDiffIsFencedToTheCurrentSpecAndSource(t *testing.T) {
	store, sha := newDiffFixture(t)
	svc := newDiffService(t, store)
	for name, c := range map[string]struct {
		mod  func(ks, src map[string]any)
		want string
	}{
		"the Kustomization's spec not observed": {func(ks, _ map[string]any) { ks["metadata"].(map[string]any)["generation"] = int64(4) }, "Kustomization's current spec"},
		"the source's spec not fetched":         {func(_, src map[string]any) { src["status"].(map[string]any)["observedGeneration"] = int64(1) }, "GitRepository's current spec"},
		"the source not Ready": {func(_, src map[string]any) {
			src["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Ready", "status": "False"}}
		}, "not Ready"},
		"an unreadable artifact": {func(_, src map[string]any) {
			src["status"].(map[string]any)["artifact"] = map[string]any{"revision": "main/" + sha}
		}, "names no commit"},
		"a revision outside the cache": {func(_, src map[string]any) {
			src["status"].(map[string]any)["artifact"] = map[string]any{"revision": "main@sha1:" + strings.Repeat("e", 40)}
		}, "tree object"},
	} {
		t.Run(name, func(t *testing.T) {
			conn := &diffConn{objs: fluxObjects(sha, c.mod)}
			d, ok := svc.cacheDiff(context.Background(), conn, "flux-system", "apps")
			if !ok || d.Error == "" || !strings.Contains(d.Error, c.want) || conn.dryRun != nil {
				t.Fatalf("got ok=%v %+v", ok, d)
			}
		})
	}
}

func TestComputeDiffLeavesUnapprovedRepositoriesToTheFluxCLI(t *testing.T) {
	store, sha := newDiffFixture(t)
	svc := newDiffService(t, store)
	for name, mod := range map[string]func(ks, src map[string]any){
		"an unapproved repository": func(_, src map[string]any) { src["spec"].(map[string]any)["url"] = "https://github.com/acme/other.git" },
		"an OCI source": func(ks, _ map[string]any) {
			ks["spec"].(map[string]any)["sourceRef"] = map[string]any{"kind": "OCIRepository", "name": "x"}
		},
	} {
		if _, ok := svc.cacheDiff(context.Background(), &diffConn{objs: fluxObjects(sha, mod)}, "flux-system", "apps"); ok {
			t.Errorf("%s was answered from the cache", name)
		}
	}
	// Through the lens: an unapproved repository reaches the flux CLI path.
	conn := &diffConn{objs: fluxObjects(sha, func(_, src map[string]any) { src["spec"].(map[string]any)["url"] = "https://github.com/acme/other.git" })}
	gs := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	SetGitOpsCacheDiff(gs, svc)
	if d := gs.FluxDiff("prd", "flux-system", "apps", ""); d.Via != "flux CLI" || !strings.Contains(d.Error, "flux CLI was not found") {
		t.Fatalf("unapproved through the lens: %+v", d)
	}
	conn.objs = fluxObjects(sha, nil)
	if d := gs.FluxDiff("prd", "flux-system", "apps", ""); d.Via != "cache" || d.Error != "" {
		t.Fatalf("approved through the lens: %+v", d)
	}
}
