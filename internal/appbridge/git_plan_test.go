package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
)

// Settings slice F2b: Git runs from its running copy, initialized from the
// startup configuration. A hand edit of git.repos is drift for every
// consumer until "Use the file's values", which writes nothing.

const fleetURL = "git@github.com:acme/fleet.git"

func (h *gitHarness) write(doc string) {
	h.t.Helper()
	if err := os.WriteFile(h.path, []byte(doc), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// adopt applies "Use the file's values".
func adopt(t *testing.T, s *GitService) PlanDTO {
	t.Helper()
	p := s.PlanGitFromFile()
	if p.Refused != "" || p.Writes {
		t.Fatalf("plan %+v", p)
	}
	if r := s.ApplyGit(p.Digest); !r.OK {
		t.Fatalf("%+v", r)
	}
	return p
}

func (h *gitHarness) refreshed(url string) bool {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	return len(h.store.refreshed[url]) > 0
}

func TestAGitHandEditIsDriftUntilAdopted(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	// the owner removes the fleet entry by hand
	edited := strings.Replace(gitFleet, "    - url: git@github.com:acme/fleet.git\n      matches: [\"https://ghp_matchtok@github.com/acme/alias\"]\n", "", 1)
	if edited == gitFleet {
		t.Fatal("fixture")
	}
	h.write(edited)
	d := h.svc.GetGitSettings()
	if !d.Diverged || len(d.Repos) != 2 || d.Repos[0].URL != fleetURL || d.ConfigError != "" {
		t.Fatalf("drift, the running copy shown: %+v", d)
	}
	// every consumer keeps the running copy: Fetch now, the cadence, the line
	if r := h.svc.FetchGitRepo(fleetURL); !r.OK || !h.refreshed(fleetURL) {
		t.Fatalf("Fetch now: %+v", r)
	}
	h.store.mu.Lock()
	h.store.refreshed = map[string][]string{}
	h.store.mu.Unlock()
	h.svc.FetchDue()
	for end := time.Now().Add(5 * time.Second); !h.refreshed(fleetURL); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the cadence dropped the running copy's repository")
		}
	}
	h.push(LineDTO{Version: 2})
	if g := h.waitLine(2); g.State == LineGitUnapproved || g.Repo != fleetURL {
		t.Fatalf("the line: %+v", g)
	}
	// an edit starts from what runs, so it is refused, not written over the edit
	if r := h.svc.ApproveGitRepo("https://github.com/acme/tools.git", "", ""); r.OK || !strings.Contains(r.Error, "use the file's values first") || h.fleet() != edited {
		t.Fatalf("%+v\n%s", r, h.fleet())
	}
	if r := h.svc.RemoveGitRepo("work-gh:acme/platform.git"); r.OK || h.fleet() != edited || len(h.store.cleared) != 0 {
		t.Fatalf("a refused removal: %+v, cleared %v", r, h.store.cleared)
	}
	p := adopt(t, h.svc)
	if !p.Activates || len(p.Ends) != 0 || len(p.Changes) != 2 || p.Changes[0].ID != "git.repos["+fleetURL+"].url" || p.Changes[0].To != "not approved" ||
		p.Changes[1].ID != "git.repos.ignored" || p.Changes[1].From != "2" || p.Changes[1].To != "1" {
		t.Fatalf("plan %+v", p)
	}
	if h.fleet() != edited {
		t.Fatalf("Use the file's values wrote:\n%s", h.fleet())
	}
	if d := h.svc.GetGitSettings(); d.Diverged || len(d.Repos) != 1 {
		t.Fatalf("adopted %+v", d)
	}
	if r := h.svc.FetchGitRepo(fleetURL); r.OK || !strings.Contains(r.Error, "only an approved repository") {
		t.Fatalf("Fetch now after: %+v", r)
	}
	// the line orders again on adoption, before any new LineDTO
	for end := time.Now().Add(5 * time.Second); h.svc.LineGit().State != LineGitUnapproved; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the line after: %+v", h.svc.LineGit())
		}
	}
	h.store.mu.Lock()
	h.store.refreshed = map[string][]string{}
	h.store.mu.Unlock()
	h.svc.FetchDue()
	time.Sleep(100 * time.Millisecond)
	if h.refreshed(fleetURL) {
		t.Fatal("the cadence fetched a repository no longer approved")
	}
	// adopted, an edit writes again
	if r := h.svc.ApproveGitRepo("https://github.com/acme/tools.git", "", ""); !r.OK || !strings.Contains(h.fleet(), "acme/tools.git") {
		t.Fatalf("%+v", r)
	}
	if len(h.svc.cache.entries) != 0 {
		t.Fatalf("an applied plan stays cached: %d", len(h.svc.cache.entries))
	}
}

// The chat's Git changes, "not here yet" and the cache diff read the
// running copy too.
func TestGitConsumersKeepTheRunningCopyUntilAdoption(t *testing.T) {
	t.Run("chat and not here yet", func(t *testing.T) {
		h, f := newNotHereHarness(t)
		h.write(strings.Replace(notHereFleet, "      matches: [git.mirror.internal/acme/fleet]\n", "", 1))
		alias := node("alias", f.c2, "./infra")
		alias.URL = "ssh://git@git.mirror.internal/acme/fleet.git"
		req := gitChangesReq{RepoURL: alias.URL, Applied: f.c1, Attempted: f.c2}
		if got := h.svc.NotHereYet(context.Background(), []NotHereYetNodeDTO{alias}); strings.Contains(got.Results[0].Reason, "not approved") {
			t.Fatalf("not here yet read the file: %+v", got.Results[0])
		}
		if _, err := h.svc.changes(context.Background(), req); errors.Is(err, errChatGitNotApproved) {
			t.Fatal("chat read the file")
		}
		adopt(t, h.svc)
		if got := h.svc.NotHereYet(context.Background(), []NotHereYetNodeDTO{alias}); !strings.Contains(got.Results[0].Reason, "not approved") {
			t.Fatalf("not here yet after: %+v", got.Results[0])
		}
		if _, err := h.svc.changes(context.Background(), req); !errors.Is(err, errChatGitNotApproved) {
			t.Fatalf("chat after: %v", err)
		}
	})
	t.Run("the cache diff", func(t *testing.T) {
		store, sha := newDiffFixture(t)
		svc := newDiffService(t, store)
		if err := os.WriteFile(svc.d.ConfigPath, []byte("clusters:\n  - name: prd\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := svc.cacheDiff(context.Background(), &diffConn{objs: fluxObjects(sha, nil)}, "flux-system", "apps"); !ok {
			t.Fatal("the cache diff read the file")
		}
		adopt(t, svc)
		if _, ok := svc.cacheDiff(context.Background(), &diffConn{objs: fluxObjects(sha, nil)}, "flux-system", "apps"); ok {
			t.Fatal("the cache diff kept a repository no longer approved")
		}
	})
}

func TestAGitPlanRefusesAMovedFile(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	h.write(strings.Replace(gitFleet, "    - url: https://ghp_tok@github.com/acme/leak.git\n", "", 1))
	p := h.svc.PlanGitFromFile()
	h.write(strings.Replace(gitFleet, "      matches: [github.com/acme/platform]\n", "", 1))
	r := h.svc.ApplyGit(p.Digest)
	if r.OK || r.Plan == nil || r.Plan.Digest == p.Digest || r.Plan.FileVersion == p.FileVersion {
		t.Fatalf("a moved file applied: %+v", r)
	}
	if d := h.svc.GetGitSettings(); len(d.Repos[1].Matches) != 1 {
		t.Fatalf("the refused plan changed what runs: %+v", d.Repos[1])
	}
	if r := h.svc.ApplyGit(r.Plan.Digest); !r.OK {
		t.Fatalf("the replacement: %+v", r)
	}
	if d := h.svc.GetGitSettings(); len(d.Repos[1].Matches) != 0 || d.Diverged {
		t.Fatalf("adopted %+v", d.Repos[1])
	}
	// nothing differs: the plan changes nothing and writes nothing
	if p := h.svc.PlanGitFromFile(); p.Activates || p.Writes || len(p.Changes) != 0 {
		t.Fatalf("no drift: %+v", p)
	}
}

// A plan binds the running copy's revision: the same file version and
// changes, after what runs moved and came back, are refused.
func TestAGitPlanRefusesAnEarlierRevision(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	planned := strings.Replace(gitFleet, "    - url: https://ghp_tok@github.com/acme/leak.git\n", "", 1)
	h.write(planned)
	p := h.svc.PlanGitFromFile()
	h.write(strings.Replace(gitFleet, "      matches: [github.com/acme/platform]\n", "", 1))
	adopt(t, h.svc)
	h.write("# as it began\n" + gitFleet)
	adopt(t, h.svc)
	h.write(planned)
	r := h.svc.ApplyGit(p.Digest)
	if r.OK || r.Plan == nil || r.Plan.FileVersion != p.FileVersion || r.Plan.Runtime["git"] == p.Runtime["git"] {
		t.Fatalf("a plan from an earlier revision applied: %+v", r)
	}
	if r := h.svc.ApplyGit(r.Plan.Digest); !r.OK || h.svc.GetGitSettings().Diverged {
		t.Fatalf("the replacement: %+v", r)
	}
}

// A hand edit between the re-plan and the write refuses the edit, and the
// running copy is untouched: the candidate never shares its matches.
func TestAGitEditWritesAgainstItsVersion(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	edited := "# edited by hand\n" + gitFleet
	h.svc.d.SaveGit = func(p, version string, g config.GitConfig) (string, error) {
		h.write(edited)
		return config.SaveGitAt(p, version, g)
	}
	r := h.svc.SetGitRepoMatch("work-gh:acme/platform.git", "github.com/acme/platform", false)
	if r.OK || !strings.Contains(r.Error, "changed while this was applied") || r.Plan == nil || h.fleet() != edited {
		t.Fatalf("%+v\n%s", r, h.fleet())
	}
	if d := h.svc.GetGitSettings(); !slices.Equal(d.Repos[1].Matches, []string{"github.com/acme/platform"}) {
		t.Fatalf("a refused edit changed what runs: %+v", d.Repos[1])
	}
}

// A fetch in flight of a repository the change stops approving is named
// under Continues, so its ending moves the plan.
func TestAGitPlanNamesAFetchInFlight(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	gate := make(chan struct{})
	h.store.mu.Lock()
	h.store.gate = gate
	h.store.mu.Unlock()
	done := make(chan ActionResultDTO, 1)
	go func() { done <- h.svc.FetchGitRepo(fleetURL) }()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		h.svc.mu.Lock()
		_, running := h.svc.fetching[fleetURL]
		h.svc.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the fetch never started")
		}
	}
	h.write(strings.Replace(gitFleet, "    - url: git@github.com:acme/fleet.git\n      matches: [\"https://ghp_matchtok@github.com/acme/alias\"]\n", "", 1))
	p := h.svc.PlanGitFromFile()
	if len(p.Continues) != 1 || p.Continues[0].ID != "fetch:"+fleetURL || p.Continues[0].Kind != "fetch" || p.Continues[0].Started == "" || len(p.Ends) != 0 {
		t.Fatalf("continues %+v", p.Continues)
	}
	close(gate)
	if r := <-done; !r.OK {
		t.Fatalf("the fetch: %+v", r)
	}
	r := h.svc.ApplyGit(p.Digest)
	if r.OK || r.Plan == nil || len(r.Plan.Continues) != 0 {
		t.Fatalf("the work moved: %+v", r)
	}
	if r := h.svc.ApplyGit(r.Plan.Digest); !r.OK {
		t.Fatalf("the replacement: %+v", r)
	}
}

// An entry Klyx ignores may carry a token: no plan or cache entry holds
// its text, only how many there are.
func TestAGitPlanHoldsNoIgnoredEntrysText(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	h.write(gitFleet + "    - url: https://ghp_newtoken@github.com/acme/x.git\n")
	p := h.svc.PlanGitFromFile()
	b, _ := json.Marshal(p)
	cached, _ := json.Marshal(h.svc.cache.entries[p.Digest].value)
	if strings.Contains(string(b)+string(cached), "ghp_") || len(p.Changes) != 1 || p.Changes[0].From != "2" || p.Changes[0].To != "3" {
		t.Fatalf("plan %s\ncached %s", b, cached)
	}
	adopt(t, h.svc)
	r := h.svc.ApproveGitRepo("https://github.com/acme/tools.git", "", "")
	b, _ = json.Marshal(r)
	if !r.OK || strings.Contains(string(b), "ghp_") || !strings.Contains(h.fleet(), "ghp_newtoken") {
		t.Fatalf("%s\n%s", b, h.fleet())
	}
}
