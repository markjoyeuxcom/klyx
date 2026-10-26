package appbridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitcache"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/localstore"
)

// GitService is The Line's read-only Git access (parent spec §7, slice 6
// note §5-6): Settings > Git - approval, URL mapping, sizes, "Fetch now",
// "Clear cache" - and every fetch. Git runs only from approved git.repos
// URLs, exactly as written; a URL read from a cluster is only matched to
// one. Nothing here writes to a cluster or to the owner's working trees.
type GitService struct {
	d   GitDeps
	ctx context.Context

	writeMu sync.Mutex // serialises Settings plans and Applies
	cache   *planCache

	mu       sync.Mutex
	ready    bool   // startup finished
	off      string // why Git is off; "" when it is on
	git      gitcli.Git
	helpers  gitcli.Helpers
	store    gitStore
	fetching map[string]time.Time // a fetch in flight, by URL: when it started
	// run is the running copy of fleet.yaml's git block (Settings slice F
	// §5): initialized from the startup configuration, read by every
	// consumer, replaced whole only by an Apply - never changed in place;
	// runRev moves with each replacement.
	run    config.GitConfig
	runRev uint64
	// The line, as LineService last pushed it (GitLineSink), and the
	// ordering computed from it (6d).
	line       LineDTO
	sources    []LineGitSource
	lineGit    LineGitDTO
	lineGitKey *LineKeyDTO // the key lineGit was ordered for (slice 8b-2)
	lineVer    uint64
	lineFP     string
	wake       chan struct{}
}

// gitStore is the cache store (internal/gitcache) as the service uses it.
type gitStore interface {
	Start(ctx context.Context) error
	RefreshHistory(ctx context.Context, url string, refs []gitcli.Ref) error
	Clear(ctx context.Context, url string) error
	Size(url string) int64
	Status(url string) gitcache.Status
	Read(url string, fn func(gitcli.Repo) error) error
}

// GitDeps wires the service. The line and the clusters' Git sources arrive
// through GitLineSink, which LineService calls after every LineDTO.
type GitDeps struct {
	ConfigPath string
	// Initial is the startup configuration's git block (nil: none): the
	// running copy until an Apply replaces it, never a second read
	Initial  *config.GitConfig
	Override *line.Key                                                      // fleet.yaml line.source, a tracked ref's other source
	Emitter  Emitter                                                        // line:git
	StartGit func(context.Context) (gitcli.Runner, gitcli.Helpers, error)   // nil: gitcli.Start with the owner's environment
	NewStore func(gitcli.Runner) gitStore                                   // nil: gitcache under the config dir
	SaveGit  func(path, version string, g config.GitConfig) (string, error) // nil: config.SaveGitAt
	Load     func(string) (*config.Config, string, error)                   // nil: config.LoadVersioned
	Now      func() time.Time                                               // nil: time.Now
}

// NewGitService prepares Git in the background - resolving git, reading
// the owner's credential helpers once, recovering every cache - so startup
// never waits on it.
func NewGitService(ctx context.Context, d GitDeps) *GitService {
	// "" (no fleet path) leaves Git off: gitcli.Start refuses the base.
	cacheBase, _ := localstore.Beside(d.ConfigPath).Path(localstore.GitCache)
	if d.StartGit == nil {
		d.StartGit = func(ctx context.Context) (gitcli.Runner, gitcli.Helpers, error) {
			return gitcli.Start(ctx, cacheBase, gitcli.ProcessEnv())
		}
	}
	if d.NewStore == nil {
		d.NewStore = func(r gitcli.Runner) gitStore { return gitcache.New(cacheBase, r) }
	}
	if d.SaveGit == nil {
		d.SaveGit = config.SaveGitAt
	}
	if d.Load == nil {
		d.Load = config.LoadVersioned
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &GitService{d: d, ctx: ctx, fetching: map[string]time.Time{}, wake: make(chan struct{}, 1), cache: newPlanCache(time.Now),
		lineGit: LineGitDTO{State: LineGitChecking, Reason: "Git is still starting", Stations: []LineGitStationDTO{},
			Ledger: []LineGitCommitDTO{}, Only: []LineGitOnlyDTO{}}}
	if d.Initial != nil {
		s.run = cloneGit(*d.Initial)
	}
	go s.lineLoop()
	go s.start()
	return s
}

func (s *GitService) start() {
	r, h, err := s.d.StartGit(s.ctx)
	var store gitStore
	if err == nil {
		store = s.d.NewStore(r)
		if serr := store.Start(s.ctx); serr != nil {
			log.Printf("git cache recovery: %v", serr)
		}
	}
	s.mu.Lock()
	s.ready, s.helpers = true, h
	if err != nil {
		s.off = gitOffReason(err)
	} else {
		s.git, s.store = r.Git, store
	}
	s.mu.Unlock()
	s.wakeLine()
}

func gitOffReason(err error) string {
	var off *gitcli.ErrOff
	if errors.As(err, &off) {
		return off.Reason
	}
	return err.Error()
}

// gitOn is the store when Git is on, or why it is not.
func (s *GitService) gitOn() (gitStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.ready:
		return nil, errors.New("Git is still starting")
	case s.off != "":
		return nil, errors.New("Git features are off: " + s.off)
	}
	return s.store, nil
}

// GitSettingsDTO is everything Settings > Git shows.
type GitSettingsDTO struct {
	Path           string             `json:"path"`
	Binary         GitBinaryDTO       `json:"binary"`
	Helpers        []string           `json:"helpers"`        // the credential helpers git is given, by name
	RefusedHelpers int                `json:"refusedHelpers"` // helpers given as shell snippets, not used
	Repos          []GitRepoDTO       `json:"repos"`
	Unapproved     []GitUnapprovedDTO `json:"unapproved"`
	Notes          []string           `json:"notes"` // fleet.yaml entries ignored, keys claimed twice
	// ConfigError: fleet.yaml cannot be read, so whether it differs from
	// what is in use is unknown
	ConfigError string `json:"configError,omitempty"`
	// Diverged: fleet.yaml's git block is not the one in use (a hand edit);
	// "Use the file's values" plans it (Settings slice F §5)
	Diverged bool `json:"diverged"`
}

// GitBinaryDTO is the binary line: the git in use, or why Git is off.
type GitBinaryDTO struct {
	Ready   bool   `json:"ready"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Off     string `json:"off,omitempty"`
}

// GitRepoDTO is one approved repository.
type GitRepoDTO struct {
	URL         string         `json:"url"` // exactly as approved: what runs
	Web         string         `json:"web,omitempty"`
	Key         string         `json:"key"`
	Matches     []string       `json:"matches"`
	Sources     []GitSourceDTO `json:"sources"` // the cluster URLs mapped to it
	Tracked     []string       `json:"tracked"` // the refs a fetch brings
	TrackNotes  []string       `json:"trackNotes"`
	Size        int64          `json:"size"`
	FetchedAt   *time.Time     `json:"fetchedAt,omitempty"`
	AttemptedAt *time.Time     `json:"attemptedAt,omitempty"`
	Error       string         `json:"error,omitempty"` // the last attempt's scrubbed error
	NeedsRepair bool           `json:"needsRepair"`
	Fetching    bool           `json:"fetching"`
}

// GitSourceDTO is one cluster object naming a Git URL, scrubbed.
type GitSourceDTO struct {
	Cluster   string `json:"cluster"`
	Origin    string `json:"origin"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	URL       string `json:"url"`
}

// GitUnapprovedDTO is a repository the clusters use that no entry serves.
type GitUnapprovedDTO struct {
	Key     string         `json:"key"`    // the repository key, or a redacted URL when there is no valid key
	Serves  string         `json:"serves"` // the key an approval from this row serves; "" when there is none
	Sources []GitSourceDTO `json:"sources"`
	// Suggest is the cluster's URL, scrubbed, when it could be approved as
	// it stands; Refused says why it could not be.
	Suggest string `json:"suggest,omitempty"`
	Refused string `json:"refused,omitempty"`
}

// BinaryState is the binary line alone, for Settings > Diagnostics.
func (s *GitService) BinaryState() GitBinaryDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := GitBinaryDTO{Ready: s.ready, Off: s.off}
	if s.ready && s.off == "" {
		b.Path, b.Version = s.git.Path, s.git.Version.Raw
	}
	return b
}

// GetGitSettings reports the git block in use, whether fleet.yaml's
// differs, the caches and the clusters' Git sources.
func (s *GitService) GetGitSettings() GitSettingsDTO {
	dto := GitSettingsDTO{Path: s.d.ConfigPath, Helpers: []string{}, Repos: []GitRepoDTO{}, Unapproved: []GitUnapprovedDTO{}, Notes: []string{}}
	s.mu.Lock()
	dto.Binary = GitBinaryDTO{Ready: s.ready, Off: s.off}
	if s.ready && s.off == "" {
		dto.Binary.Path, dto.Binary.Version = s.git.Path, s.git.Version.Raw
	}
	for _, h := range s.helpers.Values {
		dto.Helpers = append(dto.Helpers, strings.Fields(h)[0])
	}
	dto.RefusedHelpers = len(s.helpers.Refused)
	store := s.store
	fetching := copyFlags(s.fetching)
	sources, lk := s.sources, s.line.Key
	run := s.run
	s.mu.Unlock()

	if cfg, _, err := s.d.Load(s.d.ConfigPath); err != nil {
		dto.ConfigError = gitcli.Scrub(err.Error())
	} else {
		dto.Diverged = !config.GitReposEqual(fileGit(cfg).Repos, run.Repos)
	}
	repos, notes := approvedOf(run)
	dto.Notes = append(dto.Notes, notes...)
	owner, claimNotes := keyOwners(repos)
	dto.Notes = append(dto.Notes, claimNotes...)
	byEntry := map[string][]GitSourceDTO{}
	unapproved := map[string][]GitSourceDTO{}
	raw := map[string][]string{}
	for _, src := range sources {
		key := line.NormalizeRepo(src.rawURL)
		d := GitSourceDTO{Cluster: src.Cluster, Origin: src.Origin, Namespace: src.Namespace, Name: src.Name, URL: shownURL(src.rawURL)}
		if e, ok := owner[key]; ok {
			byEntry[e] = append(byEntry[e], d)
			continue
		}
		unapproved[key] = append(unapproved[key], d)
		raw[key] = append(raw[key], src.rawURL)
	}
	for _, r := range repos {
		rd := GitRepoDTO{URL: r.URL, Web: r.Web, Key: line.NormalizeRepo(r.URL), Matches: append([]string{}, r.Matches...),
			Sources: byEntry[r.URL], Tracked: []string{}, TrackNotes: []string{}, Fetching: fetching[r.URL]}
		if rd.Sources == nil {
			rd.Sources = []GitSourceDTO{}
		}
		refs, tnotes := trackedRefs(r.URL, owner, lk, s.d.Override)
		for _, ref := range refs {
			rd.Tracked = append(rd.Tracked, ref.String())
		}
		rd.TrackNotes = append(rd.TrackNotes, tnotes...)
		if store != nil {
			rd.Size = store.Size(r.URL)
			st := store.Status(r.URL)
			rd.FetchedAt, rd.AttemptedAt = timePtr(st.FetchedAt), timePtr(st.AttemptedAt)
			rd.Error, rd.NeedsRepair = st.Error, st.NeedsRepair
		}
		dto.Repos = append(dto.Repos, rd)
	}
	keys := make([]string, 0, len(unapproved))
	for k := range unapproved {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		u := GitUnapprovedDTO{Key: k, Serves: k, Sources: unapproved[k]}
		if config.ValidRepoKey(k) != nil {
			// No valid key (an unparsable URL): named redacted, approvable
			// only by typing the URL that will run.
			u.Key, u.Serves = config.RedactURL(raw[k][0]), ""
		}
		// The first of the clusters' spellings that could run as written.
		for _, rawURL := range raw[k] {
			if err := gitcli.ValidateApprovedURL(rawURL); err == nil {
				u.Suggest = rawURL
				break
			} else if u.Refused == "" {
				u.Refused = err.Error()
			}
		}
		if u.Suggest != "" {
			u.Refused = ""
		}
		dto.Unapproved = append(dto.Unapproved, u)
	}
	return dto
}

// shownURL is a cluster's URL as Settings shows it: as written when it
// could be approved as it stands (so it carries no credential), else
// redacted to its scheme, host and path.
func shownURL(raw string) string {
	if gitcli.ValidateApprovedURL(raw) == nil {
		return raw
	}
	return config.RedactURL(raw)
}

func copyFlags(m map[string]time.Time) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// approved is the running copy's approved repositories and its notes.
func (s *GitService) approved() ([]config.GitRepoConfig, []string) {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	return approvedOf(run)
}

func approvedOf(g config.GitConfig) ([]config.GitRepoConfig, []string) {
	return (&config.Config{Git: &g}).ApprovedGitRepos()
}

// fileGit is a loaded file's git block as written (none: empty).
func fileGit(cfg *config.Config) config.GitConfig {
	if cfg == nil || cfg.Git == nil {
		return config.GitConfig{}
	}
	return cloneGit(*cfg.Git)
}

// cloneGit copies g deeply: a candidate is built from the running copy
// without ever sharing a matches list with it.
func cloneGit(g config.GitConfig) config.GitConfig {
	out := config.GitConfig{Repos: make([]config.GitRepoConfig, 0, len(g.Repos))}
	for _, r := range g.Repos {
		r.Matches = slices.Clone(r.Matches)
		out.Repos = append(out.Repos, r)
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// keyOwners maps each normalised repository key to the approved entry that
// serves it - its own URL's key, or an explicit match. Two entries claiming
// one key: the first in fleet.yaml order serves it, and a note says so.
func keyOwners(repos []config.GitRepoConfig) (map[string]string, []string) {
	owner := map[string]string{}
	var notes []string
	claim := func(key, url string) {
		if key == "" {
			return
		}
		if prev, ok := owner[key]; ok && prev != url {
			notes = append(notes, fmt.Sprintf("%s is served by both %s and %s; the first is used", key, prev, url))
			return
		}
		owner[key] = url
	}
	for _, r := range repos {
		claim(line.NormalizeRepo(r.URL), r.URL)
		for _, m := range r.Matches {
			claim(m, r.URL)
		}
	}
	return owner, notes
}

// trackedRefs are the refs a fetch of the entry brings (slice 6 note §3.6):
// the line's chosen key's and the line.source override's, each when its
// repository maps to this entry - never free text. A key that names no
// single target is noted with its reason.
func trackedRefs(url string, owner map[string]string, chosen *LineKeyDTO, override *line.Key) ([]gitcli.Ref, []string) {
	type cand struct{ label, kind, repo, refKind, refName string }
	var cs []cand
	if chosen != nil {
		cs = append(cs, cand{"the line's key", chosen.Kind, chosen.Repo, chosen.RefKind, chosen.RefName})
	}
	if override != nil {
		cs = append(cs, cand{"line.source", override.Kind, override.Repo, override.RefKind, override.RefName})
	}
	var refs []gitcli.Ref
	var notes []string
	for _, c := range cs {
		if c.kind != line.KeyGit || owner[c.repo] != url {
			continue
		}
		r, err := gitcli.RefFromKey(c.refKind, c.refName)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s (%s %s): %v", c.label, c.refKind, c.refName, err))
			continue
		}
		if !slices.ContainsFunc(refs, func(x gitcli.Ref) bool { return x.String() == r.String() }) {
			refs = append(refs, r)
		}
	}
	if len(refs) == 0 && len(notes) == 0 {
		notes = append(notes, "no line key uses this repository, so there is nothing to fetch yet")
	}
	return refs, notes
}

// ApproveGitRepo adds an approved repository: url is what will run, exactly
// as written. serves, when set, is a cluster's repository key this entry
// also serves (an SSH alias that does not normalise to it).
func (s *GitService) ApproveGitRepo(url, web, serves string) ApplyResultDTO {
	url, web, serves = strings.TrimSpace(url), strings.TrimSpace(web), strings.TrimSpace(serves)
	if err := gitcli.ValidateApprovedURL(url); err != nil {
		return ApplyResultDTO{Error: "not approved: " + err.Error()}
	}
	if web != "" {
		if err := config.ValidWebLink(web); err != nil {
			return ApplyResultDTO{Error: "web link: " + err.Error()}
		}
	}
	if serves != "" {
		if err := config.ValidRepoKey(serves); err != nil {
			return ApplyResultDTO{Error: "serves: " + err.Error()}
		}
		if serves == line.NormalizeRepo(url) {
			serves = ""
		}
	}
	return s.editGit(gitEdit{Op: gitApprove, URL: url, Web: strings.TrimRight(web, "/"), Key: serves})
}

// RemoveGitRepo withdraws an approval and clears its cache.
func (s *GitService) RemoveGitRepo(url string) ApplyResultDTO {
	url = strings.TrimSpace(url)
	res := s.editGit(gitEdit{Op: gitRemove, URL: url})
	if !res.OK {
		return res
	}
	if store, err := s.gitOn(); err == nil {
		if err := store.Clear(s.ctx, url); err != nil {
			return ApplyResultDTO{Error: "the approval was removed, but its cache could not be cleared: " + err.Error()}
		}
	}
	return res
}

// SetGitRepoMatch adds (on) or removes an explicit repository key an entry
// serves.
func (s *GitService) SetGitRepoMatch(url, key string, on bool) ApplyResultDTO {
	url, key = strings.TrimSpace(url), strings.TrimSpace(key)
	if err := config.ValidRepoKey(key); err != nil {
		return ApplyResultDTO{Error: "match: " + err.Error()}
	}
	return s.editGit(gitEdit{Op: gitMatch, URL: url, Key: key, On: on})
}

// FetchGitRepo is "Fetch now": a history refresh of the entry's tracked
// refs, through the acquisition contract. It returns when the fetch ends.
func (s *GitService) FetchGitRepo(url string) ActionResultDTO {
	url = strings.TrimSpace(url)
	store, err := s.gitOn()
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	repos, _ := s.approved()
	if !slices.ContainsFunc(repos, func(r config.GitRepoConfig) bool { return r.URL == url }) {
		return ActionResultDTO{Error: "only an approved repository is fetched"}
	}
	owner, _ := keyOwners(repos)
	refs, notes := trackedRefs(url, owner, s.lineKey(), s.d.Override)
	if len(refs) == 0 {
		return ActionResultDTO{Error: "nothing to fetch: " + strings.Join(notes, "; ")}
	}
	return s.fetch(store, url, refs)
}

// fetch runs one history refresh, one per repository at a time, and
// recomputes the line's ordering after it.
func (s *GitService) fetch(store gitStore, url string, refs []gitcli.Ref) ActionResultDTO {
	s.mu.Lock()
	if _, ok := s.fetching[url]; ok {
		s.mu.Unlock()
		return ActionResultDTO{Error: "a fetch of this repository is already running"}
	}
	s.fetching[url] = s.d.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.fetching, url)
		s.mu.Unlock()
		s.wakeLine()
	}()
	if err := store.RefreshHistory(s.ctx, url, refs); err != nil {
		return ActionResultDTO{Error: gitcli.Scrub(err.Error())}
	}
	return ActionResultDTO{OK: true}
}

// fetchEvery is the fleet screen's cadence (slice 6 note §6.3).
const fetchEvery = 5 * time.Minute

// FetchDue is the fleet screen's cadence: on mount and every 5 minutes it
// asks, and each approved repository the line tracks is fetched in the
// background when its last attempt is at least 5 minutes old. It returns at
// once; line:git follows each fetch.
func (s *GitService) FetchDue() {
	store, err := s.gitOn()
	if err != nil {
		return
	}
	repos, _ := s.approved()
	owner, _ := keyOwners(repos)
	for _, r := range repos {
		refs, _ := trackedRefs(r.URL, owner, s.lineKey(), s.d.Override)
		if len(refs) == 0 {
			continue
		}
		st := store.Status(r.URL)
		s.mu.Lock()
		_, running := s.fetching[r.URL]
		s.mu.Unlock()
		if running || st.NeedsRepair || (!st.AttemptedAt.IsZero() && s.d.Now().Sub(st.AttemptedAt) < fetchEvery) {
			continue
		}
		go s.fetch(store, r.URL, refs)
	}
}

func (s *GitService) lineKey() *LineKeyDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.line.Key
}

// ClearGitCache removes a repository's cache; the next fetch rebuilds it.
func (s *GitService) ClearGitCache(url string) ActionResultDTO {
	store, err := s.gitOn()
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	if err := store.Clear(s.ctx, strings.TrimSpace(url)); err != nil {
		return ActionResultDTO{Error: gitcli.Scrub(err.Error())}
	}
	s.wakeLine()
	return ActionResultDTO{OK: true}
}
