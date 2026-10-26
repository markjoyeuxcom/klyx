// Package gitcli runs the system git for The Line's read-only Git access
// (parent spec §7, slice 6 note §3): one resolved, version-checked binary,
// an environment built from scratch, an isolated generated config, and a
// typed constructor for each allowed command - no other command can be
// built. Every process runs under internal/procrun's containment.
package gitcli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/clibin"
	"github.com/moomora/klyx/internal/envread"
	"github.com/moomora/klyx/internal/procrun"
)

// Version is a parsed `git version` line.
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

// MinVersion: GIT_NO_LAZY_FETCH (--no-lazy-fetch) is new in 2.45.
var MinVersion = Version{Major: 2, Minor: 45}

// AtLeast compares major.minor.patch.
func (v Version) AtLeast(m Version) bool {
	if v.Major != m.Major {
		return v.Major > m.Major
	}
	if v.Minor != m.Minor {
		return v.Minor > m.Minor
	}
	return v.Patch >= m.Patch
}

var versionRe = regexp.MustCompile(`^git version (\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion reads `git version 2.56.0`, `git version 2.54.0 (Apple
// Git-157)` and `git version 2.45.1.windows.1`.
func ParseVersion(out string) (Version, error) {
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	m := versionRe.FindStringSubmatch(line)
	if m == nil {
		return Version{}, fmt.Errorf("unrecognised git version output %q", truncate(line, 80))
	}
	v := Version{Raw: line}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
	}
	return v, nil
}

// Git is a resolved binary that met the version floor.
type Git struct {
	Path    string
	Version Version
}

var gitBin = clibin.Binary{Name: "git", Override: envread.KlyxGitPath, FallbackPaths: defaultGitFallbackPaths}

// Binary is how Klyx resolves git, for Settings > Diagnostics.
func Binary() clibin.Binary { return gitBin }

func defaultGitFallbackPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/bin/git", "/usr/local/bin/git", "/usr/bin/git"}
	case "linux":
		return []string{"/usr/bin/git", "/usr/local/bin/git"}
	case "windows":
		if root := strings.TrimSpace(envread.Get(envread.ProgramFiles)); root != "" {
			return []string{filepath.Join(root, "Git", "cmd", "git.exe")}
		}
	}
	return nil
}

// ErrOff wraps every reason Git features are off; its message names the
// reason once, for the note (slice 6 note §3.1).
type ErrOff struct{ Reason string }

func (e *ErrOff) Error() string { return "Git features are off: " + e.Reason }

// Locate resolves git and checks its version under containment. Any
// failure is an *ErrOff naming why.
func Locate(ctx context.Context, env Env) (Git, error) {
	if !procrun.Supported() {
		return Git{}, &ErrOff{Reason: "no reviewed process containment on this platform"}
	}
	path, ok := gitBin.Resolve()
	if !ok {
		return Git{}, &ErrOff{Reason: "git was not found"}
	}
	r := procrun.Run(ctx, procrun.Spec{Exe: path, Args: []string{"--version"}, Env: env.minimal(), Dir: env.dir(),
		StdoutMax: 4096, StderrMax: 4096, Deadline: localDeadline, WaitDelay: waitDelay})
	if r.StartErr != nil || r.TimedOut || r.ExitCode != 0 {
		return Git{}, &ErrOff{Reason: fmt.Sprintf("%s --version did not run", path)}
	}
	v, err := ParseVersion(string(r.Stdout))
	if err != nil {
		return Git{}, &ErrOff{Reason: err.Error()}
	}
	if !v.AtLeast(MinVersion) {
		return Git{}, &ErrOff{Reason: fmt.Sprintf("git %d.%d.%d is older than 2.45, which partial-clone safety needs", v.Major, v.Minor, v.Patch)}
	}
	return Git{Path: path, Version: v}, nil
}

// IsOff reports whether err says Git features are off.
func IsOff(err error) bool {
	var off *ErrOff
	return errors.As(err, &off)
}

const (
	localDeadline = 10 * time.Second
	fetchDeadline = 60 * time.Second
	waitDelay     = 2 * time.Second
	outputMax     = 1 << 20 // 1 MiB
)

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
