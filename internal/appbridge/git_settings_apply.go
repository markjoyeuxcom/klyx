package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/line"
)

// Settings > Git's plans (Settings slice F §5): every change, an approval,
// a removal, an alias or "Use the file's values", is planned from the
// running copy and applied against the file version it was planned from.
// An explicit action is its own confirmation: it applies at once when its
// plan ends nothing and needs no interlock, which in F2 none does.
//
// Every acquisition from a repository - a history refresh, the cache
// diff's and chat's commit and content fetches - is admitted when it
// starts, under the running copy as it is then: refused while an Apply
// decides, or once its repository is not approved, and registered before
// the lock is released. So an Apply's plan names each acquisition in
// flight, and none starts after a revocation (F2b review, R1).

// gitAcq is one acquisition in flight: its admission number, never
// reused, is its identity in a plan, so a later acquisition of the same
// repository is other work, whenever it started (F2b re-review, RR1).
type gitAcq struct {
	n         uint64
	url, what string
	started   time.Time
}

var (
	errGitApplying      = errors.New("the Git settings are being applied; try again in a moment")
	errNotApprovedFetch = errors.New("only an approved repository is fetched")
)

// admissibleLocked: an acquisition from url may start now.
func (s *GitService) admissibleLocked(url string) error {
	if s.applying {
		return errGitApplying
	}
	repos, _ := approvedOf(s.run)
	if !slices.ContainsFunc(repos, func(r config.GitRepoConfig) bool { return r.URL == url }) {
		return errNotApprovedFetch
	}
	return nil
}

// admitFetch admits a history refresh of url, one per repository at a
// time; release ends it.
func (s *GitService) admitFetch(url string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admissibleLocked(url); err != nil {
		return nil, err
	}
	if _, ok := s.fetching[url]; ok {
		return nil, errors.New("a fetch of this repository is already running")
	}
	s.acqSeq++
	n := s.acqSeq
	s.fetching[url] = gitAcq{n: n, url: url, what: "the fetch", started: s.d.Now()}
	return func() {
		s.mu.Lock()
		if s.fetching[url].n == n {
			delete(s.fetching, url)
		}
		s.mu.Unlock()
	}, nil
}

// admitAcquisition admits one commit or content acquisition from url for
// what; release ends it.
func (s *GitService) admitAcquisition(url, what string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admissibleLocked(url); err != nil {
		return nil, err
	}
	s.acqSeq++
	n := s.acqSeq
	s.acquiring[n] = gitAcq{n: n, url: url, what: what, started: s.d.Now()}
	return func() {
		s.mu.Lock()
		delete(s.acquiring, n)
		s.mu.Unlock()
	}, nil
}

// admittedStore is the cache as a consumer's acquisitions reach it: each
// commit or content fetch is admitted when it starts. Reads of what is
// cached never fetch (GIT_NO_LAZY_FETCH) and need no admission.
type admittedStore struct {
	cacheStore
	s    *GitService
	what string
}

func (a admittedStore) EnsureCommit(ctx context.Context, url, sha string) error {
	release, err := a.s.admitAcquisition(url, a.what)
	if err != nil {
		return err
	}
	defer release()
	return a.cacheStore.EnsureCommit(ctx, url, sha)
}

func (a admittedStore) AcquireIDs(ctx context.Context, url string, ids []string) error {
	release, err := a.s.admitAcquisition(url, a.what)
	if err != nil {
		return err
	}
	defer release()
	return a.cacheStore.AcquireIDs(ctx, url, ids)
}

// The edits Settings > Git makes.
const (
	gitApprove = "approve"
	gitRemove  = "remove"
	gitMatch   = "match"
)

// gitEdit is one Settings > Git change as data: its plan binds it, and
// Apply makes it again on the running copy. It holds no block, so neither
// the cache nor the digest holds an entry as the owner wrote it - an
// ignored one may carry a token.
type gitEdit struct {
	Op  string `json:"op"`
	URL string `json:"url"`
	Web string `json:"web,omitempty"`
	Key string `json:"key,omitempty"` // approve: the key it also serves; match: the key
	On  bool   `json:"on,omitempty"`
}

// apply makes e on g.
func (e gitEdit) apply(g *config.GitConfig) error {
	i := slices.IndexFunc(g.Repos, func(r config.GitRepoConfig) bool { return strings.TrimSpace(r.URL) == e.URL })
	switch e.Op {
	case gitApprove:
		if i >= 0 {
			return errors.New("this URL is already approved")
		}
		r := config.GitRepoConfig{URL: e.URL, Web: e.Web}
		if e.Key != "" {
			r.Matches = []string{e.Key}
		}
		g.Repos = append(g.Repos, r)
	case gitRemove:
		if i < 0 {
			return errors.New("no approved repository has this URL")
		}
		g.Repos = slices.Delete(g.Repos, i, i+1)
	case gitMatch:
		if i < 0 {
			return errors.New("no approved repository has this URL")
		}
		r := &g.Repos[i]
		has := slices.Contains(r.Matches, e.Key)
		switch {
		case e.On && !has:
			if e.Key == line.NormalizeRepo(e.URL) {
				return errors.New("this entry already serves its own repository")
			}
			r.Matches = append(r.Matches, e.Key)
		case !e.On && has:
			r.Matches = slices.DeleteFunc(r.Matches, func(m string) bool { return m == e.Key })
		}
	default:
		return fmt.Errorf("unknown Git change %q", e.Op)
	}
	return nil
}

// gitCandidate is what a plan's digest holds in the cache: the edit, or
// "Use the file's values", which adopts the file as it is at Apply.
type gitCandidate struct {
	Kind string   `json:"kind"`
	Edit *gitEdit `json:"edit,omitempty"`
}

// gitDiverged: an edit starts from what runs, so writing it would replace a
// hand edit it never showed.
const gitDiverged = "fleet.yaml's Git settings differ from the ones in use, so nothing was written: use the file's values first, or undo that edit"

func gitRefused(kind, why string) PlanDTO {
	return PlanDTO{Section: "git", Kind: kind, Refused: why, Runtime: map[string]string{}, Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
}

// candidateLocked is c's candidate as it stands now: the file's block, or
// the edit made on a copy of the running one.
func (s *GitService) candidateLocked(c gitCandidate, file config.GitConfig) (config.GitConfig, error) {
	if c.Kind == planFile {
		return cloneGit(file), nil
	}
	s.mu.Lock()
	cand := cloneGit(s.run)
	s.mu.Unlock()
	if c.Edit == nil {
		return cand, errors.New("no change to apply")
	}
	return cand, c.Edit.apply(&cand)
}

// planLocked plans cand from what runs. An edit is refused while the
// file's block differs from the one in use: the block condition, which the
// write's file version then holds to.
func (s *GitService) planLocked(c gitCandidate, cand config.GitConfig, version string, file config.GitConfig) PlanDTO {
	s.mu.Lock()
	run, rev := s.run, s.runRev
	fetching := make(map[string]gitAcq, len(s.fetching))
	for u, a := range s.fetching {
		fetching[u] = a
	}
	acquiring := make(map[uint64]gitAcq, len(s.acquiring))
	for n, a := range s.acquiring {
		acquiring[n] = a
	}
	s.mu.Unlock()
	p := PlanDTO{Section: "git", Kind: c.Kind, FileVersion: version, Changes: gitChanges(run, cand), Ends: []PlanWorkDTO{},
		Continues: gitContinues(run, cand, fetching, acquiring), Activates: !config.GitReposEqual(run.Repos, cand.Repos),
		Writes: c.Kind == planSave && !config.GitReposEqual(file.Repos, cand.Repos)}
	if c.Kind == planSave && !config.GitReposEqual(file.Repos, run.Repos) {
		p.Refused = gitDiverged
	}
	p.Runtime = map[string]string{"git": strconv.FormatUint(rev, 10), "work": workVersion(p.Continues)}
	bind, _ := json.Marshal(c)
	seal(&p, string(bind))
	if p.Refused == "" {
		s.cache.put(p.Digest, c)
	}
	return p
}

// editGit plans an explicit action and applies it at once: the action is
// its own confirmation.
func (s *GitService) editGit(e gitEdit) ApplyResultDTO {
	s.writeMu.Lock()
	r, clear := s.editLocked(e)
	s.writeMu.Unlock()
	return s.clearRemoved(r, clear)
}

func (s *GitService) editLocked(e gitEdit) (ApplyResultDTO, string) {
	cfg, version, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		return ApplyResultDTO{Error: "fleet.yaml cannot be read, so nothing was written: " + gitcli.Scrub(err.Error())}, ""
	}
	file := fileGit(cfg)
	c := gitCandidate{Kind: planSave, Edit: &e}
	cand, err := s.candidateLocked(c, file)
	if err != nil {
		return ApplyResultDTO{Error: err.Error()}, ""
	}
	p := s.planLocked(c, cand, version, file)
	if p.Refused != "" {
		return ApplyResultDTO{Error: p.Refused}, ""
	}
	return s.applyLocked(p.Digest, true)
}

// clearRemoved clears a removed repository's cache once its removal has
// applied, by whichever route applied it - the action, or a replacement
// plan applied later (F2b review, R2). Outside the settings lock: a fetch
// in flight finishes first. "Use the file's values" deletes nothing.
func (s *GitService) clearRemoved(r ApplyResultDTO, url string) ApplyResultDTO {
	if !r.OK || url == "" {
		return r
	}
	if store, err := s.gitOn(); err == nil {
		if err := store.Clear(s.ctx, url); err != nil {
			r.Note = "the approval was removed, but its cache could not be cleared: " + gitcli.Scrub(err.Error())
		}
	}
	return r
}

// PlanGitFromFile plans "Use the file's values": the file's block, from
// what runs, writing nothing.
func (s *GitService) PlanGitFromFile() PlanDTO {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cfg, version, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		return gitRefused(planFile, "fleet.yaml cannot be read: "+gitcli.Scrub(err.Error()))
	}
	file := fileGit(cfg)
	return s.planLocked(gitCandidate{Kind: planFile}, cloneGit(file), version, file)
}

// ApplyGit applies the plan with digest: re-planned from what runs,
// refused with the new plan when anything moved, written against the
// plan's file version, then made the running copy.
func (s *GitService) ApplyGit(digest string) ApplyResultDTO {
	s.writeMu.Lock()
	r, clear := s.applyLocked(digest, false)
	s.writeMu.Unlock()
	return s.clearRemoved(r, clear)
}

// applyLocked applies digest's plan under the Applying state: no
// acquisition is admitted from the re-plan, which names those in flight,
// until the new running copy is published. It returns the URL whose cache
// a removal clears once applied.
func (s *GitService) applyLocked(digest string, quiet bool) (ApplyResultDTO, string) {
	v, ok := s.cache.get(digest)
	if !ok {
		return ApplyResultDTO{Error: "This plan expired or was already applied: review the changes again."}, ""
	}
	c := v.(gitCandidate)
	s.mu.Lock()
	s.applying = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.applying = false
		s.mu.Unlock()
	}()
	cfg, version, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		return ApplyResultDTO{Error: "fleet.yaml cannot be read, so nothing was applied: " + gitcli.Scrub(err.Error())}, ""
	}
	file := fileGit(cfg)
	cand, err := s.candidateLocked(c, file)
	if err != nil {
		return ApplyResultDTO{Error: err.Error()}, ""
	}
	re := s.planLocked(c, cand, version, file)
	switch {
	case re.Digest != digest:
		return ApplyResultDTO{Error: "Something changed since this was planned: review the new plan.", Plan: &re}, ""
	case re.Refused != "":
		// unreachable while a refused plan is never cached and its refusal
		// is in its digest; kept so no refused plan can ever apply
		return ApplyResultDTO{Error: re.Refused}, ""
	case quiet && (len(re.Ends) > 0 || re.Interlock != nil):
		return ApplyResultDTO{Plan: &re}, ""
	}
	if re.Writes {
		_, err := s.d.SaveGit(s.d.ConfigPath, version, cand)
		if errors.Is(err, config.ErrFileChanged) {
			return ApplyResultDTO{Error: "fleet.yaml changed while this was applied, so nothing was written: review the new plan.", Plan: s.replanLocked(c)}, ""
		}
		if err != nil {
			return ApplyResultDTO{Error: gitcli.Scrub(err.Error())}, ""
		}
	}
	s.cache.consume(digest)
	if re.Activates {
		s.mu.Lock()
		s.run = cand
		s.runRev++
		s.mu.Unlock()
		s.wakeLine()
	}
	clear := ""
	if c.Kind == planSave && c.Edit != nil && c.Edit.Op == gitRemove {
		clear = c.Edit.URL
	}
	return ApplyResultDTO{OK: true}, clear
}

// replanLocked is c's plan from the file as it is now.
func (s *GitService) replanLocked(c gitCandidate) *PlanDTO {
	cfg, version, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		p := gitRefused(c.Kind, "fleet.yaml cannot be read: "+gitcli.Scrub(err.Error()))
		return &p
	}
	file := fileGit(cfg)
	cand, err := s.candidateLocked(c, file)
	if err != nil {
		p := gitRefused(c.Kind, err.Error())
		return &p
	}
	p := s.planLocked(c, cand, version, file)
	return &p
}

// gitChanges are the effective changes from what runs to cand: each
// approved entry, by its URL - never an ignored entry's text, only how many
// there are.
func gitChanges(from, to config.GitConfig) []PlanChangeDTO {
	out := []PlanChangeDTO{}
	fa, fn := approvedOf(from)
	ta, tn := approvedOf(to)
	byURL := func(rs []config.GitRepoConfig) map[string]config.GitRepoConfig {
		m := make(map[string]config.GitRepoConfig, len(rs))
		for _, r := range rs {
			m[r.URL] = r
		}
		return m
	}
	fm, tm := byURL(fa), byURL(ta)
	or := func(v, empty string) string {
		if v == "" {
			return empty
		}
		return v
	}
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	for _, r := range ta {
		id := "git.repos[" + r.URL + "]"
		f, had := fm[r.URL]
		if !had {
			to := "approved"
			if len(r.Matches) > 0 {
				to += "; also serves " + strings.Join(r.Matches, ", ")
			}
			if r.Web != "" {
				to += "; web link " + r.Web
			}
			out = append(out, PlanChangeDTO{ID: id + ".url", Label: "Repository " + r.URL, From: "not approved", To: to})
			continue
		}
		if f.Web != r.Web {
			out = append(out, PlanChangeDTO{ID: id + ".web", Label: r.URL + ": Open in Git link", From: or(f.Web, "(none)"), To: or(r.Web, "(none)")})
		}
		if !slices.Equal(f.Matches, r.Matches) {
			out = append(out, PlanChangeDTO{ID: id + ".matches", Label: r.URL + ": also serves", From: or(strings.Join(f.Matches, ", "), "(none)"), To: or(strings.Join(r.Matches, ", "), "(none)")})
		}
		if f.RemoteBases != r.RemoteBases {
			out = append(out, PlanChangeDTO{ID: id + ".remoteBases", Label: r.URL + ": remote Kustomize bases", From: onOff(f.RemoteBases), To: onOff(r.RemoteBases)})
		}
	}
	for _, f := range fa {
		if _, ok := tm[f.URL]; !ok {
			out = append(out, PlanChangeDTO{ID: "git.repos[" + f.URL + "].url", Label: "Repository " + f.URL, From: "approved", To: "not approved"})
		}
	}
	if !slices.Equal(fn, tn) {
		out = append(out, PlanChangeDTO{ID: "git.repos.ignored", Label: "Entries Klyx ignores", From: strconv.Itoa(len(fn)), To: strconv.Itoa(len(tn))})
	}
	return out
}

// gitContinues is what a change does not stop: an acquisition in flight
// from a repository it stops approving finishes, and nothing reads that
// history afterwards. A new one is refused from then on.
func gitContinues(from, to config.GitConfig, fetching map[string]gitAcq, acquiring map[uint64]gitAcq) []PlanWorkDTO {
	out := []PlanWorkDTO{}
	ta, _ := approvedOf(to)
	still := map[string]bool{}
	for _, r := range ta {
		still[r.URL] = true
	}
	fa, _ := approvedOf(from)
	stops := map[string]bool{}
	for _, r := range fa {
		if !still[r.URL] {
			stops[r.URL] = true
		}
	}
	const after = " in progress: it finishes, and its history is no longer used"
	for url, a := range fetching {
		if stops[url] {
			out = append(out, PlanWorkDTO{ID: fmt.Sprintf("fetch:%d:%s", a.n, url), Kind: "fetch", Started: a.started.UTC().Format(time.RFC3339), What: "the fetch of " + url + after})
		}
	}
	for n, a := range acquiring {
		if stops[a.url] {
			out = append(out, PlanWorkDTO{ID: fmt.Sprintf("acquire:%d:%s", n, a.url), Kind: "fetch", Started: a.started.UTC().Format(time.RFC3339),
				What: "a fetch from " + a.url + " for " + a.what + after})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
