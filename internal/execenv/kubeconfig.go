package execenv

import (
	"os"
	"strings"
)

// KubeconfigEnv returns the environment entry that pins a kubectl/helm/flux
// child process to a cluster's configured kubeconfig selection (a file or an
// OS path list). It returns nil when the cluster has no explicit kubeconfig:
// client-go then used the default loading rules (KUBECONFIG, else
// ~/.kube/config) and the child inherits exactly the same resolution.
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
	return append(os.Environ(), extra...)
}
