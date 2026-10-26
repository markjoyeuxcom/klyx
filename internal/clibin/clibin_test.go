package clibin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/moomora/klyx/internal/envread"
)

// bin builds a Binary with fake lookups so no test touches the real PATH or
// filesystem. lp fakes exec.LookPath; exists fakes the executable check.
func bin(lp func(string) (string, error), exists func(string) bool, fallbacks ...string) Binary {
	return Binary{
		Name:             "tool",
		Override:         envread.KlyxFluxPath,
		FallbackPaths:    func() []string { return fallbacks },
		LookPath:         lp,
		ExecutableExists: exists,
	}
}

func notOnPath(string) (string, error) { return "", exec.ErrNotFound }

func TestResolveEnvOverrideWins(t *testing.T) {
	t.Setenv("KLYX_FLUX_PATH", "/custom/tool")
	b := bin(
		func(string) (string, error) { return "/path/tool", nil },
		func(p string) bool { return p == "/custom/tool" },
		"/fallback/tool",
	)
	got, ok := b.Resolve()
	if !ok || got != "/custom/tool" {
		t.Fatalf("want env override /custom/tool, got %q ok=%v", got, ok)
	}
}

func TestResolveEnvOverrideIsTrimmed(t *testing.T) {
	t.Setenv("KLYX_FLUX_PATH", "  /custom/tool  ")
	b := bin(notOnPath, func(p string) bool { return p == "/custom/tool" })
	got, ok := b.Resolve()
	if !ok || got != "/custom/tool" {
		t.Fatalf("want trimmed override /custom/tool, got %q ok=%v", got, ok)
	}
}

func TestResolveMissingOverrideFallsThroughToPath(t *testing.T) {
	// A configured but nonexistent override must not fail hard.
	t.Setenv("KLYX_FLUX_PATH", "/gone/tool")
	b := bin(
		func(string) (string, error) { return "/path/tool", nil },
		func(p string) bool { return p == "/path/tool" },
	)
	got, ok := b.Resolve()
	if !ok || got != "/path/tool" {
		t.Fatalf("want PATH result after dead override, got %q ok=%v", got, ok)
	}
}

func TestResolvePathBeforeFallback(t *testing.T) {
	t.Setenv("KLYX_FLUX_PATH", "")
	b := bin(
		func(string) (string, error) { return "/path/tool", nil },
		func(string) bool { return true },
		"/fallback/tool",
	)
	got, ok := b.Resolve()
	if !ok || got != "/path/tool" {
		t.Fatalf("want PATH before fallback, got %q ok=%v", got, ok)
	}
}

func TestResolveFallbackOrder(t *testing.T) {
	t.Setenv("KLYX_FLUX_PATH", "")
	b := bin(
		notOnPath,
		func(p string) bool { return p == "/second/tool" },
		"/first/tool", "/second/tool",
	)
	got, ok := b.Resolve()
	if !ok || got != "/second/tool" {
		t.Fatalf("want first existing fallback, got %q ok=%v", got, ok)
	}
}

func TestResolveNotFound(t *testing.T) {
	t.Setenv("KLYX_FLUX_PATH", "")
	b := bin(notOnPath, func(string) bool { return false }, "/fallback/tool")
	if got, ok := b.Resolve(); ok || got != "" {
		t.Fatalf("want not found, got %q ok=%v", got, ok)
	}
}

func TestResolveNilFallbacks(t *testing.T) {
	t.Setenv("KLYX_FLUX_PATH", "")
	b := Binary{Name: "tool", Override: envread.KlyxFluxPath, LookPath: notOnPath, ExecutableExists: func(string) bool { return true }}
	if _, ok := b.Resolve(); ok {
		t.Fatal("nil FallbackPaths must not resolve (and must not panic)")
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("KLYX_FLUX_PATH", "")
	found := bin(notOnPath, func(p string) bool { return p == "/fallback/tool" }, "/fallback/tool")
	if !found.Detect() {
		t.Fatal("want Detect true via fallback")
	}
	missing := bin(notOnPath, func(string) bool { return false })
	if missing.Detect() {
		t.Fatal("want Detect false when nothing resolves")
	}
}

// TestExecutableExists exercises the default filesystem check with real temp
// files: executable file yes, plain file no (POSIX), directory no, missing no.
func TestExecutableExists(t *testing.T) {
	dir := t.TempDir()

	execFile := filepath.Join(dir, "tool")
	if err := os.WriteFile(execFile, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !executableExists(execFile) {
		t.Error("want true for executable file")
	}

	if runtime.GOOS != "windows" {
		plain := filepath.Join(dir, "plain")
		if err := os.WriteFile(plain, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		if executableExists(plain) {
			t.Error("want false for non-executable file")
		}
	}

	if executableExists(dir) {
		t.Error("want false for directory")
	}
	if executableExists(filepath.Join(dir, "missing")) {
		t.Error("want false for missing path")
	}
}

// Settings S1 §5: the resolution says how it found the binary, a fallback
// location among them, and a set but missing override separately.
func TestResolutionSaysItsSource(t *testing.T) {
	for _, c := range []struct {
		name, override string
		onPath         bool
		want           Resolution
	}{
		{"the override", "/custom/tool", true, Resolution{Path: "/custom/tool", Source: SourceOverride}},
		{"PATH", "", true, Resolution{Path: "/path/tool", Source: SourcePath}},
		{"a fallback", "", false, Resolution{Path: "/fallback/tool", Source: SourceFallback}},
		{"a missing override, then PATH", "/gone/tool", true, Resolution{Path: "/path/tool", Source: SourcePath, OverrideMissing: "/gone/tool"}},
		{"a missing override, then a fallback", "/gone/tool", false, Resolution{Path: "/fallback/tool", Source: SourceFallback, OverrideMissing: "/gone/tool"}},
	} {
		t.Setenv("KLYX_FLUX_PATH", c.override)
		lp := notOnPath
		if c.onPath {
			lp = func(string) (string, error) { return "/path/tool", nil }
		}
		b := bin(lp, func(p string) bool { return p == "/custom/tool" || p == "/path/tool" || p == "/fallback/tool" }, "/fallback/tool")
		if got := b.Resolution(); got != c.want {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	t.Setenv("KLYX_FLUX_PATH", "")
	if got := bin(notOnPath, func(string) bool { return false }).Resolution(); got.Source != SourceNotFound || got.Path != "" {
		t.Fatalf("not found: %+v", got)
	}
}
