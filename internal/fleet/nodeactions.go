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

// DrainNodeCmd builds the kubectl drain command. It does NOT start the process;
// the appbridge layer owns the lifecycle. The command pipes combined stdout+stderr
// so the caller can stream output directly to the UI.
//
// Requires kubectl on PATH. Returns an error if LookPath fails so the caller can
// surface "kubectl not found" before trying to start the process.
//
// An empty kubeContext is refused (same guard as ExecCommand/Helm*): kubectl
// would silently fall back to the kubeconfig's current-context, and this is the
// one destructive verb where "wrong cluster" is unrecoverable. The node name is
// validated and placed after "--" so it can never parse as a flag.
func (c *ClusterConn) DrainNodeCmd(nodeName string) (*exec.Cmd, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("cluster has no kubeconfig context")
	}
	if err := validateK8sName("node", nodeName); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return nil, fmt.Errorf("kubectl not found in PATH: %w", err)
	}
	args := []string{
		"--context", c.kubeContext,
		"drain",
		"--ignore-daemonsets",
		"--delete-emptydir-data",
		"--timeout=120s",
		"--", nodeName,
	}
	return exec.Command("kubectl", args...), nil //nolint:gosec // args are validated, not user-controlled raw input
}
