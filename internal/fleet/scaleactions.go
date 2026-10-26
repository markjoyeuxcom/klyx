package fleet

import (
	"context"
	"fmt"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/admission"
)

// ScaleWorkload sets the replica count on the ARMED Deployment or StatefulSet
// (uid, design §5b) via the scale subresource. The workload is confirmed
// first, and the Scale carries the UID as a precondition, so the server
// refuses a same-name replacement that lands after the check. DaemonSets are
// not scalable and negative replica counts are rejected.
func (c *ClusterConn) ScaleWorkload(ctx context.Context, kind, namespace, name, uid string, replicas int32) error {
	what := fmt.Sprintf("%s %s/%s", kind, namespace, name)
	if replicas < 0 {
		return notSent(fmt.Errorf("scale %s: replicas must be >= 0, got %d", what, replicas))
	}
	if kind != "Deployment" && kind != "StatefulSet" {
		return notSent(fmt.Errorf("unsupported kind %q for scale: must be Deployment or StatefulSet", kind))
	}
	if err := c.CheckWorkloadArmed(ctx, kind, namespace, name, uid); err != nil {
		return notSent(fmt.Errorf("scale %w - nothing was written", err))
	}
	scale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid)},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	}
	var err error
	var out *autoscalingv1.Scale
	if kind == "Deployment" {
		out, err = c.typed.AppsV1().Deployments(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	} else {
		out, err = c.typed.AppsV1().StatefulSets(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	}
	if err == nil && out != nil {
		// a Scale carries no generation: the effect reads the workload's
		NoteReceipt(ctx, Receipt{UID: string(out.UID), ResourceVersion: out.ResourceVersion})
	}
	// coverage S3b §3.1: an UPDATE on the scale subresource
	err = admission.Wrap(admission.Request{Operation: "UPDATE", Group: "apps", Version: "v1", Resource: workloadResource(kind), Subresource: "scale", Namespace: namespace},
		kind, namespace, name, err)
	return armedWriteErr("scale", what, err)
}

// workloadResource is a workload kind's resource (coverage S3b §3.1).
func workloadResource(kind string) string {
	switch kind {
	case "Deployment":
		return "deployments"
	case "StatefulSet":
		return "statefulsets"
	case "DaemonSet":
		return "daemonsets"
	}
	return ""
}
