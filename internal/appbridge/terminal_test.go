package appbridge

import (
	"os/exec"
	"strings"
	"testing"
)

func lookPathFor(available ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, a := range available {
		set[a] = true
	}
	return func(bin string) (string, error) {
		if set[bin] {
			return "/usr/bin/" + bin, nil
		}
		return "", exec.ErrNotFound
	}
}

func noEnv(string) string { return "" }

func TestTerminalLaunchArgv_DarwinEscapesForAppleScript(t *testing.T) {
	argv, err := terminalLaunchArgv("darwin", lookPathFor(), noEnv, `kubectl exec -it 'my pod' -- sh`)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "osascript" || argv[1] != "-e" {
		t.Fatalf("want osascript -e, got %v", argv)
	}
	if !strings.Contains(argv[2], `do script "kubectl exec -it 'my pod' -- sh"`) {
		t.Fatalf("script missing the command: %q", argv[2])
	}
	// A double quote in the command must be escaped for the AppleScript literal.
	argv, _ = terminalLaunchArgv("darwin", lookPathFor(), noEnv, `echo "hi"`)
	if !strings.Contains(argv[2], `\"hi\"`) {
		t.Fatalf("double quotes must be escaped: %q", argv[2])
	}
}

func TestTerminalLaunchArgv_LinuxTERMINALWins(t *testing.T) {
	env := func(k string) string {
		if k == "TERMINAL" {
			return "footerm"
		}
		return ""
	}
	argv, err := terminalLaunchArgv("linux", lookPathFor("footerm", "gnome-terminal"), env, "kubectl exec -it p -- sh")
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/usr/bin/footerm" || argv[1] != "-e" || argv[2] != "sh" || argv[3] != "-c" {
		t.Fatalf("$TERMINAL must win with -e sh -c, got %v", argv)
	}
	// The window must stay interactive after kubectl exits.
	if !strings.Contains(argv[4], `exec "${SHELL:-sh}"`) {
		t.Fatalf("keep-open shell missing: %q", argv[4])
	}
}

func TestTerminalLaunchArgv_LinuxProbeOrderAndSyntaxes(t *testing.T) {
	// gnome-terminal uses `--`, not `-e`.
	argv, err := terminalLaunchArgv("linux", lookPathFor("gnome-terminal", "xterm"), noEnv, "cmd")
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/usr/bin/gnome-terminal" || argv[1] != "--" || argv[2] != "sh" {
		t.Fatalf("gnome-terminal must use --, got %v", argv)
	}
	// kitty takes the command directly (no flag).
	argv, _ = terminalLaunchArgv("linux", lookPathFor("kitty"), noEnv, "cmd")
	if argv[0] != "/usr/bin/kitty" || argv[1] != "sh" {
		t.Fatalf("kitty takes the command directly, got %v", argv)
	}
	// wezterm needs `start --`.
	argv, _ = terminalLaunchArgv("linux", lookPathFor("wezterm"), noEnv, "cmd")
	if argv[1] != "start" || argv[2] != "--" {
		t.Fatalf("wezterm must use start --, got %v", argv)
	}
	// x-terminal-emulator (the Debian alternatives choice) outranks the rest.
	argv, _ = terminalLaunchArgv("linux", lookPathFor("x-terminal-emulator", "gnome-terminal", "kitty"), noEnv, "cmd")
	if argv[0] != "/usr/bin/x-terminal-emulator" {
		t.Fatalf("x-terminal-emulator must win the probe, got %v", argv)
	}
}

func TestTerminalLaunchArgv_LinuxPlainWindowAndNoEmulator(t *testing.T) {
	// Plain window: just the emulator, no -e.
	argv, err := terminalLaunchArgv("linux", lookPathFor("konsole"), noEnv, "")
	if err != nil || len(argv) != 1 || argv[0] != "/usr/bin/konsole" {
		t.Fatalf("plain window must launch bare, got %v (%v)", argv, err)
	}
	// Nothing installed: an honest, actionable error - never a silent no-op.
	_, err = terminalLaunchArgv("linux", lookPathFor(), noEnv, "cmd")
	if err == nil || !strings.Contains(err.Error(), "$TERMINAL") || !strings.Contains(err.Error(), "copy command") {
		t.Fatalf("no-emulator error must name the fixes, got %v", err)
	}
}

func TestTerminalLaunchArgv_Windows(t *testing.T) {
	// Windows Terminal preferred.
	argv, err := terminalLaunchArgv("windows", lookPathFor("wt"), noEnv, "kubectl exec -it p -- sh")
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/usr/bin/wt" || argv[1] != "cmd" || argv[2] != "/k" {
		t.Fatalf("wt must run cmd /k, got %v", argv)
	}
	// Fallback: cmd /c start with an explicit empty title (or start eats the
	// first quoted string as the window title).
	argv, _ = terminalLaunchArgv("windows", lookPathFor(), noEnv, "kubectl version")
	want := []string{"cmd", "/c", "start", "", "cmd", "/k", "kubectl version"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("cmd start fallback wrong: %v", argv)
	}
}

func TestWindowsQuoteArgv_FailsClosed(t *testing.T) {
	// Spaces double-quote; simple args stay bare.
	s, err := windowsQuoteArgv([]string{"kubectl", "--context", "my ctx"})
	if err != nil || s != `kubectl --context "my ctx"` {
		t.Fatalf("got %q (%v)", s, err)
	}
	// cmd metacharacters refuse outright - never a command-splitting bug.
	for _, bad := range []string{`a"b`, "a&b", "a|b", "a%b", "a>b"} {
		if _, err := windowsQuoteArgv([]string{bad}); err == nil {
			t.Fatalf("%q must fail closed", bad)
		}
	}
}
