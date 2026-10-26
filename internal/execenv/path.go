// Package execenv normalises the process environment for desktop launches.
package execenv

import (
	"os"
	"runtime"
	"strings"

	"github.com/moomora/klyx/internal/envread"
)

// ApplyDesktopToolPath augments PATH with common package-manager and cloud CLI
// locations. It is intentionally additive: user-provided PATH entries stay
// first, and duplicates are ignored.
func ApplyDesktopToolPath() {
	_ = envread.Set(envread.Path, AugmentPath(envread.Get(envread.Path), runtime.GOOS, envread.Get))
}

// AugmentPath returns a PATH with common desktop-tool directories appended for
// the target OS. getenv is injected for tests and for Windows ProgramFiles
// expansion.
func AugmentPath(path, goos string, getenv func(envread.Name) string) string {
	sep := pathListSeparator(goos)
	entries := splitPath(path, sep)
	for _, candidate := range desktopToolDirs(goos, getenv) {
		entries = appendPathUnique(entries, candidate)
	}
	return strings.Join(entries, sep)
}

func desktopToolDirs(goos string, getenv func(envread.Name) string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/opt/homebrew/bin",
			"/usr/local/bin",
			"/opt/local/bin",
			"/usr/bin",
			"/bin",
		}
	case "linux":
		return []string{
			"/usr/local/bin",
			"/usr/bin",
			"/bin",
			"/snap/bin",
			"/home/linuxbrew/.linuxbrew/bin",
		}
	case "windows":
		var out []string
		if pf := getenv(envread.ProgramFiles); pf != "" {
			out = append(out,
				pf+`\Amazon\AWSCLIV2`,
				pf+`\Amazon\AWSCLIV2\bin`,
				pf+`\Kubernetes`,
			)
		}
		if pf86 := getenv(envread.ProgramFilesX86); pf86 != "" {
			out = append(out,
				pf86+`\Amazon\AWSCLIV2`,
				pf86+`\Amazon\AWSCLIV2\bin`,
			)
		}
		if chocolatey := getenv(envread.ChocolateyInstall); chocolatey != "" {
			out = append(out, chocolatey+`\bin`)
		}
		return append(out, `C:\ProgramData\chocolatey\bin`)
	default:
		return nil
	}
}

func pathListSeparator(goos string) string {
	if goos == "windows" {
		return ";"
	}
	return string(os.PathListSeparator)
}

func splitPath(path, sep string) []string {
	if path == "" {
		return nil
	}
	raw := strings.Split(path, sep)
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		out = appendPathUnique(out, p)
	}
	return out
}

func appendPathUnique(entries []string, candidate string) []string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return entries
	}
	for _, existing := range entries {
		if existing == candidate {
			return entries
		}
	}
	return append(entries, candidate)
}
