// Package gitcache owns The Line's Git caches (parent spec §7, slice 6 note
// §4): one bare partial clone per approved URL under <config dir>/git-cache,
// changed only through the acquisition contract - a fleet-wide lock with a
// queue, a reservation under the size caps, a fresh staging repo, validation
// of the staging repo's own packs, and a journaled publication that recovery
// can always finish or undo. Reads run beside it and never download.
package gitcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/gitcli"
)

// The caps (parent §7, §14).
const (
	DefaultRepoCap    = 1 << 30 // 1 GiB per repo
	DefaultTotalCap   = 2 << 30 // 2 GiB across every repo
	DefaultContentCap = 64 << 20
	DefaultSample     = 250 * time.Millisecond
)

// Store is the set of caches under one root.
type Store struct {
	base string
	run  gitcli.Runner

	repoCap, totalCap, contentCap int64
	sample                        time.Duration

	// sem is the fleet-wide acquisition lock: one acquisition at a time, the
	// waiters queued on the channel; a cancelled waiter leaves the queue.
	sem chan struct{}
	// pub guards the short steps that change what a reader sees -
	// publication, recovery, Clear - so reads never wait behind a fetch.
	pub sync.RWMutex

	// crash, set only by tests, is called between journaled steps; a
	// non-nil return abandons the acquisition on the spot, as a killed
	// process would.
	crash func(step string) error
}

// New binds a store to the cache root (gitcli.CacheBase) and a runner.
func New(base string, r gitcli.Runner) *Store {
	return &Store{base: base, run: r, repoCap: DefaultRepoCap, totalCap: DefaultTotalCap,
		contentCap: DefaultContentCap, sample: DefaultSample, sem: make(chan struct{}, 1)}
}

// ErrNeedsRepair: recovery found refs it could not explain, deleted
// nothing, and stopped using the cache until it is cleared.
var ErrNeedsRepair = errors.New("this cache needs repair; clear it to rebuild it from scratch")

// File names Klyx keeps inside a cache's GIT_DIR; git ignores them.
const (
	journalName = "klyx-journal.json"
	repairName  = "klyx-needs-repair"
	statusName  = "klyx-status.json"
	stagingExt  = ".staging"
)

func (s *Store) cacheDir(url string) string { return gitcli.CacheDir(s.base, url) }

// lock takes the acquisition lock, or leaves the queue when ctx ends.
func (s *Store) lock(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) unlock() { <-s.sem }

// Start runs once at startup: every leftover staging repo is deleted and
// every cache with a journal is recovered.
func (s *Store) Start(ctx context.Context) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	s.pub.Lock()
	defer s.pub.Unlock()
	entries, err := os.ReadDir(s.base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var errs []error
	for _, e := range entries {
		p := filepath.Join(s.base, e.Name())
		switch {
		case !e.IsDir():
		case strings.HasSuffix(e.Name(), stagingExt):
			if err := os.RemoveAll(p); err != nil {
				errs = append(errs, err)
			}
		case strings.HasSuffix(e.Name(), ".git"):
			if err := s.recoverDir(ctx, p); err != nil && !errors.Is(err, ErrNeedsRepair) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Read runs fn against url's cache with no acquisition step in flight that
// changes what it sees. A missing cache or one that needs repair is an
// error; every read runs under GIT_NO_LAZY_FETCH.
func (s *Store) Read(url string, fn func(gitcli.Repo) error) error {
	s.pub.RLock()
	defer s.pub.RUnlock()
	dir := s.cacheDir(url)
	if _, err := os.Stat(filepath.Join(dir, "config")); err != nil {
		return fmt.Errorf("no history fetched yet")
	}
	if needsRepair(dir) {
		return ErrNeedsRepair
	}
	return fn(s.run.Repo(dir))
}

// Clear removes url's cache entirely, waiting for any acquisition first.
func (s *Store) Clear(ctx context.Context, url string) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	s.pub.Lock()
	defer s.pub.Unlock()
	dir := s.cacheDir(url)
	if err := os.RemoveAll(dir + stagingExt); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return gitcli.SyncDir(s.base)
}

// Size is url's cache size in bytes (0 when there is none).
func (s *Store) Size(url string) int64 { return dirSize(s.cacheDir(url)) }

// Status is a cache's last fetch, as Settings > Git and the line show it.
type Status struct {
	FetchedAt   time.Time `json:"fetchedAt,omitzero"`   // the last good history refresh
	AttemptedAt time.Time `json:"attemptedAt,omitzero"` // the last refresh attempt
	Error       string    `json:"error,omitempty"`      // the last attempt's scrubbed error; "" when it succeeded
	NeedsRepair bool      `json:"-"`
}

// Status reads url's cache status; the zero Status when nothing was fetched.
func (s *Store) Status(url string) Status {
	dir := s.cacheDir(url)
	var st Status
	if b, err := os.ReadFile(filepath.Join(dir, statusName)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	st.NeedsRepair = needsRepair(dir)
	return st
}

func (s *Store) writeStatus(dir string, st Status) {
	b, _ := json.Marshal(st)
	_ = gitcli.WriteFileDurable(filepath.Join(dir, statusName), b, 0o600)
}

func needsRepair(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, repairName))
	return err == nil
}

// usage is every cache's size, and this one's.
func (s *Store) usage(dir string) (repo, total int64) {
	entries, _ := os.ReadDir(s.base)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".git") {
			continue
		}
		n := dirSize(filepath.Join(s.base, e.Name()))
		total += n
		if filepath.Join(s.base, e.Name()) == dir {
			repo = n
		}
	}
	return repo, total
}

// reserve is the room left under the caps for one acquisition.
func (s *Store) reserve(dir string, content bool) (int64, error) {
	repo, total := s.usage(dir)
	room := min(s.repoCap-repo, s.totalCap-total)
	if content {
		room = min(room, s.contentCap)
	}
	if room <= 0 {
		return 0, fmt.Errorf("the Git cache is full (%s of %s for this repo, %s of %s in all)",
			human(repo), human(s.repoCap), human(total), human(s.totalCap))
	}
	return room, nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n>>10)
}
