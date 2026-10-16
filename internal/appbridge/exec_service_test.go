package appbridge

import (
	"context"
	"errors"
	"fmt"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
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
	// describeArgvErr, when set, is DescribeArgv's refusal.
	describeArgvErr error
	armErr          error    // CheckPodArmed's answer
	armChecks       []string // ns/name@uid the arm check was asked about
	// kindGroup/kindName/kindErr are DescribeTarget's answer (coverage S0b);
	// the default is a core Pod. snaps are DescribeSnapshot's answers, in
	// order.
	kindGroup, kindName string
	kindErr             error
	snaps               []fakeSnap
}

type fakeSnap struct {
	uid, rv string
	payload bool
	err     error
}

func (f *fakeExecConn) DescribeTarget(_ context.Context, kindArg string) (schema.GroupVersionResource, schema.GroupVersionKind, error) {
	if f.kindErr != nil {
		return schema.GroupVersionResource{}, schema.GroupVersionKind{}, f.kindErr
	}
	if f.kindName == "" {
		return schema.GroupVersionResource{Version: "v1", Resource: "pods"}, schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, nil
	}
	return schema.GroupVersionResource{Group: f.kindGroup, Version: "v1", Resource: "x"}, schema.GroupVersionKind{Group: f.kindGroup, Version: "v1", Kind: f.kindName}, nil
}

func (f *fakeExecConn) DescribeSnapshot(context.Context, schema.GroupVersionResource, schema.GroupVersionKind, string, string) (string, string, bool, error) {
	if len(f.snaps) == 0 {
		return "", "", false, errors.New("no snapshot scripted")
	}
	s := f.snaps[0]
	f.snaps = f.snaps[1:]
	return s.uid, s.rv, s.payload, s.err
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

func (f *fakeExecConn) DescribeArgv(kindArg, namespace, name string) ([]string, error) {
	if f.describeArgvErr != nil {
		return nil, f.describeArgvErr
	}
	return []string{"/fake/kubectl", "--kubeconfig", "/k/my config", "--context", f.kubeContext, "describe", kindArg, "-n", namespace, "--", name}, nil
}

func (f *fakeExecConn) CheckPodArmed(_ context.Context, namespace, name, uid string) error {
	f.armChecks = append(f.armChecks, namespace+"/"+name+"@"+uid)
	return f.armErr
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
	r := svc.Describe(context.Background(), "prod", "pod", "default", "web-abc")
	if !r.OK || r.Error != "" {
		t.Fatalf("expected ok, got ok=%v error=%q", r.OK, r.Error)
	}
	// kubectl's stdout exactly, trailing newline and all (slice 14 review 1)
	if r.Output != "Name:  web-abc\n" {
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
	r := svc.Describe(context.Background(), "prod", "pod", "default", "gone")
	if r.OK {
		t.Fatal("expected failure")
	}
	if !strings.Contains(r.Error, "NotFound") {
		t.Errorf("error should carry stderr, got %q", r.Error)
	}
	// partial stdout still comes back so the pane can show what kubectl printed
	if r.Output != "partial output\n" {
		t.Errorf("output: got %q", r.Output)
	}
}

func TestDescribe_CmdBuildErrorIsHonest(t *testing.T) {
	conn := &fakeExecConn{kubeContext: "ctx", describeErr: errEmptyContext}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe(context.Background(), "prod", "pod", "default", "web-abc")
	if r.OK || r.Error == "" {
		t.Fatalf("expected error result, got ok=%v error=%q", r.OK, r.Error)
	}
}

func TestDescribe_ClusterMiss(t *testing.T) {
	svc := NewExecService(func(string) (ExecConn, bool) { return nil, false })
	r := svc.Describe(context.Background(), "nope", "pod", "default", "web-abc")
	if r.OK || !strings.Contains(r.Error, "cluster not connected") {
		t.Fatalf("expected cluster-miss error, got ok=%v error=%q", r.OK, r.Error)
	}
}

func TestDescribe_KeepsStdoutExactly(t *testing.T) {
	conn := &fakeExecConn{
		kubeContext: "ctx",
		describeCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "printf", "Name:  a\r\nEvents:  <none>\n\n\n")
		},
	}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe(context.Background(), "prod", "pod", "default", "a")
	if r.Output != "Name:  a\r\nEvents:  <none>\n\n\n" {
		t.Errorf("output must be stdout unchanged, got %q", r.Output)
	}
}

func TestDescribe_CommandPinsTheKubeconfig(t *testing.T) {
	prev := hostGOOS
	t.Cleanup(func() { hostGOOS = prev })
	hostGOOS = "darwin"
	conn := &fakeExecConn{kubeContext: "shared"}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe(context.Background(), "prod", "gateways.gateway.networking.k8s.io", "infra", "internal")
	want := "/fake/kubectl --kubeconfig '/k/my config' --context shared describe gateways.gateway.networking.k8s.io -n infra -- internal"
	if r.Command != want || r.CommandError != "" {
		t.Errorf("command: got %q (error %q), want %q", r.Command, r.CommandError, want)
	}

	hostGOOS = "windows"
	r = svc.Describe(context.Background(), "prod", "pod", "default", "web")
	if !strings.HasPrefix(r.Command, "& '/fake/kubectl' '--kubeconfig' '/k/my config'") {
		t.Errorf("windows command: got %q", r.Command)
	}
}

func TestDescribe_CommandRefusalIsSaid(t *testing.T) {
	conn := &fakeExecConn{kubeContext: "ctx", describeArgvErr: fmt.Errorf("cluster kubeconfig is a path list, which cannot be passed to a Windows terminal: point kubeconfig at a single file")}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe(context.Background(), "prod", "pod", "default", "web")
	if !r.OK || r.Command != "" || !strings.Contains(r.CommandError, "path list") {
		t.Errorf("want the report with a command refusal, got ok=%v command=%q commandError=%q", r.OK, r.Command, r.CommandError)
	}
}

func TestDescribe_CancelStopsTheRun(t *testing.T) {
	conn := &fakeExecConn{
		kubeContext: "ctx",
		describeCmd: func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "sleep", "20") },
	}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	r := svc.Describe(ctx, "prod", "pod", "default", "web")
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("a cancelled describe must stop kubectl, took %s", took)
	}
	if r.OK || r.Error != "kubectl describe was cancelled" {
		t.Errorf("want the cancellation said, got ok=%v error=%q", r.OK, r.Error)
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

// Review 2026-10-03 (finding A): kubectl debug permanently adds an ephemeral
// container to the pod, so on a protected cluster the launch must consume the
// typed-confirmation grant like every other mutating verb. Copying the command
// runs nothing and stays ungated.
func TestOpenDebugTerminal_ProtectedClusterNeedsGrant(t *testing.T) {
	now := time.Now()
	SetProtectGuard(newTestGuard("prd-cluster", &now))
	t.Cleanup(func() { SetProtectGuard(nil) })

	looked := false
	svc := NewExecService(func(string) (ExecConn, bool) {
		looked = true
		return &fakeExecConn{kubeContext: "prd"}, true
	})

	r := svc.OpenDebugTerminal("prd-cluster", "ns", "pod", "uid-pod", "app")
	if r.OK || !strings.Contains(r.Error, "not armed") {
		t.Fatalf("unarmed debug launch on a protected cluster must refuse, got %+v", r)
	}
	if looked {
		t.Fatal("the gate must refuse before resolving the cluster or building the argv")
	}

	if dto := svc.GetDebugCommand("prd-cluster", "ns", "pod", "app"); dto.Error != "" || dto.Command == "" {
		t.Fatalf("copying the debug command must stay ungated, got %+v", dto)
	}
}

// The debug shell mutates the pod (an ephemeral container): it is armed
// against the pod object, and a replaced or missing pod launches nothing.
func TestOpenDebugTerminal_RefusesAReplacedPod(t *testing.T) {
	conn := &fakeExecConn{kubeContext: "dev", armErr: fmt.Errorf("pod ns/web-0: %w", fleet.ErrReplaced)}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.OpenDebugTerminal("c", "ns", "web-0", "uid-old", "app")
	if r.OK || !strings.Contains(r.Error, "replaced") || !strings.Contains(r.Error, "nothing was started") {
		t.Fatalf("a replaced pod must launch nothing: %+v", r)
	}
	if len(conn.armChecks) != 1 || conn.armChecks[0] != "ns/web-0@uid-old" {
		t.Fatalf("the armed UID must be checked: %v", conn.armChecks)
	}
}

// Coverage S0b §4: Describe's two modes.
func TestDescribe_WithheldForPayloadKind(t *testing.T) {
	ran := false
	conn := &fakeExecConn{kubeContext: "kind-klyx", kindGroup: "fluxcd.controlplane.io", kindName: "ResourceSet",
		snaps: []fakeSnap{{uid: "u", rv: "1", payload: true}},
		describeCmd: func(ctx context.Context) *exec.Cmd {
			ran = true
			return exec.CommandContext(ctx, "echo", "PAYLOADmarker4X")
		}}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe(context.Background(), "c", "resourcesets.fluxcd.controlplane.io", "team-a", "with-secret")
	if r.Withheld == "" || r.Output != "" || r.Error != "" || r.OK {
		t.Fatalf("withheld: %+v", r)
	}
	if ran {
		t.Fatal("kubectl must not run for a withheld kind")
	}
	if r.Command == "" {
		t.Fatal("the copy command stays")
	}
}

func TestDescribe_WithheldWhenUnclassified(t *testing.T) {
	conn := &fakeExecConn{kubeContext: "kind-klyx", kindErr: errors.New("forbidden")}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	if r := svc.Describe(context.Background(), "c", "widgets", "ns", "w"); r.Withheld == "" || r.Output != "" {
		t.Fatalf("an unclassified resource is withheld: %+v", r)
	}
}

func TestDescribe_URLScrubbedBothStreams(t *testing.T) {
	script := `echo "Url: https://bob:PAYLOADmarker4X@github.com/org/repo"; echo "warn https://x:PAYLOADmarker4X@h/x" 1>&2; exit 1`
	conn := &fakeExecConn{kubeContext: "kind-klyx", kindGroup: "source.toolkit.fluxcd.io", kindName: "GitRepository",
		describeCmd: func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "sh", "-c", script) }}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	r := svc.Describe(context.Background(), "c", "gitrepositories.source.toolkit.fluxcd.io", "flux-system", "platform")
	if strings.Contains(r.Output, "PAYLOADmarker4X") || strings.Contains(r.Error, "PAYLOADmarker4X") {
		t.Fatalf("a credential crossed: %+v", r)
	}
	if !strings.Contains(r.Output, "https://github.com/org/repo") || !r.Scrubbed {
		t.Fatalf("scrubbed and labelled: %+v", r)
	}
}

func TestDescribe_ExactWhenClean(t *testing.T) {
	conn := &fakeExecConn{kubeContext: "kind-klyx",
		describeCmd: func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "echo", "Name: web") }}
	svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
	if r := svc.Describe(context.Background(), "c", "pods", "shop", "web"); r.Scrubbed || r.Output != "Name: web\n" || !r.OK {
		t.Fatalf("a clean report is exact: %+v", r)
	}
}

// S0b revision 4: a payload kind is described only inside a version bracket.
func TestDescribe_PayloadKindBracket(t *testing.T) {
	report := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "echo", "Name: platform") }
	cases := []struct {
		name     string
		snaps    []fakeSnap
		released bool
	}{
		{"clean and unchanged: released", []fakeSnap{{uid: "u", rv: "7"}, {uid: "u", rv: "7"}}, true},
		{"changed while described: discarded", []fakeSnap{{uid: "u", rv: "7"}, {uid: "u", rv: "8"}}, false},
		{"replaced while described: discarded", []fakeSnap{{uid: "u", rv: "7"}, {uid: "u2", rv: "7"}}, false},
		{"a payload appeared: discarded", []fakeSnap{{uid: "u", rv: "7"}, {uid: "u", rv: "7", payload: true}}, false},
		{"the second read failed: discarded", []fakeSnap{{uid: "u", rv: "7"}, {err: errors.New("timeout")}}, false},
		{"the first read failed: withheld", []fakeSnap{{err: errors.New("forbidden")}}, false},
	}
	for _, c := range cases {
		conn := &fakeExecConn{kubeContext: "kind-klyx", kindGroup: "kustomize.toolkit.fluxcd.io", kindName: "Kustomization", snaps: c.snaps, describeCmd: report}
		svc := NewExecService(func(string) (ExecConn, bool) { return conn, true })
		r := svc.Describe(context.Background(), "c", "kustomizations.kustomize.toolkit.fluxcd.io", "flux-system", "apps")
		if c.released {
			if !r.OK || r.Withheld != "" || r.Output != "Name: platform\n" {
				t.Errorf("%s: %+v", c.name, r)
			}
			continue
		}
		if r.Withheld == "" || r.Output != "" || r.OK {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}
