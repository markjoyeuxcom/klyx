package healthsrc

import (
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/workloads"
)

// The §1.2 parity gate: every current consumer, run over raw and projected
// fixtures, must produce identical output. Fixtures carry the fields a
// projection drops (env, volumes, probes, annotations, images) so a consumer
// that starts reading one fails here, not in production.

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) metav1.Time { return metav1.NewTime(now.Add(-d)) }
func ptr[T any](v T) *T               { return &v }

var noise = map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"big":"json"}`, "team": "shop"}

func richContainer(name, image string) corev1.Container {
	return corev1.Container{
		Name: name, Image: image,
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
		},
		Env:             []corev1.EnvVar{{Name: "A", Value: "1"}, {Name: "S", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"}, Key: "k"}}}},
		EnvFrom:         []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}},
		VolumeMounts:    []corev1.VolumeMount{{Name: "cfg", MountPath: "/cfg"}},
		LivenessProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8080)}}},
		Command:         []string{"/bin/app"},
		SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(true)},
	}
}

func richPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		NodeName: "node-1", RestartPolicy: corev1.RestartPolicyAlways,
		InitContainers: []corev1.Container{
			{Name: "init", Image: "busybox:1.36", Command: []string{"true"}},
			{Name: "sidecar", Image: "envoy:1.30", RestartPolicy: ptr(corev1.ContainerRestartPolicyAlways)},
		},
		Containers: []corev1.Container{richContainer("app", "shop/app:1.2.3")},
		Volumes: []corev1.Volume{
			{Name: "cfg", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "mounted-config"}}}},
			{Name: "proj", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "proj-secret"}}}}}}},
		},
		Tolerations: []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
		Affinity:    &corev1.Affinity{},
	}
}

func pod(name string, owner metav1.OwnerReference, status corev1.PodStatus, mutate ...func(*corev1.Pod)) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name), ResourceVersion: "42",
			Labels: map[string]string{"app": "web", "pod-template-hash": "abc"}, Annotations: noise,
			CreationTimestamp: ago(2 * time.Hour), OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:   richPodSpec(),
		Status: status,
	}
	for _, m := range mutate {
		m(&p)
	}
	return p
}

func ctrl(kind, name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, UID: uid, Controller: ptr(true)}
}

func readyStatus() corev1.PodStatus {
	return corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.7", StartTime: ptr(ago(2 * time.Hour)),
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: ago(time.Hour)}},
		InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}},
			{Name: "sidecar", Ready: true, Started: ptr(true), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(time.Hour)}}},
		},
		ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 3, Image: "shop/app:1.2.3",
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(time.Hour)}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled", FinishedAt: ago(70 * time.Minute)}}}},
	}
}

func fixturePods() []corev1.Pod {
	rsWeb := ctrl("ReplicaSet", "web-abc", "uid-rs-web")
	crash := readyStatus()
	crash.Conditions[0].Status = corev1.ConditionFalse
	crash.ContainerStatuses[0].Ready = false
	crash.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s"}}
	pull := corev1.PodStatus{Phase: corev1.PodPending, StartTime: ptr(ago(10 * time.Minute)),
		ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "not found"}}}}}
	unsched := corev1.PodStatus{Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/3 nodes are available"}}}
	failed := corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted", Message: "The node was low on resource: memory."}
	return []corev1.Pod{
		pod("web-abc-1", rsWeb, readyStatus()),
		pod("web-abc-2", rsWeb, crash),
		pod("web-abc-3", rsWeb, pull),
		pod("batch-1", ctrl("ReplicaSet", "batch-x", "uid-rs-batch"), unsched),
		pod("db-0", ctrl("StatefulSet", "db", "uid-sts-db"), readyStatus(), func(p *corev1.Pod) { p.Labels = map[string]string{"app": "db"} }),
		pod("agent-x", ctrl("DaemonSet", "agent", "uid-ds-agent"), failed, func(p *corev1.Pod) { p.Labels = map[string]string{"app": "agent"} }),
		pod("bare-rs-1", ctrl("ReplicaSet", "bare", "uid-rs-bare"), readyStatus(), func(p *corev1.Pod) { p.Labels = map[string]string{"app": "bare"} }),
		pod("terminating", rsWeb, readyStatus(), func(p *corev1.Pod) {
			p.DeletionTimestamp = ptr(ago(10 * time.Minute))
			p.DeletionGracePeriodSeconds = ptr(int64(30))
		}),
	}
}

func fixtureReplicaSets() []metav1.PartialObjectMetadata {
	rs := func(name string, uid types.UID, owner *metav1.OwnerReference) metav1.PartialObjectMetadata {
		m := metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: uid, Annotations: noise, ResourceVersion: "9"}}
		if owner != nil {
			m.OwnerReferences = []metav1.OwnerReference{*owner}
		}
		return m
	}
	web := ctrl("Deployment", "web", "uid-dep-web")
	batch := ctrl("Deployment", "batch", "uid-dep-batch")
	return []metav1.PartialObjectMetadata{rs("web-abc", "uid-rs-web", &web), rs("batch-x", "uid-rs-batch", &batch), rs("bare", "uid-rs-bare", nil)}
}

func tmpl(app string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app}, Annotations: noise}, Spec: richPodSpec()}
}

func fixtureWorkloads() ([]appsv1.Deployment, []appsv1.StatefulSet, []appsv1.DaemonSet) {
	meta := func(name string, uid types.UID, ann map[string]string) metav1.ObjectMeta {
		a := map[string]string{}
		for k, v := range noise {
			a[k] = v
		}
		for k, v := range ann {
			a[k] = v
		}
		return metav1.ObjectMeta{Name: name, Namespace: "shop", UID: uid, Generation: 3, ResourceVersion: "77", CreationTimestamp: ago(48 * time.Hour),
			Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}, Annotations: a}
	}
	deploys := []appsv1.Deployment{
		{ObjectMeta: meta("web", "uid-dep-web", map[string]string{"reloader.stakater.com/auto": "true"}),
			Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(3)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}, Template: tmpl("web"),
				ProgressDeadlineSeconds: ptr(int32(600)), Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 3, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 3,
				Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable", LastUpdateTime: ago(time.Hour)}}}},
		{ObjectMeta: meta("batch", "uid-dep-batch", map[string]string{"configmap.reloader.stakater.com/reload": "a, b", "secret.reloader.stakater.com/reload": "s"}),
			Spec:   appsv1.DeploymentSpec{Replicas: ptr(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "batch"}}, Template: tmpl("batch")},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 0}},
		{ObjectMeta: meta("zero", "uid-dep-zero", nil), Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "zero"}}, Template: tmpl("zero")},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 3}},
	}
	stss := []appsv1.StatefulSet{{ObjectMeta: meta("db", "uid-sts-db", map[string]string{"reloader.stakater.com/auto": "true"}),
		Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(2)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}, Template: tmpl("db"),
			UpdateStrategy:       appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType, RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr(int32(1))}},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 3, Replicas: 2, ReadyReplicas: 1, CurrentReplicas: 1, UpdatedReplicas: 1, CurrentRevision: "a", UpdateRevision: "b"}}}
	dss := []appsv1.DaemonSet{{ObjectMeta: meta("agent", "uid-ds-agent", nil),
		Spec:   appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "agent"}}, Template: tmpl("agent")},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 3, DesiredNumberScheduled: 3, NumberReady: 2, UpdatedNumberScheduled: 3, NumberAvailable: 2}}}
	return deploys, stss, dss
}

func fixtureNodes() []corev1.Node {
	n := func(name string, mutate func(*corev1.Node)) corev1.Node {
		node := corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), ResourceVersion: "5", CreationTimestamp: ago(100 * time.Hour),
				Labels:      map[string]string{"node-role.kubernetes.io/control-plane": "", "kubernetes.io/arch": "arm64", "topology.kubernetes.io/zone": "westeurope-1"},
				Annotations: map[string]string{"node.alpha.kubernetes.io/ttl": "0", "volumes.kubernetes.io/controller-managed-attach-detach": "true"}},
			Spec: corev1.NodeSpec{PodCIDR: "10.244.0.0/24", ProviderID: "azure:///x", Taints: []corev1.Taint{{Key: "gpu", Effect: corev1.TaintEffectNoSchedule}}},
			Status: corev1.NodeStatus{
				Capacity:    corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi"), corev1.ResourcePods: resource.MustParse("110")},
				Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("7800m"), corev1.ResourceMemory: resource.MustParse("30Gi"), corev1.ResourcePods: resource.MustParse("110")},
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: ago(time.Hour), Reason: "KubeletReady"},
					{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				},
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.1.0.4"}, {Type: corev1.NodeHostName, Address: name}},
				NodeInfo:  corev1.NodeSystemInfo{KubeletVersion: "v1.36.1", OSImage: "Ubuntu 24.04", ContainerRuntimeVersion: "containerd://2.1", Architecture: "arm64", KernelVersion: "6.8"},
				Images:    []corev1.ContainerImage{{Names: []string{"shop/app:1.2.3"}, SizeBytes: 1 << 20}, {Names: []string{"envoy:1.30"}, SizeBytes: 1 << 22}},
			},
		}
		if mutate != nil {
			mutate(&node)
		}
		return node
	}
	return []corev1.Node{
		n("node-1", nil),
		n("node-2", func(x *corev1.Node) {
			x.Spec.Unschedulable = true
			x.Status.Conditions = append(x.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Reason: "KubeletHasDiskPressure"})
		}),
		n("node-3", func(x *corev1.Node) { x.Status.Conditions[0].Status = corev1.ConditionUnknown }),
		n("node-4", func(x *corev1.Node) { x.Status.Conditions = x.Status.Conditions[1:] }),
	}
}

func mustProject(t *testing.T, f func(any) (any, error), obj any) any {
	t.Helper()
	v, err := f(obj)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestProjectionParityPods(t *testing.T) {
	raw := fixturePods()
	snapshot := fixturePods() // the projection must never write its input
	proj := make([]corev1.Pod, len(raw))
	for i := range raw {
		proj[i] = *mustProject(t, ProjectPod, &raw[i]).(*corev1.Pod)
	}
	if !reflect.DeepEqual(raw, snapshot) {
		t.Fatal("ProjectPod mutated its input")
	}
	for i := range raw {
		if a, b := health.Pod(&raw[i], now), health.Pod(&proj[i], now); !reflect.DeepEqual(a, b) {
			t.Fatalf("health.Pod differs for %s:\nraw  %+v\nproj %+v", raw[i].Name, a, b)
		}
	}
	rawSums, projSums := workloads.SummarizePods(raw, now), workloads.SummarizePods(proj, now)
	controllers := map[types.UID]metav1.OwnerReference{}
	for _, rs := range fixtureReplicaSets() {
		if ref := metav1.GetControllerOf(&rs); ref != nil {
			controllers[rs.UID] = *ref
		}
	}
	workloads.ResolveReplicaSetOwners(rawSums, controllers)
	workloads.ResolveReplicaSetOwners(projSums, controllers)
	if !reflect.DeepEqual(rawSums, projSums) {
		t.Fatalf("pod summaries differ:\nraw  %+v\nproj %+v", rawSums, projSums)
	}
}

func TestProjectionParityWorkloads(t *testing.T) {
	deploys, stss, dss := fixtureWorkloads()
	pods := fixturePods()
	var rss []appsv1.ReplicaSet
	for _, m := range fixtureReplicaSets() {
		rss = append(rss, appsv1.ReplicaSet{ObjectMeta: m.ObjectMeta})
	}
	ev := workloads.Evidence{PodsComplete: true, ReplicaSetsComplete: true}
	for _, flux := range []bool{false, true} {
		want := workloads.AssembleObserved(deploys, rss, stss, dss, pods, ev, flux, now)

		reload := map[types.UID]*workloads.ReloadTargets{}
		var pd []appsv1.Deployment
		for i := range deploys {
			p := mustProject(t, ProjectDeployment, &deploys[i]).(*ProjectedDeployment)
			pd, reload[p.UID] = append(pd, p.Deployment), p.Reload
		}
		var ps []appsv1.StatefulSet
		for i := range stss {
			p := mustProject(t, ProjectStatefulSet, &stss[i]).(*ProjectedStatefulSet)
			ps, reload[p.UID] = append(ps, p.StatefulSet), p.Reload
		}
		var pds []appsv1.DaemonSet
		for i := range dss {
			p := mustProject(t, ProjectDaemonSet, &dss[i]).(*ProjectedDaemonSet)
			pds, reload[p.UID] = append(pds, p.DaemonSet), p.Reload
		}
		var ppods []corev1.Pod
		for i := range pods {
			ppods = append(ppods, *mustProject(t, ProjectPod, &pods[i]).(*corev1.Pod))
		}
		var prss []appsv1.ReplicaSet
		for _, m := range fixtureReplicaSets() {
			pm := mustProject(t, ProjectOwnerMeta, &m).(*metav1.PartialObjectMetadata)
			prss = append(prss, appsv1.ReplicaSet{ObjectMeta: pm.ObjectMeta})
		}
		got := workloads.AssembleProjected(pd, prss, ps, pds, ppods, ev, flux, now, reload)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("workloads differ (flux=%v):\nraw  %+v\nproj %+v", flux, want, got)
		}
		reloaders := 0
		for _, w := range got {
			if w.Reloader != nil {
				reloaders++
			}
		}
		if reloaders != 3 {
			t.Fatalf("the fixtures exercise Reloader on 3 workloads; got %d", reloaders)
		}
	}
}

func TestProjectionParityNodes(t *testing.T) {
	raw := fixtureNodes()
	var proj []corev1.Node
	for i := range raw {
		proj = append(proj, *mustProject(t, ProjectNode, &raw[i]).(*corev1.Node))
	}
	if a, b := workloads.SummarizeNodes(raw, now), workloads.SummarizeNodes(proj, now); !reflect.DeepEqual(a, b) {
		t.Fatalf("node summaries differ:\nraw  %+v\nproj %+v", a, b)
	}
	if len(proj[0].Status.Images) != 0 || len(proj[0].Annotations) != 0 {
		t.Fatal("the projection keeps the bulk it should drop")
	}
}
