// Package clibin resolves the external CLI binaries Klyx shells out to (helm,
// flux, and future tools like cmctl). Resolution order: an explicit env-var
// override, then PATH, then per-platform fallback locations. The fallback step
// exists because macOS .app launches do not inherit the user's interactive
// shell PATH, so the usual package-manager install paths must be checked
// directly.
package clibin

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/moomora/klyx/internal/envread"
)

// Binary describes one external CLI to resolve. FallbackPaths is a func (not a
// slice) so consumers can defer env-var expansion (e.g. Windows ProgramFiles)
// to call time.
type Binary struct {
	// Name is the executable name looked up on PATH, e.g. "flux".
	Name string
	// Override is the declared environment variable holding an explicit path
	// override, e.g. envread.KlyxFluxPath. The zero Name disables the
	// override step.
	Override envread.Name
	// FallbackPaths returns candidate paths checked in order after PATH.
	// Nil means no fallbacks.
	FallbackPaths func() []string

	// LookPath and ExecutableExists override the default lookups (exec.LookPath
	// and a stat + exec-mode check). Nil selects the defaults; tests inject
	// fakes here to avoid touching the real filesystem.
	LookPath         func(string) (string, error)
	ExecutableExists func(string) bool
}

// Source is how a resolution found the binary (Settings S1 §5: Diagnostics
// says it as the resolver found it, never inferred).
type Source string

const (
	SourceOverride Source = "override" // the override, chosen
	SourcePath     Source = "path"     // found on PATH
	SourceFallback Source = "fallback" // a platform or package-manager location
	SourceNotFound Source = "not found"
)

// Resolution is one resolution: the path, how it was found, and - separately
// - an override that was set but is not an executable, which fell through.
type Resolution struct {
	Path            string
	Source          Source
	OverrideMissing string
}

// Resolve returns the executable path Klyx should use for the binary. The
// env-var override wins when it points at an existing executable; a configured
// but missing override falls through to PATH and the fallback table rather
// than failing hard.
func (b Binary) Resolve() (string, bool) {
	r := b.Resolution()
	return r.Path, r.Source != SourceNotFound
}

// Resolution is Resolve with how it found the binary.
func (b Binary) Resolution() Resolution {
	lookPath := b.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	exists := b.ExecutableExists
	if exists == nil {
		exists = executableExists
	}
	var r Resolution
	if configured := strings.TrimSpace(envread.Get(b.Override)); configured != "" {
		if exists(configured) {
			return Resolution{Path: configured, Source: SourceOverride}
		}
		r.OverrideMissing = configured
	}
	if p, err := lookPath(b.Name); err == nil {
		r.Path, r.Source = p, SourcePath
		return r
	}
	if b.FallbackPaths != nil {
		for _, candidate := range b.FallbackPaths() {
			if exists(candidate) {
				r.Path, r.Source = candidate, SourceFallback
				return r
			}
		}
	}
	r.Source = SourceNotFound
	return r
}

// Detect reports whether the binary is available to Klyx.
func (b Binary) Detect() bool {
	_, ok := b.Resolve()
	return ok
}

// executableExists reports whether path is an executable regular file. Windows
// has no exec mode bit, so existence of a regular file is enough there.
func executableExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0111 != 0
}
