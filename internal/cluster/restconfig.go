// Package cluster resolves per-cluster credentials from kubeconfig. Exec
// credential plugins (kubelogin, aws eks get-token) are invoked automatically
// by client-go when the resolved context uses them.
package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/execenv"
	"github.com/moomora/klyx/internal/readfence"
)

// RESTConfig builds a *rest.Config for the cluster's kubeconfig context.
func RESTConfig(cc config.ClusterConfig) (*rest.Config, error) {
	execenv.ApplyDesktopToolPath()

	// Fail closed on an empty context: an empty CurrentContext override lets
	// client-go silently fall back to the kubeconfig's current-context, which
	// could be the wrong (or a protected prd) cluster. This matches the empty-
	// context refusal the day-2 kubectl verbs already enforce.
	if cc.Context == "" {
		return nil, fmt.Errorf("cluster %q has no kubeconfig context", cc.Name)
	}

	kubeconfigPath := cc.Kubeconfig
	loadingRules := kubeconfigLoadingRules(kubeconfigPath)
	overrides := &clientcmd.ConfigOverrides{CurrentContext: cc.Context}
	cc2 := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	rc, err := cc2.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("resolve rest config for context %q: %w", cc.Context, err)
	}
	// client-go defaults to 5 QPS / 10 burst - kubectl-conservative and far too
	// low for a platform tool that fans out reads across a cluster's CRD kinds
	// (a CRD browser counting ~100 kinds would otherwise queue for minutes behind
	// client-side throttling). Raise it; the apiserver's API Priority & Fairness
	// still protects the server. Only applied when the kubeconfig did not set it.
	if rc.QPS == 0 {
		rc.QPS = 50
	}
	if rc.Burst == 0 {
		rc.Burst = 100
	}
	// every request checks its context's read fence first: a composite AI
	// read stops at its next component once its fence changes (AI A1
	// implementation review, finding 3); a context without one is unchanged
	rc.Wrap(readfence.Transport)
	return rc, nil
}

func kubeconfigLoadingRules(path string) *clientcmd.ClientConfigLoadingRules {
	if path == "" {
		return clientcmd.NewDefaultClientConfigLoadingRules()
	}
	parts := KubeconfigPaths(path)
	if len(parts) > 1 {
		return &clientcmd.ClientConfigLoadingRules{Precedence: parts}
	}
	return &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
}

// EffectiveKubeconfigPaths returns the ABSOLUTE kubeconfig files the loading
// rules above resolve for a cluster, in precedence order: its configured
// selection when set, otherwise client-go's defaults - this process's
// KUBECONFIG, else ~/.kube/config. Duplicates are dropped, as client-go does.
//
// Subprocesses must be pinned to exactly these files. A terminal Klyx launches
// starts with its OWN environment and working directory, so an unpinned (or
// relative) selection can resolve the same context name through a different
// file - another cluster. Relative paths are made absolute against this
// process's working directory, which is what client-go resolved them against.
func EffectiveKubeconfigPaths(configured string) []string {
	var raw []string
	switch {
	case strings.TrimSpace(configured) != "":
		raw = KubeconfigPaths(configured)
	case os.Getenv(clientcmd.RecommendedConfigPathEnvVar) != "":
		raw = KubeconfigPaths(os.Getenv(clientcmd.RecommendedConfigPathEnvVar))
	default:
		raw = []string{clientcmd.RecommendedHomeFile}
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, p := range raw {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// KubeconfigPaths splits a cluster's kubeconfig setting into its files, using
// the OS path-list separator and dropping empty entries - the exact split the
// client-go loading rules above use. Exported so the kubectl/helm/flux
// subprocess builders resolve the SAME file selection as client-go.
func KubeconfigPaths(path string) []string {
	raw := strings.Split(path, string(os.PathListSeparator))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
