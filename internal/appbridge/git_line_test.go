//go:build !windows

package appbridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitcache"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/line"
)

// The Line slice 6d (parent §3.4, §3.7; slice 6 note §6): ordering the
// line against T, "held · N behind" evidence, the Ledger's commits, the
// line:git fence, and the fetch cadence - over a real repository.

type historyFixture struct {
	runner                     gitcli.Runner
	cache                      string
	c1, c2, c3, a1, f1, u1, e1 string
}

func (f historyFixture) short(s string) string { return s[:7] }

// newHistory: main c1 <- c2 <- c3 (T); ahead a1 on c3; feature f1 on c1;
// an unrelated root u1; e1 is in no history. Published into a Klyx cache.
func newHistory(t *testing.T) historyFixture {
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
	f := historyFixture{runner: gitcli.Runner{Git: g, Env: env, GlobalConfig: cfg}, e1: strings.Repeat("e", 40)}
	work := t.TempDir()
	n := 0
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command(g.Path, args...)
		c.Dir = work
		date := time.Date(2026, 10, 1, 10, n, 0, 0, time.UTC).Format(time.RFC3339)
		c.Env = []string{"HOME=" + work, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
			"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.test", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.test",
			"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg string) string {
		n++
		if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-qm", msg)
		return run("rev-parse", "HEAD")
	}
	run("init", "-q", "-b", "main")
	f.c1 = commit("one")
	f.c2 = commit("two")
	f.c3 = commit("three")
	run("checkout", "-qb", "ahead")
	f.a1 = commit("ahead one")
	run("checkout", "-qb", "feature", f.c1)
	f.f1 = commit("feature one")
	run("checkout", "-q", "--orphan", "orphan")
	f.u1 = commit("unrelated")
	f.cache = gitcli.CacheDir(base, "https://github.com/acme/fleet.git")
	if err := f.runner.InitRepo(context.Background(), f.cache, "https://github.com/acme/fleet.git"); err != nil {
		t.Fatal(err)
	}
	run("push", "-q", f.cache, "main:refs/klyx/tracked/refs/heads/main", "ahead:refs/klyx/tracked/refs/heads/ahead",
		"feature:refs/klyx/tracked/refs/heads/feature", "orphan:refs/klyx/tracked/refs/heads/orphan")
	return f
}

const lineFleet = `clusters:
  - name: prd-a
git:
  repos:
    - url: https://github.com/acme/fleet.git
`

func gitStation(cluster, state, single string, units ...LineUnitDTO) LineStationDTO {
	return LineStationDTO{Cluster: cluster, State: state, Single: single, Read: "ok", Units: units}
}

// newLineHarness is the Settings harness over the history fixture: the
// approved repository's cache answers reads, fetched at 20:31.
func newGitLineHarness(t *testing.T) (*gitHarness, historyFixture, *atomic.Int32) {
	hf := newHistory(t)
	h := newGitHarness(t, lineFleet, nil)
	reads := &atomic.Int32{}
	h.store.mu.Lock()
	h.store.status["https://github.com/acme/fleet.git"] = gitcache.Status{FetchedAt: time.Date(2026, 10, 15, 20, 31, 0, 0, time.UTC),
		AttemptedAt: time.Date(2026, 10, 15, 20, 31, 0, 0, time.UTC)}
	h.store.read = func(url string, fn func(gitcli.Repo) error) error {
		reads.Add(1)
		return fn(hf.runner.Repo(hf.cache))
	}
	h.store.mu.Unlock()
	return h, hf, reads
}

// waitLine waits for the ordering that answers LineDTO version v.
func (h *gitHarness) waitLine(v uint64) LineGitDTO {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if g := h.svc.LineGit(); g.LineVersion == v && g.State != LineGitChecking {
			return g
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no line:git for LineDTO %d; have %+v", v, h.svc.LineGit())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTheLineIsOrderedAgainstT(t *testing.T) {
	h, f, _ := newGitLineHarness(t)
	ks := func(name string, mod func(*LineUnitDTO)) LineUnitDTO {
		u := LineUnitDTO{Origin: "Kustomization", Namespace: "flux-system", Name: name}
		mod(&u)
		return u
	}
	dto := LineDTO{Version: 7, Key: h.key,
		Stations: []LineStationDTO{
			gitStation("prd-a", "differs", f.c1, ks("apps", func(u *LineUnitDTO) { u.Attempted, u.Failing = f.c3, true })),
			gitStation("prd-b", "differs", f.c1, ks("apps", func(u *LineUnitDTO) { u.SourceNotReady, u.Failing = true, true })),
			gitStation("prd-c", "differs", f.c2, ks("apps", func(u *LineUnitDTO) { u.BlockedBy = "flux-system/infra" })),
			gitStation("prd-d", "differs", f.c2, ks("apps", func(u *LineUnitDTO) { u.Attempted, u.Failing = f.c2, true })),
			gitStation("stg", "matches", f.c3),
			gitStation("dev", "differs", f.a1),
			gitStation("old", "differs", f.u1),
			gitStation("ghost", "differs", f.e1),
			gitStation("torn", "mixed", ""),
			gitStation("unknown-applied", "applied unknown", ""),
		},
		Siding: []LineStationDTO{gitStation("lab", "differs", f.f1)},
	}
	h.push(dto)
	g := h.waitLine(7)
	if g.State != LineGitReady || g.Repo != "https://github.com/acme/fleet.git" || g.Target == nil || g.Target.SHA != f.c3 ||
		g.Target.Name != "main" || g.Target.FetchedAt == nil || g.CommitLink != "https://github.com/acme/fleet/commit/{sha}" {
		t.Fatalf("ordering %+v target %+v", g, g.Target)
	}
	got := map[string]LineGitStationDTO{}
	for _, s := range g.Stations {
		got[s.Cluster] = s
	}
	want := map[string]struct {
		state string
		n     int
		mb    string
		held  string
	}{
		"prd-a": {GitBehind, 2, "", "attempted"},
		"prd-b": {GitBehind, 2, "", "source"},
		"prd-c": {GitBehind, 1, "", "dependency"},
		"prd-d": {GitBehind, 1, "", ""}, // attempted c2, not T: no evidence tied to T
		"stg":   {GitAt, 0, "", ""},
		"dev":   {GitAhead, 1, "", ""},
		"old":   {GitUnrelated, 0, "", ""},
		"ghost": {GitUnknown, 0, "", ""},
		"lab":   {GitDiverged, 0, f.c1, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("ordered %d stations, want %d (mixed and applied-unknown keep their state): %+v", len(got), len(want), g.Stations)
	}
	for c, w := range want {
		s := got[c]
		held := ""
		if s.Held != nil {
			held = s.Held.Kind
		}
		if s.State != w.state || s.N != w.n || s.MergeBase != w.mb || held != w.held {
			t.Errorf("%s: %+v (held %+v), want %+v", c, s, s.Held, w)
		}
	}
	if h := got["prd-c"].Held; h.Text != "blocked by dependency flux-system/infra" || h.Unit != "Kustomization flux-system/apps" {
		t.Errorf("dependency evidence %+v", h)
	}
	if h := got["prd-a"].Held; h.Text != "attempted "+f.short(f.c3)+", failing" {
		t.Errorf("attempted evidence %+v", h)
	}

	// The Ledger: commits not yet everywhere, newest first, each with the
	// ordered stations that contain it. Unknown and unrelated stations have
	// no dots.
	var ledger []string
	for _, c := range g.Ledger {
		ledger = append(ledger, c.Subject+": "+strings.Join(c.Has, ","))
	}
	wantLedger := []string{"three: stg,dev", "two: prd-c,prd-d,stg,dev"}
	if !reflect.DeepEqual(ledger, wantLedger) || g.LedgerMore {
		t.Fatalf("ledger %q, want %q", ledger, wantLedger)
	}
	if g.Ledger[0].SHA != f.c3 || g.Ledger[0].Author != "Ada" || g.Ledger[0].Time.IsZero() {
		t.Fatalf("ledger commit %+v", g.Ledger[0])
	}
	// Commits only on an ahead or diverged station, listed under it.
	var only []string
	for _, o := range g.Only {
		var subj []string
		for _, c := range o.Commits {
			subj = append(subj, c.Subject)
		}
		only = append(only, o.State+" "+strings.Join(o.Clusters, ",")+": "+strings.Join(subj, ","))
	}
	if !reflect.DeepEqual(only, []string{"ahead dev: ahead one", "diverged lab: feature one"}) {
		t.Fatalf("only %q", only)
	}
}

func TestLineGitIsFencedAndReused(t *testing.T) {
	h, f, reads := newGitLineHarness(t)
	dto := LineDTO{Version: 3, Key: h.key, Stations: []LineStationDTO{gitStation("prd-a", "differs", f.c1)}}
	h.push(dto)
	first := h.waitLine(3)
	n := reads.Load()
	// The same facts under a newer LineDTO: the ordering is reused, answering
	// the new version, with no git read.
	dto.Version = 4
	h.push(dto)
	second := h.waitLine(4)
	if reads.Load() != n || second.Version <= first.Version || !reflect.DeepEqual(second.Stations, first.Stations) {
		t.Fatalf("reuse: reads %d -> %d, versions %d -> %d", n, reads.Load(), first.Version, second.Version)
	}
	// A changed revision is computed again.
	dto.Version, dto.Stations = 5, []LineStationDTO{gitStation("prd-a", "differs", f.c2)}
	h.push(dto)
	if third := h.waitLine(5); reads.Load() == n || third.Stations[0].N != 1 {
		t.Fatalf("a changed station was not recomputed: %+v", third.Stations)
	}
	// Every ordering was published as line:git, in version order.
	h.em.mu.Lock()
	defer h.em.mu.Unlock()
	for i := 1; i < len(h.em.got); i++ {
		if h.em.got[i].Version <= h.em.got[i-1].Version {
			t.Fatalf("line:git versions out of order: %d then %d", h.em.got[i-1].Version, h.em.got[i].Version)
		}
	}
}

func TestNoOrderingSaysWhy(t *testing.T) {
	h, _, _ := newGitLineHarness(t)
	cases := []struct {
		key   *LineKeyDTO
		state string
		has   string
	}{
		{&LineKeyDTO{Kind: line.KeyChart, Repo: "charts.example.test", RefName: "redis"}, LineGitNone, "charts"},
		{&LineKeyDTO{Kind: line.KeyGit, Repo: "github.com/acme/other", RefKind: "branch", RefName: "main"}, LineGitUnapproved, "not approved"},
		{&LineKeyDTO{Kind: line.KeyGit, Repo: "github.com/acme/fleet", RefKind: "semver", RefName: ">=1.0"}, LineGitNoTarget, "semver"},
		{&LineKeyDTO{Kind: line.KeyGit, Repo: "github.com/acme/fleet", RefKind: "unqualified", RefName: "main"}, LineGitNoTarget, "unqualified"},
		{&LineKeyDTO{Kind: line.KeyGit, Repo: "github.com/acme/fleet", RefKind: "branch", RefName: "release"}, LineGitUnfetched, "release is not in the fetched history"},
	}
	for i, c := range cases {
		v := uint64(100 + i)
		h.push(LineDTO{Version: v, Key: c.key})
		g := h.waitLine(v)
		if g.State != c.state || !strings.Contains(g.Reason, c.has) {
			t.Errorf("%+v: %s %q", c.key, g.State, g.Reason)
		}
		if c.state == LineGitUnapproved && g.Approve != "github.com/acme/other" {
			t.Errorf("unapproved: approve %q", g.Approve)
		}
	}
	GitLineSink(h.svc)(LineDTO{Version: 200}, nil) // no key at all
	if g := h.waitLine(200); g.State != LineGitNone {
		t.Errorf("no key: %+v", g)
	}
	// Never fetched: unfetched, naming the fetch's error when there is one.
	h.store.mu.Lock()
	h.store.status["https://github.com/acme/fleet.git"] = gitcache.Status{Error: "Permission denied (publickey)"}
	h.store.mu.Unlock()
	h.push(LineDTO{Version: 201})
	if g := h.waitLine(201); g.State != LineGitUnfetched || !strings.Contains(g.Reason, "publickey") {
		t.Errorf("unfetched: %+v", g)
	}
}

func TestFetchDueFollowsTheCadence(t *testing.T) {
	h, _, _ := newGitLineHarness(t)
	now := time.Date(2026, 10, 15, 20, 34, 0, 0, time.UTC) // 3 minutes after the last attempt
	h.svc.d.Now = func() time.Time { return now }
	h.svc.FetchDue()
	time.Sleep(50 * time.Millisecond)
	h.store.mu.Lock()
	n := len(h.store.refreshed["https://github.com/acme/fleet.git"])
	h.store.mu.Unlock()
	if n != 0 {
		t.Fatal("fetched within 5 minutes of the last attempt")
	}
	now = now.Add(3 * time.Minute)
	h.svc.FetchDue()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.store.mu.Lock()
		got := h.store.refreshed["https://github.com/acme/fleet.git"]
		h.store.mu.Unlock()
		if reflect.DeepEqual(got, []string{"refs/heads/main"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not fetched after 6 minutes: %v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// An unapproved or untracked repository is never fetched by the cadence.
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.refreshed) != 1 {
		t.Fatalf("refreshed %v", h.store.refreshed)
	}
}

func TestCommitLinks(t *testing.T) {
	for _, c := range []struct{ key, web, want string }{
		{"github.com/acme/fleet", "", "https://github.com/acme/fleet/commit/{sha}"},
		{"gitlab.com/acme/group/fleet", "", "https://gitlab.com/acme/group/fleet/-/commit/{sha}"},
		{"bitbucket.org/acme/fleet", "", "https://bitbucket.org/acme/fleet/commits/{sha}"},
		{"dev.azure.com/acme/platform/_git/fleet", "", "https://dev.azure.com/acme/platform/_git/fleet/commit/{sha}"},
		{"ssh.dev.azure.com/v3/acme/platform/fleet", "", "https://dev.azure.com/acme/platform/_git/fleet/commit/{sha}"},
		{"git.internal/platform/fleet", "", ""},
		{"git.internal/platform/fleet", "https://git.internal/platform/fleet/", "https://git.internal/platform/fleet/commit/{sha}"},
		{"git.internal/platform/fleet", "https://git.internal/platform/fleet/-/commit/{sha}", "https://git.internal/platform/fleet/-/commit/{sha}"},
	} {
		if got := commitLink(c.key, c.web); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.key, c.web, got, c.want)
		}
	}
}

// Ledger containment is exact even when a station misses more than 20
// commits and commit times tie (design review of cbef8e4): every displayed
// commit's dots match git's own ancestry.
func TestLedgerContainmentIsExact(t *testing.T) {
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
		// One timestamp for every commit: git's walk order and the Ledger's
		// time-then-id order disagree at every tie.
		c.Env = []string{"HOME=" + work, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
			"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.test", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.test",
			"GIT_AUTHOR_DATE=2026-10-01T10:00:00Z", "GIT_COMMITTER_DATE=2026-10-01T10:00:00Z"}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "base")
	base := run("rev-parse", "HEAD")
	run("checkout", "-qb", "y")
	for i := 1; i <= 3; i++ {
		run("commit", "-q", "--allow-empty", "-m", fmt.Sprintf("y%d", i))
	}
	yTip := run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	for i := 1; i <= 25; i++ {
		run("commit", "-q", "--allow-empty", "-m", fmt.Sprintf("x%d", i))
	}
	run("merge", "-q", "--no-ff", "-m", "merge y", "y")
	run("commit", "-q", "--allow-empty", "-m", "after merge")
	base2 := gitcli.CacheBase(t.TempDir())
	if err := gitcli.EnsureBase(base2); err != nil {
		t.Fatal(err)
	}
	cfg := gitcli.GlobalConfigPath(base2)
	if err := gitcli.WriteGlobalConfig(cfg, gitcli.Helpers{}, ""); err != nil {
		t.Fatal(err)
	}
	runner := gitcli.Runner{Git: g, Env: env, GlobalConfig: cfg}
	cache := gitcli.CacheDir(base2, "https://github.com/acme/fleet.git")
	if err := runner.InitRepo(context.Background(), cache, "https://github.com/acme/fleet.git"); err != nil {
		t.Fatal(err)
	}
	run("push", "-q", cache, "main:refs/klyx/tracked/refs/heads/main")

	h := newGitHarness(t, lineFleet, nil)
	h.store.mu.Lock()
	h.store.status["https://github.com/acme/fleet.git"] = gitcache.Status{FetchedAt: time.Now(), AttemptedAt: time.Now()}
	h.store.read = func(url string, fn func(gitcli.Repo) error) error { return fn(runner.Repo(cache)) }
	h.store.mu.Unlock()
	h.push(LineDTO{Version: 9, Key: h.key, Stations: []LineStationDTO{
		// Both miss more than 20 commits, and git walks them differently:
		// the y side misses x commits deep in base's walk, past its 20.
		gitStation("far", "differs", base),
		gitStation("side", "differs", yTip),
	}})
	got := h.waitLine(9)
	if got.State != LineGitReady || len(got.Ledger) != 20 || !got.LedgerMore {
		t.Fatalf("ledger %d commits, more %v, state %s %s", len(got.Ledger), got.LedgerMore, got.State, got.Reason)
	}
	for _, c := range got.Ledger {
		for cluster, sha := range map[string]string{"far": base, "side": yTip} {
			truth := exec.Command(g.Path, "--git-dir="+cache, "merge-base", "--is-ancestor", c.SHA, sha).Run() == nil
			has := slices.Contains(c.Has, cluster)
			if has != truth {
				t.Errorf("%s %q: the Ledger says %s has it = %v; git says %v", c.SHA[:7], c.Subject, cluster, has, truth)
			}
		}
	}
}
