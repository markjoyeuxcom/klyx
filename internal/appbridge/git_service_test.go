package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitcache"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/line"
)

// The Line slice 6c (slice 6 note §5): Settings > Git - approval, mapping,
// the clusters' unapproved URLs, tracked refs, "Fetch now", "Clear cache".

type fakeGitStore struct {
	mu        sync.Mutex
	refreshed map[string][]string
	cleared   []string
	status    map[string]gitcache.Status
	err       error
	read      func(url string, fn func(gitcli.Repo) error) error
	gate      chan struct{} // set: a refresh waits for it to close
}

func (f *fakeGitStore) Start(context.Context) error { return nil }
func (f *fakeGitStore) RefreshHistory(_ context.Context, url string, refs []gitcli.Ref) error {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range refs {
		f.refreshed[url] = append(f.refreshed[url], r.String())
	}
	return f.err
}
func (f *fakeGitStore) Clear(_ context.Context, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, url)
	return nil
}
func (f *fakeGitStore) Size(url string) int64 { return int64(len(url)) }
func (f *fakeGitStore) Status(url string) gitcache.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[url]
}
func (f *fakeGitStore) Read(url string, fn func(gitcli.Repo) error) error {
	if f.read == nil {
		return errors.New("no history fetched yet")
	}
	return f.read(url, fn)
}

const gitFleet = `clusters:
  - name: prd
  - name: stg
git:
  repos:
    - url: git@github.com:acme/fleet.git
      matches: ["https://ghp_matchtok@github.com/acme/alias"]
    - url: work-gh:acme/platform.git
      matches: [github.com/acme/platform]
    - url: https://ghp_tok@github.com/acme/leak.git
`

type gitHarness struct {
	t       *testing.T
	svc     *GitService
	store   *fakeGitStore
	path    string
	key     *LineKeyDTO
	sources []LineGitSource
	em      *gitEvents
}

// gitEvents records line:git events.
type gitEvents struct {
	mu  sync.Mutex
	got []LineGitDTO
}

func (e *gitEvents) Emit(name string, payload any) {
	if name != LineGitEvent {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, payload.(LineGitDTO))
}

// push hands the service a LineDTO with the harness's key (unless the DTO
// names its own) and sources, as LineService does after every build.
func (h *gitHarness) push(dto LineDTO) {
	if dto.Key == nil {
		dto.Key = h.key
	}
	GitLineSink(h.svc)(dto, h.sources)
}

func newGitHarness(t *testing.T, fleet string, startErr error) *gitHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(fleet), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &gitHarness{t: t, path: path, store: &fakeGitStore{refreshed: map[string][]string{}, status: map[string]gitcache.Status{}},
		key: &LineKeyDTO{Kind: line.KeyGit, Repo: "github.com/acme/fleet", RefKind: "branch", RefName: "main"}, em: &gitEvents{}}
	fetched := time.Date(2026, 10, 15, 20, 31, 0, 0, time.UTC)
	h.store.status["git@github.com:acme/fleet.git"] = gitcache.Status{FetchedAt: fetched, AttemptedAt: fetched.Add(time.Minute), Error: "fetch failed"}
	h.sources = []LineGitSource{
		{Cluster: "prd", Origin: "GitRepository", Namespace: "flux-system", Name: "fleet", rawURL: "https://github.com/acme/fleet"},
		{Cluster: "stg", Origin: "GitRepository", Namespace: "flux-system", Name: "fleet", rawURL: "ssh://git@github.com/acme/fleet.git"},
		{Cluster: "prd", Origin: "Application", Namespace: "argocd", Name: "apps", rawURL: "https://github.com/acme/platform.git"},
		{Cluster: "prd", Origin: "GitRepository", Namespace: "flux-system", Name: "infra", rawURL: "http://git.internal/infra.git"},
		{Cluster: "stg", Origin: "GitRepository", Namespace: "flux-system", Name: "infra", rawURL: "https://deploy:s3cr3t@git.internal/infra.git"},
		{Cluster: "stg", Origin: "Application", Namespace: "argocd", Name: "tools", rawURL: "https://github.com/acme/tools.git"},
		{Cluster: "stg", Origin: "Application", Namespace: "argocd", Name: "scp", rawURL: "scpuser:scp-cluster-secret@git.internal:infra.git"},
	}
	h.svc = NewGitService(context.Background(), GitDeps{
		ConfigPath: path,
		Initial:    startupGit(t, path),
		Override:   &line.Key{Kind: line.KeyGit, Repo: "github.com/acme/platform", RefKind: "semver", RefName: ">=1.0"},
		Emitter:    h.em,
		StartGit: func(context.Context) (gitcli.Runner, gitcli.Helpers, error) {
			if startErr != nil {
				return gitcli.Runner{}, gitcli.Helpers{}, startErr
			}
			return gitcli.Runner{Git: gitcli.Git{Path: "/opt/homebrew/bin/git", Version: gitcli.Version{Major: 2, Minor: 56, Raw: "git version 2.56.0"}}},
				gitcli.Helpers{Values: []string{"osxkeychain", "store --file /x"}, Refused: []string{"!f() { echo; }; f"}}, nil
		},
		NewStore: func(gitcli.Runner) gitStore { return h.store },
	})
	h.push(LineDTO{Version: 1})
	deadline := time.Now().Add(5 * time.Second)
	for !h.svc.GetGitSettings().Binary.Ready {
		if time.Now().After(deadline) {
			t.Fatal("Git never finished starting")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h
}

// startupGit is path's git block as the app reads it at startup: the
// running copy (Settings slice F §5).
func startupGit(t *testing.T, path string) *config.GitConfig {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Git
}

func (h *gitHarness) fleet() string {
	b, err := os.ReadFile(h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func TestGitSettingsShowsApprovedUnapprovedAndWhy(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	d := h.svc.GetGitSettings()
	if d.Binary.Path != "/opt/homebrew/bin/git" || d.Binary.Version != "git version 2.56.0" || d.Binary.Off != "" {
		t.Fatalf("binary %+v", d.Binary)
	}
	if !reflect.DeepEqual(d.Helpers, []string{"osxkeychain", "store"}) || d.RefusedHelpers != 1 {
		t.Fatalf("helpers %v refused %d", d.Helpers, d.RefusedHelpers)
	}
	if len(d.Repos) != 2 {
		t.Fatalf("repos %+v", d.Repos)
	}
	fleet, platform := d.Repos[0], d.Repos[1]
	// Both clusters' spellings map to the ssh entry by key.
	if fleet.Key != "github.com/acme/fleet" || len(fleet.Sources) != 2 || !reflect.DeepEqual(fleet.Tracked, []string{"refs/heads/main"}) ||
		fleet.Error != "fetch failed" || fleet.FetchedAt == nil || fleet.Size == 0 {
		t.Fatalf("fleet %+v", fleet)
	}
	// The alias entry serves its explicit match; the override's semver range
	// has no single target and says so.
	if len(platform.Sources) != 1 || len(platform.Tracked) != 0 || len(platform.TrackNotes) != 1 ||
		!strings.Contains(platform.TrackNotes[0], "semver") {
		t.Fatalf("platform %+v", platform)
	}
	// Unapproved: grouped by key; a URL that cannot run is never suggested,
	// and a credential in a cluster's URL is never shown.
	if len(d.Unapproved) != 2 {
		t.Fatalf("unapproved %+v", d.Unapproved)
	}
	infra, tools := d.Unapproved[0], d.Unapproved[1]
	if infra.Key != "git.internal/infra" || infra.Suggest != "" || infra.Refused == "" || len(infra.Sources) != 3 {
		t.Fatalf("infra %+v", infra)
	}
	if tools.Suggest != "https://github.com/acme/tools.git" || tools.Refused != "" {
		t.Fatalf("tools %+v", tools)
	}
	all := strings.Join(d.Notes, "\n")
	for _, src := range infra.Sources {
		all += src.URL
	}
	if strings.Contains(all, "s3cr3t") || strings.Contains(all, "ghp_tok") || strings.Contains(all, "ghp_matchtok") ||
		strings.Contains(all, "scp-cluster-secret") || strings.Contains(all, "scpuser") ||
		!strings.Contains(all, "never fetched") {
		t.Fatalf("a credential leaked or the ignored entry is not noted:\n%s", all)
	}
}

func TestApproveWritesExactlyWhatWillRun(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	before := h.fleet()
	for _, bad := range []string{"https://tok@github.com/acme/tools.git", "http://github.com/acme/tools.git", "file:///srv/x", "git@github.com:acme/fleet.git"} {
		if r := h.svc.ApproveGitRepo(bad, "", ""); r.OK {
			t.Fatalf("%s was approved", bad)
		}
	}
	if h.fleet() != before {
		t.Fatal("a refused approval changed fleet.yaml")
	}
	if r := h.svc.ApproveGitRepo("https://github.com/acme/tools.git", "", "github.com/acme/tools"); !r.OK {
		t.Fatal(r.Error)
	}
	if r := h.svc.ApproveGitRepo("tools-alias:acme/tools2.git", "https://github.com/acme/tools2/", "github.com/acme/tools2"); !r.OK {
		t.Fatal(r.Error)
	}
	cfg, err := config.Load(h.path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Git.Repos[len(cfg.Git.Repos)-2:]
	want := []config.GitRepoConfig{
		{URL: "https://github.com/acme/tools.git"}, // serves its own key: no match written
		{URL: "tools-alias:acme/tools2.git", Web: "https://github.com/acme/tools2", Matches: []string{"github.com/acme/tools2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("approved %+v", got)
	}
	// The ignored entry stays as the owner wrote it.
	if !strings.Contains(h.fleet(), "ghp_tok@github.com/acme/leak.git") {
		t.Fatal("an owner-written entry was dropped")
	}
	if d := h.svc.GetGitSettings(); len(d.Unapproved) != 1 {
		t.Fatalf("tools is still unapproved: %+v", d.Unapproved)
	}
}

func TestMatchesAndRemoval(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	if r := h.svc.SetGitRepoMatch("git@github.com:acme/fleet.git", "git.internal/infra", true); !r.OK {
		t.Fatal(r.Error)
	}
	if d := h.svc.GetGitSettings(); len(d.Repos[0].Sources) != 5 || len(d.Unapproved) != 1 {
		t.Fatalf("after mapping: %+v / %+v", d.Repos[0].Sources, d.Unapproved)
	}
	for _, bad := range []string{"https://git.internal/infra", "GIT.internal/infra", "git.internal/infra.git"} {
		if r := h.svc.SetGitRepoMatch("git@github.com:acme/fleet.git", bad, true); r.OK {
			t.Fatalf("match %q accepted", bad)
		}
	}
	if r := h.svc.SetGitRepoMatch("git@github.com:acme/fleet.git", "github.com/acme/fleet", true); r.OK {
		t.Fatal("an entry matched its own key")
	}
	if r := h.svc.SetGitRepoMatch("git@github.com:acme/fleet.git", "git.internal/infra", false); !r.OK {
		t.Fatal(r.Error)
	}
	if d := h.svc.GetGitSettings(); len(d.Unapproved) != 2 {
		t.Fatalf("after unmapping: %+v", d.Unapproved)
	}
	if r := h.svc.RemoveGitRepo("git@github.com:acme/fleet.git"); !r.OK {
		t.Fatal(r.Error)
	}
	if strings.Contains(h.fleet(), "git@github.com:acme/fleet.git") || !reflect.DeepEqual(h.store.cleared, []string{"git@github.com:acme/fleet.git"}) {
		t.Fatalf("removal: cleared %v\n%s", h.store.cleared, h.fleet())
	}
	if r := h.svc.RemoveGitRepo("git@github.com:acme/fleet.git"); r.OK {
		t.Fatal("removing twice succeeded")
	}
}

func TestFetchNowFetchesOnlyTrackedRefsOfAnApprovedRepo(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	if r := h.svc.FetchGitRepo("git@github.com:acme/fleet.git"); !r.OK {
		t.Fatal(r.Error)
	}
	if got := h.store.refreshed["git@github.com:acme/fleet.git"]; !reflect.DeepEqual(got, []string{"refs/heads/main"}) {
		t.Fatalf("refreshed %v", got)
	}
	if r := h.svc.FetchGitRepo("https://github.com/acme/tools.git"); r.OK || !strings.Contains(r.Error, "approved") {
		t.Fatalf("an unapproved URL: %+v", r)
	}
	if r := h.svc.FetchGitRepo("https://ghp_tok@github.com/acme/leak.git"); r.OK {
		t.Fatal("an ignored entry was fetched")
	}
	if r := h.svc.FetchGitRepo("work-gh:acme/platform.git"); r.OK || !strings.Contains(r.Error, "nothing to fetch") {
		t.Fatalf("a repo with no target: %+v", r)
	}
	// A pinned commit on the line's key.
	h.key = &LineKeyDTO{Kind: line.KeyGit, Repo: "github.com/acme/platform", RefKind: "commit", RefName: strings.Repeat("a", 40)}
	h.push(LineDTO{Version: 2})
	if r := h.svc.FetchGitRepo("work-gh:acme/platform.git"); !r.OK {
		t.Fatal(r.Error)
	}
	if len(h.store.refreshed) != 2 {
		t.Fatalf("refreshed %v", h.store.refreshed)
	}
	if r := h.svc.ClearGitCache("work-gh:acme/platform.git"); !r.OK || len(h.store.cleared) != 1 {
		t.Fatalf("clear %+v %v", r, h.store.cleared)
	}
}

func TestGitOffSaysWhyAndFetchesNothing(t *testing.T) {
	h := newGitHarness(t, gitFleet, &gitcli.ErrOff{Reason: "git 2.44.0 is older than 2.45, which partial-clone safety needs"})
	d := h.svc.GetGitSettings()
	if !strings.Contains(d.Binary.Off, "older than 2.45") || d.Binary.Path != "" || len(d.Repos) != 2 {
		t.Fatalf("off: %+v", d)
	}
	if r := h.svc.FetchGitRepo("git@github.com:acme/fleet.git"); r.OK || !strings.Contains(r.Error, "Git features are off") {
		t.Fatalf("fetch while off: %+v", r)
	}
	// Approval still works: it only writes fleet.yaml.
	if r := h.svc.ApproveGitRepo("https://github.com/acme/tools.git", "", ""); !r.OK {
		t.Fatal(r.Error)
	}
}

func TestGitSourcesAreFluxGitRepositoriesAndArgoGitSources(t *testing.T) {
	in := line.StationInput{Station: line.Station{Cluster: "prd"}}
	in.Sources = []line.Object[flux.Source]{
		{Data: flux.Source{Kind: flux.GitRepositoryKind, Namespace: "flux-system", Name: "fleet", URL: "ssh://git@github.com/acme/fleet.git"}},
		{Data: flux.Source{Kind: "HelmRepository", Namespace: "flux-system", Name: "charts", URL: "https://charts.example.test"}},
		{Data: flux.Source{Kind: "OCIRepository", Namespace: "flux-system", Name: "oci", URL: "oci://ghcr.io/acme/x"}},
	}
	in.Apps = []line.Object[argo.App]{{Data: argo.App{Namespace: "argocd", Name: "apps", Sources: []argo.Source{
		{RepoURL: "https://github.com/acme/apps.git", Path: "apps"},
		{RepoURL: "https://charts.example.test", Chart: "redis"},
		{RepoURL: "https://github.com/acme/values.git", Ref: "values"},
	}}}}
	got := gitSources(in)
	var urls []string
	for _, s := range got {
		if s.Cluster != "prd" {
			t.Fatalf("cluster %+v", s)
		}
		urls = append(urls, s.Origin+" "+s.rawURL)
	}
	want := []string{"GitRepository ssh://git@github.com/acme/fleet.git", "Application https://github.com/acme/apps.git",
		"Application https://github.com/acme/values.git"}
	if !reflect.DeepEqual(urls, want) {
		t.Fatalf("git sources %q", urls)
	}
}

// No credential in a malformed URL - from a cluster or from fleet.yaml -
// reaches anything Settings > Git is given (design review of 3770529).
func TestNoSentinelReachesTheSettingsDTO(t *testing.T) {
	fleet := gitFleet + `    - url: https://ghp_SENTINEL_A@github.com/acme/repo%zz
    - url: "u:SENTINEL_B@github.com:acme/x.git"
    - url: SENTINEL_C@github.com/acme/bare
`
	h := newGitHarness(t, fleet, nil)
	extra := []LineGitSource{
		{Cluster: "prd", Origin: "Application", Namespace: "argocd", Name: "a", rawURL: "https://ghp_SENTINEL_D@github.com/acme/repo%zz"},
		{Cluster: "prd", Origin: "Application", Namespace: "argocd", Name: "b", rawURL: "https://SENTINEL_E:pw@git.internal/r%zz?SENTINEL_F"},
		{Cluster: "prd", Origin: "Application", Namespace: "argocd", Name: "c", rawURL: "SENTINEL_G@github.com/acme/bare"},
		{Cluster: "prd", Origin: "Application", Namespace: "argocd", Name: "d", rawURL: "u:SENTINEL_H@git.internal:infra.git"},
	}
	h.sources = append(h.sources, extra...)
	h.push(LineDTO{Version: 2})
	d := h.svc.GetGitSettings()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SENTINEL") {
		t.Fatalf("a sentinel reached the settings DTO:\n%s", b)
	}
	// The malformed URLs still appear, redacted, so the owner can act.
	if !strings.Contains(string(b), "(an unrecognised URL)") || !strings.Contains(string(b), "github.com/acme/repo") {
		t.Fatalf("the redacted rows are missing:\n%s", b)
	}
	for _, u := range d.Unapproved {
		if u.Serves != "" && config.ValidRepoKey(u.Serves) != nil {
			t.Fatalf("an approval would serve an invalid key %q", u.Serves)
		}
	}
}
