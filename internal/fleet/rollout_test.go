package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
)

const depUID = types.UID("dep-uid-1")

func rolloutDeployment(paused bool, revision string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "shop", Name: "cart", UID: depUID,
			Annotations: map[string]string{deployRevisionAnn: revision},
		},
		Spec: appsv1.DeploymentSpec{Paused: paused},
	}
}

func rolloutRS(name, revision, image, cause string, created time.Time) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "shop", Name: name,
			Annotations:       map[string]string{deployRevisionAnn: revision, changeCauseAnn: cause},
			OwnerReferences:   []metav1.OwnerReference{{UID: depUID, Kind: "Deployment", Name: "cart"}},
			CreationTimestamp: metav1.Time{Time: created},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "cart", appsv1.DefaultDeploymentUniqueLabelKey: "hash-" + revision}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
			},
		},
	}
}

func TestRolloutHistory_DeploymentNewestFirstWithCurrent(t *testing.T) {
	base := time.Unix(10_000, 0)
	typed := k8sfake.NewSimpleClientset(
		rolloutDeployment(false, "3"),
		rolloutRS("cart-1", "1", "shop/cart:v1", "initial deploy", base.Add(-3*time.Hour)),
		rolloutRS("cart-3", "3", "shop/cart:v3", "", base.Add(-10*time.Minute)),
		rolloutRS("cart-2", "2", "shop/cart:v2", "", base.Add(-1*time.Hour)),
		// A stray RS owned by someone else must not appear.
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "other",
			Annotations:     map[string]string{deployRevisionAnn: "9"},
			OwnerReferences: []metav1.OwnerReference{{UID: "other-uid"}}}},
	)
	c := &ClusterConn{typed: typed, clk: clock.NewFake(base)}

	got, err := c.RolloutHistory(context.Background(), "Deployment", "shop", "cart")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 revisions, got %+v", got)
	}
	if got[0].Revision != 3 || !got[0].Current || got[0].Images[0] != "shop/cart:v3" {
		t.Fatalf("head: %+v", got[0])
	}
	if got[1].Revision != 2 || got[1].Current || got[2].Revision != 1 {
		t.Fatalf("order: %+v", got)
	}
	if got[2].ChangeCause != "initial deploy" {
		t.Fatalf("change cause: %+v", got[2])
	}
	if got[2].AgeSeconds != int((3 * time.Hour).Seconds()) {
		t.Fatalf("age: %+v", got[2])
	}
}

func TestRolloutUndo_DeploymentPatchesOldTemplateWithoutHashLabel(t *testing.T) {
	typed := k8sfake.NewSimpleClientset(
		rolloutDeployment(false, "3"),
		rolloutRS("cart-2", "2", "shop/cart:v2", "", time.Unix(0, 0)),
		rolloutRS("cart-3", "3", "shop/cart:v3", "", time.Unix(0, 0)),
	)
	var patched []byte
	typed.PrependReactor("patch", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patched = action.(k8stesting.PatchAction).GetPatch()
		return true, rolloutDeployment(false, "4"), nil
	})
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}

	if err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", 2); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Spec struct {
			Template struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
				Spec corev1.PodSpec `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(patched, &p); err != nil {
		t.Fatal(err)
	}
	if p.Spec.Template.Spec.Containers[0].Image != "shop/cart:v2" {
		t.Fatalf("patch must carry the old template: %s", patched)
	}
	if _, has := p.Spec.Template.Metadata.Labels[appsv1.DefaultDeploymentUniqueLabelKey]; has {
		t.Fatalf("pod-template-hash must be stripped: %s", patched)
	}
	if p.Spec.Template.Metadata.Labels["app"] != "cart" {
		t.Fatalf("real labels must survive: %s", patched)
	}
}

func TestRolloutUndo_RefusesCurrentAndUnknownRevisions(t *testing.T) {
	typed := k8sfake.NewSimpleClientset(
		rolloutDeployment(false, "3"),
		rolloutRS("cart-3", "3", "shop/cart:v3", "", time.Unix(0, 0)),
	)
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	if err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", 3); err == nil || !strings.Contains(err.Error(), "already at revision") {
		t.Fatalf("undo-to-current must refuse: %v", err)
	}
	if err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", 99); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown revision must refuse: %v", err)
	}
}

func stsRevision(name string, rev int64, image string, ownerUID types.UID) *appsv1.ControllerRevision {
	raw := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"web","image":"` + image + `"}]}}}}`)
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: name,
			OwnerReferences: []metav1.OwnerReference{{UID: ownerUID, Kind: "StatefulSet", Name: "pg"}}},
		Revision: rev,
		Data:     runtime.RawExtension{Raw: raw},
	}
}

func TestRolloutHistoryAndUndo_StatefulSetViaControllerRevisions(t *testing.T) {
	stsUID := types.UID("sts-uid")
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg", UID: stsUID},
		Status:     appsv1.StatefulSetStatus{UpdateRevision: "pg-rev2"},
	}
	typed := k8sfake.NewSimpleClientset(sts,
		stsRevision("pg-rev1", 1, "postgres:16.2", stsUID),
		stsRevision("pg-rev2", 2, "postgres:16.3", stsUID),
	)
	var patched []byte
	typed.PrependReactor("patch", "statefulsets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patched = action.(k8stesting.PatchAction).GetPatch()
		return true, sts, nil
	})
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}

	hist, err := c.RolloutHistory(context.Background(), "StatefulSet", "db", "pg")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].Revision != 2 || !hist[0].Current || hist[1].Current {
		t.Fatalf("history: %+v", hist)
	}
	if hist[1].Images[0] != "postgres:16.2" {
		t.Fatalf("images from revision data: %+v", hist[1])
	}

	if err := c.RolloutUndo(context.Background(), "StatefulSet", "db", "pg", 1); err != nil {
		t.Fatal(err)
	}
	// kubectl semantics: the ControllerRevision data IS the patch.
	if !strings.Contains(string(patched), "postgres:16.2") {
		t.Fatalf("undo must patch the revision data: %s", patched)
	}
}

func TestRolloutPauseAndResumePatchSpecPaused(t *testing.T) {
	typed := k8sfake.NewSimpleClientset(rolloutDeployment(false, "1"))
	var patched []byte
	typed.PrependReactor("patch", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patched = action.(k8stesting.PatchAction).GetPatch()
		return true, rolloutDeployment(true, "1"), nil
	})
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}

	if err := c.RolloutPause(context.Background(), "shop", "cart", true); err != nil {
		t.Fatal(err)
	}
	if string(patched) != `{"spec":{"paused":true}}` {
		t.Fatalf("pause patch: %s", patched)
	}
	if err := c.RolloutPause(context.Background(), "shop", "cart", false); err != nil {
		t.Fatal(err)
	}
	if string(patched) != `{"spec":{"paused":false}}` {
		t.Fatalf("resume patch: %s", patched)
	}
}

func TestRolloutHistory_UnsupportedKind(t *testing.T) {
	c := &ClusterConn{typed: k8sfake.NewSimpleClientset(), clk: clock.NewFake(time.Unix(0, 0))}
	if _, err := c.RolloutHistory(context.Background(), "CronJob", "x", "y"); err == nil {
		t.Fatal("unsupported kind must error")
	}
}
