package fleet

import (
	"context"
	"fmt"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ScaleWorkload sets the replica count on the ARMED Deployment or StatefulSet
// (uid, design §5b) via the scale subresource. The workload is confirmed
// first, and the Scale carries the UID as a precondition, so the server
// refuses a same-name replacement that lands after the check. DaemonSets are
// not scalable and negative replica counts are rejected.
func (c *ClusterConn) ScaleWorkload(ctx context.Context, kind, namespace, name, uid string, replicas int32) error {
	what := fmt.Sprintf("%s %s/%s", kind, namespace, name)
	if replicas < 0 {
		return fmt.Errorf("scale %s: replicas must be >= 0, got %d", what, replicas)
	}
	if kind != "Deployment" && kind != "StatefulSet" {
		return fmt.Errorf("unsupported kind %q for scale: must be Deployment or StatefulSet", kind)
	}
	if err := c.CheckWorkloadArmed(ctx, kind, namespace, name, uid); err != nil {
		return fmt.Errorf("scale %w - nothing was written", err)
	}
	scale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid)},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	}
	var err error
	if kind == "Deployment" {
		_, err = c.typed.AppsV1().Deployments(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	} else {
		_, err = c.typed.AppsV1().StatefulSets(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	}
	return armedWriteErr("scale", what, err)
}
