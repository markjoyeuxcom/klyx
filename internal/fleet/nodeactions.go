package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// SetCordon marks a node schedulable (false) or unschedulable (true) via a
// strategic-merge patch on spec.unschedulable. This is equivalent to
// `kubectl cordon` / `kubectl uncordon` without shelling out.
func (c *ClusterConn) SetCordon(ctx context.Context, nodeName string, cordon bool) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"unschedulable": cordon,
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("SetCordon marshal: %w", err)
	}
	_, err = c.typed.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("SetCordon %q cordon=%v: %w", nodeName, cordon, err)
	}
	return nil
}

// DrainOptions are the user-composed kubectl drain flags. Zero value is the
// SAFE drain: daemonsets tolerated, emptyDir data preserved, no force,
// kubectl's default grace period, 120s timeout.
//
// DeleteEmptyDirData was HARDCODED ON until 2026-07-03 - flipped to an
// explicit opt-in (owner ruling): destroying pods' local scratch data is a
// choice the operator makes per drain, never a silent default.
type DrainOptions struct {
	IgnoreDaemonSets   bool // tolerate DaemonSet-managed pods (they respawn anyway)
	DeleteEmptyDirData bool // destroy emptyDir volumes - explicit opt-in
	Force              bool // evict pods without a controller (they will NOT come back)
	TimeoutSeconds     int  // 0 -> 120s default
}

// DrainNodeCmd builds the kubectl drain command. It does NOT start the process;
// the appbridge layer owns the lifecycle. The command pipes combined stdout+stderr
// so the caller can stream output directly to the UI.
//
// Resolves kubectl through the shared clibin stack (env override -> PATH ->
// package-manager fallbacks), same as DescribeCmd. Returns an error if kubectl
// can't be found so the caller can surface it before trying to start the process.
//
// An empty kubeContext is refused (same guard as ExecCommand/Helm*): kubectl
// would silently fall back to the kubeconfig's current-context, and this is the
// one destructive verb where "wrong cluster" is unrecoverable. The node name is
// validated and placed after "--" so it can never parse as a flag.
func (c *ClusterConn) DrainNodeCmd(nodeName string, opts DrainOptions) (*exec.Cmd, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("cluster has no kubeconfig context")
	}
	if err := validateK8sName("node", nodeName); err != nil {
		return nil, err
	}
	bin, ok := kubectlBin.Resolve()
	if !ok {
		return nil, fmt.Errorf("kubectl not found: install kubectl or point KLYX_KUBECTL_PATH at the binary")
	}
	timeout := opts.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	args := []string{"--context", c.kubeContext, "drain"}
	if opts.IgnoreDaemonSets {
		args = append(args, "--ignore-daemonsets")
	}
	if opts.DeleteEmptyDirData {
		args = append(args, "--delete-emptydir-data")
	}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, fmt.Sprintf("--timeout=%ds", timeout), "--", nodeName)
	return exec.Command(bin, args...), nil //nolint:gosec // argv validated above, binary resolved via clibin
}
