//go:build !windows

package appbridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitcache"
	"github.com/moomora/klyx/internal/gitcli"
)

// The Line slice 8b-2: Git's "not here yet" over a real repository - the
// commits between a node's applied sha and T that touch its path, and the
// files they change; T always Go's own; "behind" proved before anything is
// listed.

type pathHistory struct {
	runner               gitcli.Runner
	cache                string
	c1, c2, c3, t        string // main: c1 <- c2 (infra) <- many... <- c3 (apps) <- t (README)
	ahead, feature, root string
}

func newPathHistory(t *testing.T) pathHistory {
	t.Helper()
	env := gitcli.OwnerEnv(os.Getenv, os.Environ())
	env.Home = t.TempDir()
	g, err := gitcli.Locate(context.Background(), env)
	if err != nil {
		t.Skipf("no usable git: %v", err)
	}
	base := gitcli.CacheBase(t.TempDir())
	if err := gitcli.EnsureBase(base); err != nil {
		t.Fatal(err)
	}
	cfg := gitcli.GlobalConfigPath(base)
	if err := gitcli.WriteGlobalConfig(cfg, gitcli.Helpers{}, ""); err != nil {
		t.Fatal(err)
	}
	f := pathHistory{runner: gitcli.Runner{Git: g, Env: env, GlobalConfig: cfg}}
	work := t.TempDir()
	n := 0
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command(g.Path, args...)
		c.Dir = work
		date := time.Date(2026, 10, 1, 10, 0, n, 0, time.UTC).Format(time.RFC3339)
		c.Env = []string{"HOME=" + work, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
			"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.test", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.test",
			"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, body string) {
		p := filepath.Join(work, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string, files map[string]string) string {
		n++
		for p, b := range files {
			write(p, b)
		}
		run("add", "-A")
		run("commit", "-qm", msg)
		return run("rev-parse", "HEAD")
	}
	run("init", "-q", "-b", "main")
	f.c1 = commit("start", map[string]string{"apps/a.yaml": "a1", "infra/i.yaml": "i1", "README": "r1"})
	f.c2 = commit("infra change", map[string]string{"infra/i.yaml": "i2"})
	for i := 0; i < 21; i++ {
		commit(fmt.Sprintf("many %d", i), map[string]string{"many/m.yaml": fmt.Sprint(i)})
	}
	f.c3 = commit("apps change", map[string]string{"apps/a.yaml": "a2", "apps/b.yaml": "b1"})
	f.t = commit("readme", map[string]string{"README": "r2"})
	run("checkout", "-qb", "ahead")
	f.ahead = commit("ahead", map[string]string{"apps/a.yaml": "a3"})
	run("checkout", "-qb", "feature", f.c1)
	f.feature = commit("feature", map[string]string{"apps/a.yaml": "af"})
	run("checkout", "-q", "--orphan", "orphan")
	f.root = commit("unrelated", map[string]string{"x": "x"})
	f.cache = gitcli.CacheDir(base, "https://github.com/acme/fleet.git")
	if err := f.runner.InitRepo(context.Background(), f.cache, "https://github.com/acme/fleet.git"); err != nil {
		t.Fatal(err)
	}
	run("push", "-q", f.cache, "main:refs/klyx/tracked/refs/heads/main", "ahead:refs/klyx/tracked/refs/heads/ahead",
		"feature:refs/klyx/tracked/refs/heads/feature", "orphan:refs/klyx/tracked/refs/heads/orphan")
	return f
}

const notHereFleet = `clusters:
  - name: prd-a
git:
  repos:
    - url: https://github.com/acme/fleet.git
      matches: [git.mirror.internal/acme/fleet]
`

func newNotHereHarness(t *testing.T) (*gitHarness, pathHistory) {
	f := newPathHistory(t)
	h := newGitHarness(t, notHereFleet, nil)
	h.store.mu.Lock()
	h.store.status["https://github.com/acme/fleet.git"] = gitcache.Status{FetchedAt: time.Date(2026, 10, 15, 20, 31, 0, 0, time.UTC)}
	h.store.read = func(url string, fn func(gitcli.Repo) error) error {
		if url != "https://github.com/acme/fleet.git" {
			t.Errorf("read %s, not the approved URL", url)
		}
		return fn(f.runner.Repo(f.cache))
	}
	h.store.mu.Unlock()
	h.push(LineDTO{Version: 9, Stations: []LineStationDTO{gitStation("prd-a", "matches", f.t)}})
	if g := h.waitLine(9); g.State != LineGitReady || g.Target.SHA != f.t {
		t.Fatalf("the line's T: %+v", g)
	}
	return h, f
}

func node(id, applied, path string) NotHereYetNodeDTO {
	return NotHereYetNodeDTO{ID: id, URL: "https://github.com/acme/fleet", RefKind: "branch", RefName: "main", Applied: applied, Path: path}
}

func TestNotHereYet(t *testing.T) {
	h, f := newNotHereHarness(t)
	alias := node("alias", f.c2, "./infra")
	alias.URL = "ssh://git@git.mirror.internal/acme/fleet.git"
	other := node("other", f.c1, "./apps")
	other.RefKind, other.RefName = "tag", "v1"
	unapproved := node("unapproved", f.c1, "./apps")
	unapproved.URL = "https://github.com/acme/elsewhere.git"
	got := h.svc.NotHereYet(context.Background(), []NotHereYetNodeDTO{
		node("apps", f.c1, "./apps"), node("infra", f.c1, "./infra/"), node("root", f.c2, ""), alias,
		node("at", f.t, "./apps"), node("ahead", f.ahead, "./apps"), node("diverged", f.feature, "./apps"), node("unrelated", f.root, "./apps"),
		node("missing", strings.Repeat("e", 40), "./apps"), node("many", f.c1, "./many"),
		other, unapproved, node("bad-sha", "abc123", "./apps"), node("bad-path", f.c1, "../escape"),
	})
	if got.Error != "" || got.Target == nil || got.Target.SHA != f.t || got.Target.FetchedAt == nil {
		t.Fatalf("target: %+v", got)
	}
	r := map[string]NotHereYetResultDTO{}
	for _, x := range got.Results {
		r[x.ID] = x
	}
	subjects := func(x NotHereYetResultDTO) string {
		var s []string
		for _, c := range x.Commits {
			s = append(s, c.Subject)
		}
		return strings.Join(s, ",")
	}
	files := func(x NotHereYetResultDTO) string {
		var s []string
		for _, f := range x.Files {
			s = append(s, f.Status+" "+f.Path)
		}
		return strings.Join(s, ",")
	}
	// Behind, path-limited: only the commits and files under the path.
	if x := r["apps"]; x.State != NotHereBehind || subjects(x) != "apps change" || files(x) != "M apps/a.yaml,A apps/b.yaml" || x.More {
		t.Errorf("apps: %+v", x)
	}
	if x := r["infra"]; x.State != NotHereBehind || subjects(x) != "infra change" || files(x) != "M infra/i.yaml" {
		t.Errorf("infra: %+v", x)
	}
	if x := r["root"]; x.State != NotHereBehind || len(x.Commits) != 20 || !x.More || !strings.Contains(files(x), "M README") {
		t.Errorf("root: %d commits more %v files %s", len(x.Commits), x.More, files(x))
	}
	// An alias URL mapped through the approved matches is T's repository.
	if x := r["alias"]; x.State != NotHereBehind || subjects(x) != "" || files(x) != "" {
		t.Errorf("alias: %+v", x)
	}
	if x := r["many"]; x.State != NotHereBehind || len(x.Commits) != 20 || !x.More {
		t.Errorf("many: %d commits more %v", len(x.Commits), x.More)
	}
	for id, want := range map[string]string{
		"ahead": "ahead of T", "diverged": "diverged from T", "unrelated": "unrelated to T", "missing": "not in the fetched history",
		"other": "not T's", "unapproved": "not approved", "bad-sha": "not a full commit id", "bad-path": "not a clean relative path",
	} {
		if x := r[id]; x.State != NotHereUnavailable || !strings.Contains(x.Reason, want) || len(x.Commits) != 0 {
			t.Errorf("%s: %+v, want unavailable %q", id, x, want)
		}
	}
	if x := r["at"]; x.State != NotHereAt {
		t.Errorf("at T: %+v", x)
	}
}

func TestNotHereYetWithoutT(t *testing.T) {
	h := newGitHarness(t, lineFleet, nil) // the cache has no history: no T
	got := h.svc.NotHereYet(context.Background(), []NotHereYetNodeDTO{node("apps", strings.Repeat("a", 40), "./apps")})
	if got.Target != nil || len(got.Results) != 1 || got.Results[0].State != NotHereUnavailable || !strings.HasPrefix(got.Results[0].Reason, "the line has no T") {
		t.Fatalf("no T: %+v", got)
	}
	many := make([]NotHereYetNodeDTO, 25)
	if got := h.svc.NotHereYet(context.Background(), many); got.Error == "" {
		t.Fatal("more nodes than a graph holds")
	}
}

// cacheGit runs git against the fixture's cache (to add history the way a
// fetch would).
func cacheGit(t *testing.T, f pathHistory, in string, args ...string) string {
	t.Helper()
	c := exec.Command(f.runner.Git.Path, args...)
	c.Dir = f.cache
	c.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false"}
	c.Stdin = strings.NewReader(in)
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

// Design review of 09f961f: one view lists at most 200 changed paths and
// says how many it left out; a node's own failure answers only that node.
func TestNotHereYetReviewOf09f961f(t *testing.T) {
	h, f := newNotHereHarness(t)
	// A tree id is a full hex id that is no commit: its own node is
	// unavailable, and its siblings keep their answers.
	tree := cacheGit(t, f, "", "rev-parse", f.c1+"^{tree}")
	got := h.svc.NotHereYet(context.Background(), []NotHereYetNodeDTO{node("ahead", f.ahead, "apps"), node("not-a-commit", tree, "apps"), node("healthy", f.c1, "apps")})
	if !strings.Contains(got.Results[0].Reason, "ahead of T") || got.Results[1].State != NotHereUnavailable || got.Results[1].Reason == "" ||
		got.Results[2].State != NotHereBehind || len(got.Results[2].Commits) != 1 {
		t.Fatalf("siblings: %+v", got.Results)
	}

	// 201 paths changed in one commit: 200 listed, 1 omitted.
	var script strings.Builder
	fmt.Fprintf(&script, "commit refs/klyx/tracked/refs/heads/main\ncommitter Ada <ada@example.test> 1791030000 +0000\ndata 4\ncaps\nfrom %s\n", f.t)
	for i := 0; i < 201; i++ {
		fmt.Fprintf(&script, "M 100644 inline caps/f%03d.yaml\ndata 1\nx\n", i)
	}
	script.WriteString("\n")
	cacheGit(t, f, script.String(), "fast-import", "--quiet")
	capT := cacheGit(t, f, "", "rev-parse", "refs/klyx/tracked/refs/heads/main")
	h.push(LineDTO{Version: 10, Stations: []LineStationDTO{gitStation("prd-a", "matches", capT)}})
	if g := h.waitLine(10); g.State != LineGitReady || g.Target.SHA != capT {
		t.Fatalf("target %+v", g)
	}
	got = h.svc.NotHereYet(context.Background(), []NotHereYetNodeDTO{node("caps", f.t, "caps")})
	if r := got.Results[0]; r.State != NotHereBehind || len(r.Files) != 200 || r.FilesOmitted != 1 || len(r.Commits) != 1 {
		t.Fatalf("the view cap: %d files, %d omitted, %+v", len(r.Files), r.FilesOmitted, r.State)
	}
}
