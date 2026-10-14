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
// missing or cannot be read holds no builds: the answer is always "press
// Test", never a build admitted by default.
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

func (s *claudeBuildStore) loadLocked() []testedBuild {
	if s.path == "" {
		return append([]testedBuild(nil), s.mem...)
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var f claudeBuildsFile
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return f.Builds
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
	if _, err := tmp.Write(append(b, '\n')); err != nil {
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
	for _, b := range s.loadLocked() {
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
	builds := []testedBuild{t}
	for _, b := range s.loadLocked() {
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
// after a restart. A record that cannot be rewritten is reported; the
// in-memory revocation holds for this run either way.
func (s *claudeBuildStore) forget(sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.loadLocked()
	kept := cur[:0:0]
	for _, b := range cur {
		if b.SHA256 != sha {
			kept = append(kept, b)
		}
	}
	if len(kept) == len(cur) {
		return nil
	}
	if err := s.saveLocked(kept); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
