//go:build !windows

package gitcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The Line slice 6a (slice 6 note §3, §8): the binary and its version gate,
// the environment allowlist, the typed argv, ref/URL/path refusals, the
// owner's credential helpers, and the read queries on a fixture repo built
// with plain git.

const fixtureURL = "https://git.example.test/acme/fleet.git"

// envProbe as the global config path turns this test binary into a fake git
// that prints exactly the environment it received - no shell in between to
// add PWD or SHLVL.
const envProbe = "/klyx/env-probe"

func TestMain(m *testing.M) {
	if os.Getenv("GIT_CONFIG_GLOBAL") == envProbe {
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestParseVersion(t *testing.T) {
	for _, c := range []struct {
		out  string
		want Version
		ok   bool
	}{
		{"git version 2.56.0\n", Version{Major: 2, Minor: 56}, true},
		{"git version 2.54.0 (Apple Git-157)\n", Version{Major: 2, Minor: 54}, true},
		{"git version 2.45.1.windows.1\n", Version{Major: 2, Minor: 45, Patch: 1}, true},
		{"git version 2.39.5", Version{Major: 2, Minor: 39, Patch: 5}, true},
		{"git version 3.0", Version{Major: 3}, true},
		{"hub version 2.14.2", Version{}, false},
		{"", Version{}, false},
		{"git version two", Version{}, false},
	} {
		v, err := ParseVersion(c.out)
		if (err == nil) != c.ok {
			t.Fatalf("%q: err=%v", c.out, err)
		}
		if c.ok && (v.Major != c.want.Major || v.Minor != c.want.Minor || v.Patch != c.want.Patch) {
			t.Fatalf("%q: got %+v", c.out, v)
		}
	}
	if (Version{Major: 2, Minor: 44, Patch: 9}).AtLeast(MinVersion) || !(Version{Major: 2, Minor: 45}).AtLeast(MinVersion) ||
		!(Version{Major: 3}).AtLeast(MinVersion) {
		t.Fatal("the 2.45 floor is wrong")
	}
}

func fakeGit(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func testEnv(t *testing.T) Env {
	return Env{Home: t.TempDir(), Path: "/usr/bin:/bin", User: "owner", Tmpdir: os.TempDir()}
}

func TestLocateTurnsGitOffWithTheReason(t *testing.T) {
	for _, c := range []struct{ script, reason string }{
		{`echo "git version 2.44.2"`, "older than 2.45"},
		{`echo "nonsense"`, "unrecognised git version output"},
		{`exit 3`, "--version did not run"},
	} {
		t.Setenv("KLYX_GIT_PATH", fakeGit(t, c.script))
		_, err := Locate(context.Background(), testEnv(t))
		if !IsOff(err) || !strings.Contains(err.Error(), c.reason) {
			t.Fatalf("%s: got %v", c.script, err)
		}
	}
	t.Setenv("KLYX_GIT_PATH", fakeGit(t, `echo "git version 2.54.0 (Apple Git-157)"`))
	g, err := Locate(context.Background(), testEnv(t))
	if err != nil || g.Version.Minor != 54 {
		t.Fatalf("a 2.54 git: %+v %v", g, err)
	}
}

func TestTheEnvironmentIsExactlyTheAllowlist(t *testing.T) {
	environ := []string{
		"HOME=/home/o", "PATH=/usr/bin", "USER=o", "TMPDIR=/tmp/o", "SSH_AUTH_SOCK=/tmp/agent", "LANG=en_GB.UTF-8",
		"LC_ALL=en_GB.UTF-8", "XDG_CONFIG_HOME=/home/o/.xdg",
		"GIT_EXEC_PATH=/evil", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/evil/objects", "GIT_SSH_COMMAND=evil", "GIT_DIR=/evil",
		"GIT_CONFIG_PARAMETERS='core.hooksPath'='/evil'", "GIT_CONFIG_COUNT=1", "KUBECONFIG=/home/o/.kube/config",
		"AWS_SECRET_ACCESS_KEY=s3cr3t", "AZURE_CLIENT_SECRET=s3cr3t", "HTTPS_PROXY=http://proxy", "LD_PRELOAD=/evil.so",
	}
	get := func(k string) string {
		for _, kv := range environ {
			if v, ok := strings.CutPrefix(kv, k+"="); ok {
				return v
			}
		}
		return ""
	}
	env := OwnerEnv(get, environ)
	// The environment git actually receives, through procrun: a fake git
	// that prints it.
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := Runner{Git: Git{Path: self}, Env: env, GlobalConfig: envProbe}
	res, err := r.run(context.Background(), forEachRef(dir))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	want := []string{
		"HOME=/home/o", "PATH=/usr/bin", "USER=o", "TMPDIR=/tmp/o", "LANG=en_GB.UTF-8", "LC_ALL=en_GB.UTF-8",
		"SSH_AUTH_SOCK=/tmp/agent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + envProbe, "GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false", "SSH_ASKPASS=/usr/bin/false", "SSH_ASKPASS_REQUIRE=never", "GCM_INTERACTIVE=never",
		"GIT_PAGER=cat", "PAGER=cat", "GIT_LITERAL_PATHSPECS=1", "GIT_DIR=" + dir, "GIT_NO_LAZY_FETCH=1",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment:\n got %q\nwant %q", got, want)
	}
	// Only an acquisition fetch may lazy-fetch; nothing else changes.
	fetch := env.isolated(envProbe, dir, true)
	if slices.Contains(fetch, "GIT_NO_LAZY_FETCH=1") || len(fetch) != len(want)-1 {
		t.Fatalf("an acquisition fetch's environment: %q", fetch)
	}
}

func TestEveryArgvLeadsWithTheGlobalFlags(t *testing.T) {
	c := forEachRef("/c.git")
	want := []string{"--no-pager", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always", "-c", "core.fsmonitor=false",
		"-c", "submodule.recurse=false", "-c", "diff.external=", "-c", "http.followRedirects=false",
		"-c", "fetch.unpackLimit=1", "-c", "transfer.unpackLimit=1", "-c", "gc.auto=0", "-c", "maintenance.auto=false",
		"for-each-ref", "--format=%(refname)%00%(objectname)%00%(*objectname)", "--end-of-options", "refs/klyx/"}
	if !reflect.DeepEqual(c.argv(), want) {
		t.Fatalf("argv %q", c.argv())
	}
}

func mustRef(t *testing.T, kind, name string) Ref {
	t.Helper()
	r, err := RefFromKey(kind, name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTheTypedArgv(t *testing.T) {
	const a, b = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	ra, rb := Rev{a}, Rev{b}
	main := mustRef(t, "branch", "main")
	v1 := mustRef(t, "tag", "v1.2")
	pin := mustRef(t, "commit", b)
	check := func(name string, c cmd, err error, args []string, gitDir string, stdin string, fetch bool) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(c.args, args) || c.gitDir != gitDir || string(c.stdin) != stdin || c.fetch != fetch {
			t.Fatalf("%s:\n got %q dir=%q stdin=%q fetch=%v\nwant %q dir=%q stdin=%q fetch=%v", name, c.args, c.gitDir,
				c.stdin, c.fetch, args, gitDir, stdin, fetch)
		}
	}
	c, err := initBare("/k/c.git")
	check("init", c, err, []string{"init", "--bare", "--quiet", "--template=", "--end-of-options", "/k/c.git"}, "", "", false)
	c, err = fetchHistory("/k/s.git", []Ref{main, v1, pin, main})
	check("fetch history", c, err, []string{"fetch", "--atomic", "--no-tags", "--no-write-fetch-head", "--recurse-submodules=no",
		"--filter=blob:none", "--quiet", "--end-of-options", "origin",
		"+refs/heads/main:refs/klyx/staging/refs/heads/main", "+refs/tags/v1.2:refs/klyx/staging/refs/tags/v1.2",
		"+" + b + ":refs/klyx/staging/commit/" + b}, "/k/s.git", "", true)
	c, err = fetchContent("/k/s.git", []string{a, b})
	check("fetch content", c, err, []string{"-c", "fetch.negotiationAlgorithm=noop", "fetch", "--no-tags",
		"--no-write-fetch-head", "--recurse-submodules=no", "--filter=blob:none", "--quiet", "--stdin", "--end-of-options",
		"origin"}, "/k/s.git", a+"\n"+b+"\n", true)
	c, err = lsTree("/k/c.git", ra, []string{"./clusters/prd/", "apps"})
	check("ls-tree", c, err, []string{"ls-tree", "-r", "-z", "--full-tree", "--end-of-options", a, "clusters/prd", "apps"},
		"/k/c.git", "", false)
	c, err = lsTree("/k/c.git", main.Tracked(), []string{"apps", "./"})
	check("ls-tree root", c, err, []string{"ls-tree", "-r", "-z", "--full-tree", "--end-of-options",
		"refs/klyx/tracked/refs/heads/main"}, "/k/c.git", "", false)
	c, err = diffTree("/k/c.git", ra, rb, []string{"apps"})
	check("diff-tree", c, err, []string{"diff-tree", "-r", "--no-renames", "--no-ext-diff", "--no-textconv", "-z",
		"--end-of-options", a, b, "--", "apps"}, "/k/c.git", "", false)
	c, err = catFileCheck("/k/s.git", []string{a})
	check("cat-file", c, err, []string{"cat-file", "--batch-check"}, "/k/s.git", a+"\n", false)
	c, err = verifyPack("/k/s.git", "/k/s.git/objects/pack/pack-"+a+".idx")
	check("verify-pack", c, err, []string{"verify-pack", "-v", "--", "/k/s.git/objects/pack/pack-" + a + ".idx"},
		"/k/s.git", "", false)
	c, err = updateRefs("/k/c.git", []RefUpdate{
		{Name: main.Published(), New: b, Old: a},
		{Name: v1.Published(), New: a},
		{Name: pin.Published(), Old: b},
	})
	check("update-ref", c, err, []string{"update-ref", "--stdin"}, "/k/c.git",
		"update refs/klyx/tracked/refs/heads/main "+b+" "+a+"\n"+
			"update refs/klyx/tracked/refs/tags/v1.2 "+a+" "+strings.Repeat("0", 40)+"\n"+
			"delete refs/klyx/tracked/commit/"+b+" "+b+"\n", false)
	c, err = logCommits("/k/c.git", []Rev{main.Tracked()}, []Rev{ra, rb}, 20, nil)
	check("log", c, err, []string{"log", "--no-renames", "--no-mailmap", "--no-notes", "--no-show-signature", "--no-color",
		"--max-count=20", "--format=%H%x1f%an%x1f%at%x1f%ct%x1f%s%x1e", "--end-of-options", "refs/klyx/tracked/refs/heads/main",
		"^" + a, "^" + b, "--"}, "/k/c.git", "", false)
	// A path-limited log (slice 8b-2): the path cleaned, after "--"; the root means the whole tree.
	c, err = logCommits("/k/c.git", []Rev{rb}, []Rev{ra}, 20, []string{"./apps/prod/"})
	check("log path", c, err, []string{"log", "--no-renames", "--no-mailmap", "--no-notes", "--no-show-signature", "--no-color",
		"--max-count=20", "--format=%H%x1f%an%x1f%at%x1f%ct%x1f%s%x1e", "--end-of-options", b, "^" + a, "--", "apps/prod"}, "/k/c.git", "", false)
	c, err = logCommits("/k/c.git", []Rev{rb}, []Rev{ra}, 20, []string{"./"})
	check("log root", c, err, []string{"log", "--no-renames", "--no-mailmap", "--no-notes", "--no-show-signature", "--no-color",
		"--max-count=20", "--format=%H%x1f%an%x1f%at%x1f%ct%x1f%s%x1e", "--end-of-options", b, "^" + a, "--"}, "/k/c.git", "", false)
	if _, err := logCommits("/k/c.git", []Rev{rb}, nil, 20, []string{"../escape"}); err == nil {
		t.Error("a path leaving the repository was accepted")
	}
	c, err = revListCount("/k/c.git", []Rev{rb}, []Rev{ra}, nil)
	check("rev-list", c, err, []string{"rev-list", "--count", "--end-of-options", b, "^" + a, "--"}, "/k/c.git", "", false)
	c, err = mergeBase("/k/c.git", ra, rb)
	check("merge-base", c, err, []string{"merge-base", "--end-of-options", a, b}, "/k/c.git", "", false)
	c, err = isAncestor("/k/c.git", ra, rb)
	check("is-ancestor", c, err, []string{"merge-base", "--is-ancestor", "--end-of-options", a, b}, "/k/c.git", "", false)
	c, err = diffNameStatus("/k/c.git", ra, rb, nil)
	check("name-status", c, err, []string{"diff", "--name-status", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--no-color", "-z", "--end-of-options", a, b, "--"}, "/k/c.git", "", false)
	c, err = diffContent("/k/c.git", ra, rb, []string{"apps/a.yaml"})
	check("diff", c, err, []string{"diff", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--end-of-options",
		a, b, "--", "apps/a.yaml"}, "/k/c.git", "", false)
	c, err = catFileBatch("/k/c.git", []string{a, b})
	check("cat-file --batch", c, err, []string{"cat-file", "--batch"}, "/k/c.git", a+"\n"+b+"\n", false)
}

func TestTheConstructorsRefuseUnvalidatedInput(t *testing.T) {
	const a = "1111111111111111111111111111111111111111"
	ra := Rev{a}
	for name, err := range map[string]error{
		"no revision":    func() error { _, err := revListCount("/c", nil, nil, nil); return err }(),
		"zero Rev":       func() error { _, err := mergeBase("/c", ra, Rev{}); return err }(),
		"parent path":    func() error { _, err := lsTree("/c", ra, []string{"apps/../../etc"}); return err }(),
		"absolute path":  func() error { _, err := diffTree("/c", ra, ra, []string{"/etc/passwd"}); return err }(),
		"dash path":      func() error { _, err := lsTree("/c", ra, []string{"-x"}); return err }(),
		"control path":   func() error { _, err := diffContent("/c", ra, ra, []string{"a\nb"}); return err }(),
		"short batch id": func() error { _, err := catFileBatch("/c", []string{"abc"}); return err }(),
		"short id":       func() error { _, err := catFileCheck("/c", []string{"abc123"}); return err }(),
		"option id":      func() error { _, err := fetchContent("/s", []string{"--upload-pack=evil"}); return err }(),
		"too many ids":   func() error { _, err := fetchContent("/s", slices.Repeat([]string{a}, MaxContentIDs+1)); return err }(),
		"no refs":        func() error { _, err := fetchHistory("/s", nil); return err }(),
		"foreign pack":   func() error { _, err := verifyPack("/s", "/c/objects/pack/pack-1.idx"); return err }(),
		"not a pack":     func() error { _, err := verifyPack("/s", "/s/objects/pack/../../config"); return err }(),
		"foreign ref":    func() error { _, err := updateRefs("/c", []RefUpdate{{Name: "refs/heads/main", New: a}}); return err }(),
		"pattern ref": func() error {
			_, err := updateRefs("/c", []RefUpdate{{Name: "refs/klyx/tracked/refs/heads/*", New: a}})
			return err
		}(),
		"short value": func() error {
			_, err := updateRefs("/c", []RefUpdate{{Name: "refs/klyx/tracked/commit/" + a, New: "abc"}})
			return err
		}(),
		"log over 20":       func() error { _, err := logCommits("/c", []Rev{ra}, nil, 21, nil); return err }(),
		"uppercase sha rev": func() error { _, err := SHA(strings.ToUpper("abcdef1111111111111111111111111111111111")); return err }(),
		"relative init":     func() error { _, err := initBare("c.git"); return err }(),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestTrackedRefs(t *testing.T) {
	refused := []struct{ kind, name string }{
		{"branch", "*"}, {"branch", "refs/*"}, {"branch", "refs/heads/*"}, {"branch", "a:b"}, {"branch", "-x"},
		{"branch", "main..dev"}, {"branch", "x@{1}"}, {"branch", ".hidden"}, {"branch", "a/.hidden"}, {"branch", "x.lock"},
		{"branch", "a/b.lock/c"}, {"tag", "v1?"}, {"tag", "v[1]"}, {"tag", `a\b`}, {"tag", "a^"}, {"tag", "a~1"},
		{"branch", "a b"}, {"branch", "a\x01b"}, {"branch", "a\x7fb"}, {"branch", "@"}, {"branch", "a//b"},
		{"branch", "a/"}, {"branch", "a."}, {"branch", ""}, {"commit", "abc123"},
		{"commit", strings.Repeat("g", 40)}, {"name", "refs/remotes/origin/main"},
		{"unqualified", "refs/remotes/origin/main"}, {"unqualified", "main"}, {"semver", ">=1.0.0"}, {"default", ""},
		{"", ""}, {"bogus", "main"},
	}
	for _, c := range refused {
		if r, err := RefFromKey(c.kind, c.name); err == nil {
			t.Errorf("%s %q was accepted as %s", c.kind, c.name, r)
		}
	}
	sha1, sha256 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	for _, c := range []struct{ kind, name, spec string }{
		{"branch", "main", "+refs/heads/main:refs/klyx/staging/refs/heads/main"},
		{"branch", "release/2026-10", "+refs/heads/release/2026-10:refs/klyx/staging/refs/heads/release/2026-10"},
		{"tag", "v1.2.3", "+refs/tags/v1.2.3:refs/klyx/staging/refs/tags/v1.2.3"},
		{"commit", sha1, "+" + sha1 + ":refs/klyx/staging/commit/" + sha1},
		{"commit", sha256, "+" + sha256 + ":refs/klyx/staging/commit/" + sha256},
	} {
		r := mustRef(t, c.kind, c.name)
		if r.refspec() != c.spec || strings.ContainsAny(r.refspec(), "*?[") {
			t.Fatalf("%s %s: refspec %q", c.kind, c.name, r.refspec())
		}
		if checkKlyxRef(r.Staging()) != nil || checkKlyxRef(r.Published()) != nil {
			t.Fatalf("%s %s: staging %q / published %q not Klyx refs", c.kind, c.name, r.Staging(), r.Published())
		}
	}
}

func TestApprovedURLs(t *testing.T) {
	for _, u := range []string{
		"https://github.com/acme/fleet.git", "https://dev.azure.com/acme/platform/_git/fleet",
		"git@github.com:acme/fleet.git", "github-work:acme/fleet.git", "ssh://git@github.com/acme/fleet.git",
		"ssh://github.com:2222/acme/fleet.git",
	} {
		if err := ValidateApprovedURL(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range []string{
		"https://ghp_token@github.com/acme/fleet.git", "https://user:pass@github.com/acme/fleet.git",
		"https://x-access-token:t@github.com/acme/fleet.git", "https://github.com/acme/fleet.git?private_token=t",
		"https://github.com/acme/fleet.git#frag", "https://github.com/acme/fleet.git?", "ssh://git:pw@github.com/acme/fleet.git",
		"user:pw@github.com:acme/fleet.git", "http://github.com/acme/fleet.git", "file:///srv/fleet.git", "git://github.com/a/b",
		"ext::sh -c touch% /tmp/pwned", "/srv/fleet.git", "./fleet", "", "https://", "https://github.com/a b",
		"https://github.com/a\"b", "https://github.com/a\nb",
	} {
		if err := ValidateApprovedURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{
		"./clusters/prd": "clusters/prd", "clusters/prd/": "clusters/prd", ".": "", "./": "", "": "",
		"a//b/./c": "a/b/c", "a/.b": "a/.b", "a..b/c": "a..b/c",
	} {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"/abs", "../x", "a/../../b", "a/..", "-rf", "./-rf", `a\b`, "a\x00b", "a\tb"} {
		if _, err := CleanPath(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestScrub(t *testing.T) {
	in := "fatal: unable to access 'https://x-access-token:ghs_abc@github.com/acme/fleet.git/?token=t1': 403 " +
		"remote: password=hunter2 ssh://git:pw@host/x"
	got := Scrub(in)
	for _, leak := range []string{"ghs_abc", "x-access-token", "t1", "hunter2", ":pw@"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%q survived: %q", leak, got)
		}
	}
	if !strings.Contains(got, "https://github.com/acme/fleet.git/") {
		t.Fatalf("the URL itself was lost: %q", got)
	}
	for in, want := range map[string]string{
		"ssh://git@github.com/acme/fleet.git":      "ssh://git@github.com/acme/fleet.git", // an SSH user is not a secret
		"ssh://git:pw@github.com/acme/fleet.git":   "ssh://git@github.com/acme/fleet.git",
		"https://ghp_tok@github.com/a/b.git":       "https://github.com/a/b.git",
		"HTTP://user:pw@git.internal/a.git":        "HTTP://git.internal/a.git",
		"git@github.com:acme/fleet.git":            "git@github.com:acme/fleet.git",
		"https://github.com/a/b.git?private_token": "https://github.com/a/b.git",
	} {
		if got := Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- the real git ---

func realRunner(t *testing.T) Runner {
	t.Helper()
	env := OwnerEnv(os.Getenv, os.Environ())
	env.Home = t.TempDir()
	g, err := Locate(context.Background(), env)
	if err != nil {
		t.Skipf("no usable git: %v", err)
	}
	base := filepath.Join(t.TempDir(), "git-cache")
	if err := EnsureBase(base); err != nil {
		t.Fatal(err)
	}
	cfg := GlobalConfigPath(base)
	if err := WriteGlobalConfig(cfg, Helpers{}, ""); err != nil {
		t.Fatal(err)
	}
	return Runner{Git: g, Env: env, GlobalConfig: cfg}
}

// plain runs the test's own git (not Klyx's), with an isolated config and
// fixed identities, to build fixtures.
func plain(t *testing.T, g Git, dir string, args ...string) string {
	t.Helper()
	c := exec.Command(g.Path, args...)
	c.Dir = dir
	c.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.test", "GIT_COMMITTER_NAME=Ada",
		"GIT_COMMITTER_EMAIL=ada@example.test", "GIT_AUTHOR_DATE=2026-10-01T10:00:00Z", "GIT_COMMITTER_DATE=2026-10-01T10:00:00Z"}
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	src            string
	c1, c2, d1, v1 string // commits, and the annotated tag object
	aOld, aNew     string // apps/a.yaml's blobs
}

// newFixture: c1 (apps/a.yaml, apps/b.yaml, base/k.yaml) <- c2 (a.yaml
// changed) on main; dev from c1 with d1 (dev.txt); annotated v1 on c1.
func newFixture(t *testing.T, g Git) fixture {
	src := t.TempDir()
	plain(t, g, src, "init", "-q", "-b", "main")
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("apps/a.yaml", "replicas: 1\n")
	write("apps/b.yaml", "b\n")
	write("base/k.yaml", "k\n")
	plain(t, g, src, "add", ".")
	plain(t, g, src, "commit", "-qm", "first\x01 commit")
	f := fixture{src: src, c1: plain(t, g, src, "rev-parse", "HEAD"), aOld: plain(t, g, src, "rev-parse", "HEAD:apps/a.yaml")}
	write("apps/a.yaml", "replicas: 3\n")
	plain(t, g, src, "commit", "-qam", "scale a to "+strings.Repeat("x", 300))
	f.c2, f.aNew = plain(t, g, src, "rev-parse", "HEAD"), plain(t, g, src, "rev-parse", "HEAD:apps/a.yaml")
	plain(t, g, src, "checkout", "-qb", "dev", f.c1)
	write("dev.txt", "dev\n")
	plain(t, g, src, "add", ".")
	plain(t, g, src, "commit", "-qm", "dev only")
	f.d1 = plain(t, g, src, "rev-parse", "HEAD")
	plain(t, g, src, "tag", "-a", "-m", "v1", "v1", f.c1)
	f.v1 = plain(t, g, src, "rev-parse", "refs/tags/v1")
	return f
}

func TestInitRepoWritesThePartialCloneConfig(t *testing.T) {
	r := realRunner(t)
	dir := filepath.Join(t.TempDir(), "c.git")
	if err := r.InitRepo(context.Background(), dir, fixtureURL); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("cache mode %v %v", st.Mode(), err)
	}
	for _, absent := range []string{"hooks", "description", "info"} {
		if _, err := os.Stat(filepath.Join(dir, absent)); !os.IsNotExist(err) {
			t.Fatalf("%s exists: the template was not empty", absent)
		}
	}
	for k, want := range map[string]string{
		"core.repositoryformatversion": "1", "extensions.partialclone": "origin", "remote.origin.url": fixtureURL,
		"remote.origin.promisor": "true", "remote.origin.partialclonefilter": "blob:none", "core.bare": "true",
	} {
		if got := plain(t, r.Git, dir, "--git-dir="+dir, "config", "--local", "--get", k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	if err := r.InitRepo(context.Background(), dir, fixtureURL); err == nil {
		t.Fatal("an existing cache was initialised again")
	}
	if err := r.InitRepo(context.Background(), filepath.Join(t.TempDir(), "t.git"), "https://tok@github.com/a/b"); err == nil {
		t.Fatal("a credential-bearing URL was accepted")
	}
	if CacheDir("/base", fixtureURL) == CacheDir("/base", fixtureURL+"/") || filepath.Dir(CacheDir("/base", fixtureURL)) != "/base" {
		t.Fatal("CacheDir is not one directory per approved URL")
	}
}

// fullCache publishes the whole fixture into a Klyx cache with plain git,
// packed, as tracked refs.
func fullCache(t *testing.T, r Runner, f fixture) Repo {
	dir := filepath.Join(t.TempDir(), "c.git")
	if err := r.InitRepo(context.Background(), dir, fixtureURL); err != nil {
		t.Fatal(err)
	}
	plain(t, r.Git, f.src, "push", "-q", dir, "main:refs/klyx/tracked/refs/heads/main", "dev:refs/klyx/tracked/refs/heads/dev",
		"refs/tags/v1:refs/klyx/tracked/refs/tags/v1")
	plain(t, r.Git, dir, "--git-dir="+dir, "repack", "-a", "-d", "-q")
	return r.Repo(dir)
}

func TestTheReadQueries(t *testing.T) {
	r := realRunner(t)
	f := newFixture(t, r.Git)
	p := fullCache(t, r, f)
	ctx := context.Background()
	main, dev, v1 := mustRef(t, "branch", "main"), mustRef(t, "branch", "dev"), mustRef(t, "tag", "v1")
	sha := func(s string) Rev {
		r, err := SHA(s)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	refs, err := p.Refs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]RefValue{}
	for _, v := range refs {
		byName[v.Name] = v
	}
	if byName[main.Published()].Commit() != f.c2 || byName[dev.Published()].Commit() != f.d1 ||
		byName[v1.Published()].Value != f.v1 || byName[v1.Published()].Commit() != f.c1 || len(refs) != 3 {
		t.Fatalf("refs %+v", refs)
	}

	commits, err := p.Log(ctx, []Rev{main.Tracked()}, []Rev{sha(f.c1)}, 20)
	if err != nil || len(commits) != 1 || commits[0].SHA != f.c2 || commits[0].Author != "Ada" ||
		!commits[0].Time.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) || !commits[0].Committed.Equal(commits[0].Time) {
		t.Fatalf("log %+v %v", commits, err)
	}
	if n := len([]rune(commits[0].Subject)); n != 200 || !strings.HasSuffix(commits[0].Subject, "…") {
		t.Fatalf("a subject is capped at 200 characters, got %d: %q", n, commits[0].Subject)
	}
	all, err := p.Log(ctx, []Rev{main.Tracked(), dev.Tracked()}, nil, 20)
	if err != nil || len(all) != 3 {
		t.Fatalf("log of both refs %+v %v", all, err)
	}
	for _, c := range all {
		if strings.ContainsFunc(c.Subject, func(r rune) bool { return r < 0x20 }) {
			t.Fatalf("a control character reached the subject: %q", c.Subject)
		}
	}

	if n, err := p.Count(ctx, []Rev{main.Tracked()}, []Rev{dev.Tracked()}); err != nil || n != 1 {
		t.Fatalf("count %d %v", n, err)
	}
	if mb, ok, err := p.MergeBase(ctx, main.Tracked(), dev.Tracked()); err != nil || !ok || mb != f.c1 {
		t.Fatalf("merge-base %q %v %v", mb, ok, err)
	}
	if yes, err := p.IsAncestor(ctx, sha(f.c1), main.Tracked()); err != nil || !yes {
		t.Fatalf("c1 is an ancestor of main: %v %v", yes, err)
	}
	if yes, err := p.IsAncestor(ctx, dev.Tracked(), main.Tracked()); err != nil || yes {
		t.Fatalf("dev is not an ancestor of main: %v %v", yes, err)
	}
	if _, err := p.IsAncestor(ctx, sha(strings.Repeat("e", 40)), main.Tracked()); err == nil {
		t.Fatal("an unknown commit answered is-ancestor")
	}

	names, err := p.DiffNames(ctx, sha(f.c1), main.Tracked(), nil)
	if err != nil || !reflect.DeepEqual(names, []NameStatus{{Status: "M", Path: "apps/a.yaml"}}) {
		t.Fatalf("name-status %+v %v", names, err)
	}
	entries, err := p.LsTree(ctx, main.Tracked(), []string{"./apps"})
	if err != nil || len(entries) != 2 || entries[0].Path != "apps/a.yaml" || entries[0].ID != f.aNew || entries[0].Type != "blob" {
		t.Fatalf("ls-tree %+v %v", entries, err)
	}
	changes, err := p.DiffTree(ctx, sha(f.c1), sha(f.c2), []string{"apps"})
	if err != nil || len(changes) != 1 || changes[0].OldID != f.aOld || changes[0].NewID != f.aNew || changes[0].Status != "M" {
		t.Fatalf("diff-tree %+v %v", changes, err)
	}
	missing, err := p.Missing(ctx, []string{f.aOld, strings.Repeat("e", 40)})
	if err != nil || !reflect.DeepEqual(missing, []string{strings.Repeat("e", 40)}) {
		t.Fatalf("missing %v %v", missing, err)
	}
	text, truncated, err := p.Diff(ctx, sha(f.c1), sha(f.c2), []string{"apps"})
	if err != nil || truncated || !strings.Contains(text, "+replicas: 3") {
		t.Fatalf("diff %q %v %v", text, truncated, err)
	}
	var content string
	if err := p.Blobs(ctx, []string{f.aNew}, 1<<20, func(_ string, d []byte) error { content = string(d); return nil }); err != nil || content != "replicas: 3\n" {
		t.Fatalf("blobs %q %v", content, err)
	}

	idx, _ := filepath.Glob(filepath.Join(p.Dir(), "objects", "pack", "pack-*.idx"))
	if len(idx) != 1 {
		t.Fatalf("packs %v", idx)
	}
	objs, err := p.VerifyPack(ctx, idx[0])
	// commits c1 c2 d1; trees c1(root, apps, base) c2(root, apps) d1(root);
	// blobs a, a', b, k, dev; the tag v1.
	if err != nil || objs.Commits != 3 || objs.Trees != 6 || objs.Blobs != 5 || objs.Tags != 1 || len(objs.BlobIDs) != 5 {
		t.Fatalf("verify-pack %+v %v", objs, err)
	}

	// Publication's transaction: each update names its old value.
	if err := p.UpdateRefs(ctx, []RefUpdate{{Name: dev.Published(), New: f.c1, Old: f.c2}}); err == nil {
		t.Fatal("an update naming the wrong old value applied")
	}
	if err := p.UpdateRefs(ctx, []RefUpdate{
		{Name: dev.Published(), New: f.c1, Old: f.d1},
		{Name: mustRef(t, "commit", f.c2).Published(), New: f.c2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateRefs(ctx, []RefUpdate{{Name: mustRef(t, "commit", f.c2).Published(), New: f.c2}}); err == nil {
		t.Fatal("creating an existing ref applied")
	}
	refs, _ = p.Refs(ctx)
	if len(refs) != 4 {
		t.Fatalf("refs after the transaction %+v", refs)
	}
}

func TestAPartialCacheNeverLazyFetches(t *testing.T) {
	r := realRunner(t)
	f := newFixture(t, r.Git)
	plain(t, r.Git, f.src, "config", "uploadpack.allowFilter", "true")
	dir := filepath.Join(t.TempDir(), "p.git")
	plain(t, r.Git, t.TempDir(), "clone", "-q", "--bare", "--filter=blob:none", "file://"+f.src, dir)
	if err := writeRepoConfig(dir, fixtureURL); err != nil { // the promisor is now a host that does not exist
		t.Fatal(err)
	}
	p, ctx := r.Repo(dir), context.Background()
	sha := func(s string) Rev { r, _ := SHA(s); return r }
	if missing, err := p.Missing(ctx, []string{f.aNew, f.c2}); err != nil || !reflect.DeepEqual(missing, []string{f.aNew}) {
		t.Fatalf("missing %v %v", missing, err)
	}
	// Trees only: these work with every blob missing.
	if es, err := p.LsTree(ctx, sha(f.c2), []string{"apps"}); err != nil || len(es) != 2 {
		t.Fatalf("ls-tree %+v %v", es, err)
	}
	if ns, err := p.DiffNames(ctx, sha(f.c1), sha(f.c2), nil); err != nil || len(ns) != 1 {
		t.Fatalf("name-status %+v %v", ns, err)
	}
	if cs, err := p.DiffTree(ctx, sha(f.c1), sha(f.c2), nil); err != nil || len(cs) != 1 {
		t.Fatalf("diff-tree %+v %v", cs, err)
	}
	// Content fails as "content incomplete", never downloads.
	_, _, err := p.Diff(ctx, sha(f.c1), sha(f.c2), nil)
	if err == nil || !strings.Contains(err.Error(), "lazy fetching disabled") {
		t.Fatalf("a content diff with missing blobs: %v", err)
	}
	if err := p.Blobs(ctx, []string{f.aNew}, 1<<20, func(string, []byte) error { return nil }); !errors.Is(err, ErrContentMissing) {
		t.Fatalf("an exact read of a missing blob: %v", err)
	}
}

func TestTheOwnersCredentialHelpers(t *testing.T) {
	r := realRunner(t)
	home := t.TempDir()
	sys := filepath.Join(t.TempDir(), "gitconfig")
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := r.Env
	env.Home, env.XDGConfigHome, env.systemConfig = home, filepath.Join(home, ".xdg"), sys
	read := func() Helpers {
		t.Helper()
		return ReadHelpers(context.Background(), r.Git, env)
	}
	reset := func() {
		for _, p := range []string{sys, filepath.Join(home, ".gitconfig"), filepath.Join(home, ".xdg", "git", "config"),
			filepath.Join(home, "inc.cfg")} {
			_ = os.Remove(p)
		}
	}
	// A repository-local helper in HOME (a dotfiles repo) is never read.
	plain(t, r.Git, home, "init", "-q")
	plain(t, r.Git, home, "config", "--local", "credential.helper", "local-helper")

	write(sys, "[credential]\n\thelper = osxkeychain\n")
	if h := read(); !reflect.DeepEqual(h.Values, []string{"osxkeychain"}) {
		t.Fatalf("system only: %+v", h)
	}
	write(filepath.Join(home, ".gitconfig"), "[credential]\n\thelper = store\n")
	if h := read(); !reflect.DeepEqual(h.Values, []string{"osxkeychain", "store"}) {
		t.Fatalf("system then global: %+v", h)
	}
	write(filepath.Join(home, ".gitconfig"), "[credential]\n\thelper =\n\thelper = manager\n")
	if h := read(); !reflect.DeepEqual(h.Values, []string{"manager"}) {
		t.Fatalf("an empty global value clears the system helper: %+v", h)
	}
	reset()
	write(filepath.Join(home, "inc.cfg"), "[credential]\n\thelper = cache --timeout=60\n")
	write(filepath.Join(home, ".gitconfig"), "[include]\n\tpath = "+filepath.Join(home, "inc.cfg")+"\n")
	write(filepath.Join(home, ".xdg", "git", "config"), "[credential]\n\thelper = /usr/local/bin/git-credential-manager\n")
	if h := read(); !reflect.DeepEqual(h.Values, []string{"/usr/local/bin/git-credential-manager", "cache --timeout=60"}) {
		t.Fatalf("XDG then ~/.gitconfig with its include: %+v", h)
	}
	reset()
	write(filepath.Join(home, ".gitconfig"), "[credential]\n\thelper = \"!f() { echo password=x; }; f\"\n\thelper = osxkeychain\n")
	h := read()
	if !reflect.DeepEqual(h.Values, []string{"osxkeychain"}) || len(h.Refused) != 1 || !strings.HasPrefix(h.Refused[0], "!f()") {
		t.Fatalf("a shell snippet is refused: %+v", h)
	}
	reset()
	if h := read(); len(h.Values) != 0 || len(h.Refused) != 0 {
		t.Fatalf("no helper anywhere (the local one never counts): %+v", h)
	}

	write(filepath.Join(home, ".gitconfig"), "[credential]\n\thelper = !old-snippet\n\thelper =\n\thelper = store\n")
	if h := read(); !reflect.DeepEqual(h.Values, []string{"store"}) || len(h.Refused) != 0 {
		t.Fatalf("an empty value clears a snippet before it too: %+v", h)
	}
	reset()

	// The scope filter: a vendor default ("unknown", Apple Git's osxkeychain),
	// system and global count; local, worktree and command never do.
	out := []byte("unknown\x00osxkeychain\x00system\x00sys\x00local\x00local-helper\x00worktree\x00wt\x00" +
		"command\x00cmd\x00global\x00glob\x00")
	if got := scopedValues(out); !reflect.DeepEqual(got, []string{"osxkeychain", "sys", "glob"}) {
		t.Fatalf("scoped values %q", got)
	}
	if got := scopedValues([]byte("system\x00")); got != nil {
		t.Fatalf("a torn record %q", got)
	}

	// The generated config round-trips the values through git's own parser.
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	vals := []string{"osxkeychain", `store --file "/a b/c"`, `/opt/x\y`}
	if err := WriteGlobalConfig(cfg, Helpers{Values: vals}, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(cfg); st.Mode().Perm() != 0o600 {
		t.Fatalf("generated config mode %v", st.Mode())
	}
	got := strings.Split(plain(t, r.Git, t.TempDir(), "config", "--file", cfg, "--get-all", "credential.helper"), "\n")
	ssh := plain(t, r.Git, t.TempDir(), "config", "--file", cfg, "--get", "core.sshCommand")
	if !reflect.DeepEqual(got, vals) || ssh != "ssh -o BatchMode=yes" {
		t.Fatalf("round trip %q / %q", got, ssh)
	}
}

func TestPackScanReadsChunkedLines(t *testing.T) {
	s := &packScan{}
	in := strings.Repeat("a", 40) + " commit 166 125 12\n" + strings.Repeat("b", 40) + " blob   2 11 232\n" +
		strings.Repeat("c", 40) + " tree   87 94 232 1 " + strings.Repeat("d", 40) + "\nnon delta: 2 objects\n/x.pack: ok\n"
	for i := 0; i < len(in); i += 7 {
		if _, err := s.Write([]byte(in[i:min(i+7, len(in))])); err != nil {
			t.Fatal(err)
		}
	}
	s.flush()
	if s.err != nil || s.objs.Commits != 1 || s.objs.Blobs != 1 || s.objs.Trees != 1 || s.objs.BlobIDs[0] != strings.Repeat("b", 40) {
		t.Fatalf("%+v %v", s.objs, s.err)
	}
	bad := &packScan{}
	if _, err := bad.Write([]byte(strings.Repeat("a", 40) + " weird 1 1 1\n")); err == nil {
		t.Fatal("an unknown object type was accepted")
	}
	long := &packScan{}
	if _, err := long.Write([]byte(strings.Repeat("x", maxPackLine+1))); err == nil {
		t.Fatal("an unbounded line was accepted")
	}
}

func TestParsersRefuseMalformedOutput(t *testing.T) {
	if _, err := parseBatchCheck([]byte("x missing\n"), []string{strings.Repeat("a", 40)}); err == nil {
		t.Fatal("a cat-file line for another id")
	}
	if _, err := parseBatchCheck([]byte(""), []string{strings.Repeat("a", 40)}); err == nil {
		t.Fatal("a short cat-file answer")
	}
	if _, err := parseRefs([]byte("refs/heads/main\x00" + strings.Repeat("a", 40) + "\x00\n")); err == nil {
		t.Fatal("a ref outside refs/klyx")
	}
	if _, err := parseLog([]byte("nothex\x1fa\x1f1\x1f1\x1fs\x1e")); err == nil {
		t.Fatal("a log record without an id")
	}
	if _, err := parseLsTree([]byte("100644 blob short\tpath\x00")); err == nil {
		t.Fatal("an ls-tree record without an id")
	}
	if got := plainText("a\x1b[31mred\x00\n"+strings.Repeat("é", 300), 10); got != "a [31mred…" {
		t.Fatalf("plainText %q", got)
	}
}

// The cache diff's export reads exact blobs (slice 6 note §7.1): in order,
// streamed, capped; a missing blob is "content incomplete", never fetched.
func TestBlobsReadsExactBlobs(t *testing.T) {
	r := realRunner(t)
	f := newFixture(t, r.Git)
	p := fullCache(t, r, f)
	ctx := context.Background()
	got := map[string]string{}
	var order []string
	read := func(id string, data []byte) error { got[id] = string(data); order = append(order, id); return nil }
	if err := p.Blobs(ctx, []string{f.aNew, f.aOld}, 1<<20, read); err != nil {
		t.Fatal(err)
	}
	if got[f.aNew] != "replicas: 3\n" || got[f.aOld] != "replicas: 1\n" || !reflect.DeepEqual(order, []string{f.aNew, f.aOld}) {
		t.Fatalf("blobs %q in order %v", got, order)
	}
	if err := p.Blobs(ctx, []string{f.aNew}, 4, read); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("over the cap: %v", err)
	}
	tree := plain(t, r.Git, f.src, "rev-parse", f.c1+"^{tree}")
	if err := p.Blobs(ctx, []string{tree}, 1<<20, read); err == nil || !strings.Contains(err.Error(), "not a blob") {
		t.Fatalf("a tree: %v", err)
	}
	if err := p.Blobs(ctx, []string{strings.Repeat("e", 40)}, 1<<20, read); !errors.Is(err, ErrContentMissing) {
		t.Fatalf("a missing blob: %v", err)
	}
}

func TestBlobScanReadsChunkedReplies(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	reply := a + " blob 5\nhello\n" + b + " blob 0\n\n"
	got := map[string]string{}
	s := &blobScan{want: []string{a, b}, left: 100, max: 100, fn: func(id string, d []byte) error { got[id] = string(d); return nil }}
	for i := 0; i < len(reply); i += 3 {
		if _, err := s.Write([]byte(reply[i:min(i+3, len(reply))])); err != nil {
			t.Fatal(err)
		}
	}
	if s.next != 2 || got[a] != "hello" || got[b] != "" {
		t.Fatalf("got %q next %d", got, s.next)
	}
	bad := &blobScan{want: []string{a}, left: 100, max: 100, fn: func(string, []byte) error { return nil }}
	if _, err := bad.Write([]byte(b + " blob 1\nx\n")); err == nil {
		t.Fatal("a reply for another id was accepted")
	}
	unterminated := &blobScan{want: []string{a}, left: 100, max: 100, fn: func(string, []byte) error { return nil }}
	if _, err := unterminated.Write([]byte(a + " blob 1\nxy")); err == nil {
		t.Fatal("unterminated content was accepted")
	}
}
