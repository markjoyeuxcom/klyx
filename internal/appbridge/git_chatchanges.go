package appbridge

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	ssautil "github.com/fluxcd/pkg/ssa/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/procrun"
	"github.com/moomora/klyx/internal/readfence"
)

// AI A1 §3.2: the Git half of git_changes. The unit's GitRepository URL maps
// to an approved repository as the cache diff maps it; both revisions are
// ensured in the history cache; the commits and changed files between them
// are listed under spec.path; and each revision is built with the cache
// diff's confined build under today's Kustomization spec. A fleet-wide
// admission takes one comparison at a time: its two revisions build one
// after the other, and the comparison runs before the admission is
// released, so at most one render is building and two are held fleet-wide
// (A1 §1; the §8 load test found that admitting renders one by one let
// each waiting investigation hold its first render). Nothing here reaches
// a model: the tool projects what it returns.

const (
	chatChangesCommitsMax = gitcli.MaxLogCommits // 20
	chatChangesFilesMax   = 200
)

// chatRenderAdmission admits one investigation comparison at a time,
// fleet-wide; the rest wait within their turn's deadline.
var chatRenderAdmission = make(chan struct{}, 1)

// chatBuilding counts the confined builds running for investigations (the
// §8 load test reads it).
var chatBuilding atomic.Int32

// admitComparison waits for the comparison admission within ctx.
func admitComparison(ctx context.Context) (release func(), err error) {
	select {
	case chatRenderAdmission <- struct{}{}:
		return func() { <-chatRenderAdmission }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var errChatGitNotApproved = errors.New("the source's repository is not approved in Settings > Git, so Klyx reads no history for it")

// gitChangesReq is what git_changes asks of Git, read in process from the
// live Kustomization and its GitRepository.
type gitChangesReq struct {
	RepoURL            string
	Applied, Attempted string // full commit SHAs
	Spec               map[string]any
	// Compare, when set, runs with both renders inside the admission, and
	// the renders are not returned: what the comparison keeps is all that
	// outlives it
	Compare func(old, new []*unstructured.Unstructured)
}

type gitChangesOut struct {
	Commits      []gitcli.Commit
	CommitsTotal int
	Ancestor     bool // the applied revision is an ancestor of the attempted one
	Files        []gitcli.NameStatus
	FilesTotal   int
	Old, New     []*unstructured.Unstructured
	Generated    bool   // a revision had no kustomization file at the path
	RenderErr    string // the renders were not compared: why
}

// chatGit is what git_changes asks of Git.
type chatGit interface {
	changes(ctx context.Context, req gitChangesReq) (gitChangesOut, error)
}

// SetChatGit gives chat its read of Git history (AI A1 §3.2).
func SetChatGit(c *ChatService, g *GitService) {
	if g != nil {
		c.git = g
	}
}

func (s *GitService) changes(ctx context.Context, req gitChangesReq) (gitChangesOut, error) {
	var out gitChangesOut
	repos, _ := s.approved()
	owner, _ := keyOwners(repos)
	url := owner[line.NormalizeRepo(req.RepoURL)]
	if url == "" {
		return out, errChatGitNotApproved
	}
	store, err := s.gitOn()
	if err != nil {
		return out, err
	}
	cs, ok := store.(cacheStore)
	if !ok {
		return out, errors.New("the Git cache cannot fetch commits")
	}
	// each fetch is admitted when it starts (F2b review, R1)
	cs = admittedStore{cacheStore: cs, s: s, what: "chat's Git changes"}
	applied, err := gitcli.SHA(req.Applied)
	if err != nil {
		return out, err
	}
	attempted, err := gitcli.SHA(req.Attempted)
	if err != nil {
		return out, err
	}
	// the read fence before each step: a fetch, the history reads, each
	// render's inputs and its build (A1 implementation review, finding 3)
	for _, sha := range []string{req.Applied, req.Attempted} {
		if err := readfence.Check(ctx); err != nil {
			return out, err
		}
		if err := cs.EnsureCommit(ctx, url, sha); err != nil {
			return out, fmt.Errorf("fetching %s: %w", sha[:7], err)
		}
	}
	specPath, _ := req.Spec["path"].(string)
	clean, err := gitcli.CleanPath(specPath)
	if err != nil {
		return out, err
	}
	var paths []string
	if clean != "" {
		paths = []string{clean}
	}
	err = cs.Read(url, func(repo gitcli.Repo) error {
		var err error
		inc, exc := []gitcli.Rev{attempted}, []gitcli.Rev{applied}
		if err := readfence.Check(ctx); err != nil {
			return err
		}
		if out.Commits, err = repo.LogPath(ctx, inc, exc, chatChangesCommitsMax, paths); err != nil {
			return err
		}
		if err := readfence.Check(ctx); err != nil {
			return err
		}
		if out.CommitsTotal, err = repo.CountPath(ctx, inc, exc, paths); err != nil {
			return err
		}
		if err := readfence.Check(ctx); err != nil {
			return err
		}
		if out.Ancestor, err = repo.IsAncestor(ctx, applied, attempted); err != nil {
			return err
		}
		if err := readfence.Check(ctx); err != nil {
			return err
		}
		files, err := repo.DiffNames(ctx, applied, attempted, paths)
		if err != nil {
			return err
		}
		out.FilesTotal = len(files)
		if len(files) > chatChangesFilesMax {
			files = files[:chatChangesFilesMax]
		}
		out.Files = files
		return nil
	})
	if err != nil {
		return out, err
	}
	release, err := admitComparison(ctx)
	if err != nil {
		return out, err
	}
	defer release()
	// one render after the other, in this read worker: a render is a read
	// of the Git cache, and §1 counts each read's own fan-out
	var renders [2][]*unstructured.Unstructured
	for i, rev := range []gitcli.Rev{applied, attempted} {
		objs, generated, err := s.chatRender(ctx, cs, url, rev, specPath, req.Spec)
		var failed renderFailure
		switch {
		case err != nil && ctx.Err() != nil:
			return out, ctx.Err()
		case errors.As(err, &failed):
			out.RenderErr = fmt.Sprintf("the render of %s: %s", []string{req.Applied, req.Attempted}[i][:7], failed.class)
			return out, nil
		case err != nil:
			return out, err // the read fence, never a render's failure
		}
		renders[i] = objs
		out.Generated = out.Generated || generated
	}
	if req.Compare != nil {
		req.Compare(renders[0], renders[1])
		return out, nil
	}
	out.Old, out.New = renders[0], renders[1]
	return out, nil
}

// chatRender builds one revision as the cache diff does: the input set, its
// blobs, the confined build with the spec's overlay, then commonMetadata as
// the controller applies it.
func (s *GitService) chatRender(ctx context.Context, cs cacheStore, url string, rev gitcli.Rev, specPath string, spec map[string]any) ([]*unstructured.Unstructured, bool, error) {
	if err := readfence.Check(ctx); err != nil {
		return nil, false, err
	}
	source := cachediff.CacheSource{Store: cs, URL: url, Rev: rev}
	set, err := cachediff.Compute(ctx, source, specPath)
	if readfence.Failed(err) {
		return nil, false, err // the fence, never the render's failure
	}
	if err != nil {
		return nil, false, renderFailure{inputFailure(err), err}
	}
	var entries []gitcli.TreeEntry
	for _, e := range set.Files {
		entries = append(entries, e)
	}
	if err := readfence.Check(ctx); err != nil {
		return nil, false, err
	}
	contents, err := source.Read(ctx, entries)
	if readfence.Failed(err) {
		return nil, false, err
	}
	if err != nil {
		return nil, false, renderFailure{"the revision's files could not be read from the Git cache", err}
	}
	exe, err := childExe()
	if err != nil {
		return nil, false, renderFailure{"the confined build is not available", err}
	}
	if err := readfence.Check(ctx); err != nil {
		return nil, false, err
	}
	chatBuilding.Add(1)
	objs, err := cachediff.Build(ctx, exe, set, contents, spec)
	chatBuilding.Add(-1)
	if err != nil {
		return nil, false, renderFailure{buildFailure(err), err}
	}
	labels, _, _ := unstructured.NestedStringMap(spec, "commonMetadata", "labels")
	annotations, _, _ := unstructured.NestedStringMap(spec, "commonMetadata", "annotations")
	if len(labels) > 0 || len(annotations) > 0 {
		ssautil.SetCommonMetadata(objs, labels, annotations)
	}
	return objs, set.Generated, nil
}

// renderFailure is a render that was not established, told in fixed words:
// the stage and the failure's class, never the scanner's, reader's or
// builder's text. A kustomize error can quote a patch, Helm values
// included, and no scrubber recognises an ordinary value (A1
// implementation review, P1). The error itself stays for local use.
type renderFailure struct {
	class string
	err   error
}

func (f renderFailure) Error() string { return f.class }
func (f renderFailure) Unwrap() error { return f.err }

func inputFailure(err error) string {
	for _, c := range []error{cachediff.ErrInputMissing, cachediff.ErrInputUnsupported, cachediff.ErrInputUnreadable, cachediff.ErrInputBound} {
		if errors.Is(err, c) {
			return c.Error()
		}
	}
	return "the build's inputs could not be read from the Git cache"
}

func buildFailure(err error) string {
	if errors.Is(err, procrun.ErrUnsupported) {
		return "the confined build is not supported on this platform"
	}
	for _, c := range []error{cachediff.ErrBuildStart, cachediff.ErrBuildDeadline, cachediff.ErrBuildOutput, cachediff.ErrBuildDecode} {
		if errors.Is(err, c) {
			return c.Error()
		}
	}
	if errors.Is(err, cachediff.ErrBuildFailed) {
		return "kustomize reported an error; its text is not sent, since it can quote the repository's values"
	}
	return "the confined build failed"
}
