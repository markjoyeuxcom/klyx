//go:build !windows

package gitcache

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitcli"
)

// Slice 6b (slice 6 note §4, §8): the acquisition contract over the
// production transport - an HTTPS git http-backend fixture trusted through
// a test CA in the generated config; file and plain-HTTP stay forbidden.

func plainEnv(dir string) []string {
	return []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_NO_LAZY_FETCH=1", "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.test", "GIT_COMMITTER_NAME=Ada",
		"GIT_COMMITTER_EMAIL=ada@example.test", "GIT_AUTHOR_DATE=2026-10-01T10:00:00Z", "GIT_COMMITTER_DATE=2026-10-01T10:00:00Z"}
}

// plain runs the test's own git, isolated, to build and inspect fixtures.
func plain(t *testing.T, g gitcli.Git, dir string, stdin string, args ...string) string {
	t.Helper()
	c := exec.Command(g.Path, args...)
	c.Dir, c.Env, c.Stdin = dir, plainEnv(dir), strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	t      *testing.T
	g      gitcli.Git
	work   string // the upstream working repo
	served string // the bare repo http-backend serves
	url    string
	store  *Store
}

// newFixture serves fleet.git over HTTPS and builds a store whose generated
// config trusts the fixture's CA. Upstream: c1 (apps/a.yaml, apps/b.yaml,
// base/k.yaml) <- c2 (a.yaml changed) on main; annotated v1 on c1.
func newFixture(t *testing.T, serverConfig ...string) *fixture {
	env := gitcli.OwnerEnv(os.Getenv, os.Environ())
	env.Home = t.TempDir()
	g, err := gitcli.Locate(context.Background(), env)
	if err != nil {
		t.Skipf("no usable git: %v", err)
	}
	root := t.TempDir()
	f := &fixture{t: t, g: g, work: t.TempDir(), served: filepath.Join(root, "fleet.git")}
	plain(t, g, root, "", "init", "-q", "--bare", f.served)
	cfg := append([]string{"uploadpack.allowFilter", "true", "uploadpack.allowAnySHA1InWant", "true"}, serverConfig...)
	for i := 0; i+1 < len(cfg); i += 2 {
		plain(t, g, root, "", "--git-dir="+f.served, "config", cfg[i], cfg[i+1])
	}
	plain(t, g, f.work, "", "init", "-q", "-b", "main")
	f.write("apps/a.yaml", "replicas: 1\n")
	f.write("apps/b.yaml", "b\n")
	f.write("base/k.yaml", "k\n")
	f.commit("first")
	f.write("apps/a.yaml", "replicas: 3\n")
	f.commit("scale a")
	plain(t, g, f.work, "", "tag", "-a", "-m", "v1", "v1", "HEAD~1")
	f.push("main", "refs/tags/v1")

	execPath := plain(t, g, root, "", "--exec-path")
	srv := httptest.NewTLSServer(&cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + root,
			"PATH=/usr/bin:/bin"},
	})
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	f.url = srv.URL + "/fleet.git"
	base := gitcli.CacheBase(t.TempDir())
	if err := gitcli.EnsureBase(base); err != nil {
		t.Fatal(err)
	}
	gc := gitcli.GlobalConfigPath(base)
	if err := gitcli.WriteGlobalConfig(gc, gitcli.Helpers{}, ca); err != nil {
		t.Fatal(err)
	}
	f.store = New(base, gitcli.Runner{Git: g, Env: env, GlobalConfig: gc})
	return f
}

func (f *fixture) write(p, s string) {
	full := filepath.Join(f.work, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(msg string) string {
	plain(f.t, f.g, f.work, "", "add", "-A")
	plain(f.t, f.g, f.work, "", "commit", "-qm", msg)
	return f.rev("HEAD")
}

func (f *fixture) rev(r string) string { return plain(f.t, f.g, f.work, "", "rev-parse", r) }

func (f *fixture) push(refs ...string) {
	plain(f.t, f.g, f.work, "", append([]string{"push", "-q", "-f", f.served}, refs...)...)
}

func (f *fixture) cache() string { return gitcli.CacheDir(f.store.base, f.url) }

func ref(t *testing.T, kind, name string) gitcli.Ref {
	t.Helper()
	r, err := gitcli.RefFromKey(kind, name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// tracked reads the cache's Klyx refs as name -> value.
func (f *fixture) tracked() map[string]string {
	out := map[string]string{}
	err := f.store.Read(f.url, func(p gitcli.Repo) error {
		vs, err := p.Refs(context.Background())
		for _, v := range vs {
			out[v.Name] = v.Value
		}
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) missing(ids ...string) []string {
	var m []string
	err := f.store.Read(f.url, func(p gitcli.Repo) error {
		var err error
		m, err = p.Missing(context.Background(), ids)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) packFiles() []string {
	names, _ := filepath.Glob(filepath.Join(f.cache(), "objects", "pack", "pack-*"))
	for i, n := range names {
		names[i] = filepath.Base(n)
	}
	slices.Sort(names)
	return names
}

func (f *fixture) fsck() {
	f.t.Helper()
	plain(f.t, f.g, f.cache(), "", "--git-dir="+f.cache(), "fsck", "--connectivity-only", "--no-dangling")
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestAHistoryRefreshPublishesCommitsAndTreesOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	main, v1 := ref(t, "branch", "main"), ref(t, "tag", "v1")
	c1, c2 := f.rev("HEAD~1"), f.rev("HEAD")
	pin := ref(t, "commit", c1)
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main, v1, pin}); err != nil {
		t.Fatal(err)
	}
	got := f.tracked()
	if got[main.Published()] != c2 || got[v1.Published()] != f.rev("refs/tags/v1") || got[pin.Published()] != c1 || len(got) != 3 {
		t.Fatalf("tracked %v", got)
	}
	blobs := []string{f.rev("HEAD:apps/a.yaml"), f.rev("HEAD:base/k.yaml")}
	if m := f.missing(append(blobs, c1, c2)...); !slices.Equal(m, blobs) {
		t.Fatalf("a history refresh brought blobs: missing %v", m)
	}
	if exists(f.cache()+stagingExt) || exists(filepath.Join(f.cache(), journalName)) {
		t.Fatal("staging or the journal survived a publication")
	}
	if st := f.store.Status(f.url); st.FetchedAt.IsZero() || st.Error != "" {
		t.Fatalf("status %+v", st)
	}
	f.fsck()

	// After a restart: recovery finds nothing to do, and the cache still
	// passes fsck with its blobs missing.
	again := New(f.store.base, f.store.run)
	if err := again.Start(ctx); err != nil {
		t.Fatal(err)
	}
	f.store = again
	f.fsck()

	// Incremental: a new commit moves main; nothing new, nothing published.
	f.write("apps/b.yaml", "b2\n")
	c3 := f.commit("bump b")
	f.push("main")
	before := len(f.packFiles())
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main}); err != nil {
		t.Fatal(err)
	}
	if f.tracked()[main.Published()] != c3 || len(f.packFiles()) <= before {
		t.Fatalf("main did not move to %s: %v", c3, f.tracked())
	}
	packs := f.packFiles()
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.packFiles(), packs) {
		t.Fatalf("an unchanged refresh published packs: %v -> %v", packs, f.packFiles())
	}
	f.fsck()
}

func TestContentAcquisitionBringsOnlyWhatItNeeds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	main := ref(t, "branch", "main")
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main}); err != nil {
		t.Fatal(err)
	}
	c1, c2 := f.rev("HEAD~1"), f.rev("HEAD")
	r1, _ := gitcli.SHA(c1)
	r2, _ := gitcli.SHA(c2)
	aOld, aNew, b, k := f.rev("HEAD~1:apps/a.yaml"), f.rev("HEAD:apps/a.yaml"), f.rev("HEAD:apps/b.yaml"), f.rev("HEAD:base/k.yaml")

	// A content diff: the old and new blob of each changed path, nothing else.
	if err := f.store.AcquireDiff(ctx, f.url, r1, r2, []string{"apps"}); err != nil {
		t.Fatal(err)
	}
	if m := f.missing(aOld, aNew, b, k); !slices.Equal(m, []string{b, k}) {
		t.Fatalf("after the diff acquisition, missing %v", m)
	}
	var text string
	if err := f.store.Read(f.url, func(p gitcli.Repo) error {
		var err error
		text, _, err = p.Diff(ctx, r1, r2, []string{"apps"})
		return err
	}); err != nil || !strings.Contains(text, "+replicas: 3") {
		t.Fatalf("the diff after acquisition: %q %v", text, err)
	}

	// A snapshot: every blob under the paths at one revision.
	if err := f.store.AcquireSnapshot(ctx, f.url, main.Tracked(), []string{"apps"}); err != nil {
		t.Fatal(err)
	}
	if m := f.missing(aOld, aNew, b, k); !slices.Equal(m, []string{k}) {
		t.Fatalf("after the snapshot acquisition, missing %v", m)
	}
	out := filepath.Join(t.TempDir(), "apps.tar")
	if err := f.store.Read(f.url, func(p gitcli.Repo) error { return p.Archive(ctx, main.Tracked(), "apps", out) }); err != nil {
		t.Fatal(err)
	}
	if list, err := exec.Command("/usr/bin/tar", "-tf", out).Output(); err != nil ||
		!strings.Contains(string(list), "apps/b.yaml") || strings.Contains(string(list), "base/") {
		t.Fatalf("the snapshot archive holds %q %v", list, err)
	}
	// The whole tree is not acquired: archiving it fails, never downloads.
	if err := f.store.Read(f.url, func(p gitcli.Repo) error {
		return p.Archive(ctx, main.Tracked(), "", filepath.Join(t.TempDir(), "all.tar"))
	}); err == nil || !strings.Contains(err.Error(), "lazy fetching disabled") {
		t.Fatalf("archiving unacquired content: %v", err)
	}
	// Acquired already: nothing is fetched or published again.
	packs := f.packFiles()
	if err := f.store.AcquireSnapshot(ctx, f.url, main.Tracked(), []string{"apps"}); err != nil || !slices.Equal(packs, f.packFiles()) {
		t.Fatalf("a second identical acquisition: %v %v", err, f.packFiles())
	}
	f.fsck()

	// Above 5,000 ids the acquisition is refused before anything runs.
	for i := 0; i <= gitcli.MaxContentIDs; i++ {
		f.write(fmt.Sprintf("many/%04d.txt", i), fmt.Sprintf("%d\n", i))
	}
	f.commit("many")
	f.push("main")
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AcquireSnapshot(ctx, f.url, main.Tracked(), []string{"many"}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("5,001 files: %v", err)
	}
}

func TestTheContentContract(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	check := contentCheck([]string{a, b})
	for name, o := range map[string]gitcli.PackObjects{
		"an unasked blob": {Blobs: 1, BlobIDs: []string{c}},
		"a commit":        {Commits: 1, Blobs: 1, BlobIDs: []string{a}},
		"a tree":          {Trees: 1},
		"too many blobs":  {Blobs: 3, BlobIDs: []string{a, b, a}},
	} {
		if check(o) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := check(gitcli.PackObjects{Blobs: 2, BlobIDs: []string{b, a}}); err != nil {
		t.Fatal(err)
	}
}

func TestAServerIgnoringTheFilterIsRefused(t *testing.T) {
	f := newFixture(t, "uploadpack.allowFilter", "false")
	err := f.store.RefreshHistory(context.Background(), f.url, []gitcli.Ref{ref(t, "branch", "main")})
	if !errors.Is(err, ErrNoPartialClone) {
		t.Fatalf("got %v", err)
	}
	if len(f.tracked()) != 0 || len(f.packFiles()) != 0 || exists(f.cache()+stagingExt) {
		t.Fatalf("a refused refresh left refs %v or packs %v", f.tracked(), f.packFiles())
	}
	if st := f.store.Status(f.url); !strings.Contains(st.Error, "partial clone") || !st.FetchedAt.IsZero() {
		t.Fatalf("status %+v", st)
	}
}

// bigHistory adds n commits on refs/heads/big through fast-import: each
// changes one of 50 files, so every commit has new trees.
func (f *fixture) bigHistory(n int) {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		msg := fmt.Sprintf("big %d", i)
		fmt.Fprintf(&b, "commit refs/heads/big\nmark :%d\ncommitter Ada <ada@example.test> %d +0000\ndata %d\n%s\n", i, 1790000000+i, len(msg), msg)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		body := fmt.Sprintf("value %d %s\n", i, strings.Repeat("z", i%97))
		fmt.Fprintf(&b, "M 100644 inline f%02d.txt\ndata %d\n%s\n", i%50, len(body), body)
	}
	plain(f.t, f.g, f.served, b.String(), "--git-dir="+f.served, "fast-import", "--quiet")
}

func TestACapStopsTheFetchAndKeepsTheLastGoodHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	main, big := ref(t, "branch", "main"), ref(t, "branch", "big")
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main}); err != nil {
		t.Fatal(err)
	}
	good, packs := f.tracked(), f.packFiles()
	f.bigHistory(3000)
	f.store.repoCap = f.store.Size(f.url) + 8<<10
	f.store.sample = time.Millisecond
	err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main, big})
	if err == nil || !strings.Contains(err.Error(), "room left") {
		t.Fatalf("over the cap: %v", err)
	}
	if !mapsEqual(f.tracked(), good) || !slices.Equal(f.packFiles(), packs) || exists(f.cache()+stagingExt) {
		t.Fatalf("a capped refresh changed the cache: %v %v", f.tracked(), f.packFiles())
	}
	if st := f.store.Status(f.url); st.Error == "" || st.FetchedAt.IsZero() {
		t.Fatalf("a capped refresh keeps the last good time and names the failure: %+v", st)
	}
	// Nothing left at all under the total cap.
	f.store.repoCap, f.store.totalCap = DefaultRepoCap, 1
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main}); err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("a full cache: %v", err)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestACancelledCallerLeavesTheQueue(t *testing.T) {
	f := newFixture(t)
	if err := f.store.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{ref(t, "branch", "main")})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("a queued caller: %v after %v", err, time.Since(start))
	}
	f.store.unlock()
	if err := f.store.RefreshHistory(context.Background(), f.url, []gitcli.Ref{ref(t, "branch", "main")}); err != nil {
		t.Fatalf("the queue moved on: %v", err)
	}
}

func TestAFailedRefreshKeepsTheLastGoodHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	main := ref(t, "branch", "main")
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main}); err != nil {
		t.Fatal(err)
	}
	good, fetched := f.tracked(), f.store.Status(f.url).FetchedAt
	if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main, ref(t, "branch", "gone")}); err == nil {
		t.Fatal("a missing upstream ref was fetched")
	}
	st := f.store.Status(f.url)
	if !mapsEqual(f.tracked(), good) || st.Error == "" || !st.FetchedAt.Equal(fetched) || st.AttemptedAt.Before(fetched) {
		t.Fatalf("after a failed refresh: %v %+v", f.tracked(), st)
	}
}

// --- recovery (parent §7 step 5), by abandoning the acquisition between
// journaled steps as a killed process would, then restarting.

// crashFirstPublication fetches main and v1 into an empty cache and
// abandons the publication at step.
func crashFirstPublication(t *testing.T, step string) (*fixture, []string) {
	f := newFixture(t)
	f.store.crash = func(s string) error {
		if s == step {
			return errCrash
		}
		return nil
	}
	err := f.store.RefreshHistory(context.Background(), f.url, []gitcli.Ref{ref(t, "branch", "main"), ref(t, "tag", "v1")})
	if !errors.Is(err, errCrash) {
		t.Fatalf("crash at %s: %v", step, err)
	}
	j, ok, err := readJournal(f.cache())
	if err != nil || !ok {
		t.Fatalf("no journal after a crash at %s: %v", step, err)
	}
	return f, j.Files
}

// restart is a fresh store over the same root, started.
func (f *fixture) restart() error {
	f.store = New(f.store.base, f.store.run)
	return f.store.Start(context.Background())
}

func TestRecoveryRollsBackWhatNeverCommitted(t *testing.T) {
	for _, step := range []string{"journal", "files-partial", "files"} {
		t.Run(step, func(t *testing.T) {
			f, files := crashFirstPublication(t, step)
			if err := f.restart(); err != nil {
				t.Fatal(err)
			}
			if len(f.tracked()) != 0 || len(f.packFiles()) != 0 || exists(filepath.Join(f.cache(), journalName)) ||
				exists(f.cache()+stagingExt) {
				t.Fatalf("not rolled back: refs %v packs %v (journaled %v)", f.tracked(), f.packFiles(), files)
			}
			if err := f.store.RefreshHistory(context.Background(), f.url, []gitcli.Ref{ref(t, "branch", "main")}); err != nil {
				t.Fatalf("a refresh after rollback: %v", err)
			}
			f.fsck()
		})
	}
}

func TestRecoveryFinalizesWhatCommitted(t *testing.T) {
	for _, step := range []string{"refs", "mark", "remove"} {
		t.Run(step, func(t *testing.T) {
			f, files := crashFirstPublication(t, step)
			if err := f.restart(); err != nil {
				t.Fatal(err)
			}
			if len(f.tracked()) != 2 || !slices.Equal(f.packFiles(), sorted(files)) || exists(filepath.Join(f.cache(), journalName)) {
				t.Fatalf("not finalized: refs %v packs %v", f.tracked(), f.packFiles())
			}
			f.fsck()
		})
	}
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func TestRecoveryOfMixedRefs(t *testing.T) {
	main := ref(t, "branch", "main")
	t.Run("files whole: forward", func(t *testing.T) {
		f, _ := crashFirstPublication(t, "refs")
		plain(t, f.g, f.cache(), "", "--git-dir="+f.cache(), "update-ref", "-d", main.Published())
		if err := f.restart(); err != nil {
			t.Fatal(err)
		}
		if got := f.tracked(); len(got) != 2 || got[main.Published()] != f.rev("main") {
			t.Fatalf("not moved forward: %v", got)
		}
		f.fsck()
	})
	t.Run("a file missing: restore and roll back", func(t *testing.T) {
		f, files := crashFirstPublication(t, "refs")
		plain(t, f.g, f.cache(), "", "--git-dir="+f.cache(), "update-ref", "-d", main.Published())
		for _, name := range files {
			if strings.HasSuffix(name, ".rev") {
				if err := os.Remove(filepath.Join(f.cache(), "objects", "pack", name)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := f.restart(); err != nil {
			t.Fatal(err)
		}
		if len(f.tracked()) != 0 || len(f.packFiles()) != 0 || exists(filepath.Join(f.cache(), journalName)) {
			t.Fatalf("not restored: refs %v packs %v", f.tracked(), f.packFiles())
		}
	})
}

func TestUncertainRefsDeleteNothing(t *testing.T) {
	main := ref(t, "branch", "main")
	for name, tamper := range map[string]func(f *fixture){
		"a ref at a third value": func(f *fixture) {
			plain(t, f.g, f.cache(), "", "--git-dir="+f.cache(), "update-ref", main.Published(), f.rev("main~1"))
		},
		"marked yet all at old": func(f *fixture) {
			for _, r := range []string{main.Published(), ref(t, "tag", "v1").Published()} {
				plain(t, f.g, f.cache(), "", "--git-dir="+f.cache(), "update-ref", "-d", r)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			step := "refs"
			if strings.HasPrefix(name, "marked") {
				step = "mark"
			}
			f, files := crashFirstPublication(t, step)
			tamper(f)
			if err := f.restart(); err != nil {
				t.Fatal(err)
			}
			if !exists(filepath.Join(f.cache(), repairName)) || !exists(filepath.Join(f.cache(), journalName)) ||
				!slices.Equal(f.packFiles(), sorted(files)) {
				t.Fatalf("uncertain recovery deleted something or did not stop: packs %v", f.packFiles())
			}
			if err := f.store.Read(f.url, func(gitcli.Repo) error { return nil }); !errors.Is(err, ErrNeedsRepair) {
				t.Fatalf("a cache that needs repair was read: %v", err)
			}
			if err := f.store.RefreshHistory(context.Background(), f.url, []gitcli.Ref{main}); !errors.Is(err, ErrNeedsRepair) {
				t.Fatalf("a cache that needs repair was fetched into: %v", err)
			}
			if !f.store.Status(f.url).NeedsRepair {
				t.Fatal("the status does not say it needs repair")
			}
			// "Clear cache" rebuilds it from scratch.
			if err := f.store.Clear(context.Background(), f.url); err != nil || exists(f.cache()) {
				t.Fatalf("clear: %v", err)
			}
			if err := f.store.RefreshHistory(context.Background(), f.url, []gitcli.Ref{main}); err != nil {
				t.Fatal(err)
			}
			f.fsck()
		})
	}
}

func TestADamagedJournalNamesNothingToDelete(t *testing.T) {
	f, _ := crashFirstPublication(t, "journal")
	j := `{"kind":"history","files":["../../config"],"refs":[],"committed":false}`
	if err := os.WriteFile(filepath.Join(f.cache(), journalName), []byte(j), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.restart(); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(f.cache(), "config")) || !f.store.Status(f.url).NeedsRepair {
		t.Fatal("a damaged journal was acted on")
	}
}

func TestRecoveryOfAContentAcquisition(t *testing.T) {
	for step, wantPresent := range map[string]bool{"files": false, "mark": true} {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			main := ref(t, "branch", "main")
			if err := f.store.RefreshHistory(ctx, f.url, []gitcli.Ref{main}); err != nil {
				t.Fatal(err)
			}
			f.store.crash = func(s string) error {
				if s == step {
					return errCrash
				}
				return nil
			}
			if err := f.store.AcquireSnapshot(ctx, f.url, main.Tracked(), []string{"base"}); !errors.Is(err, errCrash) {
				t.Fatalf("crash: %v", err)
			}
			if err := f.restart(); err != nil {
				t.Fatal(err)
			}
			k := f.rev("HEAD:base/k.yaml")
			if present := len(f.missing(k)) == 0; present != wantPresent || exists(filepath.Join(f.cache(), journalName)) {
				t.Fatalf("after recovery the blob present=%v (want %v)", present, wantPresent)
			}
			f.fsck()
		})
	}
}

func TestValidationRefusesWhatItCannotEnumerate(t *testing.T) {
	s := &Store{}
	h := strings.Repeat("a", 40)
	for name, files := range map[string][]string{
		"a keep file":        {"objects/pack/pack-" + h + ".pack", "objects/pack/pack-" + h + ".idx", "objects/pack/pack-" + h + ".keep"},
		"a temporary pack":   {"objects/pack/tmp_pack_x1"},
		"an index alone":     {"objects/pack/pack-" + h + ".idx"},
		"a loose object":     {"objects/pack/.empty-dir", "objects/ab/" + strings.Repeat("c", 38)},
		"a multi-pack index": {"objects/pack/multi-pack-index"},
		"a bitmap":           {"objects/pack/pack-" + h + ".pack", "objects/pack/pack-" + h + ".idx", "objects/pack/pack-" + h + ".bitmap"},
	} {
		stg := t.TempDir()
		for _, f := range files {
			p := filepath.Join(stg, f)
			if strings.HasSuffix(f, ".empty-dir") {
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.validate(context.Background(), stg, 1<<20, func(gitcli.PackObjects) error { return nil }); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A step that fails (rather than a process that dies) goes to recovery at
// once: rolled back before the refs commit, finalized after - and the
// step's own error is returned either way.
func TestAFailedStepIsRecoveredAtOnce(t *testing.T) {
	injected := errors.New("injected step failure")
	for step, committed := range map[string]bool{
		"journal": false, "files-partial": false, "files": false, "refs": true, "mark": true, "remove": true,
	} {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			f.store.crash = func(s string) error {
				if s == step {
					return injected
				}
				return nil
			}
			err := f.store.RefreshHistory(context.Background(), f.url, []gitcli.Ref{ref(t, "branch", "main"), ref(t, "tag", "v1")})
			if !errors.Is(err, injected) {
				t.Fatalf("the step's error was not kept: %v", err)
			}
			if exists(filepath.Join(f.cache(), journalName)) || exists(filepath.Join(f.cache(), repairName)) {
				t.Fatal("recovery did not run at once")
			}
			if n := len(f.tracked()); (n == 2) != committed || (n != 0 && n != 2) {
				t.Fatalf("refs after a failure at %s: %v", step, f.tracked())
			}
			if (len(f.packFiles()) > 0) != committed {
				t.Fatalf("packs after a failure at %s: %v", step, f.packFiles())
			}
			f.fsck()
		})
	}
}
