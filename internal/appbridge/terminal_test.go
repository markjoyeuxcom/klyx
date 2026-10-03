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
	argv, err := posixTerminalArgv("darwin", lookPathFor(), noEnv, `kubectl exec -it 'my pod' -- sh`)
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
	argv, _ = posixTerminalArgv("darwin", lookPathFor(), noEnv, `echo "hi"`)
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
	argv, err := posixTerminalArgv("linux", lookPathFor("footerm", "gnome-terminal"), env, "kubectl exec -it p -- sh")
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
	argv, err := posixTerminalArgv("linux", lookPathFor("gnome-terminal", "xterm"), noEnv, "cmd")
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/usr/bin/gnome-terminal" || argv[1] != "--" || argv[2] != "sh" {
		t.Fatalf("gnome-terminal must use --, got %v", argv)
	}
	// kitty takes the command directly (no flag).
	argv, _ = posixTerminalArgv("linux", lookPathFor("kitty"), noEnv, "cmd")
	if argv[0] != "/usr/bin/kitty" || argv[1] != "sh" {
		t.Fatalf("kitty takes the command directly, got %v", argv)
	}
	// wezterm needs `start --`.
	argv, _ = posixTerminalArgv("linux", lookPathFor("wezterm"), noEnv, "cmd")
	if argv[1] != "start" || argv[2] != "--" {
		t.Fatalf("wezterm must use start --, got %v", argv)
	}
	// x-terminal-emulator (the Debian alternatives choice) outranks the rest.
	argv, _ = posixTerminalArgv("linux", lookPathFor("x-terminal-emulator", "gnome-terminal", "kitty"), noEnv, "cmd")
	if argv[0] != "/usr/bin/x-terminal-emulator" {
		t.Fatalf("x-terminal-emulator must win the probe, got %v", argv)
	}
}

func TestTerminalLaunchArgv_LinuxPlainWindowAndNoEmulator(t *testing.T) {
	// Plain window: just the emulator, no -e.
	argv, err := posixTerminalArgv("linux", lookPathFor("konsole"), noEnv, "")
	if err != nil || len(argv) != 1 || argv[0] != "/usr/bin/konsole" {
		t.Fatalf("plain window must launch bare, got %v (%v)", argv, err)
	}
	// Nothing installed: an honest, actionable error - never a silent no-op.
	_, err = posixTerminalArgv("linux", lookPathFor(), noEnv, "cmd")
	if err == nil || !strings.Contains(err.Error(), "$TERMINAL") || !strings.Contains(err.Error(), "copy command") {
		t.Fatalf("no-emulator error must name the fixes, got %v", err)
	}
}

// The real kubectl exec argv, metacharacters in the remote shell expression
// included (review 2026-10-03 #9: cmd.exe quoting refused every exec).
var winExecArgv = []string{
	`C:\Program Files\kubectl\kubectl.exe`, "--kubeconfig", `C:\Users\Jo O'Neil\.kube\config`,
	"--context", "aks-prd", "-n", "team-a", "exec", "-it", "web-1", "-c", "app",
	"--", "/bin/sh", "-c", "command -v bash >/dev/null && exec bash || exec sh",
}

// psUnquote is a PowerShell single-quote tokenizer for the rendered
// invocation: it proves powershellCommand round-trips to exactly argv.
func psUnquote(t *testing.T, script string) []string {
	t.Helper()
	if !strings.HasPrefix(script, "& ") {
		t.Fatalf("not a call-operator invocation: %q", script)
	}
	rs := []rune(strings.TrimPrefix(script, "& "))
	isQ := func(r rune) bool { return r == '\'' || (r >= 0x2018 && r <= 0x201b) }
	var out []string
	for i := 0; i < len(rs); {
		if rs[i] == ' ' {
			i++
			continue
		}
		if !isQ(rs[i]) {
			t.Fatalf("unquoted token at %d in %q", i, script)
		}
		i++
		var b strings.Builder
		for {
			if i >= len(rs) {
				t.Fatalf("unterminated quote in %q", script)
			}
			if isQ(rs[i]) {
				if i+1 < len(rs) && isQ(rs[i+1]) {
					b.WriteRune(rs[i])
					i += 2
					continue
				}
				i++
				break
			}
			b.WriteRune(rs[i])
			i++
		}
		out = append(out, b.String())
	}
	return out
}

func TestTerminalLaunchArgv_WindowsRunsTheExecArgvInPowerShell(t *testing.T) {
	launch, newConsole, err := terminalLaunchArgv("windows", lookPathFor("powershell"), noEnv, winExecArgv)
	if err != nil {
		t.Fatalf("the real exec argv must launch on Windows: %v", err)
	}
	if !newConsole {
		t.Fatal("Windows must open a new console window")
	}
	want := []string{"/usr/bin/powershell", "-NoLogo", "-NoExit", "-Command"}
	if strings.Join(launch[:4], "|") != strings.Join(want, "|") || len(launch) != 5 {
		t.Fatalf("launch = %v", launch)
	}
	if got := psUnquote(t, launch[4]); strings.Join(got, "\x00") != strings.Join(winExecArgv, "\x00") {
		t.Fatalf("the script must round-trip to the exec argv:\n got %q\nwant %q", got, winExecArgv)
	}
	if strings.Contains(launch[4], `"`) {
		t.Fatal("no double quotes may reach the Windows command line")
	}

	// pwsh (PowerShell 7) wins when installed; a plain window has no command.
	launch, _, _ = terminalLaunchArgv("windows", lookPathFor("pwsh", "powershell"), noEnv, nil)
	if strings.Join(launch, "|") != "/usr/bin/pwsh|-NoLogo" {
		t.Fatalf("plain window = %v", launch)
	}
	// No PowerShell at all: an honest fallback, never a silent no-op.
	if _, _, err := terminalLaunchArgv("windows", lookPathFor(), noEnv, winExecArgv); err == nil || !strings.Contains(err.Error(), "copy command") {
		t.Fatalf("missing PowerShell must name the copy fallback, got %v", err)
	}
}

func TestPowerShellCommand_QuotingRules(t *testing.T) {
	// Single and typographic single quotes are doubled; everything else is
	// literal inside the quotes.
	tricky := []string{"it's", "\u2019smart\u2018", "$env:HOME", "a;b", "--"}
	s, err := powershellCommand(tricky)
	if err != nil {
		t.Fatal(err)
	}
	if got := psUnquote(t, s); strings.Join(got, "|") != strings.Join(tricky, "|") {
		t.Fatalf("round trip: %q -> %q -> %q", tricky, s, got)
	}
	for _, bad := range []string{"a\nb", `a"b`, "a\x00b"} {
		if _, err := powershellCommand([]string{"kubectl", bad}); err == nil {
			t.Fatalf("%q must fail closed", bad)
		}
	}
}

// The copy command matches the platform's default shell.
func TestDisplayCommand_PerPlatform(t *testing.T) {
	argv := []string{"kubectl", "exec", "-it", "p", "--", "/bin/sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"}
	win, err := displayCommand("windows", argv)
	if err != nil || !strings.HasPrefix(win, "& 'kubectl' 'exec'") {
		t.Fatalf("windows copy command = %q (%v)", win, err)
	}
	if posix, _ := displayCommand("linux", argv); posix != shellQuoteArgv(argv) {
		t.Fatalf("linux copy command = %q", posix)
	}
}

// On a Windows host the exec copy command is the PowerShell form, which
// carries the remote shell expression (review 2026-10-03 #9).
func TestGetExecCommand_WindowsHostRendersPowerShell(t *testing.T) {
	old := hostGOOS
	hostGOOS = "windows"
	defer func() { hostGOOS = old }()
	svc := makeExecService("prod-aks")
	dto := svc.GetExecCommand("prod", "default", "web-abc", "web")
	if dto.Error != "" {
		t.Fatalf("exec command must render on Windows: %q", dto.Error)
	}
	if !strings.HasPrefix(dto.Command, "& 'kubectl'") || !strings.Contains(dto.Command, "'command -v bash >/dev/null && exec bash || exec sh'") {
		t.Fatalf("command = %q", dto.Command)
	}
}
