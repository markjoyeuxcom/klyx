package appbridge

// Codex's F2b implementation review probes, adopted: R1 a queued fetch
// after a revocation and admission during an Apply, R2 a removal applied
// through its replacement plan, R4 an ignored entry as an action's target.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitcache"
	"github.com/moomora/klyx/internal/gitcli"
)

// A comment-only concurrent edit returns a replacement of the same removal.
func TestF2bReviewRemovalThroughReplacementClearsCache(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	h.svc.d.SaveGit = func(path, version string, g config.GitConfig) (string, error) {
		if err := os.WriteFile(path, []byte("# concurrent comment\n"+gitFleet), 0o644); err != nil {
			t.Fatal(err)
		}
		return config.SaveGitAt(path, version, g)
	}
	r := h.svc.RemoveGitRepo(fleetURL)
	if r.OK || r.Plan == nil || r.Plan.Refused != "" {
		t.Fatalf("expected replacement, got %+v", r)
	}
	h.svc.d.SaveGit = config.SaveGitAt
	if r = h.svc.ApplyGit(r.Plan.Digest); !r.OK {
		t.Fatalf("replacement refused: %+v", r)
	}
	for _, repo := range h.svc.GetGitSettings().Repos {
		if repo.URL == fleetURL {
			t.Fatal("approval stayed")
		}
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.cleared) != 1 || h.store.cleared[0] != fleetURL {
		t.Fatalf("removal applied but its cache was never cleared: %v", h.store.cleared)
	}
}

type reviewStatusGate struct {
	*fakeGitStore
	armed   atomic.Bool
	reached chan struct{}
	release chan struct{}
	started chan string
}

func (g *reviewStatusGate) Status(url string) gitcache.Status {
	if url == fleetURL && g.armed.CompareAndSwap(true, false) {
		close(g.reached)
		<-g.release
	}
	return g.fakeGitStore.Status(url)
}

func (g *reviewStatusGate) RefreshHistory(ctx context.Context, url string, refs []gitcli.Ref) error {
	g.started <- url
	return g.fakeGitStore.RefreshHistory(ctx, url, refs)
}

func TestF2bReviewQueuedFetchDoesNotStartAfterRevocation(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	h.waitLine(1)
	g := &reviewStatusGate{fakeGitStore: h.store, reached: make(chan struct{}), release: make(chan struct{}), started: make(chan string, 8)}
	g.armed.Store(true)
	h.svc.mu.Lock()
	h.svc.store = g
	h.svc.mu.Unlock()
	done := make(chan struct{})
	go func() { h.svc.FetchDue(); close(done) }()
	select {
	case <-g.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("cadence never reached pre-fetch gate")
	}
	h.write(strings.Replace(gitFleet, "    - url: git@github.com:acme/fleet.git\n      matches: [\"https://ghp_matchtok@github.com/acme/alias\"]\n", "", 1))
	p := adopt(t, h.svc)
	if len(p.Continues) != 0 {
		t.Fatalf("the paused caller was unexpectedly named: %+v", p.Continues)
	}
	close(g.release)
	<-done
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case url := <-g.started:
			if url == fleetURL {
				t.Fatal("unapproved repository started fetching after adoption, although the plan named no continuing fetch")
			}
		case <-timer.C:
			return
		}
	}
}

func TestF2bReviewIgnoredEntryNeverEntersCandidateCache(t *testing.T) {
	for _, op := range []string{"remove", "alias"} {
		t.Run(op, func(t *testing.T) {
			h := newGitHarness(t, gitFleet, nil)
			h.svc.d.SaveGit = func(path, version string, g config.GitConfig) (string, error) {
				h.write("# concurrent comment\n" + gitFleet)
				return config.SaveGitAt(path, version, g)
			}
			url := "https://ghp_tok@github.com/acme/leak.git"
			var r ApplyResultDTO
			if op == "remove" {
				r = h.svc.RemoveGitRepo(url)
			} else {
				r = h.svc.SetGitRepoMatch(url, "github.com/acme/alias", true)
			}
			if r.OK {
				t.Fatal("expected refusal")
			}
			h.svc.cache.mu.Lock()
			defer h.svc.cache.mu.Unlock()
			for _, entry := range h.svc.cache.entries {
				b, err := json.Marshal(entry.value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), "ghp_tok") {
					t.Fatal("ignored entry's credential-bearing URL entered the plan cache")
				}
			}
		})
	}
}

func TestF2bReviewApplyingReservesItsWorkSnapshot(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	h.svc.d.SaveGit = func(path, version string, g config.GitConfig) (string, error) {
		h.write("# concurrent comment\n" + gitFleet)
		return config.SaveGitAt(path, version, g)
	}
	r := h.svc.RemoveGitRepo(fleetURL)
	if r.Plan == nil || len(r.Plan.Continues) != 0 {
		t.Fatalf("replacement: %+v", r)
	}
	saving, release := make(chan struct{}), make(chan struct{})
	h.svc.d.SaveGit = func(path, version string, g config.GitConfig) (string, error) {
		close(saving)
		<-release
		return config.SaveGitAt(path, version, g)
	}
	fetchGate := make(chan struct{})
	h.store.mu.Lock()
	h.store.gate = fetchGate
	h.store.mu.Unlock()
	defer close(fetchGate)
	applied := make(chan ApplyResultDTO, 1)
	go func() { applied <- h.svc.ApplyGit(r.Plan.Digest) }()
	<-saving
	fetched := make(chan ActionResultDTO, 1)
	go func() { fetched <- h.svc.FetchGitRepo(fleetURL) }()
	for end := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
		h.svc.mu.Lock()
		_, running := h.svc.fetching[fleetURL]
		h.svc.mu.Unlock()
		if running {
			break
		}
		select {
		case <-fetched:
			close(release)
			<-applied
			return
		default:
		}
		if time.Now().After(end) {
			close(release)
			<-applied
			t.Fatal("fetch neither refused nor admitted")
		}
	}
	close(release)
	if r := <-applied; r.OK {
		t.Fatal("Apply accepted an empty work snapshot while admitting a new fetch during its write")
	}
}

func TestF2bReviewOrdinaryRemovalStillClearsCache(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	if r := h.svc.RemoveGitRepo(fleetURL); !r.OK {
		t.Fatalf("%+v", r)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.cleared) != 1 || h.store.cleared[0] != fleetURL {
		t.Fatal("control did not clear cache")
	}
}

// R1 for the consumers' own acquisitions: the cache diff's and chat's
// fetches are admitted when they start, named under Continues while in
// flight, refused while an Apply decides and once their repository is
// revoked.
type gatedCache struct {
	*fakeGitStore
	started chan struct{}
	gate    chan struct{}
}

func (g gatedCache) EnsureCommit(context.Context, string, string) error {
	g.started <- struct{}{}
	<-g.gate
	return nil
}
func (g gatedCache) AcquireIDs(context.Context, string, []string) error { return nil }

func TestF2bConsumerFetchesAreAdmittedUnderTheRunningCopy(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	g := gatedCache{fakeGitStore: h.store, started: make(chan struct{}, 4), gate: make(chan struct{})}
	a := admittedStore{cacheStore: g, s: h.svc, what: "chat's Git changes"}
	done := make(chan error, 1)
	go func() { done <- a.EnsureCommit(context.Background(), fleetURL, strings.Repeat("a", 40)) }()
	<-g.started
	// during an Apply's write, no acquisition is admitted
	saving, release := make(chan struct{}), make(chan struct{})
	h.svc.d.SaveGit = func(path, version string, c config.GitConfig) (string, error) {
		close(saving)
		<-release
		return config.SaveGitAt(path, version, c)
	}
	applied := make(chan ApplyResultDTO, 1)
	go func() { applied <- h.svc.RemoveGitRepo("work-gh:acme/platform.git") }()
	<-saving
	if err := a.EnsureCommit(context.Background(), fleetURL, strings.Repeat("b", 40)); !errors.Is(err, errGitApplying) {
		close(release)
		t.Fatalf("admitted during an Apply: %v", err)
	}
	close(release)
	if r := <-applied; !r.OK {
		t.Fatalf("the removal: %+v", r)
	}
	h.svc.d.SaveGit = config.SaveGitAt
	if err := a.AcquireIDs(context.Background(), fleetURL, nil); err != nil {
		t.Fatalf("refused once the Apply published: %v", err)
	}
	// a revocation names the fetch in flight, which continues; a new one is refused
	h.write(strings.Replace(gitFleet, "    - url: git@github.com:acme/fleet.git\n      matches: [\"https://ghp_matchtok@github.com/acme/alias\"]\n", "", 1))
	p := h.svc.PlanGitFromFile()
	if len(p.Continues) != 1 || !strings.HasPrefix(p.Continues[0].ID, "acquire:") || !strings.Contains(p.Continues[0].What, "for chat's Git changes in progress") {
		t.Fatalf("continues %+v", p.Continues)
	}
	if r := h.svc.ApplyGit(p.Digest); !r.OK {
		t.Fatalf("%+v", r)
	}
	close(g.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureCommit(context.Background(), fleetURL, strings.Repeat("c", 40)); !errors.Is(err, errNotApprovedFetch) {
		t.Fatalf("admitted after its revocation: %v", err)
	}
	if err := a.AcquireIDs(context.Background(), fleetURL, nil); !errors.Is(err, errNotApprovedFetch) {
		t.Fatalf("content admitted after its revocation: %v", err)
	}
	h.svc.mu.Lock()
	n := len(h.svc.acquiring)
	h.svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d acquisitions left registered", n)
	}
}

// The cache diff and chat reach the cache through admittedStore: while an
// Apply decides, neither fetches.
func TestF2bTheCacheDiffAndChatFetchOnlyWhenAdmitted(t *testing.T) {
	store, sha := newDiffFixture(t)
	svc := newDiffService(t, store)
	svc.mu.Lock()
	svc.applying = true
	svc.mu.Unlock()
	d, ok := svc.cacheDiff(context.Background(), &diffConn{objs: fluxObjects(sha, nil)}, "flux-system", "apps")
	if !ok || !strings.Contains(d.Error, "being applied") || len(store.ensured) != 0 {
		t.Fatalf("the cache diff fetched during an Apply: %+v, ensured %v", d, store.ensured)
	}
	_, err := svc.changes(context.Background(), gitChangesReq{RepoURL: "https://github.com/acme/fleet.git", Applied: sha, Attempted: sha, Spec: map[string]any{"path": "./clusters/prd"}})
	if !errors.Is(err, errGitApplying) || len(store.ensured) != 0 {
		t.Fatalf("chat fetched during an Apply: %v, ensured %v", err, store.ensured)
	}
}

// R2's partial outcome: the removal applied, its cache not cleared - said
// as applied, with the part that did not happen.
func TestF2bARemovalWhoseCacheStaysSaysSo(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	h.store.mu.Lock()
	h.store.clearErr = errors.New("cache busy")
	h.store.mu.Unlock()
	r := h.svc.RemoveGitRepo(fleetURL)
	if !r.OK || !strings.Contains(r.Note, "could not be cleared: cache busy") {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(h.fleet(), "git@github.com:acme/fleet.git") {
		t.Fatal("the approval stayed")
	}
}
