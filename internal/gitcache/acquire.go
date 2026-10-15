package gitcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/moomora/klyx/internal/gitcli"
)

// ErrNoPartialClone: a history refresh brought blobs - the server ignored
// the filter (parent §7: refused, never published).
var ErrNoPartialClone = errors.New("this server does not support partial clone")

// RefreshHistory fetches commits and trees for the tracked refs from an
// approved URL into a staging repo, validates them and publishes them to
// the cache. On any failure the last good history stays; the attempt is
// recorded in the cache's Status either way.
func (s *Store) RefreshHistory(ctx context.Context, url string, refs []gitcli.Ref) error {
	if err := gitcli.ValidateApprovedURL(url); err != nil {
		return err
	}
	if len(refs) == 0 {
		return fmt.Errorf("no tracked ref to fetch")
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	dir := s.cacheDir(url)
	err := s.refreshHistory(ctx, url, dir, refs)
	if _, statErr := os.Stat(filepath.Join(dir, "config")); statErr == nil {
		st, now := s.Status(url), time.Now().UTC()
		st.AttemptedAt, st.Error = now, ""
		if err == nil {
			st.FetchedAt = now
		} else {
			st.Error = gitcli.Scrub(err.Error())
		}
		s.writeStatus(dir, st)
	}
	return err
}

func (s *Store) refreshHistory(ctx context.Context, url, dir string, refs []gitcli.Ref) error {
	if err := s.prepare(ctx, url, dir, true); err != nil {
		return err
	}
	room, err := s.reserve(dir, false)
	if err != nil {
		return err
	}
	stg, err := s.stage(ctx, url, dir)
	if err != nil {
		return err
	}
	defer s.unstage(stg)
	staging := s.run.Repo(stg)
	if err := s.fetch(ctx, stg, room, func(ctx context.Context) error { return staging.FetchHistory(ctx, refs) }); err != nil {
		return err
	}
	packs, err := s.validate(ctx, stg, room, func(o gitcli.PackObjects) error {
		if o.Blobs > 0 {
			return ErrNoPartialClone
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The refs resolve: every tracked ref landed in staging, a pinned
	// commit at exactly its id.
	got, err := staging.Refs(ctx)
	if err != nil {
		return err
	}
	fetched := map[string]string{}
	for _, v := range got {
		fetched[v.Name] = v.Value
	}
	news := map[string]string{} // published name -> new value
	for _, r := range refs {
		v, ok := fetched[r.Staging()]
		if !ok {
			return fmt.Errorf("the fetch did not bring %s", r)
		}
		if gitcli.IsSHA(r.String()) && v != r.String() {
			return fmt.Errorf("the fetch brought %s for the pinned commit %s", v, r)
		}
		news[r.Published()] = v
	}
	return s.publish(ctx, dir, kindHistory, packs, func(old map[string]string) []gitcli.RefUpdate {
		var us []gitcli.RefUpdate
		for name, v := range news {
			if old[name] != v {
				us = append(us, gitcli.RefUpdate{Name: name, New: v, Old: old[name]})
			}
		}
		slices.SortFunc(us, func(a, b gitcli.RefUpdate) int { return strings.Compare(a.Name, b.Name) })
		return us
	})
}

// EnsureCommit makes sure the cache holds one commit, a validated full id:
// when it does not, exactly that commit (a commit ref) is fetched through the
// acquisition contract (slice 6 note §7.1 point 3, the cache diff's
// revision). It never touches the history status the line reads.
func (s *Store) EnsureCommit(ctx context.Context, url, sha string) error {
	ref, err := gitcli.RefFromKey("commit", sha)
	if err != nil {
		return err
	}
	if err := gitcli.ValidateApprovedURL(url); err != nil {
		return err
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	dir := s.cacheDir(url)
	if err := s.prepare(ctx, url, dir, true); err != nil {
		return err
	}
	if missing, err := s.run.Repo(dir).Missing(ctx, []string{sha}); err != nil || len(missing) == 0 {
		return err
	}
	return s.refreshHistory(ctx, url, dir, []gitcli.Ref{ref})
}

// AcquireIDs acquires exactly these blob ids - the cache diff's input set,
// listed from trees by its caller.
func (s *Store) AcquireIDs(ctx context.Context, url string, ids []string) error {
	return s.acquireContent(ctx, url, func(gitcli.Repo) ([]string, error) { return ids, nil })
}

// AcquireSnapshot acquires every blob under paths at rev, listed from trees
// only, so the cache diff's archive can export them.
func (s *Store) AcquireSnapshot(ctx context.Context, url string, rev gitcli.Rev, paths []string) error {
	return s.acquireContent(ctx, url, func(p gitcli.Repo) ([]string, error) {
		es, err := p.LsTree(ctx, rev, paths)
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, e := range es {
			if e.Type == "blob" {
				ids = append(ids, e.ID)
			}
		}
		return ids, nil
	})
}

// AcquireDiff acquires the old and new blob of each path that changed
// between a and b under paths - nothing older.
func (s *Store) AcquireDiff(ctx context.Context, url string, a, b gitcli.Rev, paths []string) error {
	return s.acquireContent(ctx, url, func(p gitcli.Repo) ([]string, error) {
		cs, err := p.DiffTree(ctx, a, b, paths)
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, c := range cs {
			if c.OldMode != gitlinkMode && !isZero(c.OldID) {
				ids = append(ids, c.OldID)
			}
			if c.NewMode != gitlinkMode && !isZero(c.NewID) {
				ids = append(ids, c.NewID)
			}
		}
		return ids, nil
	})
}

const gitlinkMode = "160000"

func isZero(id string) bool {
	for _, c := range id {
		if c != '0' {
			return false
		}
	}
	return true
}

// ErrContentIncomplete: content still missing after its acquisition.
var ErrContentIncomplete = errors.New("content incomplete")

func (s *Store) acquireContent(ctx context.Context, url string, list func(gitcli.Repo) ([]string, error)) error {
	if err := gitcli.ValidateApprovedURL(url); err != nil {
		return err
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	dir := s.cacheDir(url)
	if err := s.prepare(ctx, url, dir, false); err != nil {
		return err
	}
	cache := s.run.Repo(dir)
	ids, err := list(cache)
	if err != nil {
		return err
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > gitcli.MaxContentIDs {
		return fmt.Errorf("%d files is over the %d limit", len(ids), gitcli.MaxContentIDs)
	}
	if len(ids) == 0 {
		return nil
	}
	missing, err := cache.Missing(ctx, ids)
	if err != nil || len(missing) == 0 {
		return err
	}
	room, err := s.reserve(dir, true)
	if err != nil {
		return err
	}
	stg, err := s.stage(ctx, url, dir)
	if err != nil {
		return err
	}
	defer s.unstage(stg)
	if err := s.fetch(ctx, stg, room, func(ctx context.Context) error { return s.run.Repo(stg).FetchContent(ctx, missing) }); err != nil {
		return err
	}
	packs, err := s.validate(ctx, stg, room, contentCheck(missing))
	if err != nil {
		return err
	}
	if err := s.publish(ctx, dir, kindContent, packs, nil); err != nil {
		return err
	}
	if still, err := cache.Missing(ctx, missing); err != nil || len(still) > 0 {
		if err == nil {
			err = fmt.Errorf("%w: %d of %d files did not arrive", ErrContentIncomplete, len(still), len(missing))
		}
		return err
	}
	return nil
}

// contentCheck: a content acquisition brings only the blob ids it asked for.
func contentCheck(asked []string) func(gitcli.PackObjects) error {
	want := map[string]bool{}
	for _, id := range asked {
		want[id] = true
	}
	return func(o gitcli.PackObjects) error {
		if o.Commits+o.Trees+o.Tags > 0 {
			return fmt.Errorf("the server sent %d commits, trees or tags for a content fetch", o.Commits+o.Trees+o.Tags)
		}
		if o.Blobs > len(asked) || len(o.BlobIDs) < o.Blobs {
			return fmt.Errorf("the server sent %d blobs for %d asked", o.Blobs, len(asked))
		}
		for _, id := range o.BlobIDs {
			if !want[id] {
				return fmt.Errorf("the server sent a blob that was not asked for (%s)", id[:12])
			}
		}
		return nil
	}
}

// prepare finishes or undoes any journal left behind, refuses a cache that
// needs repair, and - for a history refresh - creates a missing cache.
func (s *Store) prepare(ctx context.Context, url, dir string, create bool) error {
	s.pub.Lock()
	defer s.pub.Unlock()
	if err := s.recoverAll(ctx); err != nil && !errors.Is(err, ErrNeedsRepair) {
		return err
	}
	if needsRepair(dir) {
		return ErrNeedsRepair
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); err == nil {
		return nil
	}
	if !create {
		return fmt.Errorf("no history fetched yet")
	}
	if err := gitcli.EnsureBase(s.base); err != nil {
		return err
	}
	_ = os.RemoveAll(dir) // a half-created cache: nothing was ever published to it
	return s.run.InitRepo(ctx, dir, url)
}

// stage creates a fresh staging repo beside the cache that borrows the
// cache's objects through an alternates file Klyx writes - never an
// environment variable.
func (s *Store) stage(ctx context.Context, url, dir string) (string, error) {
	stg := dir + stagingExt
	if err := os.RemoveAll(stg); err != nil {
		return "", err
	}
	if err := s.run.InitRepo(ctx, stg, url); err != nil {
		return "", err
	}
	info := filepath.Join(stg, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return "", err
	}
	if err := gitcli.WriteFileDurable(filepath.Join(info, "alternates"), []byte(filepath.Join(dir, "objects")+"\n"), 0o600); err != nil {
		return "", err
	}
	return stg, nil
}

// unstage always deletes the staging repo.
func (s *Store) unstage(stg string) {
	_ = os.RemoveAll(stg)
	_ = gitcli.SyncDir(s.base)
}

// fetch runs one acquisition fetch, sampling the staging repo's size and
// killing the fetch once it passes the reservation. Overshoot is bounded to
// one interval's transfer and lives only in staging.
func (s *Store) fetch(ctx context.Context, stg string, room int64, do func(context.Context) error) error {
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var over atomic.Bool
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(s.sample)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if dirSize(stg) > room {
					over.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	err := do(fctx)
	close(done)
	if over.Load() {
		return fmt.Errorf("the fetch passed the room left in the Git cache (%s) and was stopped", human(room))
	}
	return err
}

// pack is one pack in a staging repo: its files in publication order -
// .pack, .rev and .promisor first, .idx last.
type pack struct {
	files []string
}

var packFile = regexp.MustCompile(`^(pack-[0-9a-f]{40}(?:[0-9a-f]{24})?)\.(pack|idx|rev|promisor)$`)

var packOrder = map[string]int{"pack": 0, "rev": 1, "promisor": 2, "idx": 3}

// validate enumerates the staging repo's own packs (never an alternate),
// checks each with verify-pack, applies the acquisition's rule to what
// they hold, and checks the size against the reservation.
func (s *Store) validate(ctx context.Context, stg string, room int64, check func(gitcli.PackObjects) error) ([]pack, error) {
	if err := noLooseObjects(stg); err != nil {
		return nil, err
	}
	packDir := filepath.Join(stg, "objects", "pack")
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return nil, err
	}
	byBase := map[string][]string{}
	for _, e := range entries {
		m := packFile.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			return nil, fmt.Errorf("unexpected %s in the staging packs", e.Name())
		}
		byBase[m[1]] = append(byBase[m[1]], e.Name())
	}
	var packs []pack
	var all gitcli.PackObjects
	for base, files := range byBase {
		if !slices.Contains(files, base+".pack") || !slices.Contains(files, base+".idx") {
			return nil, fmt.Errorf("pack %s is incomplete", base)
		}
		objs, err := s.run.Repo(stg).VerifyPack(ctx, filepath.Join(packDir, base+".idx"))
		if err != nil {
			return nil, err
		}
		all.Commits += objs.Commits
		all.Trees += objs.Trees
		all.Tags += objs.Tags
		all.Blobs += objs.Blobs
		all.BlobIDs = append(all.BlobIDs, objs.BlobIDs...)
		slices.SortFunc(files, func(a, b string) int {
			return packOrder[packFile.FindStringSubmatch(a)[2]] - packOrder[packFile.FindStringSubmatch(b)[2]]
		})
		packs = append(packs, pack{files: files})
	}
	if err := check(all); err != nil {
		return nil, err
	}
	if n := dirSize(stg); n > room {
		return nil, fmt.Errorf("the fetch brought %s, over the room left in the Git cache (%s)", human(n), human(room))
	}
	slices.SortFunc(packs, func(a, b pack) int { return strings.Compare(a.files[0], b.files[0]) })
	return packs, nil
}

var looseDir = regexp.MustCompile(`^[0-9a-f]{2}$`)

// noLooseObjects: the unpack limits keep every object in a pack Klyx can
// enumerate; a loose object means they did not hold.
func noLooseObjects(stg string) error {
	entries, err := os.ReadDir(filepath.Join(stg, "objects"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !looseDir.MatchString(e.Name()) {
			continue
		}
		if inner, _ := os.ReadDir(filepath.Join(stg, "objects", e.Name())); len(inner) > 0 {
			return fmt.Errorf("the fetch left loose objects, which Klyx cannot validate")
		}
	}
	return nil
}
