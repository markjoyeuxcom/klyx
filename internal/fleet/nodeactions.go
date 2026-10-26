package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/execenv"
)

// SetCordon marks the ARMED node schedulable (false) or unschedulable (true)
// via a strategic-merge patch on spec.unschedulable - `kubectl cordon` /
// `kubectl uncordon` without shelling out. uid is the node object the verb
// was armed against (design §5b): it is confirmed before the write, and the
// patch carries it as metadata.uid, so a same-name replacement (a recycled
// node name) is refused by the API server even if it lands after the check.
func (c *ClusterConn) SetCordon(ctx context.Context, nodeName, uid string, cordon bool) error {
	verb := "cordon"
	if !cordon {
		verb = "uncordon"
	}
	if err := c.CheckNodeArmed(ctx, nodeName, uid); err != nil {
		return notSent(fmt.Errorf("%s %w - nothing was written", verb, err))
	}
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"uid": uid},
		"spec":     map[string]any{"unschedulable": cordon},
	})
	if err != nil {
		return notSent(fmt.Errorf("%s marshal: %w", verb, err))
	}
	out, err := c.typed.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, body, metav1.PatchOptions{})
	err = admission.Wrap(admission.Request{Operation: "UPDATE", Version: "v1", Resource: "nodes"}, "Node", "", nodeName, err)
	if err == nil {
		NoteReceipt(ctx, Receipt{UID: string(out.UID), Generation: out.Generation, ResourceVersion: out.ResourceVersion})
	}
	return armedWriteErr(verb, "node "+nodeName, err)
}

// CheckNodeArmed confirms the node at nodeName is the armed object (by UID)
// before a verb acts on it. A missing node or another UID is ErrReplaced.
func (c *ClusterConn) CheckNodeArmed(ctx context.Context, nodeName, uid string) error {
	if uid == "" {
		return fmt.Errorf("node %s: no object identity was armed - reload the list and retry", nodeName)
	}
	n, err := c.typed.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Errorf("node %s: %w", nodeName, ErrReplaced)
	case err != nil:
		return fmt.Errorf("node %s: %w", nodeName, err)
	case string(n.UID) != uid:
		return fmt.Errorf("node %s: %w", nodeName, ErrReplaced)
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
	cmd := exec.Command(bin, args...) //nolint:gosec // argv validated above, binary resolved via clibin
	cmd.Env = execenv.Environ(c.cliEnv())
	return cmd, nil
}
