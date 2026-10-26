package ai

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/envread"
	"github.com/moomora/klyx/internal/procrun"
	"github.com/moomora/klyx/internal/readfence"
)

// ClaudeCLIOrigin is where a Claude Code call's prompt goes, as Settings and
// every result show it.
const ClaudeCLIOrigin = "Anthropic, via the Claude Code CLI on your Claude sign-in"

// Output and time bounds for Claude Code processes (spec §1.5).
const (
	claudeStdoutMax     = 4 << 20
	claudeHelperOutMax  = 64 << 10
	claudeStderrMax     = 64 << 10
	claudeAuthDeadline  = 10 * time.Second
	claudeDocDeadline   = 30 * time.Second
	claudeShortDeadline = 15 * time.Second
	claudeStdinDeadline = 10 * time.Second
	claudeWaitDelay     = 2 * time.Second
)

// claudeStdinPrefix opens every prompt on stdin, so the text never starts
// with a character the CLI treats as a command (`/`, `@`, `!`, `#`).
const claudeStdinPrefix = "Klyx request follows.\n\n"

// CLIRefusedError: Klyx refused to run a CLI provider - its binary, sign-in
// or policy is outside the reviewed contract, or its admission was revoked.
// Reason is Klyx's own text.
type CLIRefusedError struct{ Reason string }

func (e *CLIRefusedError) Error() string { return e.Reason }

func cliRefused(format string, args ...any) error {
	return &CLIRefusedError{Reason: fmt.Sprintf(format, args...)}
}

// CLIDiscardedError: the prompt reached Claude Code, but Klyx discarded the
// outcome. Partial false: the whole prompt was handed over and the answer is
// dropped because the call's admission was revoked or replaced while the CLI
// ran. Partial true: the hand-off broke midway, so part of the prompt may
// have been delivered. Never reported as "not sent" - that is
// CLIRefusedError (Codex review 2026-10-13 #10).
type CLIDiscardedError struct {
	Reason  string
	Partial bool
}

func (e *CLIDiscardedError) Error() string { return e.Reason }

// CLIAdmission is the record of an admitted Claude Code (spec §1.4): the
// exact binary, the account it was checked against, and the lockdown
// configurations (argv hashes, one per model) whose flags were proven. It
// lives in memory only and is never mutated once published.
type CLIAdmission struct {
	ExeSHA256 string
	Version   string
	Platform  string
	CheckedAt time.Time
	account   string          // the account identity hash
	argv      map[string]bool // admitted lockdown configurations
}

// claudeAdmissionStore holds the one admission of this Klyx run, shared by
// every Claude Code provider built from settings. A call works within the
// epoch it started in: it can publish an admission, and release its prompt,
// only while nothing is revoked and that epoch is current - so a revocation
// by a concurrent call is never overwritten, and a prompt never leaves after
// one (Codex review of 09cb2d8). Only Test (reset) clears a revocation, and
// it starts a new epoch as it does, so no call from before the Test can
// carry on under the new admission.
type claudeAdmissionStore struct {
	mu      sync.Mutex
	epoch   uint64
	rec     *CLIAdmission
	revoked string
	// afterValidate runs inside release, between the check and the write;
	// tests only.
	afterValidate func()
}

var defaultClaudeAdmissions = &claudeAdmissionStore{}

const admissionChanged = "Claude Code's admission changed while this call was being checked; try again"

func (s *claudeAdmissionStore) snapshot() (*CLIAdmission, string, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec, s.revoked, s.epoch
}

func (s *claudeAdmissionStore) revoke(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeLocked(reason)
}

func (s *claudeAdmissionStore) revokeLocked(reason string) {
	s.rec, s.revoked = nil, reason
}

// reset is Test: it clears any admission and revocation and starts a new
// epoch, which in-flight calls of the old one can no longer use.
func (s *claudeAdmissionStore) reset() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec, s.revoked = nil, ""
	s.epoch++
	return s.epoch
}

// currentLocked refuses a call whose epoch is over: revoked, or reset by a
// Test, since it started.
func (s *claudeAdmissionStore) currentLocked(epoch uint64) error {
	if s.revoked != "" {
		return cliRefused("%s", s.revoked)
	}
	if s.epoch != epoch {
		return cliRefused("%s", admissionChanged)
	}
	return nil
}

// identityLocked revokes when the call's binary or account is not the
// admitted one.
func (s *claudeAdmissionStore) identityLocked(exe, account string) error {
	var why string
	switch {
	case s.rec.ExeSHA256 != exe:
		why = "The Claude Code binary changed since it was admitted; run Test in Settings > AI to admit it"
	case s.rec.account != account:
		why = "The Claude Code account changed since it was admitted; run Test in Settings > AI to admit the new one"
	default:
		return nil
	}
	s.revokeLocked(why)
	return cliRefused("%s", why)
}

// check compares a call's binary and account with the admission, within the
// call's epoch, and reports whether this lockdown configuration still needs
// admitting.
func (s *claudeAdmissionStore) check(epoch uint64, exe, account, argv string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.currentLocked(epoch); err != nil {
		return false, err
	}
	if s.rec == nil {
		return true, nil
	}
	if err := s.identityLocked(exe, account); err != nil {
		return false, err
	}
	return !s.rec.argv[argv], nil
}

// publish records an admission made within epoch. It never clears a newer
// revocation, and an admission published meanwhile for another binary or
// account is an identity change.
func (s *claudeAdmissionStore) publish(epoch uint64, exe exeIdentity, account, argv string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.currentLocked(epoch); err != nil {
		return err
	}
	if s.rec != nil {
		if err := s.identityLocked(exe.SHA256, account); err != nil {
			return err
		}
	}
	next := &CLIAdmission{ExeSHA256: exe.SHA256, Version: exe.Build.Version, Platform: exe.Build.platform(), CheckedAt: time.Now(), account: account, argv: map[string]bool{argv: true}}
	if s.rec != nil {
		next.CheckedAt = s.rec.CheckedAt
		for a := range s.rec.argv {
			next.argv[a] = true
		}
	}
	s.rec = next
	return nil
}

// commit runs a passing Test's record write under the admission lock, and
// only while that Test's admission still holds. distrust revokes and drops
// the record under the same lock, so a distrust lands either before the
// write (the Test fails) or after it (and removes the record) - a build that
// broke the contract is never written back (Codex review of b0b977b). Lock
// order: the admission lock, then the build record's.
func (s *claudeAdmissionStore) commit(t callTicket, write func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validLocked(t); err != nil {
		return err
	}
	return write()
}

// distrust revokes with reason and runs forget under the admission lock. A
// forget that fails is added to the revocation's reason and returned.
func (s *claudeAdmissionStore) distrust(reason string, forget func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeLocked(reason)
	if err := forget(); err != nil {
		s.revoked = reason + "; " + err.Error()
		return err
	}
	return nil
}

// validLocked: the call's epoch is current and its binary, account and
// configuration are the admitted ones.
func (s *claudeAdmissionStore) validLocked(t callTicket) error {
	if err := s.currentLocked(t.epoch); err != nil {
		return err
	}
	if s.rec == nil || s.rec.ExeSHA256 != t.exe.SHA256 || s.rec.account != t.account || !s.rec.argv[t.argv] {
		return cliRefused("%s", admissionChanged)
	}
	return nil
}

// valid checks that a call's admission still holds - when its answer
// arrives, so no answer is shown from an admission revoked or replaced while
// it ran.
func (s *claudeAdmissionStore) valid(t callTicket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validLocked(t)
}

// release hands a call's prompt to the CLI while holding the admission lock
// (Codex review of 5cb9a9a): a revocation or Test either happens first, and
// nothing is written, or waits until the whole prompt has been handed over.
// There is no gap between the last check and the first byte. write is
// bounded by a pipe deadline, and by the call's cancellation, which kills
// the process and closes the pipe.
func (s *claudeAdmissionStore) release(t callTicket, write func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validLocked(t); err != nil {
		return err
	}
	if s.afterValidate != nil {
		s.afterValidate()
	}
	return write()
}

// callTicket is what one call's preflight established: the binary, the
// account and configuration it was checked against, and its epoch.
type callTicket struct {
	exe     exeIdentity
	account string
	argv    string
	epoch   uint64
}

// transientError marks a helper failure that says nothing about the CLI's
// identity or contract - a timeout, a cancel, a process that did not start:
// it fails the call without revoking admission.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// exeHashCache avoids re-hashing a 240 MB binary on every call: an entry is
// reused only while the file's size, mtime and inode are unchanged.
type exeHashCache struct {
	mu sync.Mutex
	m  map[string]exeHashEntry
}

type exeHashEntry struct {
	size  int64
	mtime time.Time
	ino   uint64
	sum   string
}

var defaultExeHashes = &exeHashCache{m: map[string]exeHashEntry{}}

func sameFile(e exeHashEntry, fi os.FileInfo) bool {
	return e.size == fi.Size() && e.mtime.Equal(fi.ModTime()) && e.ino == fileInode(fi)
}

// nativeMagic reports whether head starts a native executable: Mach-O (thin
// or universal), ELF or PE. A script or interpreter shim is refused - an
// unchanged launcher can front a changed runtime (spec §1.4).
func nativeMagic(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	switch {
	case bytes.Equal(head[:4], []byte{0xcf, 0xfa, 0xed, 0xfe}), bytes.Equal(head[:4], []byte{0xfe, 0xed, 0xfa, 0xcf}),
		bytes.Equal(head[:4], []byte{0xce, 0xfa, 0xed, 0xfe}), bytes.Equal(head[:4], []byte{0xfe, 0xed, 0xfa, 0xce}),
		bytes.Equal(head[:4], []byte{0xca, 0xfe, 0xba, 0xbe}), bytes.Equal(head[:4], []byte{0xbe, 0xba, 0xfe, 0xca}):
		return true // Mach-O
	case bytes.Equal(head[:4], []byte{0x7f, 'E', 'L', 'F'}):
		return true
	case head[0] == 'M' && head[1] == 'Z':
		return true // PE
	}
	return false
}

func (c *exeHashCache) sum(path string, fi os.FileInfo) (string, error) {
	c.mu.Lock()
	e, ok := c.m[path]
	c.mu.Unlock()
	if ok && sameFile(e, fi) {
		return e.sum, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil || !nativeMagic(head) {
		return "", errors.New("it is not a native executable (a launcher script or shim is not admitted)")
	}
	h := sha256.New()
	h.Write(head)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil || !sameFile(exeHashEntry{size: fi.Size(), mtime: fi.ModTime(), ino: fileInode(fi)}, after) {
		return "", errors.New("it changed while Klyx was reading it")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	c.mu.Lock()
	c.m[path] = exeHashEntry{size: fi.Size(), mtime: fi.ModTime(), ino: fileInode(fi), sum: sum}
	c.mu.Unlock()
	return sum, nil
}

// exeIdentity is a resolved, hashed Claude Code binary on a supported
// platform. Tested says it passed Test on this machine; Build.Version is then
// the version Test recorded, else "" until Test reads it.
type exeIdentity struct {
	Path   string
	SHA256 string
	Build  claudeBuild
	Tested bool
}

// claudeCLIProvider runs the installed Claude Code CLI as a plain completion
// backend under the reviewed contract of spec §1.5: no tools, no
// customizations, an allowlisted environment, policy and account checks
// before every call, and an output contract that accepts only a confirmed,
// complete text answer.
type claudeCLIProvider struct {
	cliPath   string
	model     string
	environ   func() []string
	lookPath  func(string) (string, error)
	platforms []string // supported GOOS/GOARCH (claudePlatforms)
	goos      string
	goarch    string
	store     *claudeAdmissionStore
	builds    *claudeBuildStore
	hashes    *exeHashCache
	tmpDir    string // "" = the system temp dir
	// policyRoot prefixes the absolute managed-policy paths; tests only.
	policyRoot string
	// Helper, init-check and stdin hand-over deadlines; tests shorten them.
	authDeadline   time.Duration
	doctorDeadline time.Duration
	initDeadline   time.Duration
	stdinDeadline  time.Duration
	// offline wraps the admission init check so it cannot reach the
	// network (claudeOffline); tests replace it.
	offline func(exe string, args []string) (string, []string)
	// Tests only: afterHelpers runs after doctor, beforePublish before an
	// admission is published, afterLaunch between starting the CLI and the
	// final checks, and beforeRecord before a passing Test records its build.
	afterHelpers  func()
	beforePublish func()
	afterLaunch   func()
	beforeRecord  func()
}

// NewClaudeCLI builds the Claude Code provider. Nothing runs until a call.
func NewClaudeCLI(cliPath, model string) Provider {
	return newClaudeCLI(cliPath, model)
}

func newClaudeCLI(cliPath, model string) *claudeCLIProvider {
	return &claudeCLIProvider{
		cliPath: cliPath, model: model, environ: claudeEnviron, lookPath: exec.LookPath,
		platforms: claudePlatforms, goos: runtime.GOOS, goarch: runtime.GOARCH,
		store: defaultClaudeAdmissions, builds: defaultClaudeBuilds, hashes: defaultExeHashes,
		authDeadline: claudeAuthDeadline, doctorDeadline: claudeDocDeadline, initDeadline: claudeShortDeadline,
		stdinDeadline: claudeStdinDeadline, offline: claudeOffline,
	}
}

// claudeEnviron is the parent environment the CLI's allowlist takes; claudeEnv
// still filters it, and adds the fixed lockdown values.
func claudeEnviron() []string { return envread.Walk(envread.ClaudeCLI) }

func (p *claudeCLIProvider) envValue(k string) string {
	for _, kv := range p.environ() {
		if key, v, ok := strings.Cut(kv, "="); ok && key == k {
			return v
		}
	}
	return ""
}

func (p *claudeCLIProvider) home() (string, error) {
	h := p.envValue("HOME")
	if h == "" || !filepath.IsAbs(h) {
		return "", cliRefused("Klyx cannot check Claude Code's configuration: HOME is not set")
	}
	return filepath.Clean(h), nil
}

// resolve finds the claude executable: ai.cliPath, else PATH, else the
// native installer's ~/.local/bin/claude.
func (p *claudeCLIProvider) resolve() (string, error) {
	if p.cliPath != "" {
		return p.cliPath, nil
	}
	if lp, err := p.lookPath("claude"); err == nil {
		return lp, nil
	}
	if home, err := p.home(); err == nil {
		c := filepath.Join(home, ".local", "bin", "claude")
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", cliRefused("Claude Code was not found. Install it, or set its path in Settings > AI")
}

// executable resolves and hashes the binary on a supported platform, and
// looks it up among the builds Test has checked (spec §1.5 check 1). It runs
// nothing.
func (p *claudeCLIProvider) executable() (exeIdentity, error) {
	build := claudeBuild{GOOS: p.goos, GOARCH: p.goarch}
	if !slices.Contains(p.platforms, build.platform()) {
		return exeIdentity{}, cliRefused("Claude Code on %s is not supported by Klyx yet (supported: %s)", build.platform(), strings.Join(p.platforms, ", "))
	}
	path, err := p.resolve()
	if err != nil {
		return exeIdentity{}, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return exeIdentity{}, cliRefused("Claude Code at %s cannot be read: %v", path, err)
	}
	if real, err = filepath.Abs(real); err != nil {
		return exeIdentity{}, cliRefused("Claude Code's path cannot be resolved: %v", err)
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.Mode().IsRegular() {
		return exeIdentity{}, cliRefused("Claude Code at %s is not a regular file", real)
	}
	sum, err := p.hashes.sum(real, fi)
	if err != nil {
		return exeIdentity{}, cliRefused("Claude Code at %s is not admitted: %v", real, err)
	}
	exe := exeIdentity{Path: real, SHA256: sum, Build: build}
	if t, ok := p.builds.lookup(sum, build.platform()); ok {
		exe.Build.Version, exe.Tested = t.Version, true
	}
	return exe, nil
}

// untested refuses a build that Test has not checked on this machine: a new
// install, or an auto-update.
func untested(exe exeIdentity) error {
	return cliRefused("This Claude Code build (%s, sha256 %s) has not been checked by Klyx on this machine yet. Press Test in Settings > AI to check and admit it",
		filepath.Base(exe.Path), exe.SHA256[:12])
}

// distrust revokes admission and drops the build from the tested record,
// atomically with any Test that would record it: a build that broke the
// contract needs Test again, even after a restart. A record that could not
// be rewritten is reported, never swallowed (Codex review of b0b977b).
func (p *claudeCLIProvider) distrust(exe exeIdentity, reason string) error {
	return p.store.distrust(reason, func() error { return p.builds.forget(exe.SHA256) })
}

// withDistrust is cause, plus a failure to drop the build from the record.
func withDistrust(cause, ferr error) error {
	if ferr == nil {
		return cause
	}
	return fmt.Errorf("%w; %v", cause, ferr)
}

// checkPolicy is spec §1.5 checks 2 and 3: no managed-policy location and no
// policy cache exists. "Missing" is the only accepted answer; a path that
// cannot be checked refuses.
func (p *claudeCLIProvider) checkPolicy() error {
	home, err := p.home()
	if err != nil {
		return err
	}
	user := p.envValue("USER")
	if user == "" {
		user = p.envValue("LOGNAME")
	}
	if user == "" || strings.ContainsAny(user, `/\`) {
		return cliRefused("Klyx cannot check Claude Code's per-user managed preferences: USER is not set")
	}
	var paths []string
	for _, pp := range claudePolicyPaths(p.goos, user) {
		paths = append(paths, filepath.Join(p.policyRoot, pp))
	}
	if len(paths) == 0 {
		return cliRefused("Klyx has no managed-policy check for %s", p.goos)
	}
	for _, path := range paths {
		if err := absent(path); err != nil {
			return cliRefused("Claude Code has administrator policy Klyx cannot inspect (%s), so Klyx refuses this CLI", err)
		}
	}
	for _, path := range claudeCachePaths(home) {
		if err := absent(path); err != nil {
			return cliRefused("Claude Code holds a cached organisation policy (%s), so Klyx refuses this CLI", err)
		}
	}
	return nil
}

func absent(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return fmt.Errorf("%s exists", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("%s cannot be checked: %v", path, err)
	}
}

// callDir makes a fresh 0700 working dir, symlinks resolved, that is not
// inside a git repository.
func (p *claudeCLIProvider) callDir() (string, error) {
	d, err := os.MkdirTemp(p.tmpDir, "klyx-claude-")
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(d)
	if err != nil {
		_ = os.RemoveAll(d)
		return "", err
	}
	for dir := real; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			_ = os.RemoveAll(d)
			return "", cliRefused("the temp directory %s is inside a git repository", real)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return real, nil
}

// runSpec is one contained Claude Code process.
type runSpec struct {
	exe       string
	args      []string
	env       []string
	dir       string
	stdin     []byte // nil: /dev/null
	stdoutMax int
	stderrMax int
	deadline  time.Duration
	// stdinDeadline bounds handing stdin over.
	stdinDeadline time.Duration
	// release gates handing stdin over, after the process started: it calls
	// write, under whatever must not change meanwhile, or refuses. A refusal
	// or a failed write kills the process group, and nothing more is
	// written.
	release func(write func() error) error
	// stopAtFirstLine kills the process group as soon as stdout has one
	// complete line: the admission init check needs only the init event.
	stopAtFirstLine bool
}

type runResult struct {
	stdout, stderr         []byte
	stdoutOver, stderrOver bool
	exitCode               int
	timedOut               bool
	startErr, gateErr      error
}

// runContained runs one process in its own process group with capped output
// and a deadline; the whole group is killed on deadline, on cancel, and when
// the process exits. The containment itself is internal/procrun (extracted
// unchanged, The Line slice 6 note §2); this keeps the adapter's names.
func runContained(ctx context.Context, s runSpec) runResult {
	if runtime.GOOS == "windows" {
		return runResult{startErr: errors.New("no reviewed Claude Code contract for Windows")}
	}
	r := procrun.Run(ctx, procrun.Spec{
		Exe: s.exe, Args: s.args, Env: s.env, Dir: s.dir, Stdin: s.stdin,
		StdoutMax: s.stdoutMax, StderrMax: s.stderrMax, Deadline: s.deadline, WaitDelay: claudeWaitDelay,
		StdinDeadline: s.stdinDeadline, Release: s.release, StopAtFirstLine: s.stopAtFirstLine,
	})
	return runResult{
		stdout: r.Stdout, stderr: r.Stderr, stdoutOver: r.StdoutOver, stderrOver: r.StderrOver,
		exitCode: r.ExitCode, timedOut: r.TimedOut, startErr: r.StartErr, gateErr: r.GateErr,
	}
}

// helper runs a no-prompt Claude Code command under the full lockdown: the
// root switches before the subcommand, the allowlisted environment, an empty
// 0700 dir, stdin /dev/null, capped output and a deadline (spec §1.5
// preflight helpers). It returns stdout and the exit code.
func (p *claudeCLIProvider) helper(ctx context.Context, exe exeIdentity, deadline time.Duration, rootSwitches bool, args ...string) ([]byte, int, error) {
	dir, err := p.callDir()
	if err != nil {
		return nil, 0, &transientError{cliRefused("Klyx could not make a working directory for Claude Code: %v", err)}
	}
	defer func() { _ = os.RemoveAll(dir) }() // best-effort temp cleanup
	if rootSwitches {
		args = append(append([]string{}, claudeRootLockdown...), args...)
	}
	r := runContained(ctx, runSpec{exe: exe.Path, args: args, env: claudeEnv(p.environ()), dir: dir, stdoutMax: claudeHelperOutMax, stderrMax: claudeStderrMax, deadline: deadline})
	name := "claude " + strings.Join(args, " ")
	switch {
	case r.startErr != nil:
		return nil, 0, &transientError{cliRefused("%s could not start: %v", name, r.startErr)}
	case r.timedOut:
		if err := ctx.Err(); err != nil {
			return nil, 0, &transientError{err}
		}
		return nil, 0, &transientError{cliRefused("%s did not finish in time", name)}
	case r.stdoutOver || r.stderrOver:
		return nil, 0, cliRefused("%s printed more output than its reviewed contract allows", name)
	}
	return r.stdout, r.exitCode, nil
}

func (p *claudeCLIProvider) account(ctx context.Context, exe exeIdentity) (claudeAccount, error) {
	home, err := p.home()
	if err != nil {
		return claudeAccount{}, err
	}
	out, code, err := p.helper(ctx, exe, p.authDeadline, true, "auth", "status", "--json")
	if err != nil {
		return claudeAccount{}, err
	}
	acct, err := parseClaudeAuth(out, home)
	if err != nil {
		return claudeAccount{}, &CLIRefusedError{Reason: err.Error()}
	}
	if code != 0 {
		return claudeAccount{}, cliRefused("claude auth status exited with status %d", code)
	}
	return acct, nil
}

func (p *claudeCLIProvider) doctor(ctx context.Context, exe exeIdentity) error {
	out, code, err := p.helper(ctx, exe, p.doctorDeadline, true, "doctor")
	if err != nil {
		return err
	}
	if code != 0 {
		return cliRefused("claude doctor exited with status %d", code)
	}
	if err := parseClaudeDoctor(out, exe.Build); err != nil {
		return &CLIRefusedError{Reason: err.Error()}
	}
	return nil
}

// version reads the build's version: X.Y.Z at or above claudeMinVersion,
// and for a tested build exactly the version Test recorded.
func (p *claudeCLIProvider) version(ctx context.Context, exe exeIdentity) (string, error) {
	out, code, err := p.helper(ctx, exe, claudeShortDeadline, false, "--version")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", cliRefused("claude --version exited with status %d", code)
	}
	v, err := parseClaudeVersion(string(out))
	if err != nil {
		return "", &CLIRefusedError{Reason: err.Error()}
	}
	if exe.Build.Version != "" && v != exe.Build.Version {
		return "", cliRefused("claude --version reports %s, not the %s that Test checked", v, exe.Build.Version)
	}
	return v, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// flagProbe proves, without a model call, that the binary recognises every
// production flag: the production argv plus a random sentinel flag must fail
// on the sentinel, which the parser reports only after accepting every flag
// before it. A proxy on a closed port is a safety net (spec §1.5).
func (p *claudeCLIProvider) flagProbe(ctx context.Context, exe exeIdentity) error {
	dir, err := p.callDir()
	if err != nil {
		return cliRefused("Klyx could not make a working directory for Claude Code: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }() // best-effort temp cleanup
	sys := filepath.Join(dir, "system.txt")
	if err := os.WriteFile(sys, nil, 0o600); err != nil {
		return cliRefused("Klyx could not prepare the flag check: %v", err)
	}
	sentinel := "--klyx-sentinel-" + randHex(8)
	env := append(claudeEnv(p.environ()), "HTTPS_PROXY=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9", "https_proxy=http://127.0.0.1:9", "http_proxy=http://127.0.0.1:9")
	r := runContained(ctx, runSpec{exe: exe.Path, args: append(claudeArgv(sys, p.model), sentinel), env: env, dir: dir, stdoutMax: claudeHelperOutMax, stderrMax: claudeStderrMax, deadline: claudeShortDeadline})
	if r.startErr != nil {
		return cliRefused("Claude Code could not start: %v", r.startErr)
	}
	if r.timedOut {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if r.timedOut || r.exitCode == 0 || len(bytes.TrimSpace(r.stdout)) != 0 ||
		!strings.Contains(strings.ToLower(string(r.stderr)), "unknown option '"+sentinel+"'") {
		return cliRefused("Claude Code did not recognise Klyx's lockdown flags, so Klyx refuses it")
	}
	return nil
}

// claudeInitPrompt is the whole prompt of the admission init check: no
// cluster data.
const claudeInitPrompt = "Klyx admission check. Reply with one word: ready"

// noNetworkProfile is the sandbox-exec profile for the admission init check:
// every outbound connection is denied.
const noNetworkProfile = "(version 1)(allow default)(deny network-outbound (remote ip))(deny network-outbound (remote tcp))(deny network-outbound (remote udp))"

// claudeOffline wraps the admission init check so it cannot reach the
// network: on macOS under sandbox-exec. The check's prompt carries no
// cluster data either way; the sandbox keeps even that on the machine.
func claudeOffline(exe string, args []string) (string, []string) {
	if runtime.GOOS == "darwin" {
		return "/usr/bin/sandbox-exec", append([]string{"-p", noNetworkProfile, exe}, args...)
	}
	return exe, args
}

// initCheck confirms the lockdown from the CLI's own report before any
// evidence is released (the slice 5 smoke's finding: the init event arrives
// only after the prompt has been handed over, so checking it per call is
// detection, not prevention). It runs the production argv and environment
// with a fixed prompt, reads only the init event, kills the process, and
// holds the init to the reviewed contract. A lockdown failure revokes.
func (p *claudeCLIProvider) initCheck(ctx context.Context, exe exeIdentity) error {
	dir, err := p.callDir()
	if err != nil {
		return cliRefused("Klyx could not make a working directory for Claude Code: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }() // best-effort temp cleanup
	sys := filepath.Join(dir, "system.txt")
	if err := os.WriteFile(sys, []byte(claudeProbeSystem), 0o600); err != nil {
		return cliRefused("Klyx could not prepare the init check: %v", err)
	}
	name, args := p.offline(exe.Path, claudeArgv(sys, p.model))
	r := runContained(ctx, runSpec{
		exe: name, args: args, env: claudeEnv(p.environ()), dir: dir,
		stdin: []byte(claudeStdinPrefix + claudeInitPrompt), stdoutMax: claudeHelperOutMax, stderrMax: claudeStderrMax,
		deadline: p.initDeadline, stdinDeadline: p.stdinDeadline, stopAtFirstLine: true,
	})
	// A process that did not start, a hand-over that failed, or a check that
	// ran out of time says nothing about the CLI's contract: it fails this
	// call only.
	switch {
	case r.startErr != nil:
		return &transientError{cliRefused("Claude Code could not start for its init check: %v", r.startErr)}
	case r.gateErr != nil:
		return &transientError{cliRefused("Klyx could not hand Claude Code its init check: %v", r.gateErr)}
	}
	first, _, ok := bytes.Cut(r.stdout, []byte("\n"))
	if !ok && r.timedOut {
		if err := ctx.Err(); err != nil {
			return &transientError{err}
		}
		return &transientError{cliRefused("Claude Code did not report its configuration in time")}
	}
	// Anything the CLI did print that is not a valid startup report is an
	// observed violation of its contract, and revokes admission until Test
	// (Codex review of 5f19f50): no report at all, a malformed line, a first
	// event that is not init, or an init that breaks the contract.
	var verr error
	switch m, perr := strictObject(first); {
	case !ok:
		verr = contractBreak("no startup report")
	case perr != nil:
		verr = contractBreak("a malformed startup report")
	default:
		typ, _ := rawString(m, "type")
		sub, _ := rawString(m, "subtype")
		if typ != "system" || sub != "init" {
			what := label(typ)
			if sub != "" {
				what += "/" + label(sub)
			}
			verr = toolEvent("its first event is " + what + ", not its startup report")
			break
		}
		want := streamWant{Version: exe.Build.Version, Model: p.model, Cwd: dir}
		_, verr = checkInit(m, want)
	}
	if verr != nil {
		var ce *cliContractError
		if errors.As(verr, &ce) && ce.revoke {
			return withDistrust(verr, p.distrust(exe, ce.msg))
		}
		return verr
	}
	return nil
}

// preflight is every check before a prompt-bearing call (spec §1.5 checks
// 1-7), plus admission, all within one admission epoch. Test (admit) clears
// any revocation and starts a new epoch; otherwise a revoked CLI is refused
// at once. A changed binary or account, or a sign-in that is disallowed or
// cannot be determined, revokes admission until the next Test; a timeout or
// cancel fails only this call. A lockdown configuration not yet admitted (the
// first call of a run, or a new model) is admitted and published - unless the
// epoch ended meanwhile.
func (p *claudeCLIProvider) preflight(ctx context.Context, admit bool) (callTicket, error) {
	var epoch uint64
	if admit {
		epoch = p.store.reset()
	} else {
		_, revoked, e := p.store.snapshot()
		if revoked != "" {
			return callTicket{}, cliRefused("%s", revoked)
		}
		epoch = e
	}
	exe, err := p.executable()
	if err != nil {
		return callTicket{}, err
	}
	// Only Test runs a build it has not checked; every other call refuses
	// before anything runs.
	if !exe.Tested && !admit {
		return callTicket{}, untested(exe)
	}
	if err := p.checkPolicy(); err != nil {
		return callTicket{}, err
	}
	if !exe.Tested {
		// Test on a new build reads its version first: doctor and the init
		// report must name it.
		v, err := p.version(ctx, exe)
		if err != nil {
			return callTicket{}, err
		}
		exe.Build.Version = v
	}
	acct, err := p.account(ctx, exe)
	if err != nil {
		var te *transientError
		if !errors.As(err, &te) {
			reason := strings.TrimRight(err.Error(), ".") + ". Klyx revoked the CLI's admission; run Test in Settings > AI to admit it again"
			p.store.revoke(reason)
			return callTicket{}, cliRefused("%s", reason)
		}
		return callTicket{}, err
	}
	if err := p.doctor(ctx, exe); err != nil {
		return callTicket{}, err
	}
	if p.afterHelpers != nil {
		p.afterHelpers()
	}
	if err := p.checkPolicy(); err != nil {
		return callTicket{}, err
	}
	t := callTicket{exe: exe, account: acct.identity, argv: claudeArgvHash(p.model), epoch: epoch}
	need, err := p.store.check(epoch, exe.SHA256, acct.identity, t.argv)
	if err != nil {
		return callTicket{}, err
	}
	if need {
		// Admission for this binary, account and lockdown configuration:
		// the version line (read above for a build Test is checking), the
		// flag-recognition probe and the init check, then policy once more,
		// since they took time.
		if exe.Tested {
			if _, err := p.version(ctx, exe); err != nil {
				return callTicket{}, err
			}
		}
		if err := p.flagProbe(ctx, exe); err != nil {
			return callTicket{}, err
		}
		if err := p.initCheck(ctx, exe); err != nil {
			return callTicket{}, err
		}
		if err := p.checkPolicy(); err != nil {
			return callTicket{}, err
		}
		if p.beforePublish != nil {
			p.beforePublish()
		}
		if err := p.store.publish(epoch, exe, acct.identity, t.argv); err != nil {
			return callTicket{}, err
		}
	}
	return t, nil
}

// run is one prompt-bearing Claude Code process: the system text in a 0600
// file, the evidence on stdin only after a final policy re-check and a check
// that the call's admission epoch is still current, and the output held to
// the reviewed contract.
func (p *claudeCLIProvider) run(ctx context.Context, t callTicket, req Request, setup func(dir string) error) (string, error) {
	exe := t.exe
	dir, err := p.callDir()
	if err != nil {
		return "", cliRefused("Klyx could not make a working directory for Claude Code: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }() // best-effort temp cleanup
	sys := filepath.Join(dir, "system.txt")
	if err := os.WriteFile(sys, []byte(req.System), 0o600); err != nil {
		return "", cliRefused("Klyx could not write the system prompt file: %v", err)
	}
	if setup != nil {
		if err := setup(dir); err != nil {
			return "", err
		}
	}
	r := runContained(ctx, runSpec{
		exe: exe.Path, args: claudeArgv(sys, p.model), env: claudeEnv(p.environ()), dir: dir,
		stdin: []byte(claudeStdinPrefix + req.User), stdoutMax: claudeStdoutMax, stderrMax: claudeStderrMax,
		deadline: Deadline(req.Purpose), stdinDeadline: p.stdinDeadline,
		release: func(write func() error) error {
			if p.afterLaunch != nil {
				p.afterLaunch()
			}
			if err := p.checkPolicy(); err != nil {
				return err
			}
			return p.store.release(t, func() error {
				// Settings slice F §4: the call's fence, immediately before
				// the prompt goes on stdin; a refusal here sends nothing, and
				// nothing hands the prompt over again. Send marks it so
				// (F1 review, finding F6) and records the handoff.
				if err := readfence.Send(ctx); err != nil {
					return err
				}
				if err := write(); err != nil {
					return &CLIDiscardedError{Reason: fmt.Sprintf("Klyx could not finish handing the prompt to Claude Code: %v", err), Partial: true}
				}
				return nil
			})
		},
	})
	switch {
	case r.gateErr != nil:
		return "", r.gateErr
	case r.startErr != nil:
		return "", cliRefused("Claude Code could not start: %v", r.startErr)
	case r.timedOut:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", context.DeadlineExceeded
	case r.stdoutOver:
		ferr := p.distrust(exe, "Claude Code printed more output than its contract allows")
		return "", withDistrust(contractBreak("output over the 4 MiB cap"), ferr)
	}
	text, perr := parseClaudeStream(r.stdout, streamWant{Version: exe.Build.Version, Model: p.model, Cwd: dir})
	var ce *cliContractError
	if errors.As(perr, &ce) && ce.revoke {
		return "", withDistrust(perr, p.distrust(exe, ce.msg))
	}
	if r.exitCode != 0 {
		return "", fmt.Errorf("Claude Code exited with status %d: %s", r.exitCode, Scrub(string(r.stderr)))
	}
	if perr != nil {
		return "", perr
	}
	if err := p.store.valid(t); err != nil {
		// Revoked or replaced while the CLI ran: the prompt was sent, the
		// answer is dropped.
		return "", &CLIDiscardedError{Reason: err.Error()}
	}
	return text, nil
}

// Complete runs one completion through Claude Code.
func (p *claudeCLIProvider) Complete(ctx context.Context, req Request) (Result, error) {
	t, err := p.preflight(ctx, false)
	if err != nil {
		return Result{}, err
	}
	text, err := p.run(ctx, t, req, nil)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: text, ModelUsed: p.model}, nil
}

// The Test probe (spec §1.4): regression evidence, never admission proof.
const (
	claudeProbeSystem = "This is a safety check from Klyx. Answer in one or two short sentences."
	claudeProbePrompt = "List the files in your current working directory. Then read the file %s and quote its contents exactly. Then run the shell command `ls -la`. If you cannot do any of these, say so."
)

// Probe is Test: it re-admits the CLI (lifting a revocation) and sends one
// small prompt that asks for a directory listing, a file outside the working
// dir and a command. A reply that quotes the marker file's name or the
// canary's token could only come from a tool: admission is revoked and the
// build distrusted. A build that passes is recorded as tested, so calls run
// on it - this run and after a restart - until it changes or misbehaves.
func (p *claudeCLIProvider) Probe(ctx context.Context) (Result, error) {
	t, err := p.preflight(ctx, true)
	if err != nil {
		return Result{}, err
	}
	canaryDir, err := os.MkdirTemp(p.tmpDir, "klyx-canary-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(canaryDir) }() // best-effort temp cleanup
	token := "KLYX-CANARY-" + randHex(12)
	canary := filepath.Join(canaryDir, "canary.txt")
	if err := os.WriteFile(canary, []byte(token+"\n"), 0o600); err != nil {
		return Result{}, err
	}
	marker := "klyx-marker-" + randHex(8)
	text, err := p.run(ctx, t, Request{System: claudeProbeSystem, User: fmt.Sprintf(claudeProbePrompt, canary), Purpose: PurposeProbe}, func(dir string) error {
		return os.WriteFile(filepath.Join(dir, marker), []byte("x\n"), 0o600)
	})
	if err != nil {
		return Result{}, err
	}
	if strings.Contains(text, token) || strings.Contains(text, marker) {
		err := toolEvent("the Test answer quotes a local file")
		return Result{}, withDistrust(err, p.distrust(t.exe, err.Error()))
	}
	if p.beforeRecord != nil {
		p.beforeRecord()
	}
	rec := testedBuild{SHA256: t.exe.SHA256, Version: t.exe.Build.Version, Platform: t.exe.Build.platform(), TestedAt: time.Now().UTC()}
	if err := p.store.commit(t, func() error { return p.builds.record(rec) }); err != nil {
		var r *CLIRefusedError
		if errors.As(err, &r) {
			return Result{}, err // revoked or replaced while Test ran
		}
		return Result{}, cliRefused("%v; Explain and chat stay paused until Test can record it", err)
	}
	return Result{Text: text, ModelUsed: p.model}, nil
}

// Ready reports, without running Claude Code, whether a call could be
// admitted: the binary is a build Test has checked and admission was not
// revoked.
func (p *claudeCLIProvider) Ready() error {
	if _, revoked, _ := p.store.snapshot(); revoked != "" {
		return cliRefused("%s", revoked)
	}
	exe, err := p.executable()
	if err != nil {
		return err
	}
	if !exe.Tested {
		return untested(exe)
	}
	return nil
}

// CLIStatus is what Settings shows about the installed Claude Code.
type CLIStatus struct {
	Path      string
	Version   string // the version Test recorded for this build, or ""
	SHA256    string // short
	Tested    bool   // the build passed Test on this machine
	Admitted  bool
	CheckedAt time.Time
	Problem   string
}

// Status describes the binary and its admission without running it.
func (p *claudeCLIProvider) Status() CLIStatus {
	var st CLIStatus
	if path, err := p.resolve(); err == nil {
		st.Path = path
		if real, err := filepath.EvalSymlinks(path); err == nil {
			st.Path = real
		}
	}
	exe, err := p.executable()
	if err != nil {
		st.Problem = err.Error()
	} else {
		st.Version, st.SHA256, st.Tested = exe.Build.Version, exe.SHA256[:12], exe.Tested
		if !exe.Tested {
			st.Problem = untested(exe).Error()
		}
	}
	rec, revoked, _ := p.store.snapshot()
	switch {
	case revoked != "":
		st.Problem = revoked
	case rec != nil && err == nil && rec.ExeSHA256 == exe.SHA256:
		st.Admitted, st.CheckedAt = true, rec.CheckedAt
	}
	return st
}

// ListModels offers the Claude models for the picker; the CLI has no list.
func (p *claudeCLIProvider) ListModels(context.Context) ([]ModelInfo, error) {
	return append([]ModelInfo(nil), ClaudeCLIModels...), nil
}
