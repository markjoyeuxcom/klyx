package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/gitops/argo"
)

// The Argo verbs follow the Flux UID-armed pattern (design §5b, 1c slice 3
// §5.4): only the Application the operator armed is written.

func seedArgoApp(name, uid string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": name, "namespace": "argocd", "uid": uid},
		"spec":       map[string]interface{}{},
	}}
}

func argoActionConn(objs ...runtime.Object) (*ClusterConn, *fake.FakeDynamicClient) {
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{argo.AppGVR: "ApplicationList"}, objs...)
	return newActionConn(dyn), dyn
}

func TestArgoVerbsWriteOnlyTheArmedApplication(t *testing.T) {
	verbs := map[string]func(c *ClusterConn, uid string) error{
		"refresh": func(c *ClusterConn, uid string) error {
			return c.RefreshArgoApp(context.Background(), "argocd", "demo", uid)
		},
		"sync": func(c *ClusterConn, uid string) error {
			return c.SyncArgoApp(context.Background(), "argocd", "demo", uid, argo.SyncOptions{Revision: "main"})
		},
	}
	for name, verb := range verbs {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				what  string
				seed  []runtime.Object
				armed string
				want  error
			}{
				{"replaced", []runtime.Object{seedArgoApp("demo", "uid-new")}, "uid-old", ErrReplaced},
				{"gone", nil, "uid-demo", ErrReplaced},
				{"unarmed", []runtime.Object{seedArgoApp("demo", "uid-demo")}, "", nil},
			} {
				c, dyn := argoActionConn(tc.seed...)
				err := verb(c, tc.armed)
				if err == nil || (tc.want != nil && (!errors.Is(err, tc.want) || !strings.Contains(err.Error(), "nothing was written"))) ||
					(tc.want == nil && !strings.Contains(err.Error(), "no object identity was armed")) {
					t.Fatalf("%s: want a refusal, got %v", tc.what, err)
				}
				for _, a := range dyn.Actions() {
					if _, ok := a.(k8stesting.PatchAction); ok {
						t.Fatalf("%s: nothing may be written", tc.what)
					}
				}
			}
			// the armed Application is written, its patch UID-guarded
			c, dyn := argoActionConn(seedArgoApp("demo", "uid-demo"))
			if err := verb(c, "uid-demo"); err != nil {
				t.Fatal(err)
			}
			guarded := false
			for _, a := range dyn.Actions() {
				if pa, ok := a.(k8stesting.PatchAction); ok && strings.Contains(string(pa.GetPatch()), `"uid":"uid-demo"`) {
					guarded = true
				}
			}
			if !guarded {
				t.Fatal("the write must carry the armed UID")
			}
		})
	}
}
