//go:build !windows

package appbridge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// S1 §5's contract, against stand-ins.
func TestCheckVersionsRunsContained(t *testing.T) {
	t.Setenv("AWS_PROFILE", "must-not-pass")
	t.Setenv("KLYX_SETTINGS_SENTINEL", "must-not-pass")
	rec := t.TempDir()
	argv, envf := filepath.Join(rec, "argv"), filepath.Join(rec, "env")
	kubectl := standIn(t, "kubectl", `printf '%s\n' "$@" > `+argv+`
env > `+envf+`
[ -n "$KUBECONFIG" ] || exit 4
[ -f "$KUBECONFIG" ] && [ ! -s "$KUBECONFIG" ] || exit 3
printf '{"clientVersion":{"gitVersion":"v1.31.2","major":"1"},"kustomizeVersion":"v5.4.2"}'`,
		[]string{"version", "--client", "--output=json"}, parseKubectlVersion)
	loud := standIn(t, "helm", `yes v3.16.2 | head -c 200000`, []string{"version", "--short"}, parseHelmVersion)
	pidf := filepath.Join(rec, "pid")
	orphan := standIn(t, "flux", `sleep 30 &
echo $! > `+pidf+`
echo 'flux: v2.4.0'`, []string{"version", "--client"}, parseFluxVersion)
	failing := standIn(t, "fail", `echo boom >&2; exit 7`, []string{"version"}, parseHelmVersion)
	garbled := standIn(t, "garbled", `printf 'helm\033[31m v3\n'`, []string{"version"}, parseHelmVersion)
	git := diagTool{name: "git"}

	got := settingsWith(git, kubectl, loud, orphan, failing, garbled).CheckVersions(context.Background())
	byName := map[string]ToolVersionDTO{}
	for _, v := range got {
		byName[v.Name] = v
	}
	if v := byName["kubectl"]; v.Version != "v1.31.2" || v.Failure != "" {
		t.Fatalf("kubectl %+v", v)
	}
	if b, _ := os.ReadFile(argv); string(b) != "version\n--client\n--output=json\n" {
		t.Fatalf("argv %q", b)
	}
	env, _ := os.ReadFile(envf)
	for _, line := range strings.Split(strings.TrimSpace(string(env)), "\n") {
		k, _, _ := strings.Cut(line, "=")
		switch k {
		case "PATH", "HOME", "KUBECONFIG", "PWD", "SHLVL", "_", "OLDPWD": // the shell adds the last four
		default:
			t.Errorf("the stand-in saw %s", k)
		}
	}
	if v := byName["helm"]; v.Version != "" || !strings.Contains(v.Failure, "64 KiB") {
		t.Fatalf("helm %+v", v)
	}
	if v := byName["flux"]; v.Version != "v2.4.0" {
		t.Fatalf("flux %+v", v)
	}
	// the child that outlived a normal exit was killed with its group
	if b, err := os.ReadFile(pidf); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		deadline := time.Now().Add(3 * time.Second)
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			t.Fatalf("the stand-in's child %d is still running", pid)
		}
	} else {
		t.Fatal(err)
	}
	if v := byName["fail"]; v.Exit == nil || *v.Exit != 7 || strings.Contains(v.Failure, "boom") {
		t.Fatalf("fail %+v", v)
	}
	if v := byName["garbled"]; v.Version != "" || v.Failure == "" {
		t.Fatalf("garbled %+v", v)
	}
	if v := byName["git"]; !v.Reused || v.Failure == "" {
		t.Fatalf("git without a Git service %+v", v)
	}
}

func TestCheckVersionsHangsAndCancelsAreBounded(t *testing.T) {
	hang := standIn(t, "kubectl", `sleep 60`, []string{"version"}, parseKubectlVersion)
	s := settingsWith(hang)
	s.deadline = 300 * time.Millisecond
	start := time.Now()
	if v := s.CheckVersions(context.Background()); !strings.Contains(v[0].Failure, "did not finish") {
		t.Fatalf("%+v", v)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the deadline did not hold")
	}
	s.deadline = versionDeadline
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start = time.Now()
	if v := s.CheckVersions(ctx); v[0].Failure != "cancelled" {
		t.Fatalf("%+v", v)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("the cancel did not hold")
	}
}
