package envread

import (
	"os"
	"os/exec"
	"reflect"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

func withEnv(t *testing.T, env map[string]string) *int {
	t.Helper()
	reads := 0
	pl, pe, ps := lookupEnv, environ, setenv
	lookupEnv = func(k string) (string, bool) {
		reads++
		v, ok := env[k]
		return v, ok
	}
	environ = func() []string {
		reads++
		var out []string
		for _, k := range []string{"HOME", "PATH", "LANG", "LC_ALL", "LC_CTYPE", "AWS_PROFILE", "ANTHROPIC_API_KEY", "LOGNAME", "KUBECONFIG"} {
			if v, ok := env[k]; ok {
				out = append(out, k+"="+v)
			}
		}
		return out
	}
	setenv = func(k, v string) error { env[k] = v; return nil }
	t.Cleanup(func() { lookupEnv, environ, setenv = pl, pe, ps })
	return &reads
}

// Settings S1 §3.1: a zero handle - the only one other code can make - is
// refused before any access.
func TestAZeroHandleIsRefusedBeforeAccess(t *testing.T) {
	reads := withEnv(t, map[string]string{"KLYX_CONFIG": "/x"})
	if v, ok := Lookup(Name{}); v != "" || ok || Get(Name{}) != "" || (Name{}).Key() != "" {
		t.Fatal("a zero name was read")
	}
	if Walk(Family{}) != nil || Dynamic(Purpose{})("KLYX_CONFIG") != "" {
		t.Fatal("a zero family or purpose was read")
	}
	if got := ForChild(Family{}, []string{"A=1"}); !reflect.DeepEqual(got, []string{"A=1"}) {
		t.Fatalf("a zero family's child environment: %v", got)
	}
	if err := Set(Name{}, "x"); err != ErrUndeclared {
		t.Fatal(err)
	}
	if *reads != 0 {
		t.Fatalf("%d reads", *reads)
	}
	// a handle missing from the table (a forged one, as only this package
	// could make) is refused too
	if v := Get(Name{&decl{key: "KLYX_CONFIG"}}); v != "" || *reads != 0 {
		t.Fatal("a non-member handle was read")
	}
	if err := Set(Home, "x"); err != ErrUndeclared {
		t.Fatal("a name not declared writable was set")
	}
}

func TestDeclaredHandlesRead(t *testing.T) {
	env := map[string]string{"KLYX_CONFIG": "/cfg", "PATH": "/bin"}
	withEnv(t, env)
	if Get(KlyxConfig) != "/cfg" || Get(Terminal) != "" {
		t.Fatal("a declared read")
	}
	if _, ok := Lookup(Terminal); ok {
		t.Fatal("unset reads as unset")
	}
	if err := Set(Path, "/bin:/opt"); err != nil || env["PATH"] != "/bin:/opt" {
		t.Fatal(err)
	}
}

// §3.3: native tools inherit the whole environment (nil when there is no
// extra, as execenv.Environ does today), the CLI and Git their allowlists.
func TestChildEnvironmentsKeepTheirPolicies(t *testing.T) {
	withEnv(t, map[string]string{"HOME": "/h", "PATH": "/bin", "LANG": "C", "LC_ALL": "C", "AWS_PROFILE": "p", "ANTHROPIC_API_KEY": "k", "LOGNAME": "me", "KUBECONFIG": "/k"})
	if ForChild(NativeTools, nil) != nil {
		t.Fatal("native tools with no extra inherit (nil)")
	}
	native := ForChild(NativeTools, []string{"KUBECONFIG=/pinned"})
	if native[len(native)-1] != "KUBECONFIG=/pinned" || !contains(native, "AWS_PROFILE=p") {
		t.Fatalf("native: %v", native)
	}
	cli := Walk(ClaudeCLI)
	if !reflect.DeepEqual(cli, []string{"HOME=/h", "PATH=/bin", "LANG=C", "LC_ALL=C", "LOGNAME=me"}) {
		t.Fatalf("cli: %v", cli)
	}
	if git := Walk(GitLocale); !reflect.DeepEqual(git, []string{"LANG=C", "LC_ALL=C"}) {
		t.Fatalf("git: %v", git)
	}
}

// The library constant KUBECONFIG is read through agrees with ours.
func TestKubeconfigIsClientGosName(t *testing.T) {
	if Kubeconfig.Key() != clientcmd.RecommendedConfigPathEnvVar {
		t.Fatalf("%q != %q", Kubeconfig.Key(), clientcmd.RecommendedConfigPathEnvVar)
	}
}

func TestDynamicReadsForItsPurpose(t *testing.T) {
	withEnv(t, map[string]string{"MY_KEY": "secret"})
	if Dynamic(AIKey)("MY_KEY") != "secret" {
		t.Fatal("the AI key's dynamic read")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Codex's S1 implementation review, R2: nil is the inherit-everything
// sentinel, so a refused family and an allowlist that admits nothing are
// never nil.
func TestOnlyNativeToolsInheritEverything(t *testing.T) {
	withEnv(t, map[string]string{"HOME": "/h", "AWS_PROFILE": "p"})
	if got := ForChild(Family{}, nil); got == nil || len(got) != 0 {
		t.Fatalf("a zero family: %#v", got)
	}
	if got := ForChild(Family{&family{id: "forged", inheritAll: true}}, nil); got == nil || len(got) != 0 {
		t.Fatalf("a forged inherit-all family: %#v", got)
	}
	if got := ForChild(GitLocale, nil); got == nil || len(got) != 0 {
		t.Fatalf("an allowlist that admits nothing: %#v", got)
	}
	if ForChild(NativeTools, nil) != nil {
		t.Fatal("native tools still inherit (nil)")
	}
}

// And in a real child: a refused family's process sees none of the parent.
func TestARefusedFamilysChildSeesNothing(t *testing.T) {
	if os.Getenv("KLYX_ENVREAD_CHILD") == "1" {
		if os.Getenv("KLYX_ENVREAD_MARKER") != "" {
			os.Exit(3)
		}
		os.Exit(0)
	}
	t.Setenv("KLYX_ENVREAD_MARKER", "inherited")
	cmd := exec.Command(os.Args[0], "-test.run=^TestARefusedFamilysChildSeesNothing$")
	cmd.Env = append(ForChild(Family{}, nil), "KLYX_ENVREAD_CHILD=1")
	if err := cmd.Run(); err != nil {
		t.Fatalf("the child saw the parent: %v", err)
	}
}
