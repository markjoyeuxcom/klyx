package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func logPod(name string, containers ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name)}}
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c})
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{Name: c})
	}
	return p
}

func TestPodLogSnapshotReadsOneContainerWithoutFollowing(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(logPod("web", "app"))
	var opts *corev1.PodLogOptions
	cs.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if ga, ok := a.(k8stesting.GenericAction); ok && ga.GetSubresource() == "log" {
			opts, _ = ga.GetValue().(*corev1.PodLogOptions)
		}
		return false, nil, nil
	})
	c := &ClusterConn{typed: cs, clk: clock.Real{}}
	snap, err := c.PodLogSnapshot(context.Background(), "shop", "web", "", false, 50)
	if err != nil || snap.Container != "app" || snap.PodUID != "uid-web" || snap.Text == "" {
		t.Fatalf("a single-container pod reads its one container: %+v %v", snap, err)
	}
	if opts == nil || (opts.Follow || opts.LimitBytes == nil || *opts.LimitBytes != LogSnapshotMaxBytes || opts.TailLines == nil || *opts.TailLines != 50) {
		t.Fatalf("the read never follows and is bounded on the server: %+v", opts)
	}
}

func TestPodLogSnapshotAsksWhichContainer(t *testing.T) {
	c := &ClusterConn{typed: k8sfake.NewSimpleClientset(logPod("web", "app", "sidecar"))}
	snap, err := c.PodLogSnapshot(context.Background(), "shop", "web", "", false, 50)
	if err != nil || snap.Text != "" || strings.Join(snap.Containers, ",") != "app,sidecar" {
		t.Fatalf("several containers and none named reads nothing and lists them: %+v %v", snap, err)
	}
	if _, err := c.PodLogSnapshot(context.Background(), "shop", "web", "nope", false, 50); err == nil {
		t.Fatal("an unknown container is refused")
	}
	if _, err := c.PodLogSnapshot(context.Background(), "shop", "web", "app", true, 50); err == nil || !strings.Contains(err.Error(), "has not restarted") {
		t.Fatalf("previous on a container that never restarted is its own error: %v", err)
	}
}

func TestPodLogSnapshotDiscardsAReplacedPod(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(logPod("web", "app"))
	gets := 0
	cs.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if ga, ok := a.(k8stesting.GetAction); ok && ga.GetSubresource() == "" {
			gets++
			p := logPod("web", "app")
			if gets > 1 {
				p.UID = "uid-replacement"
			}
			return true, p, nil
		}
		return false, nil, nil
	})
	c := &ClusterConn{typed: cs}
	if _, err := c.PodLogSnapshot(context.Background(), "shop", "web", "app", false, 50); !errors.Is(err, ErrPodReplaced) {
		t.Fatalf("a pod replaced during the read is discarded: %v", err)
	}
}

func TestChatResolveKind(t *testing.T) {
	typed := k8sfake.NewSimpleClientset()
	crud := []string{"get", "list", "watch"}
	typed.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", SingularName: "pod", Kind: "Pod", Namespaced: true, ShortNames: []string{"po"}, Verbs: crud},
			{Name: "pods/log", Kind: "Pod", Namespaced: true, Verbs: []string{"get"}},
			{Name: "bindings", Kind: "Binding", Namespaced: true, Verbs: []string{"create"}},
		}},
		{GroupVersion: "cert-manager.io/v1", APIResources: []metav1.APIResource{{Name: "certificates", SingularName: "certificate", Kind: "Certificate", Namespaced: true, ShortNames: []string{"cert"}, Verbs: crud}}},
		{GroupVersion: "networking.internal.knative.dev/v1alpha1", APIResources: []metav1.APIResource{{Name: "certificates", SingularName: "certificate", Kind: "Certificate", Namespaced: true, Verbs: crud}}},
	}
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	for _, name := range []string{"Pod", "pods", "po", "POD"} {
		ks, err := c.ChatResolveKind(context.Background(), name, "")
		if err != nil || len(ks) != 1 || ks[0].GVR.Resource != "pods" {
			t.Fatalf("%q resolves to pods only (no subresource): %+v %v", name, ks, err)
		}
	}
	if ks, _ := c.ChatResolveKind(context.Background(), "Binding", ""); len(ks) != 0 {
		t.Fatalf("a kind without get and list is not resolvable: %+v", ks)
	}
	if ks, _ := c.ChatResolveKind(context.Background(), "certificate", ""); len(ks) != 2 {
		t.Fatalf("a kind served by two groups is ambiguous: %+v", ks)
	}
	if ks, _ := c.ChatResolveKind(context.Background(), "certificate", "cert-manager.io"); len(ks) != 1 || ks[0].GVR.Group != "cert-manager.io" {
		t.Fatalf("a group disambiguates: %+v", ks)
	}
}
