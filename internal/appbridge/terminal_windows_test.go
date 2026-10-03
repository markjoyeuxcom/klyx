//go:build windows

package appbridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Batch-3 review R4: the launch must not pin the child to NUL handles.
func TestNewConsoleStartupInfo_UsesTheConsolesOwnHandles(t *testing.T) {
	si := newConsoleStartupInfo()
	if si.Flags&windows.STARTF_USESTDHANDLES != 0 {
		t.Fatal("STARTF_USESTDHANDLES would pin the child to the given (NUL) handles")
	}
	if si.StdInput != 0 || si.StdOutput != 0 || si.StdErr != 0 {
		t.Fatal("no standard handles may be passed")
	}
}

// The child process check: PowerShell started by the real launcher sees a
// console, not redirected NUL, on stdin and stdout. Opens (and closes) a
// console window.
func TestStartInNewConsole_ChildHasConsoleStdio(t *testing.T) {
	ps, err := exec.LookPath("powershell")
	if err != nil {
		t.Skip("powershell not found")
	}
	out := filepath.Join(t.TempDir(), "redirected.txt")
	q, err := psQuote(out) // keeps the temp path literal
	if err != nil {
		t.Fatal(err)
	}
	script := "[string][Console]::IsInputRedirected + ',' + [string][Console]::IsOutputRedirected | Set-Content -LiteralPath " + q
	if err := startInNewConsole([]string{ps, "-NoLogo", "-NoProfile", "-Command", script}); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil && len(b) > 0 {
			if got := strings.TrimSpace(string(b)); got != "False,False" {
				t.Fatalf("child stdio redirected (input,output) = %s, want False,False", got)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the PowerShell child never ran")
}
