package gitcli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/moomora/klyx/internal/procrun"
)

// globalArgs lead every argv (parent §7, "On every call"). gc.auto and
// maintenance.auto are off as well, so no command repacks a cache or a
// staging repo behind the acquisition contract's pack enumeration.
var globalArgs = []string{
	"--no-pager",
	"-c", "core.hooksPath=/dev/null",
	"-c", "protocol.allow=never",
	"-c", "protocol.https.allow=always",
	"-c", "protocol.ssh.allow=always",
	"-c", "core.fsmonitor=false",
	"-c", "submodule.recurse=false",
	"-c", "diff.external=",
	"-c", "http.followRedirects=false",
	"-c", "fetch.unpackLimit=1",
	"-c", "transfer.unpackLimit=1",
	"-c", "gc.auto=0",
	"-c", "maintenance.auto=false",
}

// Runner runs the typed commands with one resolved git, the owner's
// allowlisted environment and the generated global config.
type Runner struct {
	Git          Git
	Env          Env
	GlobalConfig string // the generated config's path (WriteGlobalConfig)
}

// cmd is one built invocation. Only the constructors in commands.go build
// one; nothing takes a free-form argv.
type cmd struct {
	args   []string // after globalArgs
	stdin  []byte
	gitDir string // GIT_DIR, and the working directory; "" for init
	dir    string // the working directory when gitDir is ""
	fetch  bool   // an acquisition fetch: the fetch deadline, lazy fetch allowed
	stdout io.Writer
}

func (c cmd) argv() []string { return append(append([]string{}, globalArgs...), c.args...) }

// CmdError is a command that did not succeed. Stderr is scrubbed and capped.
type CmdError struct {
	Verb     string
	ExitCode int
	TimedOut bool
	Stderr   string
	Err      error
}

func (e *CmdError) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("git %s: %v", e.Verb, e.Err)
	case e.TimedOut:
		return fmt.Sprintf("git %s timed out", e.Verb)
	case e.Stderr != "":
		return fmt.Sprintf("git %s failed: %s", e.Verb, e.Stderr)
	}
	return fmt.Sprintf("git %s exited %d", e.Verb, e.ExitCode)
}

func (c cmd) verb() string {
	for _, a := range c.args {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			return a
		}
	}
	return "?"
}

// run executes c under procrun. A nonzero exit is a *CmdError unless ok
// accepts that exit code (merge-base's 1).
func (r Runner) run(ctx context.Context, c cmd, ok ...int) (procrun.Result, error) {
	deadline := localDeadline
	if c.fetch {
		deadline = fetchDeadline
	}
	dir := c.gitDir
	if dir == "" {
		dir = c.dir
	}
	res := procrun.Run(ctx, procrun.Spec{
		Exe: r.Git.Path, Args: c.argv(), Env: r.Env.isolated(r.GlobalConfig, c.gitDir, c.fetch), Dir: dir,
		Stdin: c.stdin, StdinDeadline: deadline, Stdout: c.stdout,
		StdoutMax: outputMax, StderrMax: stderrMax, Deadline: deadline, WaitDelay: waitDelay,
	})
	if res.StartErr != nil || res.GateErr != nil {
		err := res.StartErr
		if err == nil {
			err = res.GateErr
		}
		return res, &CmdError{Verb: c.verb(), ExitCode: -1, Err: err}
	}
	if res.TimedOut || ctx.Err() != nil {
		return res, &CmdError{Verb: c.verb(), ExitCode: res.ExitCode, TimedOut: res.TimedOut, Err: ctx.Err()}
	}
	if res.ExitCode == 0 {
		return res, nil
	}
	for _, code := range ok {
		if res.ExitCode == code {
			return res, nil
		}
	}
	return res, &CmdError{Verb: c.verb(), ExitCode: res.ExitCode, Stderr: truncate(Scrub(strings.TrimSpace(string(res.Stderr))), 500)}
}

const stderrMax = 64 << 10

var (
	httpUserinfo = regexp.MustCompile(`(?i)\b(https?://)[^/\s@]*@`)
	urlPassword  = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^/\s@:]*):[^/\s@]*@`)
	urlQuery     = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s?#]*)[?#][^\s'"]*`)
	secretPair   = regexp.MustCompile(`(?i)\b(access_token|token|password|passwd|secret|authorization|private[-_]token)(\s*[=:]\s*)\S+`)
)

// Scrub removes what could be a credential from text shown to the owner
// (slice 6 note §4): all of an http(s) URL's userinfo (a token often rides
// as the user), any other URL's password, URLs' query and fragment, and
// token-like pairs. An SSH user alone ("git@") is not a secret and stays.
func Scrub(s string) string {
	s = httpUserinfo.ReplaceAllString(s, "$1")
	s = urlPassword.ReplaceAllString(s, "$1@")
	s = urlQuery.ReplaceAllString(s, "$1")
	return secretPair.ReplaceAllString(s, "$1$2…")
}
