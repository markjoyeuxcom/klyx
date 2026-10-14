package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// testedBuild is one Claude Code build that passed Test on this machine:
// every admission check plus the canary probe (spec §1.5).
type testedBuild struct {
	SHA256   string    `json:"sha256"`
	Version  string    `json:"version"`
	Platform string    `json:"platform"`
	TestedAt time.Time `json:"testedAt"`
}

// claudeBuildsKept bounds the record: old builds fall out as new ones are
// tested.
const claudeBuildsKept = 8

// claudeBuildStore remembers which builds passed Test, across Klyx runs, so
// an auto-update pauses the CLI until Test once instead of until a Klyx
// release. With a path it lives in a 0600 file beside fleet.yaml; without
// one, in memory (tests, or a run with no config dir). A file that is
// missing, cannot be read, or cannot be written holds no builds: the answer
// is always "press Test", never a build admitted by default - and a record
// Klyx could not drop a broken build from is never trusted (Codex review of
// b0b977b).
type claudeBuildStore struct {
	mu   sync.Mutex
	path string
	mem  []testedBuild
}

var defaultClaudeBuilds = &claudeBuildStore{}

// UseClaudeBuildsFile sets where the tested builds are kept. main calls it
// once at startup, before any provider is built.
func UseClaudeBuildsFile(path string) {
	defaultClaudeBuilds.mu.Lock()
	defer defaultClaudeBuilds.mu.Unlock()
	defaultClaudeBuilds.path = path
}

type claudeBuildsFile struct {
	Builds []testedBuild `json:"builds"`
}

// readLocked reads the record as it is on disk, whether or not lookup would
// trust it. A missing or malformed record holds no builds; any other failure
// is returned.
func (s *claudeBuildStore) readLocked() ([]testedBuild, error) {
	if s.path == "" {
		return append([]testedBuild(nil), s.mem...), nil
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f claudeBuildsFile
	if json.Unmarshal(b, &f) != nil {
		return nil, nil
	}
	return f.Builds, nil
}

// trustedLocked is what lookup may trust: the record, only while Klyx can
// write it, so a revocation can always reach what lookup reads.
func (s *claudeBuildStore) trustedLocked() []testedBuild {
	if s.path != "" {
		w, err := os.OpenFile(s.path, os.O_WRONLY, 0)
		if err != nil {
			return nil
		}
		_ = w.Close()
	}
	builds, err := s.readLocked()
	if err != nil {
		return nil
	}
	return builds
}

func (s *claudeBuildStore) saveLocked(builds []testedBuild) error {
	if s.path == "" {
		s.mem = append([]testedBuild(nil), builds...)
		return nil
	}
	b, err := json.MarshalIndent(claudeBuildsFile{Builds: builds}, "", "  ")
	if err != nil {
		return err
	}
	data := append(b, '\n')
	if err := s.replaceLocked(data); err != nil {
		// The directory refused the atomic replace (read-only, say): rewrite
		// the existing file in place, which needs only the file to be
		// writable. A torn write reads as no builds, which is safe.
		if _, serr := os.Stat(s.path); serr != nil {
			return err
		}
		if werr := os.WriteFile(s.path, data, 0o600); werr != nil {
			return fmt.Errorf("%v; rewriting in place: %v", err, werr)
		}
	}
	return nil
}

// replaceLocked writes data to a temp file beside the record and renames it
// over the record.
func (s *claudeBuildStore) replaceLocked(data []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".claude-builds-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after the rename; cleans up a failed write
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// lookup finds a tested build by its binary hash and platform.
func (s *claudeBuildStore) lookup(sha, platform string) (testedBuild, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.trustedLocked() {
		if b.SHA256 == sha && b.Platform == platform {
			return b, true
		}
	}
	return testedBuild{}, false
}

// record adds a build that passed Test, replacing an older record of the
// same binary, and keeps the newest claudeBuildsKept.
func (s *claudeBuildStore) record(t testedBuild) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An unreadable record holds nothing worth keeping; Test starts it over.
	cur, _ := s.readLocked()
	builds := []testedBuild{t}
	for _, b := range cur {
		if b.SHA256 != t.SHA256 {
			builds = append(builds, b)
		}
	}
	sort.SliceStable(builds, func(i, j int) bool { return builds[i].TestedAt.After(builds[j].TestedAt) })
	if len(builds) > claudeBuildsKept {
		builds = builds[:claudeBuildsKept]
	}
	if err := s.saveLocked(builds); err != nil {
		return fmt.Errorf("Klyx could not record the tested Claude Code build: %w", err)
	}
	return nil
}

// forget drops a build that broke the contract, so it needs Test again even
// after a restart. It reaches the record whatever its permissions: a record
// lookup would not trust today can be trusted again once its permissions are
// repaired, so its entry must go too (Codex review of 7872e17). Klyx owns
// the file, so it can restore its own access for the rewrite, then puts the
// mode back. A removal that still fails is reported with the file to delete.
func (s *claudeBuildStore) forget(sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(err error) error {
		return fmt.Errorf("Klyx could not drop this build from its tested record (%v); delete %s before restarting Klyx", err, s.path)
	}
	if s.path != "" {
		fi, err := os.Stat(s.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fail(err)
		}
		if mode := fi.Mode().Perm(); mode&0o600 != 0o600 {
			if err := os.Chmod(s.path, 0o600); err != nil {
				return fail(err)
			}
			defer func() { _ = os.Chmod(s.path, mode) }() // the owner's mode; lookup still distrusts it
		}
	}
	cur, err := s.readLocked()
	if err != nil {
		return fail(err)
	}
	kept := cur[:0:0]
	for _, b := range cur {
		if b.SHA256 != sha {
			kept = append(kept, b)
		}
	}
	if len(kept) == len(cur) {
		return nil
	}
	if err := s.saveLocked(kept); err != nil {
		return fail(err)
	}
	return nil
}
