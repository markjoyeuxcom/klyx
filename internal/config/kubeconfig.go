package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/envread"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// DefaultKubeconfigPath resolves the kubeconfig Klyx reads contexts from:
// KUBECONFIG when set (including Kubernetes' path-list form), else ~/.kube/config.
func DefaultKubeconfigPath() string {
	if env := envread.Get(envread.Kubeconfig); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}

// KubeContexts lists the context names declared in a kubeconfig or KUBECONFIG
// path list, sorted. A missing file is not an error - it returns an empty list
// for that entry (a fresh machine simply has no contexts yet).
func KubeContexts(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	parts := strings.Split(path, string(os.PathListSeparator))
	if len(parts) == 1 {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil, nil
		}
		kc, err := clientcmd.LoadFromFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse kubeconfig %q: %w", path, err)
		}
		return sortedContextNames(kc.Contexts), nil
	}

	rules := &clientcmd.ClientConfigLoadingRules{Precedence: nonEmptyPaths(parts)}
	kc, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig %q: %w", path, err)
	}
	return sortedContextNames(kc.Contexts), nil
}

func nonEmptyPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sortedContextNames(contexts map[string]*clientcmdapi.Context) []string {
	out := make([]string, 0, len(contexts))
	for name := range contexts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
