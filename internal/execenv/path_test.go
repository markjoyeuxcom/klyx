package execenv

import (
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/envread"
)

func TestAugmentPathAddsMacDesktopToolDirsAfterExistingPath(t *testing.T) {
	got := AugmentPath("/custom/bin:/usr/bin", "darwin", func(envread.Name) string { return "" })
	parts := strings.Split(got, ":")
	if parts[0] != "/custom/bin" || parts[1] != "/usr/bin" {
		t.Fatalf("existing PATH order changed: %v", parts)
	}
	for _, want := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"} {
		if !contains(parts, want) {
			t.Fatalf("PATH missing %s: %v", want, parts)
		}
	}
	if strings.Count(got, "/usr/bin") != 1 {
		t.Fatalf("duplicate /usr/bin in %q", got)
	}
}

func TestAugmentPathAddsWindowsAWSCLIPath(t *testing.T) {
	env := map[string]string{
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"ChocolateyInstall": `C:\Tools\choco`,
	}
	got := AugmentPath(`C:\Windows\System32`, "windows", func(n envread.Name) string { return env[n.Key()] })
	parts := strings.Split(got, ";")
	for _, want := range []string{
		`C:\Windows\System32`,
		`C:\Program Files\Amazon\AWSCLIV2`,
		`C:\Program Files\Amazon\AWSCLIV2\bin`,
		`C:\Tools\choco\bin`,
	} {
		if !contains(parts, want) {
			t.Fatalf("PATH missing %s: %v", want, parts)
		}
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
