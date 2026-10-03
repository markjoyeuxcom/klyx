package appbridge

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecConn is the per-cluster surface ExecService needs (lookup seam).
type ExecConn interface {
	ExecCommand(namespace, pod, container string) ([]string, error)
	DebugCommand(namespace, pod, container string) ([]string, error)
	DescribeCmd(ctx context.Context, kindArg, namespace, name string) (*exec.Cmd, error)
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

// ExecService is bound to JS - the kubectl escape-hatch verbs:
//   - GetExecCommand: returns the kubectl exec argv and a display/copy command string.
//   - OpenExecTerminal: launches the OS terminal running the kubectl exec command.
//   - Describe: runs kubectl describe and returns the output (read-only).
type ExecService struct {
	lookup func(string) (ExecConn, bool)
}

// NewExecService creates an ExecService with the given cluster-lookup function.
func NewExecService(lookup func(string) (ExecConn, bool)) *ExecService {
	return &ExecService{lookup: lookup}
}

// GetExecCommand returns the kubectl exec argv and the copyable command for the
// host's default shell (commandDTO) for the given cluster/namespace/pod/
// container. container may be "" (omitted).
func (s *ExecService) GetExecCommand(cluster, ns, pod, container string) ExecCommandDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ExecCommandDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.ExecCommand(ns, pod, container)
	if err != nil {
		return ExecCommandDTO{Error: err.Error()}
	}
	return commandDTO(argv)
}

// commandDTO pairs argv with the copyable command for the host's default
// shell (displayCommand: PowerShell on Windows, POSIX elsewhere).
func commandDTO(argv []string) ExecCommandDTO {
	cmd, err := displayCommand(hostGOOS, argv)
	if err != nil {
		return ExecCommandDTO{Error: err.Error()}
	}
	return ExecCommandDTO{Command: cmd, Argv: argv}
}

// OpenExecTerminal launches the OS terminal with the kubectl exec command -
// Terminal.app on macOS, the user's emulator ($TERMINAL / x-terminal-emulator /
// the common set) on Linux, PowerShell in a new console on Windows. When
// no terminal can be found the error says to use the copy-command affordance.
func (s *ExecService) OpenExecTerminal(cluster, ns, pod, container string) ActionResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.ExecCommand(ns, pod, container)
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}

	return launchTerminal(argv)
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
	return commandDTO(argv)
}

// OpenDebugTerminal launches the OS terminal running kubectl debug against the
// pod. Same platform behavior as OpenExecTerminal.
//
// Unlike exec, kubectl debug MUTATES the pod: it adds an ephemeral container
// to the pod spec that Kubernetes cannot remove until the pod is recreated. So
// it consumes the protected-cluster grant like every other mutating verb - the
// frontend arms it through the shared ConfirmDialog. GetDebugCommand (copy the
// command) stays ungated: copying runs nothing.
func (s *ExecService) OpenDebugTerminal(cluster, ns, pod, container string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.DebugCommand(ns, pod, container)
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return launchTerminal(argv)
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

// DescribeResultDTO is the outcome of a kubectl describe run. Output carries
// whatever kubectl printed to stdout (possibly partial on a failed run);
// Error is set when the run could not happen or exited non-zero.
type DescribeResultDTO struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

// describeTimeout bounds one kubectl describe run. Same 30s budget as the
// other request/response verbs.
const describeTimeout = 30 * time.Second

// Describe runs `kubectl describe <kindArg> <name>` against the cluster's
// kubeconfig context and returns the raw output. Read-only: describe never
// mutates, so no protected-cluster gate applies. kindArg is "pod"/"node" for
// the core panels and plural.group (plural alone when group is "") for typed
// instances; the fleet layer validates every argument and resolves the
// kubectl binary, so a missing kubectl comes back as an actionable Error -
// never as silently empty output.
func (s *ExecService) Describe(cluster, kindArg, namespace, name string) DescribeResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return DescribeResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), describeTimeout)
	defer cancel()
	cmd, err := conn.DescribeCmd(ctx, kindArg, namespace, name)
	if err != nil {
		return DescribeResultDTO{Error: err.Error()}
	}
	// Stdout and stderr stay separate: describe writes the report to stdout and
	// real errors to stderr. Combining them would let a deprecation warning
	// corrupt the report text.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out := strings.TrimRight(stdout.String(), "\n")
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() == context.DeadlineExceeded {
			msg = fmt.Sprintf("kubectl describe timed out after %s", describeTimeout)
		} else if msg == "" {
			msg = runErr.Error()
		}
		return DescribeResultDTO{Output: out, Error: msg}
	}
	return DescribeResultDTO{OK: true, Output: out}
}

// OpenTerminal opens a plain OS terminal window (no command) - the sidebar's
// external-terminal escape hatch. Same platform behavior as OpenExecTerminal.
func (s *ExecService) OpenTerminal() ActionResultDTO {
	return launchTerminal(nil)
}
