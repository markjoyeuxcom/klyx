package ai

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeBuildStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "klyx", "claude-builds.json")
	s := &claudeBuildStore{path: path}
	if _, ok := s.lookup("a", "darwin/arm64"); ok {
		t.Fatal("a missing file holds no builds")
	}
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < claudeBuildsKept+2; i++ {
		if err := s.record(testedBuild{SHA256: fmt.Sprint("sha-", i), Version: fmt.Sprint("2.1.", 300+i), Platform: "darwin/arm64", TestedAt: t0.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the record is a 0600 file: %v %v", fi, err)
	}
	// The newest are kept; the oldest fell out.
	if _, ok := s.lookup("sha-0", "darwin/arm64"); ok {
		t.Fatal("the oldest build falls out past the bound")
	}
	b, ok := s.lookup(fmt.Sprint("sha-", claudeBuildsKept+1), "darwin/arm64")
	if !ok || b.Version != fmt.Sprint("2.1.", 300+claudeBuildsKept+1) {
		t.Fatalf("the newest build is kept: %+v %v", b, ok)
	}
	if _, ok := s.lookup(fmt.Sprint("sha-", claudeBuildsKept+1), "linux/amd64"); ok {
		t.Fatal("a build is tested only on the platform Test ran on")
	}
	// A re-test replaces the record of the same binary.
	if err := s.record(testedBuild{SHA256: "sha-5", Version: "2.1.305", Platform: "darwin/arm64", TestedAt: t0.Add(100 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := len((&claudeBuildStore{path: path}).loadLocked()); got != claudeBuildsKept {
		t.Fatalf("a re-test does not add a second record: %d kept", got)
	}
	// forget drops one build, and a second store reading the file agrees.
	if err := s.forget("sha-5"); err != nil {
		t.Fatal(err)
	}
	if _, ok := (&claudeBuildStore{path: path}).lookup("sha-5", "darwin/arm64"); ok {
		t.Fatal("a forgotten build stays on the record")
	}
	// A file that cannot be read holds no builds: the answer is Test.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.lookup(fmt.Sprint("sha-", claudeBuildsKept+1), "darwin/arm64"); ok {
		t.Fatal("a malformed record admits a build")
	}
}

func TestParseClaudeVersion(t *testing.T) {
	for out, want := range map[string]string{
		"2.1.296 (Claude Code)\n": "2.1.296",
		"2.1.300 (Claude Code)\n": "2.1.300",
		"2.2.0 (Claude Code)\n":   "2.2.0",
		"3.0.1 (Claude Code)":     "3.0.1",
	} {
		if v, err := parseClaudeVersion(out); err != nil || v != want {
			t.Errorf("%q: got (%q, %v), want %q", out, v, err, want)
		}
	}
	for out, want := range map[string]string{
		"2.1.295 (Claude Code)\n":      "older than 2.1.296",
		"1.9.999 (Claude Code)\n":      "older than 2.1.296",
		"2.1.300\n":                    "not a Claude Code version",
		"2.1.300-rc1 (Claude Code)\n":  "not a Claude Code version",
		"2.1.300 (Claude Code) extra":  "not a Claude Code version",
		"":                             "not a Claude Code version",
		"2.1.300 (Claude Code)\n\nx\n": "not a Claude Code version",
	} {
		if _, err := parseClaudeVersion(out); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want an error containing %q, got %v", out, want, err)
		}
	}
}
