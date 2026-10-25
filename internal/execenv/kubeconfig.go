package execenv

import (
	"strings"

	"github.com/moomora/klyx/internal/envread"
)

// KubeconfigEnv returns the environment entry that pins a kubectl/helm/flux
// child process to a kubeconfig selection (a file or an OS path list) - the
// fleet layer passes the cluster's EFFECTIVE selection, absolute, so a child
// never re-resolves it through its own environment or working directory. It
// returns nil for an empty selection, which leaves the child to inherit.
//
// Passing the selection through KUBECONFIG rather than --kubeconfig keeps
// path-list semantics: --kubeconfig accepts a single file, while KUBECONFIG is
// merged with the same rules client-go applied to the list.
func KubeconfigEnv(kubeconfig string) []string {
	if strings.TrimSpace(kubeconfig) == "" {
		return nil
	}
	return []string{"KUBECONFIG=" + kubeconfig}
}

// Environ returns the parent environment followed by extra entries. os/exec
// keeps the last value of a duplicated key, so extra overrides an inherited
// entry (e.g. a KUBECONFIG exported in the launching shell). It returns nil
// when extra is empty, which tells os/exec to inherit the parent environment
// unchanged.
func Environ(extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	return envread.ForChild(envread.NativeTools, extra)
}
