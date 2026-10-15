package appbridge

import (
	"context"
	"fmt"

	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/gitops/depgraph"
	"github.com/moomora/klyx/internal/line"
)

// Git's "not here yet" for the dependency graph (The Line slice 8b-2, slice
// 8 note §3): for a node behind T, the commits between its applied sha and
// T that touch its spec.path, and the files they change (name-status, trees
// only). T is always the GitService's own; the node's facts only say which
// repository, ref, sha and path the cluster states, and each is checked.

// NotHereYetNodeDTO is one node's facts, as the graph returned them.
type NotHereYetNodeDTO struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	RefKind string `json:"refKind"`
	RefName string `json:"refName"`
	Applied string `json:"applied"`
	Path    string `json:"path"`
}

type NameStatusDTO struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// NotHereYetResultDTO: "behind" with the commits and files; "at" T (nothing
// is not here yet); or "unavailable" with the reason.
type NotHereYetResultDTO struct {
	ID      string             `json:"id"`
	State   string             `json:"state"` // behind | at | unavailable
	Reason  string             `json:"reason,omitempty"`
	Commits []LineGitCommitDTO `json:"commits"`
	More    bool               `json:"more"` // more commits touch the path than are listed
	Files   []NameStatusDTO    `json:"files"`
}

// NotHereYetDTO carries T with its fetch freshness.
type NotHereYetDTO struct {
	Target  *LineGitTargetDTO     `json:"target,omitempty"`
	Results []NotHereYetResultDTO `json:"results"`
	Error   string                `json:"error,omitempty"`
}

const (
	NotHereBehind      = "behind"
	NotHereAt          = "at"
	NotHereUnavailable = "unavailable"
)

// NotHereYet answers for at most the graph's node count; cancelling the
// call cancels the Git reads.
func (s *GitService) NotHereYet(ctx context.Context, nodes []NotHereYetNodeDTO) NotHereYetDTO {
	out := NotHereYetDTO{Results: make([]NotHereYetResultDTO, 0, len(nodes))}
	if len(nodes) > depgraph.MaxNodes {
		out.Error = fmt.Sprintf("at most %d nodes per call", depgraph.MaxNodes)
		return out
	}
	res := make([]NotHereYetResultDTO, len(nodes))
	for i, n := range nodes {
		res[i] = NotHereYetResultDTO{ID: n.ID, State: NotHereUnavailable, Commits: []LineGitCommitDTO{}, Files: []NameStatusDTO{}}
	}
	all := func(reason string) NotHereYetDTO {
		for i := range res {
			res[i].Reason = reason
		}
		out.Results = res
		return out
	}
	s.mu.Lock()
	lg, key := s.lineGit, s.lineGitKey
	s.mu.Unlock()
	if lg.State != LineGitReady || key == nil || lg.Target == nil || !gitcli.IsSHA(lg.Target.SHA) {
		reason := "the line has no T"
		if lg.Reason != "" {
			reason += ": " + lg.Reason
		}
		return all(reason)
	}
	t := *lg.Target
	out.Target = &t
	store, err := s.gitOn()
	if err != nil {
		return all(err.Error())
	}
	cfg, err := s.d.Load(s.d.ConfigPath)
	if err != nil {
		return all(gitcli.Scrub(err.Error()))
	}
	repos, _ := cfg.ApprovedGitRepos()
	owner, _ := keyOwners(repos)
	url := owner[key.Repo]
	if url == "" || url != lg.Repo {
		return all("the line's repository changed since T was read")
	}
	tKey := line.Key{Kind: line.KeyGit, Repo: key.Repo, RefKind: key.RefKind, RefName: key.RefName}
	type work struct {
		i       int
		applied gitcli.Rev
		path    string
	}
	var todo []work
	for i, n := range nodes {
		nk := line.OverrideKey(n.URL, n.RefKind, n.RefName)
		path, perr := gitcli.CleanPath(n.Path)
		a, aerr := gitcli.SHA(n.Applied)
		switch {
		case aerr != nil || !gitcli.IsSHA(n.Applied):
			res[i].Reason = "the applied revision is not a full commit id"
		case perr != nil:
			res[i].Reason = "the path is not a clean relative path"
		case owner[nk.Repo] == "":
			res[i].Reason = "its repository is not approved in Settings > Git"
		case owner[nk.Repo] != url || nk.RefKind != tKey.RefKind || nk.RefName != tKey.RefName:
			res[i].Reason = fmt.Sprintf("it tracks %s %s, not T's %s", nk.RefKind, nk.RefName, tKey.String())
		case n.Applied == t.SHA:
			res[i].State = NotHereAt
		default:
			todo = append(todo, work{i, a, path})
		}
	}
	if len(todo) > 0 {
		tRev, err := gitcli.SHA(t.SHA)
		if err != nil {
			return all(err.Error())
		}
		err = store.Read(url, func(repo gitcli.Repo) error {
			for _, w := range todo {
				if err := notHereYet(ctx, repo, w.applied, tRev, w.path, &res[w.i]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			for _, w := range todo {
				if res[w.i].State != NotHereBehind {
					res[w.i].Reason = gitcli.Scrub(err.Error())
				}
			}
		}
	}
	out.Results = res
	return out
}

// notHereYet proves applied is behind T before listing anything: ahead,
// diverged, unrelated or missing history are said, never "not here yet".
func notHereYet(ctx context.Context, repo gitcli.Repo, applied, t gitcli.Rev, path string, r *NotHereYetResultDTO) error {
	missing, err := repo.Missing(ctx, []string{applied.String()})
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		r.Reason = "its applied revision is not in the fetched history"
		return nil
	}
	behind, err := repo.IsAncestor(ctx, applied, t)
	if err != nil {
		return err
	}
	if !behind {
		ahead, err := repo.IsAncestor(ctx, t, applied)
		if err != nil {
			return err
		}
		if ahead {
			r.Reason = "its applied revision is ahead of T"
			return nil
		}
		if _, ok, err := repo.MergeBase(ctx, applied, t); err != nil {
			return err
		} else if ok {
			r.Reason = "its applied revision has diverged from T"
		} else {
			r.Reason = "its applied revision is unrelated to T"
		}
		return nil
	}
	cs, err := repo.LogPath(ctx, []gitcli.Rev{t}, []gitcli.Rev{applied}, gitcli.MaxLogCommits, []string{path})
	if err != nil {
		return err
	}
	files, err := repo.DiffNames(ctx, applied, t, []string{path})
	if err != nil {
		return err
	}
	if len(cs) == gitcli.MaxLogCommits {
		n, err := repo.CountPath(ctx, []gitcli.Rev{t}, []gitcli.Rev{applied}, []string{path})
		if err != nil {
			return err
		}
		r.More = n > len(cs)
	}
	r.State, r.Reason = NotHereBehind, ""
	for _, c := range cs {
		r.Commits = append(r.Commits, commitDTO(c))
	}
	for _, f := range files {
		r.Files = append(r.Files, NameStatusDTO{Status: f.Status, Path: f.Path})
	}
	return nil
}
