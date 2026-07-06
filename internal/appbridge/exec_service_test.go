package appbridge

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// fakeExecConn implements ExecConn for testing.
type fakeExecConn struct {
	kubeContext string
	// describeCmd, when set, is returned by DescribeCmd (a real innocuous
	// process the service can actually run); describeErr wins if set.
	describeCmd func(ctx context.Context) *exec.Cmd
	describeErr error
	// gotDescribe records the arguments DescribeCmd was called with.
	gotDescribe []string
}

func (f *fakeExecConn) DescribeCmd(ctx context.Context, kindArg, namespace, name string) (*exec.Cmd, error) {
	f.gotDescribe = []string{kindArg, namespace, name}
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	if f.describeCmd != nil {
		return f.describeCmd(ctx), nil
	}
	return exec.CommandContext(ctx, "true"), nil
}

func (f *fakeExecConn) DebugCommand(namespace, pod, container string) ([]string, error) {
	if f.kubeContext == "" {
		return nil, errEmptyContext
	}
	argv := []string{"kubectl", "--context", f.kubeContext, "-n", namespace, "debug", "-it", pod, "--image=busybox:1.36"}
	if container != "" {
		argv = append(argv, "--target="+container)
	}
	return append(argv, "--", "sh"), nil
}

func (f *fakeExecConn) ExecCommand(namespace, pod, container string) ([]string, error) {
	// Reproduce the same logic as ClusterConn.ExecCommand without importing fleet.
	if f.kubeContext == "" {
		return nil, errEmptyContext
	}
	argv := []string{
		"kubectl",
		"--context", f.kubeContext,
		"-n", namespace,
		"exec", "-it", pod,
	}
	if container != "" {
		argv = append(argv, "-c", container)
	}
	argv = append(argv,
		"--",
		"/bin/sh", "-c",
		"command -v bash >/dev/null && exec bash || exec sh",
	)
	return argv, nil
}

// errEmptyContext is a sentinel used in fakeExecConn.
var errEmptyContext = &emptyContextError{}

type emptyContextError struct{}

func (e *emptyContextError) Error() string { return "cluster has no kubeconfig context" }

// --- shellQuoteArg unit tests ---

func TestShellQuoteArg(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Plain identifiers: no quoting needed.
		{"kubectl", "kubectl"},
		{"--context", "--context"},
		{"prod-aks", "prod-aks"},
		{"exec", "exec"},
		{"-it", "-it"},
		// Arguments with spaces must be quoted.
		{"hello world", "'hello world'"},
		// Arguments with $ must be quoted.
		{"$HOME", "'$HOME'"},
		// The shell expression with special chars must be quoted.
		{
			"command -v bash >/dev/null && exec bash || exec sh",
			"'command -v bash >/dev/null && exec bash || exec sh'",
		},
		// Embedded single quote -> '\'' escape.
		{"it's", `'it'\''s'`},
		// Multiple special chars.
		{"a|b;c", "'a|b;c'"},
		// Allowlist quoting: anything outside the safe set is wrapped, not just
		// a known-special denylist. A bare newline would otherwise split the
		// command string into a second shell command.
		{"x\nreboot", "'x\nreboot'"},
		{"x\rreboot", "'x\rreboot'"},
		{`back\slash`, `'back\slash'`},
		{"tilde~", "'tilde~'"},
		{"paren(s)", "'paren(s)'"},
		{"", "''"},
		// Safe punctuation stays bare (flags, paths, images, timeouts).
		{"--timeout=120s", "--timeout=120s"},
		{"--image=busybox:1.36", "--image=busybox:1.36"},
		{"/bin/sh", "/bin/sh"},
	}
	for _, tc := range cases {
		got := shellQuoteArg(tc.in)
		if got != tc.want {
			t.Errorf("shellQuoteArg(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// --- ExecService.GetExecCommand unit tests ---

func makeExecService(ctx string) *ExecService {
	conn := &fakeExecConn{kubeContext: ctx}
	return NewExecService(func(cluster string) (ExecConn, bool) {
		if cluster == "prod" {
			return conn, true
		}
		return nil, false
	})
}

func TestGetExecCommand_WithContainer(t *testing.T) {
	svc := makeExecService("prod-aks")
	dto := svc.GetExecCommand("prod", "default", "web-abc", "web")
	if dto.Error != "" {
		t.Fatalf("unexpected error: %s", dto.Error)
	}
	wantArgv := []string{
		"kubectl", "--context", "prod-aks",
		"-n", "default",
		"exec", "-it", "web-abc",
		"-c", "web",
		"--",
		"/bin/sh", "-c",
		"command -v bash >/dev/null && exec bash || exec sh",
	}
	if len(dto.Argv) != len(wantArgv) {
		t.Fatalf("argv length: got %d want %d", len(dto.Argv), len(wantArgv))
	}
	for i, w := range wantArgv {
		if dto.Argv[i] != w {
			t.Errorf("argv[%d]: got %q want %q", i, dto.Argv[i], w)
		}
	}
	// Command string must contain the quoted shell expression.
	if !strings.Contains(dto.Command, "kubectl") {
		t.Error("Command should contain 'kubectl'")
	}
	// The shell expression contains special chars — must be quoted in Command.
	if !strings.Contains(dto.Command, "'command -v bash") {
		t.Errorf("expected shell expression to be quoted in Command, got: %s", dto.Command)
	}
}

func TestGetExecCommand_WithoutContainer(t *testing.T) {
	svc := makeExecService("prod-aks")
	dto := svc.GetExecCommand("prod", "monitoring", "grafana-xyz", "")
	if dto.Error != "" {
		t.Fatalf("unexpected error: %s", dto.Error)
	}
	// The "-c <container>" pair must be absent. We look for "-c" that is followed
	// by something OTHER than "--" (the separator). The only valid "-c" in the
	// argv is the one after "--" used for /bin/sh, not the container selector.
	seenSep := false
	for i, arg := range dto.Argv {
		if arg == "--" {
			seenSep = true
			continue
		}
		if arg == "-c" && !seenSep {
			t.Errorf("argv[%d] = -c before '--', but container was empty — container selector should be omitted", i)
		}
	}
}

func TestGetExecCommand_ClusterMiss(t *testing.T) {
	svc := makeExecService("prod-aks")
	dto := svc.GetExecCommand("unknown", "default", "pod-abc", "")
	if dto.Error == "" {
		t.Fatal("expected error for unknown cluster, got none")
	}
	if !strings.Contains(dto.Error, "cluster not connected") {
		t.Errorf("error should mention cluster not connected, got: %s", dto.Error)
	}
}

func TestGetExecCommand_EmptyContext(t *testing.T) {
	svc := makeExecService("") // empty context
	dto := svc.GetExecCommand("prod", "default", "pod-abc", "app")
	if dto.Error == "" {
		t.Fatal("expected error for empty context, got none")
	}
}

// --- shellQuoteArgv integration: check the full command string ---

func TestShellQuoteArgv_FullCommand(t *testing.T) {
	argv := []string{
		"kubectl",
		"--context", "prod-aks",
		"-n", "default",
		"exec", "-it", "web-abc",
		"-c", "web",
		"--",
		"/bin/sh", "-c",
		"command -v bash >/dev/null && exec bash || exec sh",
	}
	got := shellQuoteArgv(argv)

	// Plain args must appear unquoted.
	if !strings.Contains(got, "kubectl ") {
		t.Error("expected 'kubectl' unquoted at start")
	}
	// /bin/sh contains a slash, no special chars — unquoted.
	if !strings.Contains(got, "/bin/sh") {
		t.Error("expected /bin/sh unquoted")
	}
	// Shell expression must be single-quoted.
	if !strings.Contains(got, "'command -v bash >/dev/null && exec bash || exec sh'") {
		t.Errorf("expected shell expression single-quoted, got: %s", got)
	}
}

// --- ExecService.Describe unit tests ---

func TestDescribe_Success(t *testing.T) {
	conn := &fakeExecConn{
		kubeContext: "ctx",
		describeCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "echo 'Name:  web-abc'; echo 'ignored warning' >&2")
		},
	}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe("prod", "pod", "default", "web-abc")
	if !r.OK || r.Error != "" {
		t.Fatalf("expected ok, got ok=%v error=%q", r.OK, r.Error)
	}
	if r.Output != "Name:  web-abc" {
		t.Errorf("output: got %q", r.Output)
	}
	// stderr noise (deprecation warnings) must NOT corrupt the report text
	if strings.Contains(r.Output, "ignored warning") {
		t.Errorf("stderr leaked into output: %q", r.Output)
	}
	want := []string{"pod", "default", "web-abc"}
	for i, w := range want {
		if conn.gotDescribe[i] != w {
			t.Errorf("DescribeCmd arg[%d]: got %q want %q", i, conn.gotDescribe[i], w)
		}
	}
}

func TestDescribe_NonZeroExitReportsStderr(t *testing.T) {
	conn := &fakeExecConn{
		kubeContext: "ctx",
		describeCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "echo 'partial output'; echo 'Error from server (NotFound)' >&2; exit 1")
		},
	}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe("prod", "pod", "default", "gone")
	if r.OK {
		t.Fatal("expected failure")
	}
	if !strings.Contains(r.Error, "NotFound") {
		t.Errorf("error should carry stderr, got %q", r.Error)
	}
	// partial stdout still comes back so the pane can show what kubectl printed
	if r.Output != "partial output" {
		t.Errorf("output: got %q", r.Output)
	}
}

func TestDescribe_CmdBuildErrorIsHonest(t *testing.T) {
	conn := &fakeExecConn{kubeContext: "ctx", describeErr: errEmptyContext}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe("prod", "pod", "default", "web-abc")
	if r.OK || r.Error == "" {
		t.Fatalf("expected error result, got ok=%v error=%q", r.OK, r.Error)
	}
}

func TestDescribe_ClusterMiss(t *testing.T) {
	svc := NewExecService(func(string) (ExecConn, bool) { return nil, false })
	r := svc.Describe("nope", "pod", "default", "web-abc")
	if r.OK || !strings.Contains(r.Error, "cluster not connected") {
		t.Fatalf("expected cluster-miss error, got ok=%v error=%q", r.OK, r.Error)
	}
}

func TestGetDebugCommand(t *testing.T) {
	s := NewExecService(func(string) (ExecConn, bool) {
		return &fakeExecConn{kubeContext: "ctx"}, true
	})
	dto := s.GetDebugCommand("c", "cert-manager", "cm-abc", "cert-manager-controller")
	if dto.Error != "" {
		t.Fatalf("unexpected error: %s", dto.Error)
	}
	want := "kubectl --context ctx -n cert-manager debug -it cm-abc --image=busybox:1.36 --target=cert-manager-controller -- sh"
	if dto.Command != want {
		t.Fatalf("command:\n got %q\nwant %q", dto.Command, want)
	}

	miss := NewExecService(func(string) (ExecConn, bool) { return nil, false })
	if d := miss.GetDebugCommand("nope", "ns", "p", "c"); d.Error == "" {
		t.Fatal("cluster miss must error")
	}
}
