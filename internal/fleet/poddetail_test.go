package fleet

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/workloads"
)

func makeFakePod(ns, name string) *corev1.Pod {
	ctrl := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         ns,
			Name:              name,
			UID:               "uid-test-1",
			Labels:            map[string]string{"app": "web"},
			Annotations:       map[string]string{"owner": "platform"},
			CreationTimestamp: metav1.NewTime(time.Unix(100_000, 0)),
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "web-rs", Controller: &ctrl},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-1",
			ServiceAccountName: "web-sa",
			Containers:         []corev1.Container{{Name: "web", Image: "nginx:1.25"}},
		},
		Status: corev1.PodStatus{
			Phase:    corev1.PodRunning,
			PodIP:    "10.0.0.5",
			QOSClass: corev1.PodQOSBurstable,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue, Reason: ""},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "web",
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}
}

func TestPodDetail_BasicFields(t *testing.T) {
	now := time.Unix(200_000, 0)
	pod := makeFakePod("team-a", "web-abc")
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "web-abc.e1", Namespace: "team-a"},
		InvolvedObject: corev1.ObjectReference{UID: "uid-test-1"},
		Type:           "Warning",
		Reason:         "Backoff",
		Message:        "back off",
		Count:          2,
		LastTimestamp:  metav1.NewTime(now),
	}

	cs := typedfake.NewSimpleClientset(pod, ev)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(now)}

	d, err := c.PodDetail(context.Background(), "team-a", "web-abc")
	if err != nil {
		t.Fatalf("PodDetail: %v", err)
	}

	// Summary populated.
	if d.Summary.Name != "web-abc" || d.Summary.Namespace != "team-a" {
		t.Errorf("summary name/ns: %+v", d.Summary)
	}
	if d.Summary.Rank != workloads.Healthy {
		t.Errorf("rank: got %v, want Healthy", d.Summary.Rank)
	}
	if d.Summary.Node != "node-1" || d.Summary.IP != "10.0.0.5" {
		t.Errorf("node/ip: %+v", d.Summary)
	}
	if d.Summary.OwnerKind != "ReplicaSet" || d.Summary.OwnerName != "web-rs" {
		t.Errorf("owner: %+v", d.Summary)
	}

	// Detail-specific fields.
	if d.Labels["app"] != "web" {
		t.Errorf("labels: %+v", d.Labels)
	}
	if d.Annotations["owner"] != "platform" {
		t.Errorf("annotations: %+v", d.Annotations)
	}
	if d.ServiceAccount != "web-sa" {
		t.Errorf("serviceAccount: %q", d.ServiceAccount)
	}
	if d.QoSClass != "Burstable" {
		t.Errorf("qosClass: %q", d.QoSClass)
	}

	// Conditions mapped.
	if len(d.Conditions) != 1 || d.Conditions[0].Type != "Ready" || d.Conditions[0].Status != "True" {
		t.Errorf("conditions: %+v", d.Conditions)
	}

	// Events via instanceEvents.
	if len(d.Events) != 1 || d.Events[0].Reason != "Backoff" || d.Events[0].Count != 2 {
		t.Errorf("events: %+v", d.Events)
	}

	// YAML contains pod kind.
	if !strings.Contains(d.YAML, "kind: Pod") {
		t.Errorf("yaml missing 'kind: Pod': %s", d.YAML)
	}
	// managedFields stripped.
	if strings.Contains(d.YAML, "managedFields") {
		t.Errorf("yaml must not contain managedFields")
	}
}

func TestPodDetail_NotFound(t *testing.T) {
	cs := typedfake.NewSimpleClientset()
	c := &ClusterConn{typed: cs, clk: clock.Real{}}
	_, err := c.PodDetail(context.Background(), "ns", "missing")
	if err == nil {
		t.Fatal("want error for missing pod, got nil")
	}
}

func TestPodDetail_EmptyLabelsNonNil(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "bare",
			UID:       "uid-bare",
			// No labels set.
		},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img:1"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	cs := typedfake.NewSimpleClientset(pod)
	c := &ClusterConn{typed: cs, clk: clock.Real{}}

	d, err := c.PodDetail(context.Background(), "ns", "bare")
	if err != nil {
		t.Fatalf("PodDetail: %v", err)
	}
	if d.Labels == nil {
		t.Error("Labels must be non-nil even when pod has no labels")
	}
	if d.Annotations == nil {
		t.Error("Annotations must be non-nil even when pod has no annotations")
	}
}

// Coverage S11b §3.5, §7: the pod's claims as the scheduler maps them, and
// its device health, init containers first, bounded, messages clipped.
func TestPodDetail_ClaimsAndDeviceHealth(t *testing.T) {
	pod := makeFakePod("team-a", "web-abc")
	gen, direct, tmpl := "web-abc-gpu-x", "shared-nic", "gpu"
	pod.Spec.ResourceClaims = []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: &tmpl}, {Name: "nic", ResourceClaimName: &direct}}
	pod.Status.ResourceClaimStatuses = []corev1.PodResourceClaimStatus{{Name: "gpu", ResourceClaimName: &gen}}
	long := strings.Repeat("m", 300)
	var many []corev1.ResourceHealth
	for i := 0; i < 20; i++ {
		many = append(many, corev1.ResourceHealth{ResourceID: corev1.ResourceID("cdi/" + string(rune('a'+i))), Health: corev1.ResourceHealthStatusHealthy})
	}
	many[0] = corev1.ResourceHealth{ResourceID: "cdi/a", Health: corev1.ResourceHealthStatusUnhealthy, Message: &long}
	pod.Status.ContainerStatuses[0].AllocatedResourcesStatus = []corev1.ResourceStatus{{Name: "claim:gpu/req", Resources: many}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "side", AllocatedResourcesStatus: []corev1.ResourceStatus{{Name: "claim:nic",
		Resources: []corev1.ResourceHealth{{ResourceID: "cdi/n", Health: corev1.ResourceHealthStatusUnknown}}}}}}
	c := &ClusterConn{typed: typedfake.NewSimpleClientset(pod), clk: clock.NewFake(time.Unix(200_000, 0))}
	d, err := c.PodDetail(context.Background(), "team-a", "web-abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Claims) != 2 || d.Claims[0].Claim != gen || d.Claims[0].Source != "template" || d.Claims[1].Claim != direct || d.Claims[1].Source != "claim" {
		t.Fatalf("claims: %+v", d.Claims)
	}
	if len(d.DeviceHealth) != 2 || !d.DeviceHealth[0].Init || d.DeviceHealth[0].Name != "claim:nic" || d.DeviceHealth[1].Container != "web" {
		t.Fatalf("device health, init containers first: %+v", d.DeviceHealth)
	}
	devs := d.DeviceHealth[1].Devices
	if len(devs) != maxDeviceHealthDevices || devs[0].Health != "Unhealthy" || devs[0].Message != strings.Repeat("m", 256)+"…" {
		t.Fatalf("devices bounded at %d, message clipped at 256: %d, %+v", maxDeviceHealthDevices, len(devs), devs[0])
	}
	// a pod with no claims and no reports carries neither
	plain := makeFakePod("team-a", "plain")
	c = &ClusterConn{typed: typedfake.NewSimpleClientset(plain), clk: clock.NewFake(time.Unix(200_000, 0))}
	if d, err := c.PodDetail(context.Background(), "team-a", "plain"); err != nil || len(d.Claims) != 0 || len(d.DeviceHealth) != 0 {
		t.Fatalf("plain: %+v %v", d.Claims, err)
	}
}

// Codex's implementation review of 177bfe9, finding 3: device health is
// counted over every device, the unhealthy kept first, and entries past
// the bound are counted.
func TestPodDetail_DeviceHealthCountsEverything(t *testing.T) {
	pod := makeFakePod("team-a", "web-abc")
	var res []corev1.ResourceHealth
	for i := 0; i < 16; i++ {
		res = append(res, corev1.ResourceHealth{ResourceID: corev1.ResourceID(fmt.Sprintf("cdi/h%02d", i)), Health: corev1.ResourceHealthStatusHealthy})
	}
	res = append(res, corev1.ResourceHealth{ResourceID: "cdi/bad", Health: corev1.ResourceHealthStatusUnhealthy})
	var statuses []corev1.ResourceStatus
	for i := 0; i < maxDeviceHealth+3; i++ {
		statuses = append(statuses, corev1.ResourceStatus{Name: corev1.ResourceName(fmt.Sprintf("claim:c%02d", i)), Resources: res})
	}
	pod.Status.ContainerStatuses[0].AllocatedResourcesStatus = statuses
	c := &ClusterConn{typed: typedfake.NewSimpleClientset(pod), clk: clock.NewFake(time.Unix(200_000, 0))}
	d, err := c.PodDetail(context.Background(), "team-a", "web-abc")
	if err != nil {
		t.Fatal(err)
	}
	h := d.DeviceHealth[0]
	if h.Unhealthy != 1 || h.Healthy != 16 || h.DevicesOmitted != 1 || len(h.Devices) != maxDeviceHealthDevices || h.Devices[0].ID != "cdi/bad" {
		t.Fatalf("counted over all, the unhealthy kept first: %+v", h)
	}
	if len(d.DeviceHealth) != maxDeviceHealth || d.DeviceHealthOmitted != 3 {
		t.Fatalf("entries past the bound counted: %d, %d", len(d.DeviceHealth), d.DeviceHealthOmitted)
	}
}
