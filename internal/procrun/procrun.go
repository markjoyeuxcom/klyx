// Package procrun runs one external process under containment: its own
// process group, killed on a deadline, a cancel, and when it exits; capped
// stdout and stderr; an environment the caller builds (never inherited);
// and stdin handed over through a gate the caller controls. It was
// extracted unchanged from the Claude Code CLI adapter (The Line slice 6
// note §2) so git runs under the same reviewed containment.
package procrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// ErrUnsupported: no reviewed containment on this platform (Windows needs a
// Job Object with kill-on-close); nothing is started.
var ErrUnsupported = errors.New("no reviewed process containment on this platform")

// Supported reports whether Run can contain a process here.
func Supported() bool { return runtime.GOOS != "windows" }

// Spec is one contained process.
type Spec struct {
	Exe       string
	Args      []string
	Env       []string // the whole environment; nil means empty, never inherited
	Dir       string
	Stdin     []byte // nil: /dev/null
	StdoutMax int
	StderrMax int
	// Stdout, when set, receives stdout as it arrives instead of a capped
	// buffer: for output whose size is not bounded up front (verify-pack's
	// object listing), consumed line by line. StdoutMax and
	// StopAtFirstLine are then unused; Result.Stdout stays empty.
	Stdout   io.Writer
	Deadline time.Duration
	// WaitDelay bounds waiting for output after the process is gone.
	WaitDelay time.Duration
	// StdinDeadline bounds handing stdin over.
	StdinDeadline time.Duration
	// Release gates handing stdin over, after the process started: it calls
	// write, under whatever must not change meanwhile, or refuses. A refusal
	// or a failed write kills the process group, and nothing more is
	// written.
	Release func(write func() error) error
	// StopAtFirstLine kills the process group as soon as stdout has one
	// complete line.
	StopAtFirstLine bool
}

// Result is what a contained process left.
type Result struct {
	Stdout, Stderr         []byte
	StdoutOver, StderrOver bool
	ExitCode               int
	TimedOut               bool
	StartErr, GateErr      error
}

// cappedBuffer keeps at most max bytes and records that more arrived. With
// firstLine set, it closes the channel once the first newline has arrived.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	over      bool
	firstLine chan struct{}
	sawLine   bool
}

func (c *cappedBuffer) Write(b []byte) (int, error) {
	if c.firstLine != nil && !c.sawLine && bytes.IndexByte(b, '\n') >= 0 {
		defer func() { c.sawLine = true; close(c.firstLine) }()
	}
	room := c.max - c.buf.Len()
	if len(b) > room {
		c.over = true
		if room > 0 {
			c.buf.Write(b[:room])
		}
		return len(b), nil
	}
	c.buf.Write(b)
	return len(b), nil
}

// Run runs one process in its own process group with capped output and a
// deadline; the whole group is killed on deadline, on cancel, and when the
// process exits.
func Run(ctx context.Context, s Spec) Result {
	if !Supported() {
		return Result{StartErr: ErrUnsupported}
	}
	ctx, cancel := context.WithTimeout(ctx, s.Deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Exe, s.Args...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	if cmd.Env == nil {
		cmd.Env = []string{} // never inherit
	}
	setProcGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = s.WaitDelay
	so, se := &cappedBuffer{max: s.StdoutMax}, &cappedBuffer{max: s.StderrMax}
	if s.StopAtFirstLine {
		so.firstLine = make(chan struct{})
	}
	cmd.Stdout, cmd.Stderr = so, se
	if s.Stdout != nil {
		cmd.Stdout = s.Stdout
	}
	// stdin is a pipe the caller's gate owns, so the hand-over can carry a
	// deadline and happen synchronously inside the release gate.
	var pr, pw *os.File
	if s.Stdin != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return Result{StartErr: err}
		}
		pr, pw = r, w
		cmd.Stdin = pr
	}
	if err := cmd.Start(); err != nil {
		if pr != nil {
			_ = pr.Close()
			_ = pw.Close()
		}
		return Result{StartErr: err}
	}
	if pr != nil {
		_ = pr.Close() // the child holds its own copy of the read end
		write := func() error {
			if err := pw.SetWriteDeadline(time.Now().Add(s.StdinDeadline)); err != nil {
				return err
			}
			if _, err := pw.Write(s.Stdin); err != nil {
				return err
			}
			return pw.Close()
		}
		release := s.Release
		if release == nil {
			release = func(w func() error) error { return w() }
		}
		if err := release(write); err != nil {
			_ = pw.Close()
			_ = killGroup(cmd)
			_ = cmd.Wait()
			return Result{GateErr: err}
		}
	}
	if s.StopAtFirstLine && s.Stdout == nil {
		exited := make(chan struct{})
		defer close(exited)
		go func() {
			select {
			case <-so.firstLine:
				_ = killGroup(cmd)
			case <-exited:
			}
		}()
	}
	_ = cmd.Wait()
	_ = killGroup(cmd) // anything the process left running in its group
	r := Result{
		Stdout: so.buf.Bytes(), Stderr: se.buf.Bytes(), StdoutOver: so.over, StderrOver: se.over,
		ExitCode: -1, TimedOut: ctx.Err() != nil,
	}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	return r
}
