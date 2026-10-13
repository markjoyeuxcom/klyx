//go:build !windows

package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The fake Claude Code is this test binary re-executed: TestMain hands it to
// fakeClaude when it runs without test flags and its HOME holds a fake spec.
// The spec travels through HOME because the adapter passes no other variable
// of its own choosing.
const fakeSpecFile = ".klyx-fake-claude-v1.json"

type fakeSpec struct {
	Version       string `json:"version"`
	Auth          string `json:"auth"`
	AuthExit      int    `json:"authExit"`
	Doctor        string `json:"doctor"`
	DoctorCreates string `json:"doctorCreates"` // a path doctor creates (policy appearing)
	IgnoreFlags   bool   `json:"ignoreFlags"`   // accept the sentinel (a CLI that does not know the flags)
	SentinelExit0 bool   `json:"sentinelExit0"` // print the parser's error but exit 0 (contradictory)
	Stream        string `json:"stream"`        // -p output; {{CWD}}, {{TEXT}} are filled in
	Text          string `json:"text"`
	EchoFile      bool   `json:"echoFile"` // append the file the prompt names (and the cwd listing) to the answer
	StreamExit    int    `json:"streamExit"`
	Stderr        string `json:"stderr"`
	Hang          bool   `json:"hang"`  // -p: start a child, then sleep
	Flood         int    `json:"flood"` // -p: print this many bytes
}

func TestMain(m *testing.M) {
	if code, ok := fakeClaude(); ok {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func fakeClaude() (int, bool) {
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-test.") {
			return 0, false
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "klyx-fake-sleep" {
		time.Sleep(time.Hour)
		return 0, true
	}
	home := os.Getenv("HOME")
	data, err := os.ReadFile(filepath.Join(home, fakeSpecFile))
	if err != nil {
		return 0, false
	}
	var spec fakeSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return 3, true
	}
	args := os.Args[1:]
	logCall := func(what string) {
		f, _ := os.OpenFile(filepath.Join(home, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintln(f, what)
		f.Close()
	}
	has := func(s string) bool {
		for _, a := range args {
			if a == s {
				return true
			}
		}
		return false
	}
	switch {
	case has("--version"):
		logCall("version")
		fmt.Println(spec.Version)
		return 0, true
	case has("auth") && has("status"):
		logCall("auth " + strings.Join(args, " "))
		fmt.Print(spec.Auth)
		return spec.AuthExit, true
	case has("doctor"):
		logCall("doctor " + strings.Join(args, " "))
		fmt.Print(spec.Doctor)
		if spec.DoctorCreates != "" {
			os.MkdirAll(spec.DoctorCreates, 0o700)
		}
		return 0, true
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--klyx-sentinel-") {
			logCall("sentinel")
			if spec.IgnoreFlags {
				return 0, true
			}
			fmt.Fprintf(os.Stderr, "error: unknown option '%s'\n", a)
			if spec.SentinelExit0 {
				return 0, true
			}
			return 1, true
		}
	}
	if !has("-p") {
		return 4, true
	}
	logCall("prompt")
	cwd, _ := os.Getwd()
	os.WriteFile(filepath.Join(home, "started"), nil, 0o600)
	stdin, _ := io.ReadAll(os.Stdin)
	rec, _ := json.Marshal(map[string]any{"argv": args, "env": os.Environ(), "cwd": cwd, "stdin": string(stdin)})
	os.WriteFile(filepath.Join(home, "record.json"), rec, 0o600)
	if spec.Hang {
		child := exec.Command(os.Args[0], "klyx-fake-sleep")
		child.Start()
		os.WriteFile(filepath.Join(home, "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		time.Sleep(time.Hour)
	}
	if spec.Flood > 0 {
		os.Stdout.Write(bytes.Repeat([]byte("x"), spec.Flood))
		return 0, true
	}
	text := spec.Text
	if spec.EchoFile {
		if m := regexp.MustCompile(`read the file (\S+)`).FindStringSubmatch(string(stdin)); m != nil {
			b, _ := os.ReadFile(m[1])
			text += " " + strings.TrimSpace(string(b))
		}
		entries, _ := os.ReadDir(cwd)
		for _, e := range entries {
			text += " " + e.Name()
		}
	}
	jt, _ := json.Marshal(text)
	out := strings.ReplaceAll(spec.Stream, "{{CWD}}", cwd)
	out = strings.ReplaceAll(out, `"{{TEXT}}"`, string(jt))
	fmt.Print(out)
	fmt.Fprint(os.Stderr, spec.Stderr)
	return spec.StreamExit, true
}

// fakeCLI is one test's fake Claude Code: a private HOME, policy root and
// temp dir, and a provider whose only reviewed contract is this test binary.
type fakeCLI struct {
	t          *testing.T
	home       string
	policyRoot string
	spec       fakeSpec
	p          *claudeCLIProvider
	contract   claudeContract
}

const fakeVersion = "9.9.9"

func fakeStream(t *testing.T) string {
	t.Helper()
	events := cleanEvents()
	events[0]["cwd"] = "{{CWD}}"
	events[0]["claude_code_version"] = fakeVersion
	events[2] = assistantEvent(map[string]any{"type": "text", "text": "{{TEXT}}"})
	events[5]["result"] = "{{TEXT}}"
	return string(stream(t, events))
}

func newFakeCLI(t *testing.T) *fakeCLI {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	c := claudeContract{
		Version: fakeVersion, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SHA256: hex.EncodeToString(sum[:]),
		RemotePolicyLine: reviewedClaudeContracts[0].RemotePolicyLine, OrgPolicyLine: reviewedClaudeContracts[0].OrgPolicyLine,
	}
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	f := &fakeCLI{t: t, home: home, policyRoot: t.TempDir(), contract: c}
	doctor := strings.NewReplacer("Running: native (2.1.296)", "Running: native ("+fakeVersion+")",
		"Platform: darwin-arm64", "Platform: "+runtime.GOOS+"-"+runtime.GOARCH).Replace(string(fixture(t, "doctor-promax.synth.txt")))
	f.spec = fakeSpec{
		Version: fakeVersion + " (Claude Code)",
		Auth:    string(authWith(t, map[string]any{"configDirectory": filepath.Join(home, ".claude")})),
		Doctor:  doctor,
		Stream:  fakeStream(t),
		Text:    answer,
	}
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp)
	f.p = &claudeCLIProvider{
		cliPath: exe, model: tModel,
		environ: func() []string {
			return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "USER=tester", "LANG=C", "TMPDIR=" + tmp,
				"KUBECONFIG=/k/config", "ANTHROPIC_API_KEY=sk-ant-api03-parentkey00000000", "CLAUDE_CONFIG_DIR=/c",
				"KLYX_SECRET=1", "NODE_OPTIONS=--require /x.js", "HTTPS_PROXY=http://proxy:1", "AWS_SECRET_ACCESS_KEY=a"}
		},
		lookPath:  exec.LookPath,
		contracts: []claudeContract{c},
		goos:      runtime.GOOS, goarch: runtime.GOARCH,
		store:      &claudeAdmissionStore{},
		hashes:     &exeHashCache{m: map[string]exeHashEntry{}},
		tmpDir:     tmp,
		policyRoot: f.policyRoot,
	}
	f.write()
	return f
}

func (f *fakeCLI) write() {
	b, _ := json.Marshal(f.spec)
	if err := os.WriteFile(filepath.Join(f.home, fakeSpecFile), b, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeCLI) calls() []string {
	b, _ := os.ReadFile(filepath.Join(f.home, "calls.log"))
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "_"))
}

func (f *fakeCLI) prompts() int {
	n := 0
	for _, c := range f.calls() {
		if c == "prompt" {
			n++
		}
	}
	return n
}

func (f *fakeCLI) complete() (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return f.p.Complete(ctx, Request{System: "SYSTEM-TEXT", User: "EVIDENCE-TEXT @~/.kube/config", Purpose: PurposeExplain})
}

func wantRefused(t *testing.T, err error, want string) {
	t.Helper()
	var r *CLIRefusedError
	if !errors.As(err, &r) || !strings.Contains(err.Error(), want) {
		t.Fatalf("want a CLI refusal containing %q, got %T %v", want, err, err)
	}
}

func TestClaudeCLI_CleanCall(t *testing.T) {
	f := newFakeCLI(t)
	res, err := f.complete()
	if err != nil || res.Text != answer || res.ModelUsed != tModel {
		t.Fatalf("got (%+v, %v)", res, err)
	}
	// Admission ran once: version and the sentinel probe, after the helpers.
	if got := strings.Join(f.calls(), " "); !regexp.MustCompile(`^auth_--restricted_--safe-mode_auth_status_--json doctor_--restricted_--safe-mode_doctor version sentinel prompt$`).MatchString(got) {
		t.Fatalf("calls = %s", got)
	}
	var rec struct {
		Argv  []string
		Env   []string
		Cwd   string
		Stdin string
	}
	b, _ := os.ReadFile(filepath.Join(f.home, "record.json"))
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	// The prompt is on stdin only, behind a prefix; never in argv.
	if !strings.HasPrefix(rec.Stdin, claudeStdinPrefix) || !strings.Contains(rec.Stdin, "EVIDENCE-TEXT") {
		t.Fatalf("stdin = %q", rec.Stdin)
	}
	for _, a := range rec.Argv {
		if strings.Contains(a, "EVIDENCE") || strings.Contains(a, "SYSTEM-TEXT") {
			t.Fatalf("prompt text in argv: %q", rec.Argv)
		}
	}
	sys := filepath.Join(rec.Cwd, "system.txt")
	if want := claudeArgv(sys, tModel); strings.Join(rec.Argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", rec.Argv, want)
	}
	// The environment is the allowlist; nothing secret passed.
	env := strings.Join(rec.Env, "\n")
	for _, bad := range []string{"KUBECONFIG", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR", "KLYX_SECRET", "NODE_OPTIONS", "HTTPS_PROXY", "AWS_SECRET"} {
		if strings.Contains(env, bad) {
			t.Fatalf("%s reached the CLI: %s", bad, env)
		}
	}
	if !strings.Contains(env, "CLAUDE_CODE_DISABLE_ATTACHMENTS=1") {
		t.Fatal("attachments must be disabled")
	}
	// A fresh 0700 dir, removed afterwards.
	if _, err := os.Stat(rec.Cwd); !os.IsNotExist(err) {
		t.Fatalf("the call dir %s outlived the call", rec.Cwd)
	}
	rec2, _ := f.p.store.get()
	if rec2 == nil || rec2.ExeSHA256 != f.contract.SHA256 || rec2.Version != fakeVersion {
		t.Fatalf("admission = %+v", rec2)
	}

	// A second call re-checks policy, account and doctor, but does not
	// re-admit.
	if _, err := f.complete(); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(); got[len(got)-1] != "prompt" || strings.Count(strings.Join(got, " "), "sentinel") != 1 || strings.Count(strings.Join(got, " "), "doctor_--restricted") != 2 {
		t.Fatalf("calls = %v", got)
	}
}

func TestClaudeCLI_UnreviewedBinaryRunsNothing(t *testing.T) {
	f := newFakeCLI(t)
	f.p.contracts = []claudeContract{{Version: "2.1.296", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SHA256: strings.Repeat("0", 64)}}
	_, err := f.complete()
	wantRefused(t, err, "no reviewed Klyx contract")
	if len(f.calls()) != 0 {
		t.Fatalf("an unreviewed binary must not run: %v", f.calls())
	}
	if err := f.p.Ready(); err == nil {
		t.Fatal("Ready reports the unreviewed binary")
	}

	f = newFakeCLI(t)
	f.p.goarch = "riscv64"
	_, err = f.complete()
	wantRefused(t, err, "on "+runtime.GOOS+"/riscv64 has no reviewed Klyx contract")
	if len(f.calls()) != 0 {
		t.Fatalf("an unreviewed platform must not run anything: %v", f.calls())
	}
}

func TestClaudeCLI_ShimRefused(t *testing.T) {
	f := newFakeCLI(t)
	shim := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(shim, []byte("#!/bin/sh\nexec node cli.js \"$@\"\n"), 0o755)
	f.p.cliPath = shim
	_, err := f.complete()
	wantRefused(t, err, "not a native executable")
}

func TestClaudeCLI_PolicyRefusesBeforeAnyHelper(t *testing.T) {
	for _, path := range []string{
		"/Library/Application Support/ClaudeCode/managed-settings.json",
		"/etc/claude-code/managed-settings.json",
	} {
		f := newFakeCLI(t)
		pp := claudePolicyPaths(runtime.GOOS, "tester")
		if len(pp) == 0 || !strings.HasPrefix(path, pp[0]) {
			continue // the other platform's location
		}
		full := filepath.Join(f.policyRoot, path)
		os.MkdirAll(filepath.Dir(full), 0o700)
		os.WriteFile(full, []byte("{}"), 0o600)
		_, err := f.complete()
		wantRefused(t, err, "administrator policy")
		if len(f.calls()) != 0 {
			t.Fatalf("no helper may run while policy is present: %v", f.calls())
		}
	}
	f := newFakeCLI(t)
	os.MkdirAll(filepath.Join(f.home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(f.home, ".claude", "remote-settings.json"), []byte("{}"), 0o600)
	_, err := f.complete()
	wantRefused(t, err, "cached organisation policy")
	if len(f.calls()) != 0 {
		t.Fatalf("no helper may run while a policy cache is present: %v", f.calls())
	}
}

// Policy that appears while the helpers run is caught before launch - on an
// admitted CLI too, where no admission re-check follows the helpers.
func TestClaudeCLI_PolicyAppearsDuringHelpers(t *testing.T) {
	pp := claudePolicyPaths(runtime.GOOS, "tester")
	if len(pp) == 0 {
		t.Skip("no policy paths on this platform")
	}
	for _, admitted := range []bool{false, true} {
		f := newFakeCLI(t)
		if admitted {
			if _, err := f.complete(); err != nil {
				t.Fatal(err)
			}
		}
		launched := false
		f.p.afterLaunch = func() { launched = true }
		f.spec.DoctorCreates = filepath.Join(f.policyRoot, pp[0])
		f.write()
		_, err := f.complete()
		wantRefused(t, err, "administrator policy")
		if launched {
			t.Fatalf("admitted=%v: the CLI was launched after policy appeared", admitted)
		}
	}
}

// Policy that appears after launch kills the CLI before stdin is written.
func TestClaudeCLI_PolicyAppearsAfterLaunch(t *testing.T) {
	f := newFakeCLI(t)
	f.p.afterLaunch = func() {
		os.MkdirAll(filepath.Join(f.home, ".claude"), 0o700)
		os.WriteFile(filepath.Join(f.home, ".claude", "policy-limits.json"), []byte("{}"), 0o600)
	}
	_, err := f.complete()
	wantRefused(t, err, "cached organisation policy")
	if _, err := os.Stat(filepath.Join(f.home, "record.json")); !os.IsNotExist(err) {
		t.Fatal("the evidence must never reach the CLI's stdin")
	}
}

// Codex design review of 274535f: a Pro admission followed by a Team,
// API-key or other login, a cache appearing, or undeterminable auth delivers
// no evidence.
func TestClaudeCLI_AccountChecksEveryCall(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *fakeCLI)
		want   string
	}{
		{"team login", func(f *fakeCLI) {
			f.spec.Auth = string(authWith(t, map[string]any{"configDirectory": filepath.Join(f.home, ".claude"), "subscriptionType": "team"}))
		}, "Team or Enterprise"},
		{"API key login", func(f *fakeCLI) {
			f.spec.Auth = string(authWith(t, map[string]any{"configDirectory": filepath.Join(f.home, ".claude"), "apiKeySource": "ANTHROPIC_API_KEY"}))
		}, "API key"},
		{"another Max account", func(f *fakeCLI) {
			f.spec.Auth = string(authWith(t, map[string]any{"configDirectory": filepath.Join(f.home, ".claude"), "email": "other@example.test"}))
		}, "account changed"},
		{"undeterminable auth", func(f *fakeCLI) { f.spec.Auth, f.spec.AuthExit = "Not logged in\n", 1 }, "could not be read"},
		{"auth exits non-zero", func(f *fakeCLI) { f.spec.AuthExit = 1 }, "exited with status 1"},
		{"remote policy now loaded", func(f *fakeCLI) {
			f.spec.Doctor = strings.Replace(f.spec.Doctor, f.contract.RemotePolicyLine, "Managed settings (remote): loaded", 1)
		}, "outside the reviewed contract"},
		{"cache appears", func(f *fakeCLI) {
			os.MkdirAll(filepath.Join(f.home, ".claude"), 0o700)
			os.WriteFile(filepath.Join(f.home, ".claude", "remote-settings.json"), []byte("{}"), 0o600)
		}, "cached organisation policy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCLI(t)
			if _, err := f.complete(); err != nil {
				t.Fatal(err)
			}
			tc.mutate(f)
			f.write()
			_, err := f.complete()
			wantRefused(t, err, tc.want)
			if f.prompts() != 1 {
				t.Fatalf("no evidence may be delivered after the change: %v", f.calls())
			}
		})
	}
}

// A changed account revokes admission: switching back does not restore it;
// only Test does.
func TestClaudeCLI_RevocationHoldsUntilTest(t *testing.T) {
	f := newFakeCLI(t)
	if _, err := f.complete(); err != nil {
		t.Fatal(err)
	}
	orig := f.spec.Auth
	f.spec.Auth = string(authWith(t, map[string]any{"configDirectory": filepath.Join(f.home, ".claude"), "email": "other@example.test"}))
	f.write()
	_, err := f.complete()
	wantRefused(t, err, "account changed")
	f.spec.Auth = orig
	f.write()
	_, err = f.complete()
	wantRefused(t, err, "account changed")
	if f.p.Ready() == nil {
		t.Fatal("Ready reports the revocation")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := f.p.Probe(ctx); err != nil {
		t.Fatalf("Test re-admits: %v", err)
	}
	if _, err := f.complete(); err != nil {
		t.Fatalf("after Test: %v", err)
	}
}

func TestClaudeCLI_BinaryChangeRevokes(t *testing.T) {
	f := newFakeCLI(t)
	if _, err := f.complete(); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.p.store.get()
	changed := *rec
	changed.ExeSHA256 = strings.Repeat("a", 64) // admitted against another build
	f.p.store.set(&changed)
	_, err := f.complete()
	wantRefused(t, err, "binary changed")
	if f.prompts() != 1 {
		t.Fatal("no evidence after a binary change")
	}
}

func TestClaudeCLI_FlagsNotRecognised(t *testing.T) {
	for _, mode := range []string{"ignores the sentinel", "reports it but exits 0"} {
		f := newFakeCLI(t)
		f.spec.IgnoreFlags = mode == "ignores the sentinel"
		f.spec.SentinelExit0 = mode == "reports it but exits 0"
		f.write()
		_, err := f.complete()
		wantRefused(t, err, "did not recognise")
		if f.prompts() != 0 {
			t.Fatalf("%s: no prompt without the flag check", mode)
		}
	}
}

func TestClaudeCLI_WrongVersionLine(t *testing.T) {
	f := newFakeCLI(t)
	f.spec.Version = "9.9.10 (Claude Code)"
	f.write()
	_, err := f.complete()
	wantRefused(t, err, "reviewed version")
}

func TestClaudeCLI_ToolEventRevokes(t *testing.T) {
	f := newFakeCLI(t)
	events := cleanEvents()
	events[0]["cwd"], events[0]["claude_code_version"] = "{{CWD}}", fakeVersion
	events[2] = assistantEvent(map[string]any{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"file_path": "/etc/hosts"}})
	f.spec.Stream = string(stream(t, events))
	f.write()
	_, err := f.complete()
	if !errors.Is(err, ErrUnexpectedOutput) || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("got %v", err)
	}
	f.spec.Stream = fakeStream(t)
	f.write()
	_, err = f.complete()
	wantRefused(t, err, "attempted a tool")
	if f.prompts() != 1 {
		t.Fatal("a revoked CLI gets no further prompts")
	}
}

func TestClaudeCLI_ExitAndStderr(t *testing.T) {
	f := newFakeCLI(t)
	f.spec.StreamExit = 1
	f.spec.Stderr = "fatal: auth header x-api-key: sk-ant-api03-abcdefghijklmnop rejected"
	f.write()
	_, err := f.complete()
	if err == nil || !strings.Contains(err.Error(), "exited with status 1") || strings.Contains(err.Error(), "abcdefghijklmnop") {
		t.Fatalf("got %v", err)
	}
}

func TestClaudeCLI_OversizedStdout(t *testing.T) {
	f := newFakeCLI(t)
	f.spec.Flood = claudeStdoutMax + 1024
	f.write()
	_, err := f.complete()
	if !errors.Is(err, ErrUnexpectedOutput) || !strings.Contains(err.Error(), "4 MiB cap") {
		t.Fatalf("got %v", err)
	}
	if f.p.Ready() == nil {
		t.Fatal("an oversized output revokes admission")
	}
}

// A deadline kills the whole process group, including what the CLI started.
func TestClaudeCLI_DeadlineKillsGroup(t *testing.T) {
	f := newFakeCLI(t)
	if _, err := f.complete(); err != nil { // admit first, so the deadline is the call's
		t.Fatal(err)
	}
	f.spec.Hang = true
	f.write()
	// Long enough for the preflight helpers under -race (each re-exec of
	// this binary takes about a second), far shorter than the hang.
	const budget = 8 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	_, err := f.p.Complete(ctx, Request{System: "s", User: "u", Purpose: PurposeExplain})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > budget+claudeWaitDelay+2*time.Second {
		t.Fatal("the deadline did not bound the call")
	}
	b, err := os.ReadFile(filepath.Join(f.home, "child.pid"))
	if err != nil {
		t.Fatal("the fake did not start its child")
	}
	pid, _ := strconv.Atoi(string(b))
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the CLI's child %d outlived the call", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The Test probe: a reply that quotes the canary or the working dir's marker
// could only come from a tool, and revokes admission.
func TestClaudeCLI_ProbeCatchesALocalRead(t *testing.T) {
	f := newFakeCLI(t)
	f.spec.Text = "I cannot access files."
	f.write()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := f.p.Probe(ctx)
	if err != nil || res.Text != "I cannot access files." {
		t.Fatalf("a clean probe passes, got (%+v, %v)", res, err)
	}

	f.spec.EchoFile = true
	f.write()
	_, err = f.p.Probe(ctx)
	if !errors.Is(err, ErrUnexpectedOutput) || f.p.Ready() == nil {
		t.Fatalf("a probe that read a file revokes admission, got %v", err)
	}
}

func TestClaudeCLI_Status(t *testing.T) {
	f := newFakeCLI(t)
	st := f.p.Status()
	if !st.Reviewed || st.Admitted || st.Version != fakeVersion || len(st.SHA256) != 12 {
		t.Fatalf("before admission: %+v", st)
	}
	if _, err := f.complete(); err != nil {
		t.Fatal(err)
	}
	if st := f.p.Status(); !st.Admitted || st.Problem != "" {
		t.Fatalf("after admission: %+v", st)
	}
}
