package fleet

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func TestRolloutUndo_DeploymentRestoresOldTemplateWithoutHashLabel(t *testing.T) {
	dep := rolloutDeployment(false, "3")
	dep.Spec.Template = rolloutRS("cart-3", "3", "shop/cart:v3", "", time.Unix(0, 0)).Spec.Template
	typed := k8sfake.NewSimpleClientset(
		dep,
		rolloutRS("cart-2", "2", "shop/cart:v2", "", time.Unix(0, 0)),
		rolloutRS("cart-3", "3", "shop/cart:v3", "", time.Unix(0, 0)),
	)
	var patchType types.PatchType
	typed.PrependReactor("patch", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchType = action.(k8stesting.PatchAction).GetPatchType()
		return false, nil, nil // fall through: the tracker applies the real patch
	})
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}

	if err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 2); err != nil {
		t.Fatal(err)
	}
	if patchType != types.JSONPatchType {
		t.Fatalf("undo must replace the template with a JSON patch, got %q", patchType)
	}
	got, err := typed.AppsV1().Deployments("shop").Get(context.Background(), "cart", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "shop/cart:v2" {
		t.Fatalf("want the old template's image, got %q", img)
	}
	if _, has := got.Spec.Template.Labels[appsv1.DefaultDeploymentUniqueLabelKey]; has {
		t.Fatalf("pod-template-hash must be stripped: %v", got.Spec.Template.Labels)
	}
	if got.Spec.Template.Labels["app"] != "cart" {
		t.Fatalf("real labels must survive: %v", got.Spec.Template.Labels)
	}
}

// Review 2026-10-03 #4 (Codex's repro): a strategic merge kept containers, env
// and labels added after the target revision. The undo must yield exactly the
// historical template.
func TestRolloutUndo_DeploymentDropsFieldsAddedAfterTarget(t *testing.T) {
	dep := rolloutDeployment(false, "2")
	dep.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "cart", "new-label": "retained"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", Image: "v2", Env: []corev1.EnvVar{{Name: "NEW_CONFIG", Value: "new"}}},
			{Name: "new-sidecar", Image: "sidecar:v2"},
		}},
	}
	old := rolloutRS("cart-1", "1", "v1", "", time.Now())
	typed := k8sfake.NewSimpleClientset(dep, old)
	c := &ClusterConn{typed: typed}

	if err := c.undoDeployment(context.Background(), "shop", "cart", string(depUID), 1); err != nil {
		t.Fatal(err)
	}
	got, err := typed.AppsV1().Deployments("shop").Get(context.Background(), "cart", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := old.Spec.Template.DeepCopy()
	delete(want.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	if !reflect.DeepEqual(got.Spec.Template, *want) {
		t.Fatalf("undo must restore exactly the historical template\n got %+v\nwant %+v", got.Spec.Template, *want)
	}
}

func TestDeploymentUndoPatch_ChangeCauseAndIdentityGuard(t *testing.T) {
	ops := func(dep *appsv1.Deployment, rs *appsv1.ReplicaSet) []map[string]interface{} {
		t.Helper()
		body, err := deploymentUndoPatch(dep, rs)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]interface{}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	const causePath = "/metadata/annotations/kubernetes.io~1change-cause"

	// Identity guard first: a recreated Deployment (new UID) must fail the test op.
	got := ops(rolloutDeployment(false, "3"), rolloutRS("cart-2", "2", "v2", "", time.Unix(0, 0)))
	if got[0]["op"] != "test" || got[0]["path"] != "/metadata/uid" || got[0]["value"] != string(depUID) {
		t.Fatalf("first op must test metadata.uid, got %v", got[0])
	}

	// Target carries a change-cause: it becomes the Deployment's.
	got = ops(rolloutDeployment(false, "3"), rolloutRS("cart-2", "2", "v2", "pin v2", time.Unix(0, 0)))
	last := got[len(got)-1]
	if last["op"] != "add" || last["path"] != causePath || last["value"] != "pin v2" {
		t.Fatalf("want change-cause carried from the target revision, got %v", last)
	}

	// Target has none but the Deployment does: drop it rather than misattribute.
	dep := rolloutDeployment(false, "3")
	dep.Annotations[changeCauseAnn] = "deploy v3"
	got = ops(dep, rolloutRS("cart-2", "2", "v2", "", time.Unix(0, 0)))
	last = got[len(got)-1]
	if last["op"] != "remove" || last["path"] != causePath {
		t.Fatalf("want the newer change-cause removed, got %v", last)
	}

	// Neither has one: template replace only.
	got = ops(rolloutDeployment(false, "3"), rolloutRS("cart-2", "2", "v2", "", time.Unix(0, 0)))
	if len(got) != 2 {
		t.Fatalf("want test+replace only, got %v", got)
	}
}

// Codex's batch-1 review: assert the RESULTING Deployment for every
// change-cause shape, including a non-nil EMPTY annotations map (omitted on the
// wire, so a nested add would fail).
func TestRolloutUndo_ChangeCauseResultingObject(t *testing.T) {
	for _, kind := range []string{"nil annotations", "empty map", "replace cause", "remove cause"} {
		t.Run(kind, func(t *testing.T) {
			dep := rolloutDeployment(false, "2")
			dep.Spec.Template = rolloutRS("cart-2", "2", "v2", "", time.Now()).Spec.Template
			old := rolloutRS("cart-1", "1", "v1", "older", time.Now())
			switch kind {
			case "nil annotations":
				dep.Annotations = nil
			case "empty map":
				dep.Annotations = map[string]string{}
			case "replace cause":
				dep.Annotations[changeCauseAnn] = "newer"
			case "remove cause":
				dep.Annotations[changeCauseAnn] = "newer"
				delete(old.Annotations, changeCauseAnn)
			}
			typed := k8sfake.NewSimpleClientset(dep, old)
			c := &ClusterConn{typed: typed}
			if err := c.undoDeployment(context.Background(), "shop", "cart", string(depUID), 1); err != nil {
				t.Fatal(err)
			}
			got, err := typed.AppsV1().Deployments("shop").Get(context.Background(), "cart", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "remove cause" {
				if _, ok := got.Annotations[changeCauseAnn]; ok {
					t.Fatalf("newer change-cause must be removed: %v", got.Annotations)
				}
			} else if got.Annotations[changeCauseAnn] != "older" {
				t.Fatalf("want the target's change-cause, got %v", got.Annotations)
			}
			if got.Spec.Template.Spec.Containers[0].Image != "v1" {
				t.Fatalf("template not restored: %+v", got.Spec.Template.Spec.Containers)
			}
		})
	}
}

// A Deployment deleted and recreated (new UID) between the read and the patch
// must fail the uid test op and stay untouched.
func TestRolloutUndo_RecreatedDeploymentIsNotRolledBack(t *testing.T) {
	dep := rolloutDeployment(false, "2")
	dep.Spec.Template = rolloutRS("cart-2", "2", "v2", "", time.Now()).Spec.Template
	old := rolloutRS("cart-1", "1", "v1", "old cause", time.Now())
	typed := k8sfake.NewSimpleClientset(dep, old)
	typed.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		newer := dep.DeepCopy()
		newer.UID = "replacement-uid"
		if err := typed.Tracker().Update(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, newer, "shop"); err != nil {
			t.Fatal(err)
		}
		return false, nil, nil
	})
	c := &ClusterConn{typed: typed}
	if err := c.undoDeployment(context.Background(), "shop", "cart", string(depUID), 1); err == nil {
		t.Fatal("undo against a recreated Deployment must fail the uid precondition")
	}
	got, err := typed.AppsV1().Deployments("shop").Get(context.Background(), "cart", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Template.Spec.Containers[0].Image != "v2" {
		t.Fatal("the recreated Deployment must not be mutated")
	}
}

func TestRolloutUndo_RefusesCurrentAndUnknownRevisions(t *testing.T) {
	typed := k8sfake.NewSimpleClientset(
		rolloutDeployment(false, "3"),
		rolloutRS("cart-3", "3", "shop/cart:v3", "", time.Unix(0, 0)),
	)
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	if err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 3); err == nil || !strings.Contains(err.Error(), "already at revision") {
		t.Fatalf("undo-to-current must refuse: %v", err)
	}
	if err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 99); err == nil || !strings.Contains(err.Error(), "not found") {
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

	if err := c.RolloutUndo(context.Background(), "StatefulSet", "db", "pg", string(stsUID), 1); err != nil {
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

	if err := c.RolloutPause(context.Background(), "shop", "cart", string(depUID), true); err != nil {
		t.Fatal(err)
	}
	if string(patched) != `{"metadata":{"uid":"dep-uid-1"},"spec":{"paused":true}}` {
		t.Fatalf("pause patch: %s", patched)
	}
	if err := c.RolloutPause(context.Background(), "shop", "cart", string(depUID), false); err != nil {
		t.Fatal(err)
	}
	if string(patched) != `{"metadata":{"uid":"dep-uid-1"},"spec":{"paused":false}}` {
		t.Fatalf("resume patch: %s", patched)
	}
}

func TestRolloutHistory_UnsupportedKind(t *testing.T) {
	c := &ClusterConn{typed: k8sfake.NewSimpleClientset(), clk: clock.NewFake(time.Unix(0, 0))}
	if _, err := c.RolloutHistory(context.Background(), "CronJob", "x", "y"); err == nil {
		t.Fatal("unsupported kind must error")
	}
}
