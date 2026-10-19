package healthsrc

import (
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/workloads"
)

// Projections build the pruned object a model keeps (1c addendum §1.2). They
// never write their input. Status and specs are deep-copied; label maps and
// container ports/resources are shared with the decoded input, which only
// the health-owned reflector holds and nothing writes - every consumer is
// read-only. What each keeps is exactly what the current consumers read -
// pod and node summaries, workload assembly, internal/health - and
// parity_test.go runs those consumers over raw and projected fixtures.

func projectMeta(m metav1.ObjectMeta, labels bool) metav1.ObjectMeta {
	out := metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace, UID: m.UID, Generation: m.Generation,
		CreationTimestamp: m.CreationTimestamp, DeletionTimestamp: m.DeletionTimestamp,
		DeletionGracePeriodSeconds: m.DeletionGracePeriodSeconds}
	if labels {
		out.Labels = m.Labels
	}
	if len(m.OwnerReferences) > 0 {
		out.OwnerReferences = append([]metav1.OwnerReference(nil), m.OwnerReferences...)
	}
	return out
}

func projectContainers(cs []corev1.Container) []corev1.Container {
	if cs == nil {
		return nil
	}
	out := make([]corev1.Container, len(cs))
	for i, c := range cs {
		out[i] = corev1.Container{Name: c.Name, Image: c.Image, Ports: c.Ports, Resources: c.Resources, RestartPolicy: c.RestartPolicy}
	}
	return out
}

// ProjectPod keeps metadata identity, labels, owners and lifecycle times;
// node, restart policy and per-container name/image/ports/resources/restart
// policy; the pod-level resources and the RuntimeClass overhead, which the
// scheduler counts (coverage S0); and the complete status. Env, volumes,
// mounts, probes, commands, security context, scheduling constraints and
// annotations are dropped.
func ProjectPod(obj any) (any, error) {
	p, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("healthsrc: want *Pod, got %T", obj)
	}
	return &corev1.Pod{
		ObjectMeta: projectMeta(p.ObjectMeta, true),
		Spec: corev1.PodSpec{NodeName: p.Spec.NodeName, RestartPolicy: p.Spec.RestartPolicy,
			Containers: projectContainers(p.Spec.Containers), InitContainers: projectContainers(p.Spec.InitContainers),
			Resources: p.Spec.Resources.DeepCopy(), Overhead: p.Spec.Overhead.DeepCopy()},
		Status: *p.Status.DeepCopy(),
	}, nil
}

// projectTemplate keeps a pod template's container names and images only.
func projectTemplate(t corev1.PodTemplateSpec) corev1.PodTemplateSpec {
	cs := make([]corev1.Container, len(t.Spec.Containers))
	for i, c := range t.Spec.Containers {
		cs[i] = corev1.Container{Name: c.Name, Image: c.Image}
	}
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: cs}}
}

// ProjectedDeployment is a pruned Deployment plus the Reloader targets
// precomputed from the raw object (its annotations and template are dropped).
type ProjectedDeployment struct {
	appsv1.Deployment
	Reload *workloads.ReloadTargets
}

type ProjectedStatefulSet struct {
	appsv1.StatefulSet
	Reload *workloads.ReloadTargets
}

type ProjectedDaemonSet struct {
	appsv1.DaemonSet
	Reload *workloads.ReloadTargets
}

func ProjectDeployment(obj any) (any, error) {
	d, ok := obj.(*appsv1.Deployment)
	if !ok {
		return nil, fmt.Errorf("healthsrc: want *Deployment, got %T", obj)
	}
	spec := *d.Spec.DeepCopy()
	spec.Template = projectTemplate(d.Spec.Template)
	return &ProjectedDeployment{
		Deployment: appsv1.Deployment{ObjectMeta: projectMeta(d.ObjectMeta, true), Spec: spec, Status: *d.Status.DeepCopy()},
		Reload:     workloads.ReloadTargetsOf(d.Annotations, d.Spec.Template.Spec),
	}, nil
}

func ProjectStatefulSet(obj any) (any, error) {
	s, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return nil, fmt.Errorf("healthsrc: want *StatefulSet, got %T", obj)
	}
	spec := *s.Spec.DeepCopy()
	spec.Template = projectTemplate(s.Spec.Template)
	spec.VolumeClaimTemplates = nil
	return &ProjectedStatefulSet{
		StatefulSet: appsv1.StatefulSet{ObjectMeta: projectMeta(s.ObjectMeta, true), Spec: spec, Status: *s.Status.DeepCopy()},
		Reload:      workloads.ReloadTargetsOf(s.Annotations, s.Spec.Template.Spec),
	}, nil
}

func ProjectDaemonSet(obj any) (any, error) {
	ds, ok := obj.(*appsv1.DaemonSet)
	if !ok {
		return nil, fmt.Errorf("healthsrc: want *DaemonSet, got %T", obj)
	}
	spec := *ds.Spec.DeepCopy()
	spec.Template = projectTemplate(ds.Spec.Template)
	return &ProjectedDaemonSet{
		DaemonSet: appsv1.DaemonSet{ObjectMeta: projectMeta(ds.ObjectMeta, true), Spec: spec, Status: *ds.Status.DeepCopy()},
		Reload:    workloads.ReloadTargetsOf(ds.Annotations, ds.Spec.Template.Spec),
	}, nil
}

// ProjectNode drops bulk the node summary never reads:
// the image list (often hundreds of entries) and annotations.
func ProjectNode(obj any) (any, error) {
	n, ok := obj.(*corev1.Node)
	if !ok {
		return nil, fmt.Errorf("healthsrc: want *Node, got %T", obj)
	}
	meta := projectMeta(n.ObjectMeta, true)
	status := *n.Status.DeepCopy()
	status.Images = nil
	return &corev1.Node{ObjectMeta: meta, Spec: *n.Spec.DeepCopy(), Status: status}, nil
}

// ProjectOwnerMeta keeps identity and owner references (ReplicaSets: the
// Deployment ownership chain).
func ProjectOwnerMeta(obj any) (any, error) {
	m, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("healthsrc: want *PartialObjectMetadata, got %T", obj)
	}
	return &metav1.PartialObjectMetadata{ObjectMeta: projectMeta(m.ObjectMeta, false)}, nil
}

// ProjectNameMeta keeps identity only (namespaces: a count).
func ProjectNameMeta(obj any) (any, error) {
	m, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("healthsrc: want *PartialObjectMetadata, got %T", obj)
	}
	return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: m.Name, UID: m.UID}}, nil
}

// ProjectNamespaceMeta keeps a namespace's name, UID and labels: the labels
// are what the Gateway API's namespace selectors read (coverage S7 §8).
func ProjectNamespaceMeta(obj any) (any, error) {
	m, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("healthsrc: want *PartialObjectMetadata, got %T", obj)
	}
	out := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: m.Name, UID: m.UID}}
	if len(m.Labels) > 0 {
		out.Labels = make(map[string]string, len(m.Labels))
		for k, v := range m.Labels {
			out.Labels[k] = v
		}
	}
	return out, nil
}

// namespaceSize is a projected namespace's JSON-encoded length.
func namespaceSize(v any) int {
	m, ok := v.(*metav1.PartialObjectMetadata)
	if !ok {
		return 1 << 30
	}
	b, err := json.Marshal(m.ObjectMeta)
	if err != nil {
		return 1 << 30
	}
	return len(b)
}

func errWant(want string, got any) error {
	return fmt.Errorf("healthsrc: want %s, got %T", want, got)
}
