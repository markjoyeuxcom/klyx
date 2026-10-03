package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// terminalGOOS is the platform the terminal argv is built for; tests override it
// to exercise the Windows refusal on any host.
var terminalGOOS = runtime.GOOS

// kubectlTerminalArgv returns the argv prefix that runs kubectl against THIS
// cluster from a terminal Klyx launches (or a command the user copies). That
// terminal does not inherit Klyx's environment or working directory - macOS
// Terminal.app starts a fresh login shell with its own KUBECONFIG - so the
// effective kubeconfig selection (absolute files, recorded when the connection
// was made) must travel in the argv itself, or the same context name can
// resolve through a different file to a different cluster:
//   - one file: kubectl --kubeconfig <file>;
//   - several: env KUBECONFIG=<list> kubectl (--kubeconfig takes a single
//     file; KUBECONFIG merges the list with the rules client-go applied). There
//     is no `env` on Windows, so a list is refused there rather than dropped;
//   - none recorded (a conn built outside the factory): plain kubectl.
//
// kubectl itself is resolved through clibin (KLYX_KUBECTL_PATH, PATH, fallbacks)
// like drain/describe, and a relative resolution is made absolute for the same
// working-directory reason. When Klyx cannot find kubectl, the bare name is
// left for the terminal's own PATH.
func (c *ClusterConn) kubectlTerminalArgv() ([]string, error) {
	bin, ok := kubectlBin.Resolve()
	if !ok {
		bin = "kubectl"
	} else if !filepath.IsAbs(bin) {
		if abs, err := filepath.Abs(bin); err == nil {
			bin = abs
		}
	}
	paths := c.kubeconfigPaths
	switch {
	case len(paths) == 0:
		return []string{bin}, nil
	case len(paths) == 1:
		return []string{bin, "--kubeconfig", paths[0]}, nil
	case terminalGOOS == "windows":
		return nil, fmt.Errorf("cluster kubeconfig is a path list, which cannot be passed to a Windows terminal: point kubeconfig at a single file")
	default:
		return []string{"env", "KUBECONFIG=" + strings.Join(paths, string(os.PathListSeparator)), bin}, nil
	}
}

// ExecCommand returns the kubectl exec invocation for an interactive shell in
// the container, as argv (no shell quoting — the caller renders or launches it).
// Probes nothing: /bin/sh is the universal fallback.
//
// The shell expression uses the bash-if-available trick: command -v bash checks
// whether bash is on PATH without spawning a subshell, and execs into it if
// found, otherwise falls back to sh. This gives a better interactive experience
// on nodes that have bash (most distros) while remaining safe on minimal images.
//
// Returns an error if kubeContext is empty ("cluster has no kubeconfig context").
// Names are validated so a flag-shaped or control-character-bearing argument
// can never reach the argv (or the shell quoting layer above it).
func (c *ClusterConn) ExecCommand(namespace, pod, container string) ([]string, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("cluster has no kubeconfig context")
	}
	if err := validateExecNames(namespace, pod, container); err != nil {
		return nil, err
	}
	argv, err := c.kubectlTerminalArgv()
	if err != nil {
		return nil, err
	}
	argv = append(argv,
		"--context", c.kubeContext,
		"-n", namespace,
		"exec", "-it", pod,
	)
	if container != "" {
		argv = append(argv, "-c", container)
	}
	argv = append(argv,
		"--",
		"/bin/sh", "-c",
		"command -v bash >/dev/null && exec bash || exec sh",
	)
	return argv, nil
}

// DebugCommand builds the kubectl debug argv: an interactive ephemeral
// busybox container attached to the pod. --target shares the target
// container's process namespace, so a distroless container's processes (and
// filesystem, via /proc/1/root) are inspectable even though exec has no shell
// to run there. Honesty note carried to the UI: the ephemeral container stays
// listed on the pod spec until the pod is recreated - it exits with the
// shell, but Kubernetes has no API to remove it.
func (c *ClusterConn) DebugCommand(namespace, pod, container string) ([]string, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("cluster has no kubeconfig context")
	}
	if err := validateExecNames(namespace, pod, container); err != nil {
		return nil, err
	}
	argv, err := c.kubectlTerminalArgv()
	if err != nil {
		return nil, err
	}
	argv = append(argv,
		"--context", c.kubeContext,
		"-n", namespace,
		"debug", "-it", pod,
		"--image=busybox:1.36",
	)
	if container != "" {
		argv = append(argv, "--target="+container)
	}
	argv = append(argv, "--", "sh")
	return argv, nil
}

// validateExecNames guards the exec/debug argv builders: namespace and pod are
// required names; container is optional (empty = default container).
func validateExecNames(namespace, pod, container string) error {
	if err := validateK8sName("namespace", namespace); err != nil {
		return err
	}
	if err := validateK8sName("pod", pod); err != nil {
		return err
	}
	if container != "" {
		if err := validateK8sName("container", container); err != nil {
			return err
		}
	}
	return nil
}
