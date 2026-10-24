package fleet

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
)

// The workload verbs are armed against one object (design §5b): restart,
// scale, pause and undo confirm the UID before writing and carry it on the
// write, so a same-name replacement is never written.

type workloadVerb struct {
	name, kind, ns, obj string
	run                 func(c *ClusterConn, uid string) error
}

func workloadVerbs() []workloadVerb {
	ctx := context.Background()
	return []workloadVerb{
		{"restart deployment", "Deployment", "default", "api", func(c *ClusterConn, uid string) error {
			return c.RolloutRestart(ctx, "Deployment", "default", "api", uid)
		}},
		{"restart statefulset", "StatefulSet", "db", "pg", func(c *ClusterConn, uid string) error { return c.RolloutRestart(ctx, "StatefulSet", "db", "pg", uid) }},
		{"restart daemonset", "DaemonSet", "kube-system", "cilium", func(c *ClusterConn, uid string) error {
			return c.RolloutRestart(ctx, "DaemonSet", "kube-system", "cilium", uid)
		}},
		{"scale deployment", "Deployment", "default", "api", func(c *ClusterConn, uid string) error {
			return c.ScaleWorkload(ctx, "Deployment", "default", "api", uid, 2)
		}},
		{"scale statefulset", "StatefulSet", "db", "pg", func(c *ClusterConn, uid string) error { return c.ScaleWorkload(ctx, "StatefulSet", "db", "pg", uid, 2) }},
		{"pause", "Deployment", "default", "api", func(c *ClusterConn, uid string) error { return c.RolloutPause(ctx, "default", "api", uid, true) }},
		{"undo", "Deployment", "default", "api", func(c *ClusterConn, uid string) error {
			return c.RolloutUndo(ctx, "Deployment", "default", "api", uid, 1, nil)
		}},
	}
}

func seedFor(v workloadVerb) runtime.Object {
	switch v.kind {
	case "StatefulSet":
		return seedStatefulSet(v.ns, v.obj)
	case "DaemonSet":
		return seedDaemonSet(v.ns, v.obj)
	}
	return seedDeployment(v.ns, v.obj)
}

// writes counts the verb's mutations: patches and scale updates.
func writes(typed *typedfake.Clientset) int {
	n := 0
	for _, a := range typed.Actions() {
		switch a.GetVerb() {
		case "patch", "update", "create", "delete":
			n++
		}
	}
	return n
}

func TestWorkloadVerbs_RefuseAnUnarmedReplacedOrMissingWorkload(t *testing.T) {
	for _, v := range workloadVerbs() {
		for _, tc := range []struct {
			name, uid string
			seed      bool
			replaced  bool
			want      string
		}{
			{"replaced", "uid-old", true, true, "nothing was written"},
			{"gone", "uid-" + v.obj, false, true, "nothing was written"},
			{"unarmed", "", true, false, "no object identity was armed"},
		} {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				typed := typedfake.NewClientset()
				if tc.seed {
					typed = typedfake.NewClientset(seedFor(v))
				}
				c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
				err := v.run(c, tc.uid)
				if err == nil || errors.Is(err, ErrReplaced) != tc.replaced || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want a refusal (%s, replaced=%v), got %v", tc.want, tc.replaced, err)
				}
				if n := writes(typed); n != 0 {
					t.Fatalf("a refused verb must not write: %d writes", n)
				}
			})
		}
	}
}

func TestWorkloadVerbs_CarryTheArmedUIDOnTheWrite(t *testing.T) {
	ctx := context.Background()
	for _, v := range workloadVerbs()[:6] { // undo's guards are covered in rollout_test
		t.Run(v.name, func(t *testing.T) {
			typed := typedfake.NewClientset(seedFor(v))
			var carried string
			typed.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
				carried = string(a.(k8stesting.PatchAction).GetPatch())
				return false, nil, nil
			})
			typed.PrependReactor("update", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
				if s, ok := a.(k8stesting.UpdateAction).GetObject().(*autoscalingv1.Scale); ok {
					carried = `"uid":"` + string(s.UID) + `"`
				}
				return false, nil, nil
			})
			c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
			if err := v.run(c, "uid-"+v.obj); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(carried, `"uid":"uid-`+v.obj+`"`) {
				t.Fatalf("the write must carry the armed UID: %s", carried)
			}
		})
	}
	// a StatefulSet undo patches the ControllerRevision data, guarded by the UID
	sts := &appsv1.StatefulSet{ObjectMeta: seedStatefulSet("db", "pg").ObjectMeta, Status: appsv1.StatefulSetStatus{UpdateRevision: "pg-rev2"}}
	typed := typedfake.NewClientset(sts, stsRevision("pg-rev1", 1, "postgres:16.2", sts.UID), stsRevision("pg-rev2", 2, "postgres:16.3", sts.UID))
	var patched string
	typed.PrependReactor("patch", "statefulsets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		patched = string(a.(k8stesting.PatchAction).GetPatch())
		return true, sts, nil
	})
	c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
	if err := c.RolloutUndo(ctx, "StatefulSet", "db", "pg", "uid-pg", 1, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patched, `"uid":"uid-pg"`) || !strings.Contains(patched, "postgres:16.2") {
		t.Fatalf("the revision patch must carry the armed UID: %s", patched)
	}
}

// A replacement after the pre-check is refused by the server for the UID;
// other failures say whether the write may have landed.
func TestWorkloadVerbs_ReportTheServersAnswer(t *testing.T) {
	uidConflict := apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments/scale"}, "api",
		errors.New("StorageError: invalid object, Code: 4, Key: /registry/deployments/x/api, ResourceVersion: 0, AdditionalErrorMsg: Precondition failed: UID in precondition: uid-api, UID in object meta: uid-new"))
	for _, v := range workloadVerbs() {
		for _, tc := range []struct {
			name     string
			err      error
			replaced bool
			want     string
		}{
			{"uid refused", uidConflict, true, "its write was refused"},
			{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "api", errors.New("rbac")), false, "nothing was written"},
			{"transport", errors.New("connection reset"), false, "the outcome is unverified"},
		} {
			if v.name == "undo" && tc.name == "uid refused" {
				continue // a Deployment undo is a JSON patch: see TestRolloutUndo_A422NamesAReplacementOnlyWhenConfirmed
			}
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				typed := typedfake.NewClientset(seedFor(v))
				if v.name == "undo" {
					dep := rolloutDeployment(false, "2")
					dep.Namespace, dep.Name, dep.UID = "default", "api", "uid-api"
					old := rolloutRS("api-1", "1", "v1", "", time.Unix(0, 0))
					old.Namespace, old.OwnerReferences[0].UID = "default", "uid-api"
					cur := rolloutRS("api-2", "2", "v2", "", time.Unix(0, 0))
					cur.Namespace, cur.OwnerReferences[0].UID = "default", "uid-api"
					typed = typedfake.NewClientset(dep, old, cur)
				}
				fail := func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, tc.err }
				typed.PrependReactor("patch", "*", fail)
				typed.PrependReactor("update", "*", fail)
				c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
				err := v.run(c, "uid-"+v.obj)
				if err == nil || errors.Is(err, ErrReplaced) != tc.replaced || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q (replaced=%v), got %v", tc.want, tc.replaced, err)
				}
			})
		}
	}
}

// A failed JSON-patch op is a 422 that does not name the op: the undo says
// "replaced" only when a re-read confirms another UID took the name, and
// otherwise reports the refusal as nothing written.
func TestRolloutUndo_A422NamesAReplacementOnlyWhenConfirmed(t *testing.T) {
	failed := apierrors.NewGenericServerResponse(http.StatusUnprocessableEntity, "", schema.GroupResource{}, "",
		"testing value /metadata/uid failed: test failed", 0, false)
	for _, replaced := range []bool{true, false} {
		dep := rolloutDeployment(false, "2")
		dep.Spec.Template = rolloutRS("cart-2", "2", "v2", "", time.Now()).Spec.Template
		typed := typedfake.NewClientset(dep, rolloutRS("cart-1", "1", "v1", "", time.Now()), rolloutRS("cart-2", "2", "v2", "", time.Now()))
		typed.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
			if replaced {
				newer := dep.DeepCopy()
				newer.UID = "replacement-uid"
				if err := typed.Tracker().Update(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, newer, "shop"); err != nil {
					t.Fatal(err)
				}
			}
			return true, nil, failed
		})
		c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
		err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 1, nil)
		if errors.Is(err, ErrReplaced) != replaced {
			t.Fatalf("replaced=%v: %v", replaced, err)
		}
		want := "nothing was written"
		if replaced {
			want = "its write was refused"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("replaced=%v: want %q, got %v", replaced, want, err)
		}
	}
}

// A 422 whose re-read fails is not a confirmed replacement: nothing was
// written, and the error never claims "replaced".
func TestRolloutUndo_A422WithAFailedReReadIsNotAReplacement(t *testing.T) {
	failed := apierrors.NewGenericServerResponse(http.StatusUnprocessableEntity, "", schema.GroupResource{}, "", "test failed", 0, false)
	dep := rolloutDeployment(false, "2")
	dep.Spec.Template = rolloutRS("cart-2", "2", "v2", "", time.Now()).Spec.Template
	typed := typedfake.NewClientset(dep, rolloutRS("cart-1", "1", "v1", "", time.Now()), rolloutRS("cart-2", "2", "v2", "", time.Now()))
	patched := false
	typed.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { patched = true; return true, nil, failed })
	typed.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		if patched {
			return true, nil, errors.New("connection reset")
		}
		return false, nil, nil
	})
	c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
	err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 1, nil)
	if err == nil || errors.Is(err, ErrReplaced) || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("an unconfirmed 422 is a refusal, never a replacement: %v", err)
	}
}

// The undo paths re-check the armed UID against the object they read, so a
// replacement landing after RolloutUndo's pre-check is never rolled back:
// a Deployment's JSON-patch test op would otherwise test the replacement's
// own UID, and a ControllerRevision is matched by the read owner's UID.
func TestRolloutUndo_ReadObjectMustBeTheArmedOne(t *testing.T) {
	ctx := context.Background()
	dep := rolloutDeployment(false, "2")
	typed := typedfake.NewClientset(dep, rolloutRS("cart-1", "1", "v1", "", time.Now()))
	c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
	if err := c.undoDeployment(ctx, "shop", "cart", "uid-old", 1, nil); !errors.Is(err, ErrReplaced) || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("a Deployment read with another UID is ErrReplaced: %v", err)
	}
	sts := &appsv1.StatefulSet{ObjectMeta: seedStatefulSet("db", "pg").ObjectMeta}
	typed = typedfake.NewClientset(sts, stsRevision("pg-rev1", 1, "postgres:16.2", sts.UID))
	c = newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
	if err := c.undoFromControllerRevision(ctx, "StatefulSet", "db", "pg", "uid-old", 1, nil); !errors.Is(err, ErrReplaced) {
		t.Fatalf("a StatefulSet read with another UID is ErrReplaced: %v", err)
	}
	for _, a := range typed.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatal("a replaced workload must not be written")
		}
	}
}

// A replacement landing between the arm check and the history read is
// refused: the history read names another UID, so nothing is written.
func TestRolloutUndo_HistoryOfAReplacementIsRefused(t *testing.T) {
	dep := rolloutDeployment(false, "2")
	typed := typedfake.NewClientset(dep, rolloutRS("cart-1", "1", "v1", "", time.Now()), rolloutRS("cart-2", "2", "v2", "", time.Now()))
	gets := 0
	typed.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return false, nil, nil // the arm check sees the armed object
		}
		newer := dep.DeepCopy()
		newer.UID = "replacement-uid"
		return true, newer, nil
	})
	c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
	err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 1, nil)
	if !errors.Is(err, ErrReplaced) || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("the history of a replacement is refused: %v", err)
	}
	for _, a := range typed.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatal("a replaced workload must not be written")
		}
	}
}

// CheckPodArmed fences a verb that can only name a pod (kubectl debug).
func TestCheckPodArmed(t *testing.T) {
	ctx := context.Background()
	typed := typedfake.NewClientset(seedPod("ns", "web-0"))
	c := newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0)))
	if err := c.CheckPodArmed(ctx, "ns", "web-0", "uid-web-0"); err != nil {
		t.Fatalf("the armed pod must pass: %v", err)
	}
	for _, tc := range [][3]string{{"ns", "web-0", "uid-old"}, {"ns", "gone", "uid-gone"}} {
		if err := c.CheckPodArmed(ctx, tc[0], tc[1], tc[2]); !errors.Is(err, ErrReplaced) {
			t.Fatalf("%v must be ErrReplaced, got %v", tc, err)
		}
	}
	if err := c.CheckPodArmed(ctx, "ns", "web-0", ""); err == nil || errors.Is(err, ErrReplaced) || !strings.Contains(err.Error(), "no object identity was armed") {
		t.Fatalf("an unarmed check is refused: %v", err)
	}
	typed.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection reset")
	})
	if err := c.CheckPodArmed(ctx, "ns", "web-0", "uid-web-0"); err == nil || errors.Is(err, ErrReplaced) {
		t.Fatalf("a failed read is not a replacement: %v", err)
	}
}
