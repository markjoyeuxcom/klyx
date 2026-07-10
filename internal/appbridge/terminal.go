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
// terminalLaunchArgv is the PURE resolver: given the platform, a lookPath, the
// env, and the (POSIX-quoted) command string, it returns the argv that opens a
// terminal running the command - or an honest error naming the fallback (copy
// the command). cmdStr=="" means "open a plain interactive terminal".
func terminalLaunchArgv(goos string, lookPath func(string) (string, error), getenv func(string) string, cmdStr string) ([]string, error) {
	switch goos {
	case "darwin":
		return darwinTerminalArgv(cmdStr), nil
	case "linux":
		return linuxTerminalArgv(lookPath, getenv, cmdStr)
	case "windows":
		return windowsTerminalArgv(lookPath, cmdStr)
	default:
		return nil, fmt.Errorf("open-terminal not supported on %s - use copy command", goos)
	}
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

// windowsTerminalArgv prefers Windows Terminal (wt) and falls back to a classic
// `cmd /c start` window. cmd.exe quoting is not POSIX: the incoming cmdStr is
// rebuilt by the caller with windowsQuoteArgv, which fails closed on arguments
// cmd.exe cannot carry safely.
func windowsTerminalArgv(lookPath func(string) (string, error), cmdStr string) ([]string, error) {
	if wt, err := lookPath("wt"); err == nil {
		if cmdStr == "" {
			return []string{wt}, nil
		}
		return []string{wt, "cmd", "/k", cmdStr}, nil
	}
	// `start` needs an explicit (empty) title argument or it eats the first
	// quoted string as the window title.
	if cmdStr == "" {
		return []string{"cmd", "/c", "start", "", "cmd"}, nil
	}
	return []string{"cmd", "/c", "start", "", "cmd", "/k", cmdStr}, nil
}

// windowsQuoteArgv joins argv for cmd.exe: bare when simple, double-quoted when
// containing spaces, and FAILS CLOSED on cmd metacharacters - kubernetes names
// and kubeconfig contexts never legitimately contain them, so refusing beats
// shipping a command-splitting bug on the platform with the messiest quoting.
func windowsQuoteArgv(argv []string) (string, error) {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		if strings.ContainsAny(arg, `"^&|<>%!`) {
			return "", fmt.Errorf("argument %q contains characters unsafe for cmd.exe - use copy command", arg)
		}
		if strings.ContainsAny(arg, " \t") {
			parts[i] = `"` + arg + `"`
		} else {
			parts[i] = arg
		}
	}
	return strings.Join(parts, " "), nil
}

// launchTerminal resolves and starts the platform terminal for argv (empty argv
// = a plain window). Fire-and-forget: the launcher child is reaped by a
// goroutine while the terminal itself lives on detached.
func launchTerminal(argv []string) ActionResultDTO {
	var cmdStr string
	var err error
	if len(argv) > 0 {
		if runtime.GOOS == "windows" {
			cmdStr, err = windowsQuoteArgv(argv)
		} else {
			cmdStr = shellQuoteArgv(argv)
		}
		if err != nil {
			return ActionResultDTO{Error: err.Error()}
		}
	}
	launch, err := terminalLaunchArgv(runtime.GOOS, exec.LookPath, os.Getenv, cmdStr)
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	cmd := exec.Command(launch[0], launch[1:]...) //nolint:gosec // argv built internally from validated parts
	if err := cmd.Start(); err != nil {
		return ActionResultDTO{Error: "open terminal: " + err.Error()}
	}
	go func() { _ = cmd.Wait() }() // reap the launcher; the terminal is detached
	return ActionResultDTO{OK: true}
}
