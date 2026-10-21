package appbridge

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/fleet"
)

// AI A1 §2: a workload's current pods, listed by its controller UIDs - the
// workload's own, or (a Deployment) its ReplicaSets' whose controller is the
// Deployment by UID. A label selector alone is not ownership: a pod of
// another workload with the same labels is not this one's.

const (
	chatWorkloadPodsMax = 100 // pods listed by the selector
	chatReplicaSetsMax  = 50
)

var replicaSetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}

// chatPodRef is one pod (or ReplicaSet) of a workload, by its UID, with its
// controller.
type chatPodRef struct {
	Name     string `json:"name"`
	UID      string `json:"uid"`
	OwnerUID string `json:"-"`
}

// workloadKinds are the kinds whose pods are found by controller UID.
var workloadKinds = map[string]bool{"apps/Deployment": true, "apps/StatefulSet": true, "apps/DaemonSet": true, "apps/ReplicaSet": true, "batch/Job": true}

type chatWorkload struct {
	Object      *unstructured.Unstructured
	ReplicaSets []chatPodRef // a Deployment's, by controller UID
	Pods        []chatPodRef
	// Partial: a list was capped, so pods may be missing
	Partial bool
}

func controllerUID(u *unstructured.Unstructured) string {
	for _, r := range u.GetOwnerReferences() {
		if r.Controller != nil && *r.Controller {
			return string(r.UID)
		}
	}
	return ""
}

// workloadPods reads the workload and lists its pods by controller UID.
func (t *chatTools) workloadPods(ctx context.Context, k fleet.ChatKind, ns, name string) (chatWorkload, error) {
	var w chatWorkload
	obj, err := t.conn.ChatGet(ctx, k.GVR, ns, name)
	if err != nil {
		return w, err
	}
	w.Object = obj
	raw, _, _ := unstructured.NestedMap(obj.Object, "spec", "selector")
	var ls metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &ls); err != nil {
		return w, fmt.Errorf("the workload's selector could not be read: %w", err)
	}
	sel, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil || sel.Empty() {
		return w, fmt.Errorf("the workload has no usable selector")
	}
	owners := map[string]bool{string(obj.GetUID()): true}
	if k.GVR.Group == "apps" && k.Kind == "Deployment" {
		owners = map[string]bool{}
		rss, err := t.conn.ChatList(ctx, replicaSetsGVR, ns, sel.String(), chatReplicaSetsMax)
		if err != nil {
			return w, err
		}
		if len(rss.Items) >= chatReplicaSetsMax || rss.GetContinue() != "" {
			w.Partial = true
		}
		for i := range rss.Items {
			if controllerUID(&rss.Items[i]) == string(obj.GetUID()) {
				owners[string(rss.Items[i].GetUID())] = true
				w.ReplicaSets = append(w.ReplicaSets, chatPodRef{Name: rss.Items[i].GetName(), UID: string(rss.Items[i].GetUID()), OwnerUID: string(obj.GetUID())})
			}
		}
	}
	pods, err := t.conn.ChatList(ctx, podsGVRv1, ns, sel.String(), chatWorkloadPodsMax)
	if err != nil {
		return w, err
	}
	if len(pods.Items) >= chatWorkloadPodsMax || pods.GetContinue() != "" {
		w.Partial = true
	}
	for i := range pods.Items {
		if ou := controllerUID(&pods.Items[i]); owners[ou] {
			w.Pods = append(w.Pods, chatPodRef{Name: pods.Items[i].GetName(), UID: string(pods.Items[i].GetUID()), OwnerUID: ou})
		}
	}
	return w, nil
}

var podsGVRv1 = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
