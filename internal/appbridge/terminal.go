package appbridge

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Terminal launching, per platform. Klyx's deliberate stance is "shell out to a
// REAL terminal, don't embed xterm.js" - but until now that shell-out was
// hard-gated to macOS, leaving the exec/debug escape hatches dead on the Linux
// and Windows builds (including the owner's own k3s homelab).
//
// hostGOOS is the platform terminals are launched and copy commands rendered
// for; tests override it.
var hostGOOS = runtime.GOOS

// terminalLaunchArgv is the PURE resolver: given the platform, a lookPath, the
// env, and the command argv, it returns the argv that opens a terminal running
// the command - or an honest error naming the fallback (copy the command).
// Empty argv means "open a plain interactive terminal". newConsole asks the
// launcher for a fresh console window (Windows).
func terminalLaunchArgv(goos string, lookPath func(string) (string, error), getenv func(string) string, argv []string) (launch []string, newConsole bool, err error) {
	if goos == "windows" {
		launch, err := windowsTerminalArgv(lookPath, argv)
		return launch, true, err
	}
	cmdStr := ""
	if len(argv) > 0 {
		cmdStr = shellQuoteArgv(argv)
	}
	launch, err = posixTerminalArgv(goos, lookPath, getenv, cmdStr)
	return launch, false, err
}

// posixTerminalArgv resolves the terminal for a POSIX-quoted command string
// (cmdStr == "" opens a plain interactive terminal).
func posixTerminalArgv(goos string, lookPath func(string) (string, error), getenv func(string) string, cmdStr string) ([]string, error) {
	switch goos {
	case "darwin":
		return darwinTerminalArgv(cmdStr), nil
	case "linux":
		return linuxTerminalArgv(lookPath, getenv, cmdStr)
	default:
		return nil, fmt.Errorf("open-terminal not supported on %s - use copy command", goos)
	}
}

// displayCommand renders argv as the command a user copies and pastes into the
// platform's default shell: POSIX quoting everywhere but Windows, where the
// default shell is PowerShell (cmd.exe quoting cannot carry the exec shell
// expression - review 2026-10-03 #9). Fails only for an argument no Windows
// rendering can carry.
func displayCommand(goos string, argv []string) (string, error) {
	if goos == "windows" {
		return powershellCommand(argv)
	}
	return shellQuoteArgv(argv), nil
}

// darwinTerminalArgv opens Terminal.app via osascript. The command is escaped
// for the AppleScript string literal context (a raw newline would end the
// literal and let the remainder run as a second AppleScript statement).
func darwinTerminalArgv(cmdStr string) []string {
	escaped := strings.ReplaceAll(cmdStr, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, "\n", `\n`)
	escaped = strings.ReplaceAll(escaped, "\r", `\r`)
	script := fmt.Sprintf(`tell application "Terminal" to do script "%s"`, escaped) +
		"\n" + `tell application "Terminal" to activate`
	return []string{"osascript", "-e", script}
}

// linuxEmulators is the probe order when $TERMINAL is unset. x-terminal-emulator
// (the Debian alternatives symlink honoring the user's system-wide choice)
// first, then the common emulators. Each entry knows how to run a command:
// most take `-e cmd args...`, gnome-terminal wants `--`, wezterm wants
// `start --`, and kitty/foot take the command directly.
var linuxEmulators = []struct {
	bin     string
	runArgs func(shellArgv []string) []string
}{
	{"x-terminal-emulator", func(a []string) []string { return append([]string{"-e"}, a...) }},
	{"gnome-terminal", func(a []string) []string { return append([]string{"--"}, a...) }},
	{"konsole", func(a []string) []string { return append([]string{"-e"}, a...) }},
	{"xfce4-terminal", func(a []string) []string { return append([]string{"-x"}, a...) }},
	{"alacritty", func(a []string) []string { return append([]string{"-e"}, a...) }},
	{"kitty", func(a []string) []string { return a }},
	{"foot", func(a []string) []string { return a }},
	{"wezterm", func(a []string) []string { return append([]string{"start", "--"}, a...) }},
	{"xterm", func(a []string) []string { return append([]string{"-e"}, a...) }},
}

// linuxTerminalArgv resolves the user's terminal emulator: $TERMINAL wins (the
// user's explicit choice, assumed to accept `-e cmd...` - the de-facto
// convention), then the probe table. The command runs as
// `sh -c "<cmd>; exec ${SHELL:-sh}"` so the window stays interactive after
// kubectl exits instead of vanishing with its output.
func linuxTerminalArgv(lookPath func(string) (string, error), getenv func(string) string, cmdStr string) ([]string, error) {
	shellArgv := []string{}
	if cmdStr != "" {
		shellArgv = []string{"sh", "-c", cmdStr + `; exec "${SHELL:-sh}"`}
	}

	if t := strings.TrimSpace(getenv("TERMINAL")); t != "" {
		if path, err := lookPath(t); err == nil {
			if len(shellArgv) == 0 {
				return []string{path}, nil
			}
			return append([]string{path, "-e"}, shellArgv...), nil
		}
	}
	for _, e := range linuxEmulators {
		path, err := lookPath(e.bin)
		if err != nil {
			continue
		}
		if len(shellArgv) == 0 {
			return []string{path}, nil
		}
		return append([]string{path}, e.runArgs(shellArgv)...), nil
	}
	return nil, fmt.Errorf("no terminal emulator found (set $TERMINAL, or install one of gnome-terminal/konsole/alacritty/kitty/xterm) - use copy command")
}

// windowsTerminalArgv runs the command in PowerShell (pwsh when installed,
// else the always-present Windows PowerShell) in a NEW console window with
// that console's own stdio (startInNewConsole) - Windows Terminal hosts it
// when it is the default terminal. -NoExit keeps the window
// open after kubectl exits, so an error stays readable.
//
// Review 2026-10-03 #9: the old path went through cmd.exe (`wt cmd /k` or `cmd
// /c start ... cmd /k`), whose quoting could not carry the exec shell
// expression (`command -v bash >/dev/null && exec bash || exec sh`), so every
// exec was refused; and Go's argv escaping (\") is not cmd.exe's, so even a
// quoted path with a space split. PowerShell single-quoted strings are
// literal: no expansion, and & | > ; are inert, with one escape rule.
func windowsTerminalArgv(lookPath func(string) (string, error), argv []string) ([]string, error) {
	ps := ""
	for _, bin := range []string{"pwsh", "powershell"} {
		if p, err := lookPath(bin); err == nil {
			ps = p
			break
		}
	}
	if ps == "" {
		return nil, fmt.Errorf("PowerShell not found - use copy command")
	}
	if len(argv) == 0 {
		return []string{ps, "-NoLogo"}, nil
	}
	script, err := powershellCommand(argv)
	if err != nil {
		return nil, err
	}
	return []string{ps, "-NoLogo", "-NoExit", "-Command", script}, nil
}

// powershellCommand renders argv as a PowerShell invocation: the call operator
// and every argument single-quoted (`& 'kubectl' '--context' 'x' ...`). Quoting
// every argument also keeps a bare `--` from being consumed by PowerShell's own
// parser. Fails closed on control characters and double quotes, which no
// Windows command line carries reliably (Kubernetes names, contexts and paths
// never contain them).
func powershellCommand(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	parts := make([]string, 0, len(argv)+1)
	parts = append(parts, "&")
	for _, arg := range argv {
		q, err := psQuote(arg)
		if err != nil {
			return "", err
		}
		parts = append(parts, q)
	}
	return strings.Join(parts, " "), nil
}

// psQuote renders s as a PowerShell single-quoted string. Its content is
// literal; a single quote is escaped by doubling - and PowerShell treats the
// typographic single quotes (U+2018-U+201B) as quote characters too, so those
// are doubled as well.
func psQuote(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			return "", fmt.Errorf("argument %q contains a control character - use copy command", s)
		case r == '"':
			return "", fmt.Errorf("argument %q contains a double quote, which a Windows command line cannot carry safely", s)
		case r == '\'' || (r >= 0x2018 && r <= 0x201b):
			b.WriteRune(r)
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String(), nil
}

// launchTerminal resolves and starts the platform terminal for argv (empty argv
// = a plain window). Fire-and-forget: the launcher child is reaped by a
// goroutine while the terminal itself lives on detached.
func launchTerminal(argv []string) ActionResultDTO {
	launch, newConsole, err := terminalLaunchArgv(hostGOOS, exec.LookPath, os.Getenv, argv)
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	if newConsole {
		// Not os/exec: it would hand the child NUL standard handles that
		// Windows keeps even in the new console (startInNewConsole).
		if err := startInNewConsole(launch); err != nil {
			return ActionResultDTO{Error: "open terminal: " + err.Error()}
		}
		return ActionResultDTO{OK: true}
	}
	cmd := exec.Command(launch[0], launch[1:]...) //nolint:gosec // argv built internally from validated parts
	if err := cmd.Start(); err != nil {
		return ActionResultDTO{Error: "open terminal: " + err.Error()}
	}
	go func() { _ = cmd.Wait() }() // reap the launcher; the terminal is detached
	return ActionResultDTO{OK: true}
}
