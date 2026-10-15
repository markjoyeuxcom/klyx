//go:build !windows

package cachediff

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestLabServerSideDiff dry-runs against the kind lab (slice 6 note §7.1
// point 6). Dry-run only: nothing is created or changed.
//
//	KLYX_LAB_CONTEXT=kind-klyx go test ./internal/cachediff -run TestLab -v -count=1
func TestLabServerSideDiff(t *testing.T) {
	ctxName := os.Getenv("KLYX_LAB_CONTEXT")
	if ctxName == "" {
		t.Skip("set KLYX_LAB_CONTEXT=kind-klyx")
	}
	if !strings.HasPrefix(ctxName, "kind-") {
		t.Fatalf("refusing %q: lab tests run only against kind clusters", ctxName)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: ctxName}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	probe := fmt.Sprintf("klyx-cachediff-probe-%d", time.Now().UnixNano())
	obj := func(y map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: y} }
	objs := []*unstructured.Unstructured{
		obj(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": probe, "namespace": "default"},
			"data": map[string]any{"a": "1"}}),
		obj(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "default",
			"labels": map[string]any{"klyx.dev/cachediff-probe": "1"}}}),
	}
	changes, err := ServerSideDiff(ctx, cfg, objs, []string{"default_" + probe + "-gone__ConfigMap"}, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Change{}
	for _, c := range changes {
		got[c.Kind+"/"+c.Name] = c
	}
	if c := got["ConfigMap/"+probe]; c.Action != "created" {
		t.Errorf("the new ConfigMap: %+v", c)
	}
	if c := got["Namespace/default"]; c.Action != "configured" || !strings.Contains(c.Diff, "klyx.dev/cachediff-probe") {
		t.Errorf("the labelled namespace: %+v", c)
	}
	if c := got["ConfigMap/"+probe+"-gone"]; c.Action != ActionPruned {
		t.Errorf("the pruned entry: %+v", c)
	}
	// Nothing was created.
	cl, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cm := &unstructured.Unstructured{}
	cm.SetAPIVersion("v1")
	cm.SetKind("ConfigMap")
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "default", Name: probe}, cm); !apierrors.IsNotFound(err) {
		t.Fatalf("the dry-run created the ConfigMap: %v", err)
	}
}
