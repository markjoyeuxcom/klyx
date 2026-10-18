package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/clibin"
)

// Coverage S6 §6: the debug options and the kubectl gate.
func TestDebugOptions(t *testing.T) {
	if o, err := ValidateDebugOptions(DebugOptions{}); err != nil || o.Image != DefaultDebugImage || o.Profile != "general" {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	for _, img := range []string{"busybox:1.36", "nicolaka/netshoot:v0.13", "registry.k8s.io/e2e-test-images/agnhost:2.53", "localhost:5000/dbg@sha256:" + strings.Repeat("a", 64), "ghcr.io/org/tool"} {
		if _, err := ValidateDebugOptions(DebugOptions{Image: img}); err != nil {
			t.Errorf("%q is a reference: %v", img, err)
		}
	}
	for _, img := range []string{"busybox 1.36", "Busybox", "busybox:1.36;rm", "-it", "$(evil)", strings.Repeat("a", 256), "a/b/../c"} {
		if _, err := ValidateDebugOptions(DebugOptions{Image: img}); err == nil {
			t.Errorf("%q must be refused", img)
		}
	}
	for p := range DebugProfiles {
		if _, err := ValidateDebugOptions(DebugOptions{Profile: p}); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := ValidateDebugOptions(DebugOptions{Profile: "legacy"}); err == nil {
		t.Fatal("legacy is not offered")
	}
	c := &ClusterConn{kubeContext: "ctx"}
	argv, err := c.DebugCommand("ns", "pod", "app", DebugOptions{Image: "nicolaka/netshoot:v0.13", Profile: "sysadmin"})
	if err != nil || !contains(argv, "--image=nicolaka/netshoot:v0.13") || !contains(argv, "--profile=sysadmin") {
		t.Fatalf("argv: %v %v", argv, err)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestDebugKubectlGate(t *testing.T) {
	cases := []struct {
		minor     int
		ok        bool
		qualified bool
	}{{29, false, false}, {30, true, false}, {37, true, false}, {38, true, true}}
	for _, c := range cases {
		k := gateDebugKubectl(DebugKubectl{Version: "v1.x", Minor: c.minor})
		if k.OK != c.ok || (k.Qualified != "") != c.qualified || (!k.OK && k.Reason == "") {
			t.Errorf("1.%d: %+v", c.minor, k)
		}
	}
}

// The gate reads the same resolved binary the terminal runs.
func TestDebugKubectlCheckReadsTheResolvedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for kubectl")
	}
	script := func(body string) string {
		p := filepath.Join(t.TempDir(), "kubectl")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	use := func(path string, found bool) {
		old := kubectlBin
		t.Cleanup(func() { kubectlBin = old })
		kubectlBin = clibin.Binary{Name: "kubectl", LookPath: func(string) (string, error) {
			if !found {
				return "", errors.New("not found")
			}
			return path, nil
		}}
	}
	c := &ClusterConn{}
	use(script(`echo '{"clientVersion":{"major":"1","minor":"31+","gitVersion":"v1.31.4"}}'`), true)
	if k := c.DebugKubectlCheck(context.Background()); !k.OK || k.Minor != 31 || k.Version != "v1.31.4" {
		t.Fatalf("1.31: %+v", k)
	}
	use(script(`echo '{"clientVersion":{"major":"1","minor":"29","gitVersion":"v1.29.0"}}'`), true)
	if k := c.DebugKubectlCheck(context.Background()); k.OK || !strings.Contains(k.Reason, "older than 1.30") {
		t.Fatalf("1.29 refused: %+v", k)
	}
	use(script(`echo nonsense`), true)
	if k := c.DebugKubectlCheck(context.Background()); k.OK || !strings.Contains(k.Reason, "could not be read") {
		t.Fatalf("unreadable refused: %+v", k)
	}
	use("", false)
	if k := c.DebugKubectlCheck(context.Background()); k.OK || !strings.Contains(k.Reason, "not found") {
		t.Fatalf("unresolved refused: %+v", k)
	}
}
