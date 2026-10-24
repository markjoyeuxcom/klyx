package evalrun

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The scenarios' scripts against stand-in kind and kubectl executables:
// setup and teardown touch only objects the eval directory owns (Codex's
// implementation review, finding 2). No cluster is read or written.

// fakeKubectl answers the scripts' calls from a directory of objects: each
// `get <args> -o json` is the file named by its args, if any; every write
// is logged and changes nothing.
const fakeKubectl = `#!/usr/bin/env bash
set -u
args=()
skip=0
for a in "$@"; do
	if ((skip)); then skip=0; continue; fi
	case $a in --kubeconfig|--context) skip=1 ;; *) args+=("$a") ;; esac
done
# a leading -n <namespace> goes after the verb, as the scripts' k() puts it first
if [[ "${args[0]}" == "-n" ]]; then args=("${args[2]}" -n "${args[1]}" "${args[@]:3}"); fi
echo "${args[*]}" >>"$FAKE/calls"
case "${args[0]}" in
config) echo "$FAKE_SERVER" ;;
get)
	if [[ "${args[*]}" == *"configmap klyx-eval-owner"* ]]; then cat "$FAKE/token"; exit 0; fi
	key=$(printf '%s_' "${args[@]:1}" | sed 's/_-o_json_$//; s/[^A-Za-z0-9_.-]/_/g')
	[[ -f "$FAKE/obj/$key" ]] || exit 1
	cat "$FAKE/obj/$key" ;;
apply) cat >/dev/null; [[ -z "${FAKE_APPLY_FAIL:-}" ]] ;;
*) ;;
esac
`

const fakeKind = `#!/usr/bin/env bash
echo "    server: $FAKE_SERVER"
`

type scriptLab struct {
	t                 *testing.T
	dir, fake, labDir string
	env               []string
}

func newScriptLab(t *testing.T) *scriptLab {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	root := t.TempDir()
	l := &scriptLab{t: t, dir: filepath.Join(root, "eval"), fake: filepath.Join(root, "fake"), labDir: "../../scripts/lab/eval"}
	bin := filepath.Join(root, "bin")
	for _, d := range []string{l.dir, filepath.Join(l.fake, "obj"), bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "kubectl"), fakeKubectl, 0o755)
	write(filepath.Join(bin, "kind"), fakeKind, 0o755)
	write(filepath.Join(l.dir, ".klyx-eval"), "", 0o600)
	write(filepath.Join(l.dir, "kubeconfig"), "unused", 0o600)
	write(filepath.Join(l.dir, "owner-token"), "t-123", 0o600)
	write(filepath.Join(l.fake, "token"), "t-123", 0o600)
	l.env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE="+l.fake, "FAKE_SERVER=https://127.0.0.1:52011", "KLYX_EVAL_DIR="+l.dir)
	return l
}

// object stores what `kubectl get <args> -o json` returns.
func (l *scriptLab) object(getArgs string, labels map[string]string) {
	var pairs []string
	for k, v := range labels {
		pairs = append(pairs, `"`+k+`":"`+v+`"`)
	}
	key := strings.NewReplacer(" ", "_", "/", "_").Replace(getArgs)
	if err := os.WriteFile(filepath.Join(l.fake, "obj", key), []byte(`{"metadata":{"labels":{`+strings.Join(pairs, ",")+`}}}`), 0o600); err != nil {
		l.t.Fatal(err)
	}
}

func (l *scriptLab) run(script string, extra ...string) (string, string, error) {
	cmd := exec.Command("bash", filepath.Join(l.labDir, script))
	cmd.Env = append(l.env, extra...)
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(filepath.Join(l.fake, "calls"))
	_ = os.Remove(filepath.Join(l.fake, "calls"))
	return string(out), string(calls), err
}

func ownerID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}

func TestTeardownDeletesOnlyTheEvalsObjects(t *testing.T) {
	l := newScriptLab(t)
	// nothing exists yet: teardown before setup deletes nothing
	if out, calls, err := l.run("e2-healthy-control/teardown.sh"); err != nil || strings.Contains(calls, "delete") {
		t.Fatalf("%v %s %s", err, out, calls)
	}
	// a namespace of that name that is not the eval's is left alone
	l.object("namespace klyx-eval", map[string]string{"team": "someone-else"})
	if out, calls, err := l.run("e2-healthy-control/teardown.sh"); err != nil || strings.Contains(calls, "delete") || !strings.Contains(out, "left namespace klyx-eval alone") {
		t.Fatalf("%v %s %s", err, out, calls)
	}
	// the eval's own is deleted
	l.object("namespace klyx-eval", map[string]string{"klyx.dev/eval-owner": ownerID("t-123")})
	if _, calls, err := l.run("e2-healthy-control/teardown.sh"); err != nil || !strings.Contains(calls, "delete namespace klyx-eval") {
		t.Fatalf("%v %s", err, calls)
	}
	// E1: the A1 lab's objects, not adopted, are left alone
	l.object("namespace klyx-a1-app", map[string]string{"klyx.dev/lab": "a1"})
	l.object("-n klyx-a1-flux kustomization a1-web", map[string]string{"klyx.dev/lab": "a1"})
	if _, calls, err := l.run("e1-flux-crashloop/teardown.sh"); err != nil || strings.Contains(calls, "delete") {
		t.Fatalf("%v %s", err, calls)
	}
}

func TestSetupRefusesWhatIsNotTheEvals(t *testing.T) {
	l := newScriptLab(t)
	l.object("namespace klyx-eval", map[string]string{"team": "someone-else"})
	out, calls, err := l.run("e2-healthy-control/setup.sh")
	if err == nil || strings.Contains(calls, "apply") || !strings.Contains(out, "is not this eval's") {
		t.Fatalf("a foreign namespace is refused before any write: %v %s %s", err, out, calls)
	}
	// the A1 lab's objects: refused without adoption, labelled with it
	l.object("namespace klyx-a1-app", map[string]string{"klyx.dev/lab": "a1"})
	if out, calls, err := l.run("e1-flux-crashloop/setup.sh", "FAKE_APPLY_FAIL=1"); err == nil || strings.Contains(calls, "label") || !strings.Contains(out, "KLYX_EVAL_ADOPT=a1") {
		t.Fatalf("%v %s %s", err, out, calls)
	}
	// the stand-in's apply fails, so setup stops after its claims
	_, calls, _ = l.run("e1-flux-crashloop/setup.sh", "KLYX_EVAL_ADOPT=a1", "FAKE_APPLY_FAIL=1")
	if !strings.Contains(calls, "label --overwrite namespace klyx-a1-app klyx.dev/eval-owner="+ownerID("t-123")) {
		t.Fatalf("an explicit adoption labels the A1 lab's namespace: %s", calls)
	}
	// adoption never takes an object that is not the A1 lab's
	l.object("namespace klyx-a1-app", map[string]string{"team": "someone-else"})
	if out, calls, err := l.run("e1-flux-crashloop/setup.sh", "KLYX_EVAL_ADOPT=a1", "FAKE_APPLY_FAIL=1"); err == nil || strings.Contains(calls, "label") {
		t.Fatalf("%v %s %s", err, out, calls)
	}
}

func TestTheScriptsRefuseAnotherCluster(t *testing.T) {
	l := newScriptLab(t)
	l.env = append(l.env, "FAKE_SERVER=https://api.production.example.invalid:6443")
	out, calls, err := l.run("e2-healthy-control/teardown.sh")
	if err == nil || strings.Contains(calls, "delete") || !strings.Contains(out, "not loopback") {
		t.Fatalf("%v %s %s", err, out, calls)
	}
}
