package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitcache"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/line"
)

// The line with history (The Line slice 6d, parent §3.4 and §3.7, slice 6
// note §6): after each LineDTO, the GitService orders the stations against
// the target T - the cache's tip of the line key's ref - and publishes
// line:git. The frontend shows it only beside the LineDTO version it
// answers, so an ordering never sits on another line's facts.

// LineGitEvent carries the line's Git ordering.
const LineGitEvent = "line:git"

// Ordering states (parent §3.4), per ordered station.
const (
	GitAt        = "at"
	GitBehind    = "behind"
	GitAhead     = "ahead"
	GitDiverged  = "diverged"
	GitUnrelated = "unrelated"
	GitUnknown   = "unknown" // not in fetched history
)

// Why there is no ordering, when there is none.
const (
	LineGitReady      = "ready"
	LineGitNone       = "none"       // no Git key compared
	LineGitChecking   = "checking"   // Git is still starting
	LineGitOff        = "off"        // Git is off, or the cache needs repair
	LineGitUnapproved = "unapproved" // the key's repository is not approved
	LineGitNoTarget   = "notarget"   // the key's ref has no single target
	LineGitUnfetched  = "unfetched"  // no history fetched yet, or the ref is not in it
	LineGitError      = "error"      // a git read failed
)

// Ledger caps (parent §7: 20 commits per view).
const (
	ledgerMax = 20
	onlyMax   = 5
)

// LineGitDTO is the line's ordering against T.
type LineGitDTO struct {
	Version     uint64 `json:"version"`
	LineVersion uint64 `json:"lineVersion"` // the LineDTO it answers
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	Repo        string `json:"repo,omitempty"`    // the approved URL that served the history
	Approve     string `json:"approve,omitempty"` // the key to approve, when unapproved
	// CommitLink opens a commit: "{sha}" stands for its full id; "" when
	// the host is not known and git.repos[].web is not set.
	CommitLink string              `json:"commitLink,omitempty"`
	Target     *LineGitTargetDTO   `json:"target,omitempty"`
	Stations   []LineGitStationDTO `json:"stations"` // ordered stations only; the rest keep their §3.3 state
	Ledger     []LineGitCommitDTO  `json:"ledger"`   // commits not yet everywhere, newest first
	LedgerMore bool                `json:"ledgerMore,omitempty"`
	Only       []LineGitOnlyDTO    `json:"only"` // commits only on a diverged or ahead station
}

// LineGitTargetDTO is T and its freshness: "main at a1b9e3c · fetched 20:31".
type LineGitTargetDTO struct {
	Ref       string     `json:"ref"`  // refs/heads/main, refs/tags/v1, or a commit id
	Name      string     `json:"name"` // main, v1, or the id
	SHA       string     `json:"sha,omitempty"`
	FetchedAt *time.Time `json:"fetchedAt,omitempty"`
	Error     string     `json:"error,omitempty"` // the last fetch's scrubbed error: "history as of <time>; fetch failed: <error>"
}

// LineGitStationDTO is one ordered station.
type LineGitStationDTO struct {
	Cluster   string          `json:"cluster"`
	SHA       string          `json:"sha"`
	State     string          `json:"state"`
	N         int             `json:"n,omitempty"`         // behind or ahead by N
	MergeBase string          `json:"mergeBase,omitempty"` // diverged at
	Held      *LineGitHeldDTO `json:"held,omitempty"`      // behind T, with evidence tied to T
}

// LineGitHeldDTO is "held · N behind" evidence (parent §3.4).
type LineGitHeldDTO struct {
	Kind string `json:"kind"` // source | attempted | dependency
	Text string `json:"text"`
	Unit string `json:"unit"` // the unit the evidence is read from: Kind ns/name
}

// LineGitCommitDTO is one commit. Subject and author are untrusted plain
// text, capped; never sent to an AI provider.
type LineGitCommitDTO struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
	Has     []string  `json:"has"` // the ordered stations whose revision contains it
}

// LineGitOnlyDTO lists commits that exist only on a diverged or ahead
// station: never merged into the line's order.
type LineGitOnlyDTO struct {
	SHA      string             `json:"sha"`
	Clusters []string           `json:"clusters"`
	State    string             `json:"state"`
	Commits  []LineGitCommitDTO `json:"commits"`
	More     int                `json:"more,omitempty"`
}

// LineGit returns the current ordering, for bootstrapping (subscribe to
// line:git, then read).
func (s *GitService) LineGit() LineGitDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lineGit
}

// GitLineSink is LineService's hook into the GitService: every LineDTO and
// the clusters' Git sources it was built from. A package-level accessor, so
// it is not bound to the webview. It never blocks the line's worker.
func GitLineSink(s *GitService) func(LineDTO, []LineGitSource) {
	return func(dto LineDTO, srcs []LineGitSource) {
		s.mu.Lock()
		s.line, s.sources = dto, srcs
		s.mu.Unlock()
		s.wakeLine()
	}
}

func (s *GitService) wakeLine() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// lineLoop recomputes the ordering whenever the line, the caches or the
// approvals change, and publishes it.
func (s *GitService) lineLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
		s.mu.Lock()
		dto := s.line
		s.mu.Unlock()
		out := s.computeLine(s.ctx, dto)
		s.mu.Lock()
		s.lineVer++
		out.Version = s.lineVer
		s.lineGit = out
		s.lineGitKey = nil
		if dto.Key != nil {
			k := *dto.Key
			s.lineGitKey = &k
		}
		s.mu.Unlock()
		if s.d.Emitter != nil {
			s.d.Emitter.Emit(LineGitEvent, out)
		}
	}
}

// computeLine orders the line against T, reusing the last ordering when
// nothing it reads has changed.
func (s *GitService) computeLine(ctx context.Context, dto LineDTO) LineGitDTO {
	out := LineGitDTO{LineVersion: dto.Version, Stations: []LineGitStationDTO{}, Ledger: []LineGitCommitDTO{}, Only: []LineGitOnlyDTO{}}
	k := dto.Key
	if k == nil {
		out.State, out.Reason = LineGitNone, "nothing is compared"
		return out
	}
	if k.Kind != line.KeyGit {
		out.State, out.Reason = LineGitNone, "the line compares charts, which have no Git history"
		return out
	}
	store, err := s.gitOn()
	if err != nil {
		out.State, out.Reason = LineGitOff, err.Error()
		if strings.Contains(err.Error(), "starting") {
			out.State = LineGitChecking
		}
		return out
	}
	repos, _ := s.approved()
	owner, _ := keyOwners(repos)
	url := owner[k.Repo]
	if url == "" {
		out.State, out.Reason = LineGitUnapproved, "the line's repository is not approved in Settings > Git"
		if config.ValidRepoKey(k.Repo) == nil {
			out.Approve = k.Repo
		}
		return out
	}
	out.Repo = url
	i := slices.IndexFunc(repos, func(r config.GitRepoConfig) bool { return r.URL == url })
	out.CommitLink = commitLink(k.Repo, repos[i].Web)
	ref, err := gitcli.RefFromKey(k.RefKind, k.RefName)
	if err != nil {
		out.State, out.Reason = LineGitNoTarget, err.Error()
		return out
	}
	st := store.Status(url)
	if st.NeedsRepair {
		out.State, out.Reason = LineGitOff, "the Git cache needs repair: clear it in Settings > Git"
		return out
	}
	out.Target = &LineGitTargetDTO{Ref: ref.String(), Name: k.RefName, FetchedAt: timePtr(st.FetchedAt), Error: st.Error}
	if st.FetchedAt.IsZero() {
		out.State, out.Reason = LineGitUnfetched, "no history fetched yet"
		if st.Error != "" {
			out.Reason = "fetch failed: " + st.Error
		}
		return out
	}
	// The same inputs give the same ordering: reuse it for this LineDTO.
	fp := lineFingerprint(dto, url, out.CommitLink, st)
	s.mu.Lock()
	last, lastFP := s.lineGit, s.lineFP
	s.mu.Unlock()
	if fp == lastFP && last.State == LineGitReady {
		last.LineVersion = dto.Version
		return last
	}
	err = store.Read(url, func(repo gitcli.Repo) error { return orderLine(ctx, repo, ref, dto, &out) })
	switch {
	case errors.Is(err, errRefNotFetched):
		out.State, out.Reason = LineGitUnfetched, fmt.Sprintf("%s is not in the fetched history yet", k.RefName)
	case err != nil:
		out.State, out.Reason = LineGitError, gitcli.Scrub(err.Error())
	default:
		out.State = LineGitReady
		s.mu.Lock()
		s.lineFP = fp
		s.mu.Unlock()
	}
	return out
}

// lineFingerprint is everything an ordering reads: the key, each station's
// revision, state and held evidence, the repository and the cache's fetch.
func lineFingerprint(dto LineDTO, url, link string, st gitcache.Status) string {
	type unit struct {
		Attempted, BlockedBy  string
		Failing, NotReady     bool
		Namespace, Name, Kind string
	}
	type station struct {
		Cluster, Single, State string
		Units                  []unit
	}
	var ss []station
	for _, s := range append(append([]LineStationDTO{}, dto.Stations...), dto.Siding...) {
		x := station{Cluster: s.Cluster, Single: s.Single, State: s.State}
		for _, u := range s.Units {
			x.Units = append(x.Units, unit{u.Attempted, u.BlockedBy, u.Failing, u.SourceNotReady, u.Namespace, u.Name, u.Origin})
		}
		ss = append(ss, x)
	}
	b, _ := json.Marshal(struct {
		Key      *LineKeyDTO
		URL      string
		Link     string
		Status   gitcache.Status
		Stations []station
	}{dto.Key, url, link, st, ss})
	return string(b)
}

var errRefNotFetched = errors.New("the tracked ref is not in the cache")

// orderLine classifies each station with a single applied revision against
// T, finds held evidence for the stations behind it, and builds the Ledger.
func orderLine(ctx context.Context, repo gitcli.Repo, ref gitcli.Ref, dto LineDTO, out *LineGitDTO) error {
	refs, err := repo.Refs(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(refs, func(v gitcli.RefValue) bool { return v.Name == ref.Published() })
	if i < 0 {
		return errRefNotFetched
	}
	t := refs[i].Commit()
	out.Target.SHA = t
	tRev, err := gitcli.SHA(t)
	if err != nil {
		return err
	}
	type result struct {
		state string
		n     int
		mb    string
	}
	memo := map[string]result{}
	var shas []string                 // distinct, in station order
	state := map[string]string{}      // sha -> its ordering
	clusters := map[string][]string{} // sha -> the stations that run it
	for _, s := range append(append([]LineStationDTO{}, dto.Stations...), dto.Siding...) {
		// Ordered only from state 5: one known applied revision.
		if (s.State != "matches" && s.State != "differs") || !gitcli.IsSHA(s.Single) {
			continue
		}
		r, ok := memo[s.Single]
		if !ok {
			st, n, mb, err := classify(ctx, repo, s.Single, t, tRev)
			if err != nil {
				return err
			}
			r = result{st, n, mb}
			memo[s.Single], state[s.Single] = r, st
			shas = append(shas, s.Single)
		}
		sd := LineGitStationDTO{Cluster: s.Cluster, SHA: s.Single, State: r.state, N: r.n, MergeBase: r.mb}
		if r.state == GitBehind {
			sd.Held = heldEvidence(s, t)
		}
		out.Stations = append(out.Stations, sd)
		clusters[s.Single] = append(clusters[s.Single], s.Cluster)
	}
	return ledger(ctx, repo, tRev, shas, state, clusters, out)
}

// classify orders one revision against T (parent §3.4's table).
func classify(ctx context.Context, repo gitcli.Repo, sha, t string, tRev gitcli.Rev) (string, int, string, error) {
	if sha == t {
		return GitAt, 0, "", nil
	}
	missing, err := repo.Missing(ctx, []string{sha})
	if err != nil {
		return "", 0, "", err
	}
	if len(missing) > 0 {
		return GitUnknown, 0, "", nil
	}
	r, err := gitcli.SHA(sha)
	if err != nil {
		return "", 0, "", err
	}
	if behind, err := repo.IsAncestor(ctx, r, tRev); err != nil {
		return "", 0, "", err
	} else if behind {
		n, err := repo.Count(ctx, []gitcli.Rev{tRev}, []gitcli.Rev{r})
		return GitBehind, n, "", err
	}
	if ahead, err := repo.IsAncestor(ctx, tRev, r); err != nil {
		return "", 0, "", err
	} else if ahead {
		n, err := repo.Count(ctx, []gitcli.Rev{r}, []gitcli.Rev{tRev})
		return GitAhead, n, "", err
	}
	mb, ok, err := repo.MergeBase(ctx, r, tRev)
	if err != nil {
		return "", 0, "", err
	}
	if !ok {
		return GitUnrelated, 0, "", nil
	}
	return GitDiverged, 0, mb, nil
}

// heldEvidence is "held · N behind" evidence tied to T, in the parent's
// order: the source is not ready; a unit attempted T and is failing; a unit
// reports DependencyNotReady (the controller's own report).
func heldEvidence(s LineStationDTO, t string) *LineGitHeldDTO {
	name := func(u LineUnitDTO) string { return u.Origin + " " + u.Namespace + "/" + u.Name }
	for _, u := range s.Units {
		if u.SourceNotReady {
			return &LineGitHeldDTO{Kind: "source", Text: "its GitRepository is not ready", Unit: name(u)}
		}
	}
	for _, u := range s.Units {
		if u.Failing && u.Attempted == t {
			return &LineGitHeldDTO{Kind: "attempted", Text: "attempted " + t[:7] + ", failing", Unit: name(u)}
		}
	}
	for _, u := range s.Units {
		if u.BlockedBy != "" {
			return &LineGitHeldDTO{Kind: "dependency", Text: "blocked by dependency " + u.BlockedBy, Unit: name(u)}
		}
	}
	return nil
}

// ledger builds "commits not yet everywhere": the union of each ordered
// station's missing set (rev-list T ^sha), newest first by commit time,
// capped; each with the ordered stations that contain it. Commits only on a
// diverged or ahead station are listed under that station.
//
// Containment is exact (design review of cbef8e4): a station's list of its
// newest missing commits is complete when its missing count fits in it, and
// then absence from the list proves the station has the commit; otherwise
// each displayed commit absent from the list is checked with
// merge-base --is-ancestor. An at-target or ahead station contains every
// commit of T's.
func ledger(ctx context.Context, repo gitcli.Repo, tRev gitcli.Rev, shas []string, state map[string]string, clusters map[string][]string, out *LineGitDTO) error {
	missing := map[string]map[string]bool{} // station sha -> its newest missing commits
	complete := map[string]bool{}           // station sha -> that list is its whole missing set
	union := map[string]gitcli.Commit{}
	for _, sha := range shas {
		if state[sha] != GitBehind && state[sha] != GitDiverged {
			continue
		}
		r, _ := gitcli.SHA(sha)
		cs, err := repo.Log(ctx, []gitcli.Rev{tRev}, []gitcli.Rev{r}, ledgerMax)
		if err != nil {
			return err
		}
		n, err := repo.Count(ctx, []gitcli.Rev{tRev}, []gitcli.Rev{r})
		if err != nil {
			return err
		}
		missing[sha], complete[sha] = map[string]bool{}, n <= len(cs)
		for _, c := range cs {
			missing[sha][c.SHA] = true
			union[c.SHA] = c
		}
	}
	all := make([]gitcli.Commit, 0, len(union))
	for _, c := range union {
		all = append(all, c)
	}
	slices.SortFunc(all, func(a, b gitcli.Commit) int {
		if c := b.Committed.Compare(a.Committed); c != 0 {
			return c
		}
		return strings.Compare(a.SHA, b.SHA)
	})
	if len(all) > ledgerMax {
		all, out.LedgerMore = all[:ledgerMax], true
	}
	for _, ok := range complete {
		out.LedgerMore = out.LedgerMore || !ok // a station misses more than its list holds
	}
	for _, c := range all {
		d := commitDTO(c)
		cRev, _ := gitcli.SHA(c.SHA)
		for _, sha := range shas {
			has := false
			switch st := state[sha]; {
			case st == GitAt || st == GitAhead:
				has = true
			case st != GitBehind && st != GitDiverged, missing[sha][c.SHA]:
			case complete[sha]:
				has = true
			default: // beyond the station's list: ask git
				r, _ := gitcli.SHA(sha)
				anc, err := repo.IsAncestor(ctx, cRev, r)
				if err != nil {
					return err
				}
				has = anc
			}
			if has {
				d.Has = append(d.Has, clusters[sha]...)
			}
		}
		out.Ledger = append(out.Ledger, d)
	}
	for _, sha := range shas {
		if state[sha] != GitDiverged && state[sha] != GitAhead {
			continue
		}
		r, _ := gitcli.SHA(sha)
		cs, err := repo.Log(ctx, []gitcli.Rev{r}, []gitcli.Rev{tRev}, onlyMax)
		if err != nil {
			return err
		}
		n, err := repo.Count(ctx, []gitcli.Rev{r}, []gitcli.Rev{tRev})
		if err != nil {
			return err
		}
		o := LineGitOnlyDTO{SHA: sha, Clusters: clusters[sha], State: state[sha], Commits: []LineGitCommitDTO{}, More: max(0, n-len(cs))}
		for _, c := range cs {
			d := commitDTO(c)
			d.Has = clusters[sha]
			o.Commits = append(o.Commits, d)
		}
		out.Only = append(out.Only, o)
	}
	return nil
}

func commitDTO(c gitcli.Commit) LineGitCommitDTO {
	return LineGitCommitDTO{SHA: c.SHA, Subject: c.Subject, Author: c.Author, Time: c.Time, Has: []string{}}
}

// commitLink is "Open in Git" for a repository key: the owner's web link
// ("{sha}" placed where the id goes, else "/commit/{sha}" appended), or a
// known host's own commit page.
func commitLink(key, web string) string {
	if web != "" {
		if strings.Contains(web, "{sha}") {
			return web
		}
		return strings.TrimRight(web, "/") + "/commit/{sha}"
	}
	host, path, _ := strings.Cut(key, "/")
	switch host {
	case "github.com":
		return "https://github.com/" + path + "/commit/{sha}"
	case "gitlab.com":
		return "https://gitlab.com/" + path + "/-/commit/{sha}"
	case "bitbucket.org":
		return "https://bitbucket.org/" + path + "/commits/{sha}"
	case "dev.azure.com":
		return "https://dev.azure.com/" + path + "/commit/{sha}"
	case "ssh.dev.azure.com":
		if p := strings.Split(path, "/"); len(p) == 4 && p[0] == "v3" {
			return "https://dev.azure.com/" + p[1] + "/" + p[2] + "/_git/" + p[3] + "/commit/{sha}"
		}
	}
	return ""
}
