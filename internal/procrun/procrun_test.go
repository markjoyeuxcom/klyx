//go:build !windows

package procrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The containment contract (The Line slice 6 note §2): a process group
// killed on deadline, capped output, an environment never inherited, a
// gated stdin.

func sh(script string) Spec {
	return Spec{Exe: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/bin:/usr/bin"}, StdoutMax: 64, StderrMax: 64, Deadline: 5 * time.Second, WaitDelay: time.Second, StdinDeadline: time.Second}
}

func TestOutputIsCappedAndOverflowRecorded(t *testing.T) {
	r := Run(context.Background(), sh(`i=0; while [ $i -lt 100 ]; do printf 0123456789; i=$((i+1)); done; echo err >&2`))
	if len(r.Stdout) != 64 || !r.StdoutOver || r.StderrOver || strings.TrimSpace(string(r.Stderr)) != "err" || r.ExitCode != 0 {
		t.Fatalf("got %d bytes over=%v stderr=%q exit=%d", len(r.Stdout), r.StdoutOver, r.Stderr, r.ExitCode)
	}
}

func TestTheEnvironmentIsNeverInherited(t *testing.T) {
	t.Setenv("KLYX_PROCRUN_LEAK", "leaked")
	s := sh(`env`)
	s.Exe, s.Args, s.Env, s.StdoutMax = "/usr/bin/env", nil, nil, 4096
	r := Run(context.Background(), s)
	if strings.Contains(string(r.Stdout), "KLYX_PROCRUN_LEAK") || strings.Contains(string(r.Stdout), "HOME=") {
		t.Fatalf("a nil Env inherited the parent's environment: %q", r.Stdout)
	}
}

func TestADeadlineKillsTheWholeGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	s := sh(`sleep 30 & echo $! > ` + pidFile + `; wait`)
	s.Deadline = 300 * time.Millisecond
	start := time.Now()
	r := Run(context.Background(), s)
	if !r.TimedOut || time.Since(start) > 4*time.Second {
		t.Fatalf("timedOut=%v after %v", r.TimedOut, time.Since(start))
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return // the background child went with its group
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background child %d survived the deadline", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestACancelKillsTheGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	r := Run(ctx, sh(`sleep 30`))
	if !r.TimedOut || r.ExitCode == 0 {
		t.Fatalf("a cancelled run: timedOut=%v exit=%d", r.TimedOut, r.ExitCode)
	}
}

func TestStdinIsHandedOverThroughTheGate(t *testing.T) {
	s := sh(`cat`)
	s.Stdin = []byte("hello")
	called := false
	s.Release = func(write func() error) error { called = true; return write() }
	r := Run(context.Background(), s)
	if !called || string(r.Stdout) != "hello" {
		t.Fatalf("release called=%v stdout=%q", called, r.Stdout)
	}
	s.Release = func(func() error) error { return errors.New("refused") }
	r = Run(context.Background(), s)
	if r.GateErr == nil || len(r.Stdout) != 0 {
		t.Fatalf("a refused gate must kill and write nothing: %+v", r)
	}
}

func TestStopAtFirstLine(t *testing.T) {
	s := sh(`echo first; sleep 30; echo second`)
	s.StopAtFirstLine = true
	start := time.Now()
	r := Run(context.Background(), s)
	if string(r.Stdout) != "first\n" || time.Since(start) > 4*time.Second {
		t.Fatalf("stdout=%q after %v", r.Stdout, time.Since(start))
	}
}

func TestAStdoutWriterReceivesEverythingUncapped(t *testing.T) {
	var got strings.Builder
	s := sh(`i=0; while [ $i -lt 100 ]; do printf 0123456789; i=$((i+1)); done`)
	s.Stdout = &got
	r := Run(context.Background(), s)
	if got.Len() != 1000 || len(r.Stdout) != 0 || r.StdoutOver || r.ExitCode != 0 {
		t.Fatalf("writer got %d bytes, result stdout=%d over=%v exit=%d", got.Len(), len(r.Stdout), r.StdoutOver, r.ExitCode)
	}
}
