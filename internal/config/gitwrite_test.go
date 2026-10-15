package config

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The Line slice 6c (slice 6 note §5.1): the git block writer changes only
// git, and loading refuses what could carry a credential.

func TestSaveGit_WritesOnlyTheGitBlock(t *testing.T) {
	path := writeFleet(t, `# my fleet
clusters:
  - name: a # inline comment
ai:
  provider: off

# trailing section comment
extra: keep-me
`)
	g := GitConfig{Repos: []GitRepoConfig{
		{URL: "git@github.com:acme/fleet.git"},
		{URL: "https://dev.azure.com/acme/platform/_git/fleet", Web: "https://dev.azure.com/acme/platform/_git/fleet",
			Matches: []string{"ssh.dev.azure.com/v3/acme/platform/fleet"}},
	}}
	if err := SaveGit(path, g); err != nil {
		t.Fatal(err)
	}
	got := readFleet(t, path)
	for _, want := range []string{"# my fleet\n", "  - name: a # inline comment\n", "\n# trailing section comment\nextra: keep-me\n",
		"ai:\n  provider: off\n", "git:\n  repos:\n    - url: git@github.com:acme/fleet.git\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	repos, notes := cfg.ApprovedGitRepos()
	if len(notes) != 0 || !reflect.DeepEqual(repos, g.Repos) {
		t.Fatalf("read back %+v notes %v", repos, notes)
	}
	// A second save replaces the block, and an empty list is written as one.
	if err := SaveGit(path, GitConfig{}); err != nil {
		t.Fatal(err)
	}
	got = readFleet(t, path)
	if !strings.Contains(got, "git:\n  repos: []\n") || strings.Contains(got, "acme/fleet") || !strings.Contains(got, "extra: keep-me\n") {
		t.Fatalf("after clearing:\n%s", got)
	}
}

func TestSaveGit_RefusesAnEditMadeMeanwhile(t *testing.T) {
	path := writeFleet(t, "clusters:\n  - name: a\n")
	beforeReplace = func() { _ = os.WriteFile(path, []byte("clusters:\n  - name: a\n  - name: b\n"), 0o644) }
	defer func() { beforeReplace = nil }()
	if err := SaveGit(path, GitConfig{Repos: []GitRepoConfig{{URL: "git@github.com:acme/fleet.git"}}}); !errors.Is(err, ErrFleetChangedOnDisk) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(readFleet(t, path), "git:") {
		t.Fatal("the outside edit was overwritten")
	}
}

func TestApprovedGitRepos_IgnoresWhatCouldCarryACredential(t *testing.T) {
	path := writeFleet(t, `clusters:
  - name: a
git:
  repos:
    - url: https://ghp_secrettoken@github.com/acme/fleet.git
    - url: https://user:hunter2@github.com/acme/other.git
    - url: https://github.com/acme/q.git?private_token=abc
    - url: ssh://git:pw@github.com/acme/x.git
    - url: http://github.com/acme/plain.git
    - url: file:///srv/fleet.git
    - url: "scpuser:scp-secret@github.com:acme/scp.git"
    - url: ghp_bare_url_secret@github.com/acme/bare
    - url: git@github.com:acme/fleet.git
      web: http://github.com/acme/fleet
      matches: [github.com/acme/fleet, "https://github.com/acme/fleet", GitHub.com/acme/fleet, github.com/acme/fleet.git]
    - url: git@github.com:acme/fleet.git
    - url: https://github.com/acme/ok.git
      web: https://github.com/acme/ok/
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	repos, notes := cfg.ApprovedGitRepos()
	want := []GitRepoConfig{
		{URL: "git@github.com:acme/fleet.git", Matches: []string{"github.com/acme/fleet"}},
		{URL: "https://github.com/acme/ok.git", Web: "https://github.com/acme/ok"},
	}
	if !reflect.DeepEqual(repos, want) {
		t.Fatalf("approved %+v", repos)
	}
	all := strings.Join(notes, "\n")
	for _, leak := range []string{"ghp_secrettoken", "hunter2", "abc", ":pw@", "scp-secret", "scpuser", "ghp_bare_url_secret"} {
		if strings.Contains(all, leak) {
			t.Fatalf("a note repeats a credential (%q):\n%s", leak, all)
		}
	}
	// eight refused URLs, the web link, three matches, the duplicate
	if len(notes) != 13 || !strings.Contains(all, "listed twice") || !strings.Contains(all, "github.com:acme/scp.git") {
		t.Fatalf("notes:\n%s", all)
	}
	if w := strings.Join(cfg.Warnings(), "\n"); !strings.Contains(w, "never fetched") {
		t.Fatalf("the load warnings do not name the ignored entries:\n%s", w)
	}
}

func TestValidRepoKey(t *testing.T) {
	for _, k := range []string{"github.com/acme/fleet", "github.com/Acme/Fleet", "git.example.com:2222/a/b", "ssh.dev.azure.com/v3/acme/p/r"} {
		if err := ValidRepoKey(k); err != nil {
			t.Errorf("%q refused: %v", k, err)
		}
	}
	for _, k := range []string{"", "github.com", "github.com/", "https://github.com/a/b", "GitHub.com/a/b", "github.com/a/b.git",
		"github.com/a/b/", "git@github.com:a/b", "github.com/a b", "github.com/a?b"} {
		if err := ValidRepoKey(k); err == nil {
			t.Errorf("%q accepted", k)
		}
	}
}

// A rejected match or web link is never repeated in a note: it may hold a
// token (design review of 28a8bff).
func TestApprovedGitRepos_NotesNeverRepeatARejectedValue(t *testing.T) {
	path := writeFleet(t, `clusters:
  - name: a
git:
  repos:
    - url: git@github.com:acme/fleet.git
      web: ftp://webuser:webpw-secret@git.example.com/x
      matches: ["https://ghp_matchsecret@github.com/acme/alias", "ghp_bare_secret@github.com/acme", "github.com/acme/ok"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	repos, notes := cfg.ApprovedGitRepos()
	all := strings.Join(notes, "\n")
	for _, leak := range []string{"ghp_matchsecret", "ghp_bare_secret", "webpw-secret", "webuser"} {
		if strings.Contains(all, leak) {
			t.Fatalf("a note repeats %q:\n%s", leak, all)
		}
	}
	if len(notes) != 3 || len(repos) != 1 || !reflect.DeepEqual(repos[0].Matches, []string{"github.com/acme/ok"}) ||
		!strings.Contains(all, "git.repos[0].matches[0] ignored") {
		t.Fatalf("repos %+v notes:\n%s", repos, all)
	}
}
