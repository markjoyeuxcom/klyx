package execenv

import (
	"os"
	"reflect"
	"runtime"
	"testing"

	"github.com/moomora/klyx/internal/envread"
)

// Settings S1 §3.3: through envread, native tools still inherit the whole
// parent environment - nil with no extra, extras last - as the old
// append(os.Environ(), extra...) did.
func TestEnvironMatchesTheOldInheritance(t *testing.T) {
	t.Setenv("AWS_PROFILE", "klyx-parity")
	t.Setenv("KLYX_PARITY_PLUGIN", "exec-plugin-own")
	if Environ(nil) != nil || Environ([]string{}) != nil {
		t.Fatal("no extra inherits (nil)")
	}
	extra := []string{"KUBECONFIG=/pinned"}
	if got, old := Environ(extra), append(os.Environ(), extra...); !reflect.DeepEqual(got, old) {
		t.Fatalf("new %v\nold %v", got, old)
	}
}

// The desktop PATH extension reads and writes PATH through envread, with the
// same order and platform expansion as the old os.Getenv/os.Setenv pair.
func TestApplyDesktopToolPathMatchesTheOld(t *testing.T) {
	t.Setenv("PATH", "/klyx/parity/bin"+string(os.PathListSeparator)+"/usr/bin")
	want := AugmentPath(os.Getenv("PATH"), runtime.GOOS, func(n envread.Name) string { return os.Getenv(n.Key()) })
	ApplyDesktopToolPath()
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("PATH %q, want %q", got, want)
	}
}
