package appbridge

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// ExecConn is the per-cluster surface ExecService needs (lookup seam).
type ExecConn interface {
	ExecCommand(namespace, pod, container string) ([]string, error)
	DebugCommand(namespace, pod, container string) ([]string, error)
}

// ExecCommandDTO is the response from GetExecCommand.
type ExecCommandDTO struct {
	// Command is the display/copy string: argv joined with shell quoting applied
	// to any argument that contains shell-special characters.
	Command string `json:"command"`
	// Argv is the raw unquoted argument vector, suitable for os/exec.Command.
	Argv  []string `json:"argv"`
	Error string   `json:"error"`
}

// ExecService is bound to JS. It provides two operations:
//   - GetExecCommand: returns the kubectl exec argv and a display/copy command string.
//   - OpenExecTerminal: launches the OS terminal running the kubectl exec command.
type ExecService struct {
	lookup func(string) (ExecConn, bool)
}

// NewExecService creates an ExecService with the given cluster-lookup function.
func NewExecService(lookup func(string) (ExecConn, bool)) *ExecService {
	return &ExecService{lookup: lookup}
}

// GetExecCommand returns the kubectl exec argv and a shell-quoted display command
// for the given cluster/namespace/pod/container. container may be "" (omitted).
func (s *ExecService) GetExecCommand(cluster, ns, pod, container string) ExecCommandDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ExecCommandDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.ExecCommand(ns, pod, container)
	if err != nil {
		return ExecCommandDTO{Error: err.Error()}
	}
	return ExecCommandDTO{
		Command: shellQuoteArgv(argv),
		Argv:    argv,
	}
}

// OpenExecTerminal launches the OS terminal with the kubectl exec command.
// On macOS it uses osascript to open Terminal.app; on other platforms it
// returns an error asking the user to copy and paste the command.
//
// The process is started but not waited on (fire-and-forget). A goroutine
// calls cmd.Wait() to reap the osascript child and avoid zombies; the
// terminal process itself is detached and lives beyond osascript's exit.
func (s *ExecService) OpenExecTerminal(cluster, ns, pod, container string) ActionResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.ExecCommand(ns, pod, container)
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}

	return openCommandInTerminal(argv)
}

// GetDebugCommand returns the kubectl debug argv (ephemeral busybox container,
// process-namespace-targeted at the given container) and its display string -
// the escape hatch for distroless images where exec has no shell to run.
func (s *ExecService) GetDebugCommand(cluster, ns, pod, container string) ExecCommandDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ExecCommandDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.DebugCommand(ns, pod, container)
	if err != nil {
		return ExecCommandDTO{Error: err.Error()}
	}
	return ExecCommandDTO{Command: shellQuoteArgv(argv), Argv: argv}
}

// OpenDebugTerminal launches the OS terminal running kubectl debug against the
// pod. Same platform constraint as OpenExecTerminal (macOS only for now).
func (s *ExecService) OpenDebugTerminal(cluster, ns, pod, container string) ActionResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.DebugCommand(ns, pod, container)
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return openCommandInTerminal(argv)
}

// openCommandInTerminal runs argv in a new Terminal.app window via osascript.
// The process is started but not waited on (fire-and-forget); a goroutine
// reaps the osascript child while the terminal lives on detached.
func openCommandInTerminal(argv []string) ActionResultDTO {
	if runtime.GOOS != "darwin" {
		return ActionResultDTO{Error: "open-terminal not supported on this platform yet - use copy command"}
	}

	// Build the AppleScript. The inner do script argument is the quoted kubectl
	// command; we escape double quotes for the AppleScript string context.
	cmdStr := shellQuoteArgv(argv)
	// Escape backslashes, double quotes, and line breaks for embedding in an
	// AppleScript string literal - a raw newline would end the literal and let
	// the remainder run as a second AppleScript statement.
	escaped := strings.ReplaceAll(cmdStr, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, "\n", `\n`)
	escaped = strings.ReplaceAll(escaped, "\r", `\r`)
	script := fmt.Sprintf(
		`tell application "Terminal" to do script "%s"`,
		escaped,
	) + "\n" +
		`tell application "Terminal" to activate`

	cmd := exec.Command("osascript", "-e", script) //nolint:gosec // argv built internally
	if err := cmd.Start(); err != nil {
		return ActionResultDTO{Error: "open terminal: " + err.Error()}
	}
	// Reap the osascript child to avoid zombies. Terminal itself is detached.
	go func() { _ = cmd.Wait() }()

	return ActionResultDTO{OK: true}
}

// shellQuoteArgv joins argv into a single string, single-quoting any argument
// that is not provably safe to leave bare.
func shellQuoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = shellQuoteArg(arg)
	}
	return strings.Join(parts, " ")
}

// shellSafe reports whether every rune of arg is in the conservative allowlist
// of characters that never need quoting in a POSIX shell. An allowlist, not a
// special-character denylist: a denylist that forgets one character (newline,
// backslash, ~) turns into a command-splitting bug; an allowlist fails closed.
func shellSafe(arg string) bool {
	if arg == "" {
		return false // an empty argument only survives as ''
	}
	for _, ch := range arg {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case strings.ContainsRune("@%_+=:,./-", ch):
		default:
			return false
		}
	}
	return true
}

func shellQuoteArg(arg string) string {
	if shellSafe(arg) {
		return arg
	}
	// Single-quote the whole argument; escape embedded single quotes as '\''.
	// Newlines and every other character are literal inside single quotes.
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// OpenTerminal opens a plain OS terminal window (no command) - the sidebar's
// external-terminal escape hatch. macOS only for now, same constraint as
// OpenExecTerminal.
func (s *ExecService) OpenTerminal() ActionResultDTO {
	if runtime.GOOS != "darwin" {
		return ActionResultDTO{Error: "open-terminal not supported on this platform yet"}
	}
	script := `tell application "Terminal" to do script ""` + "\n" +
		`tell application "Terminal" to activate`
	cmd := exec.Command("osascript", "-e", script) //nolint:gosec // fixed script
	if err := cmd.Start(); err != nil {
		return ActionResultDTO{Error: "open terminal: " + err.Error()}
	}
	go func() { _ = cmd.Wait() }()
	return ActionResultDTO{OK: true}
}
