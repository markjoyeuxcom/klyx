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

	writeMu sync.Mutex // serialises Settings writes to fleet.yaml

	mu       sync.Mutex
	ready    bool   // startup finished
	off      string // why Git is off; "" when it is on
	git      gitcli.Git
	helpers  gitcli.Helpers
	store    gitStore
	fetching map[string]bool
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
	Override   *line.Key                                                    // fleet.yaml line.source, a tracked ref's other source
	Emitter    Emitter                                                      // line:git
	StartGit   func(context.Context) (gitcli.Runner, gitcli.Helpers, error) // nil: gitcli.Start with the owner's environment
	NewStore   func(gitcli.Runner) gitStore                                 // nil: gitcache under the config dir
	SaveGit    func(string, config.GitConfig) error                         // nil: config.SaveGit
	Load       func(string) (*config.Config, error)                         // nil: config.Load
	Now        func() time.Time                                             // nil: time.Now
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
		d.SaveGit = config.SaveGit
	}
	if d.Load == nil {
		d.Load = config.Load
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &GitService{d: d, ctx: ctx, fetching: map[string]bool{}, wake: make(chan struct{}, 1),
		lineGit: LineGitDTO{State: LineGitChecking, Reason: "Git is still starting", Stations: []LineGitStationDTO{},
			Ledger: []LineGitCommitDTO{}, Only: []LineGitOnlyDTO{}}}
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
	ConfigError    string             `json:"configError,omitempty"`
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

// GetGitSettings reads fleet.yaml's git block, the caches and the clusters'
// Git sources.
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
	s.mu.Unlock()

	cfg, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		dto.ConfigError = gitcli.Scrub(err.Error())
		return dto
	}
	repos, notes := cfg.ApprovedGitRepos()
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

func copyFlags(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
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
func (s *GitService) ApproveGitRepo(url, web, serves string) ActionResultDTO {
	url, web, serves = strings.TrimSpace(url), strings.TrimSpace(web), strings.TrimSpace(serves)
	if err := gitcli.ValidateApprovedURL(url); err != nil {
		return ActionResultDTO{Error: "not approved: " + err.Error()}
	}
	if web != "" {
		if err := config.ValidWebLink(web); err != nil {
			return ActionResultDTO{Error: "web link: " + err.Error()}
		}
	}
	if serves != "" {
		if err := config.ValidRepoKey(serves); err != nil {
			return ActionResultDTO{Error: "serves: " + err.Error()}
		}
		if serves == line.NormalizeRepo(url) {
			serves = ""
		}
	}
	return s.editGit(func(g *config.GitConfig) error {
		for _, r := range g.Repos {
			if strings.TrimSpace(r.URL) == url {
				return errors.New("this URL is already approved")
			}
		}
		e := config.GitRepoConfig{URL: url, Web: strings.TrimRight(web, "/")}
		if serves != "" {
			e.Matches = []string{serves}
		}
		g.Repos = append(g.Repos, e)
		return nil
	})
}

// RemoveGitRepo withdraws an approval and clears its cache.
func (s *GitService) RemoveGitRepo(url string) ActionResultDTO {
	url = strings.TrimSpace(url)
	res := s.editGit(func(g *config.GitConfig) error {
		i := slices.IndexFunc(g.Repos, func(r config.GitRepoConfig) bool { return strings.TrimSpace(r.URL) == url })
		if i < 0 {
			return errors.New("no approved repository has this URL")
		}
		g.Repos = slices.Delete(g.Repos, i, i+1)
		return nil
	})
	if !res.OK {
		return res
	}
	if store, err := s.gitOn(); err == nil {
		if err := store.Clear(s.ctx, url); err != nil {
			return ActionResultDTO{Error: "the approval was removed, but its cache could not be cleared: " + err.Error()}
		}
	}
	return res
}

// SetGitRepoMatch adds (on) or removes an explicit repository key an entry
// serves.
func (s *GitService) SetGitRepoMatch(url, key string, on bool) ActionResultDTO {
	url, key = strings.TrimSpace(url), strings.TrimSpace(key)
	if err := config.ValidRepoKey(key); err != nil {
		return ActionResultDTO{Error: "match: " + err.Error()}
	}
	return s.editGit(func(g *config.GitConfig) error {
		i := slices.IndexFunc(g.Repos, func(r config.GitRepoConfig) bool { return strings.TrimSpace(r.URL) == url })
		if i < 0 {
			return errors.New("no approved repository has this URL")
		}
		r := &g.Repos[i]
		has := slices.Contains(r.Matches, key)
		switch {
		case on && !has:
			if key == line.NormalizeRepo(url) {
				return errors.New("this entry already serves its own repository")
			}
			r.Matches = append(r.Matches, key)
		case !on && has:
			r.Matches = slices.DeleteFunc(r.Matches, func(m string) bool { return m == key })
		}
		return nil
	})
}

// editGit applies one change to fleet.yaml's git block, serialised.
func (s *GitService) editGit(change func(*config.GitConfig) error) ActionResultDTO {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cfg, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		return ActionResultDTO{Error: gitcli.Scrub(err.Error())}
	}
	var g config.GitConfig
	if cfg.Git != nil {
		g.Repos = append([]config.GitRepoConfig(nil), cfg.Git.Repos...)
	}
	if err := change(&g); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	if err := s.d.SaveGit(s.d.ConfigPath, g); err != nil {
		return ActionResultDTO{Error: gitcli.Scrub(err.Error())}
	}
	s.wakeLine()
	return ActionResultDTO{OK: true}
}

// FetchGitRepo is "Fetch now": a history refresh of the entry's tracked
// refs, through the acquisition contract. It returns when the fetch ends.
func (s *GitService) FetchGitRepo(url string) ActionResultDTO {
	url = strings.TrimSpace(url)
	store, err := s.gitOn()
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	cfg, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		return ActionResultDTO{Error: gitcli.Scrub(err.Error())}
	}
	repos, _ := cfg.ApprovedGitRepos()
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
	if s.fetching[url] {
		s.mu.Unlock()
		return ActionResultDTO{Error: "a fetch of this repository is already running"}
	}
	s.fetching[url] = true
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
	cfg, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		return
	}
	repos, _ := cfg.ApprovedGitRepos()
	owner, _ := keyOwners(repos)
	for _, r := range repos {
		refs, _ := trackedRefs(r.URL, owner, s.lineKey(), s.d.Override)
		if len(refs) == 0 {
			continue
		}
		st := store.Status(r.URL)
		s.mu.Lock()
		running := s.fetching[r.URL]
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
