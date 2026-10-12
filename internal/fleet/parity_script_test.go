package fleet_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The parity fixture script never deletes what it does not own (slice 6
// review, finding 1): no cluster is needed - kind is a stub that reports
// none and records any create.

func parityScript(t *testing.T, dir, stubs string, args ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts/lab/klyx-parity.sh")}, args...)...)
	cmd.Env = append(os.Environ(), "KLYX_PARITY_DIR="+dir, "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func kindStub(t *testing.T) string {
	t.Helper()
	stubs := t.TempDir()
	// no clusters; any other subcommand is recorded, never run
	script := "#!/usr/bin/env bash\nif [[ $1 == get && $2 == clusters ]]; then exit 0; fi\necho \"$*\" >>\"" + filepath.Join(stubs, "kind.calls") + "\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(stubs, "kind"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stubs
}

func TestParityScriptDirectoryOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the lab script is bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	sentinel := func(dir string) string {
		p := filepath.Join(dir, "unrelated.txt")
		if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("down refuses an unmarked directory, with no cluster", func(t *testing.T) {
		stubs, dir := kindStub(t), t.TempDir()
		keep := sentinel(dir)
		// files named like the script's own are not its own without the marker
		for _, f := range []string{"kubeconfig", "owner"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out, err := parityScript(t, dir, stubs, "down")
		if err == nil || !strings.Contains(out, "not a klyx-parity directory") {
			t.Fatalf("down in an unmarked directory: err %v, output %q", err, out)
		}
		for _, p := range []string{keep, filepath.Join(dir, "kubeconfig"), filepath.Join(dir, "owner")} {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("down deleted %s: %v", p, err)
			}
		}
	})

	t.Run("up refuses a non-empty unmarked directory before creating anything", func(t *testing.T) {
		stubs, dir := kindStub(t), t.TempDir()
		keep := sentinel(dir)
		out, err := parityScript(t, dir, stubs, "up")
		if err == nil || !strings.Contains(out, "not empty and not a klyx-parity directory") {
			t.Fatalf("up in a non-empty unmarked directory: err %v, output %q", err, out)
		}
		if _, err := os.Stat(filepath.Join(stubs, "kind.calls")); err == nil {
			b, _ := os.ReadFile(filepath.Join(stubs, "kind.calls"))
			t.Fatalf("up called kind beyond listing clusters: %s", b)
		}
		if _, err := os.Stat(filepath.Join(dir, ".klyx-parity-dir")); err == nil {
			t.Fatal("up marked a directory it refused")
		}
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("up touched the unrelated file: %v", err)
		}
	})

	t.Run("down in its own directory with no cluster removes only its named files", func(t *testing.T) {
		stubs, dir := kindStub(t), t.TempDir()
		keep := sentinel(dir)
		for _, f := range []string{".klyx-parity-dir", "kubeconfig", "owner", "sa-kubeconfig"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := parityScript(t, dir, stubs, "down"); err != nil {
			t.Fatalf("down: %v\n%s", err, out)
		}
		for _, f := range []string{".klyx-parity-dir", "kubeconfig", "owner", "sa-kubeconfig"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				t.Fatalf("down left its own %s", f)
			}
		}
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("down deleted the unrelated file: %v", err)
		}
	})

	t.Run("every cluster command refuses an unmarked directory", func(t *testing.T) {
		stubs, dir := kindStub(t), t.TempDir()
		for _, args := range [][]string{{"check"}, {"case", "1"}, {"revert", "1"}, {"rbac", "none"}, {"sa-kubeconfig"}, {"restart-apiserver"}} {
			out, err := parityScript(t, dir, stubs, args...)
			if err == nil || !strings.Contains(out, "not a klyx-parity directory") {
				t.Fatalf("%v in an unmarked directory: err %v, output %q", args, err, out)
			}
		}
	})
}
