package fleet

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/restmapper"
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
	args, err := c.describeArgs(kindArg, namespace, name)
	if err != nil {
		return nil, err
	}
	bin, ok := kubectlBin.Resolve()
	if !ok {
		return nil, fmt.Errorf("kubectl not found: install kubectl or point KLYX_KUBECTL_PATH at the binary")
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // argv validated above, binary resolved via clibin
	cmd.Env = execenv.Environ(c.cliEnv())
	return cmd, nil
}

// DescribeArgv is the same describe as a command for a terminal (The Line
// slice 14, design review 1): the cluster's kubeconfig selection travels in
// the argv (kubectlTerminalArgv - --kubeconfig for one file, env KUBECONFIG
// for a list, a list refused on Windows), so a copied command can never
// resolve the context through another file to another cluster.
func (c *ClusterConn) DescribeArgv(kindArg, namespace, name string) ([]string, error) {
	args, err := c.describeArgs(kindArg, namespace, name)
	if err != nil {
		return nil, err
	}
	argv, err := c.kubectlTerminalArgv()
	if err != nil {
		return nil, err
	}
	return append(argv, args...), nil
}

// describeArgs validates one describe and returns its kubectl arguments.
func (c *ClusterConn) describeArgs(kindArg, namespace, name string) ([]string, error) {
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
	args := []string{"--context", c.kubeContext, "describe", kindArg}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return append(args, "--", name), nil
}

// DescribeKind resolves a describe's resource argument to its group and
// kind through discovery, the way kubectl will (a resource, a short name,
// resource.group or resource.version.group), so an alias cannot slip past
// the payload classification (coverage S0b §4). An error means it could not
// be classified, and the caller withholds.
func (c *ClusterConn) DescribeKind(ctx context.Context, kindArg string) (string, string, error) {
	if c.typed == nil {
		return "", "", fmt.Errorf("discovery unavailable")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	dc := c.typed.Discovery()
	grs, err := restmapper.GetAPIGroupResources(dc)
	if len(grs) == 0 {
		if err == nil {
			err = fmt.Errorf("discovery returned no groups")
		}
		return "", "", err
	}
	m := restmapper.NewShortcutExpander(restmapper.NewDiscoveryRESTMapper(grs), dc, nil)
	gvr, gr := schema.ParseResourceArg(kindArg)
	if gvr != nil {
		if gvk, err := m.KindFor(*gvr); err == nil && !gvk.Empty() {
			return gvk.Group, gvk.Kind, nil
		}
	}
	gvk, kerr := m.KindFor(gr.WithVersion(""))
	if kerr != nil || gvk.Empty() {
		if kerr == nil {
			kerr = fmt.Errorf("unknown resource %q", kindArg)
		}
		return "", "", kerr
	}
	return gvk.Group, gvk.Kind, nil
}
