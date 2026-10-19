package capability

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var veleroServed = map[string]bool{veleroGroup: true, veleroGroup + "/v1": true}

func veleroDep(ns, name string, ready, rolled bool, c corev1.Container) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 2, Labels: map[string]string{"component": "velero"}},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}}}
	if ready {
		d.Status.AvailableReplicas = 1
	}
	if rolled {
		d.Status.ObservedGeneration, d.Status.UpdatedReplicas, d.Status.Replicas = 2, 1, 1
	}
	return d
}

func veleroC(image string, env []corev1.EnvVar, args ...string) corev1.Container {
	return corev1.Container{Name: "velero", Image: image, Args: append([]string{"server"}, args...), Env: env}
}

func detectV(objs ...runtime.Object) VeleroCapability {
	return (&Detector{cs: fake.NewSimpleClientset(objs...)}).detectVelero(context.Background(), veleroServed)
}

// Coverage S2a §3: servers by Velero's own selector, and only the image,
// the literal TZ and --disable-controllers read from them.
func TestDetectVelero(t *testing.T) {
	if v := (&Detector{cs: fake.NewSimpleClientset()}).detectVelero(context.Background(), map[string]bool{}); v.Present || v.Tier != Absent {
		t.Fatalf("absent: %+v", v)
	}
	ok := detectV(veleroDep("velero", "velero", true, true, veleroC("velero/velero:v1.18.4",
		[]corev1.EnvVar{{Name: "TZ", Value: "Europe/Amsterdam"}, {Name: "AWS_SECRET", Value: "s3cr3t"}}, "--disable-controllers=gc,schedule")))
	if !ok.Present || !ok.Healthy || !ok.Verified || !ok.ServersRead || len(ok.Servers) != 1 {
		t.Fatalf("%+v", ok)
	}
	s := ok.Servers[0]
	if s.Image != "velero/velero:v1.18.4" || s.TZ != "Europe/Amsterdam" || !s.TZSet || !s.ScheduleDisabled || !s.RolledOut {
		t.Fatalf("%+v", s)
	}
	// no other env value is kept
	if strings.Contains(strings.Join([]string{s.TZ, s.Image, s.Name}, " "), "s3cr3t") {
		t.Fatal("an unrelated env value was read")
	}

	down := detectV(veleroDep("velero", "velero", false, false, veleroC("velero/velero:v1.18.4", nil)))
	if down.Healthy || !down.Verified || !strings.Contains(down.Reason, "velero/velero is not ready") || down.Servers[0].RolledOut {
		t.Fatalf("not ready: %+v", down)
	}

	// a component=velero Deployment without a velero container is not a server
	other := veleroDep("velero", "ui", true, true, corev1.Container{Name: "ui", Image: "x"})
	none := detectV(other)
	if none.Healthy || none.Verified || !none.ServersRead || len(none.Servers) != 0 || none.Reason != veleroNoServer {
		t.Fatalf("no server: %+v", none)
	}

	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	unread := (&Detector{cs: cs}).detectVelero(context.Background(), veleroServed)
	if unread.ServersRead || unread.Verified || !strings.HasPrefix(unread.Reason, veleroServersUnread) {
		t.Fatalf("unreadable: %+v", unread)
	}

	ref := detectV(veleroDep("backup", "velero", true, true, veleroC("velero/velero:v1.17.2",
		[]corev1.EnvVar{{Name: "TZ", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "tz"}}}}, "--disable-controllers", "gc")))
	if s := ref.Servers[0]; !s.TZFromRef || s.TZ != "" || s.ScheduleDisabled {
		t.Fatalf("valueFrom TZ and another disabled controller: %+v", s)
	}
}

func TestScheduleDisabledArgs(t *testing.T) {
	for argv, want := range map[string]bool{
		"server --disable-controllers=schedule":       true,
		"server --disable-controllers schedule,gc":    true,
		"server --disable-controllers=backup-sync,gc": false,
		"server --features=EnableCSI":                 false,
		"server --disable-controllers":                false,
	} {
		if got := scheduleDisabled(strings.Fields(argv)); got != want {
			t.Errorf("%q: %v", argv, got)
		}
	}
}
