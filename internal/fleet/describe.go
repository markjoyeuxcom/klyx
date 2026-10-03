package fleet

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/moomora/klyx/internal/clibin"
	"github.com/moomora/klyx/internal/execenv"
)

// kubectlBin is the shared kubectl resolution stack (env override -> PATH ->
// package-manager fallbacks), same shape as fluxcli/helmcli. The fallback step
// matters on macOS .app launches, which do not inherit the interactive shell
// PATH. Tests swap the lookup hooks.
var kubectlBin = clibin.Binary{
	Name:          "kubectl",
	EnvVar:        "KLYX_KUBECTL_PATH",
	FallbackPaths: defaultKubectlFallbackPaths,
}

func defaultKubectlFallbackPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/bin/kubectl", "/usr/local/bin/kubectl", "/opt/local/bin/kubectl"}
	case "linux":
		return []string{"/usr/local/bin/kubectl", "/usr/bin/kubectl", "/snap/bin/kubectl"}
	case "windows":
		var paths []string
		if root := strings.TrimSpace(os.Getenv("ChocolateyInstall")); root != "" {
			paths = append(paths, filepath.Join(root, "bin", "kubectl.exe"))
		}
		if root := strings.TrimSpace(os.Getenv("ProgramFiles")); root != "" {
			paths = append(paths, filepath.Join(root, "kubectl", "kubectl.exe"))
		}
		return paths
	default:
		return nil
	}
}

// validateObjectName guards the describe argv against flag or whitespace
// injection while accepting every legal Kubernetes object name. It is wider
// than validateK8sName because not all object names are DNS-1123 subdomains:
// RBAC objects legally carry ':' and uppercase (system:node-proxier). Still
// an allowlist that fails closed: must start alphanumeric (never
// flag-shaped), only [A-Za-z0-9._:-] after that, 253 max.
func validateObjectName(what, name string) error {
	if name == "" || len(name) > 253 {
		return fmt.Errorf("invalid %s name %q: must be 1-253 characters", what, name)
	}
	if !isObjectNameAlnum(name[0]) {
		return fmt.Errorf("invalid %s name %q: must start with an alphanumeric character", what, name)
	}
	for i := 1; i < len(name); i++ {
		ch := name[i]
		if isObjectNameAlnum(ch) || ch == '-' || ch == '.' || ch == '_' || ch == ':' {
			continue
		}
		return fmt.Errorf("invalid %s name %q: character %q is not allowed", what, name, rune(ch))
	}
	return nil
}

func isObjectNameAlnum(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
}

// DescribeCmd builds the `kubectl describe` invocation for one object. It does
// NOT run the process; the appbridge layer owns the lifecycle (buffers +
// timeout via ctx). Read-only by construction: describe never mutates.
//
// kindArg is the kubectl resource argument: "pod"/"node" for the core panels,
// plural.group for typed instances (plural alone when group is ""). It is
// validated as a DNS-1123 subdomain - which plural.group always is - so a
// flag-shaped or whitespace-bearing argument can never reach the argv. The
// object name is validated by validateObjectName and additionally placed
// after "--" so it can never parse as a flag (same guard as DrainNodeCmd).
//
// An empty kubeContext is refused (same guard as ExecCommand/DrainNodeCmd):
// kubectl would silently fall back to the kubeconfig's current-context and
// describe an object on whatever cluster the user's terminal points at.
func (c *ClusterConn) DescribeCmd(ctx context.Context, kindArg, namespace, name string) (*exec.Cmd, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("cluster has no kubeconfig context")
	}
	if err := validateK8sName("resource", kindArg); err != nil {
		return nil, err
	}
	if namespace != "" {
		if err := validateK8sName("namespace", namespace); err != nil {
			return nil, err
		}
	}
	if err := validateObjectName("object", name); err != nil {
		return nil, err
	}
	bin, ok := kubectlBin.Resolve()
	if !ok {
		return nil, fmt.Errorf("kubectl not found: install kubectl or point KLYX_KUBECTL_PATH at the binary")
	}
	args := []string{"--context", c.kubeContext, "describe", kindArg}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "--", name)
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // argv validated above, binary resolved via clibin
	cmd.Env = execenv.Environ(c.cliEnv())
	return cmd, nil
}
