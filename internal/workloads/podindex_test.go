package workloads

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/health"
)

// The join index is an optimisation only: on a randomized fixture, every
// workload gets exactly the pods a full scan gives it - by ownership and by
// selector, in the same order.
func TestPodIndexEqualsAFullScan(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	t0 := time.Now()
	nss := []string{"a", "b", "c"}
	kinds := []string{"ReplicaSet", "StatefulSet", "DaemonSet", "Job", ""}
	uids := []types.UID{"u1", "u2", "u3", "u4", "", "u5"}
	var pods []corev1.Pod
	for i := 0; i < 600; i++ {
		p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i), Namespace: nss[r.Intn(len(nss))], UID: types.UID(fmt.Sprintf("p%d", i)),
			Labels: map[string]string{"app": fmt.Sprintf("x%d", r.Intn(4)), "tier": fmt.Sprintf("t%d", r.Intn(2))}}}
		if k := kinds[r.Intn(len(kinds))]; k != "" {
			p.OwnerReferences = []metav1.OwnerReference{{Kind: k, UID: uids[r.Intn(len(uids))], Controller: ptrTo(r.Intn(5) != 0)}}
		}
		pods = append(pods, p)
	}
	verdicts := make([]health.Verdict, len(pods))
	for i := range pods {
		verdicts[i] = health.Pod(&pods[i], t0)
	}
	all := make([]int, len(pods))
	for i := range all {
		all[i] = i
	}
	idx := indexPods(pods)
	rsOwner := map[types.UID]types.UID{"u1": "d1", "u2": "d1", "u3": "d2", "u5": ""}
	rsOf := map[types.UID][]types.UID{"d1": {"u1", "u2"}, "d2": {"u3"}}
	for _, ns := range nss {
		for _, dep := range []types.UID{"d1", "d2", "d3", ""} {
			owns := ownedByDeployment(dep, rsOwner)
			want := ownedPods(ns, pods, verdicts, all, owns)
			if got := ownedPods(ns, pods, verdicts, idx.controlledBy("ReplicaSet", rsOf[dep]...), owns); !reflect.DeepEqual(got, want) {
				t.Fatalf("deployment %q in %s: indexed %d pods, full scan %d", dep, ns, len(got), len(want))
			}
		}
		for _, kind := range []string{"StatefulSet", "DaemonSet"} {
			for _, uid := range uids {
				owns := ownedByUID(kind, uid)
				want := ownedPods(ns, pods, verdicts, all, owns)
				if got := ownedPods(ns, pods, verdicts, idx.controlledBy(kind, uid), owns); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s %q in %s: indexed %d pods, full scan %d", kind, uid, ns, len(got), len(want))
				}
			}
		}
		for _, sel := range []*metav1.LabelSelector{
			{MatchLabels: map[string]string{"app": "x1"}},
			{MatchLabels: map[string]string{"app": "x2", "tier": "t0"}},
			{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"x0", "x3"}}}},
			{}, nil,
		} {
			want := matchPods(ns, sel, pods, all)
			if got := matchPods(ns, sel, pods, idx.byNS[ns]); !reflect.DeepEqual(got, want) {
				t.Fatalf("selector %v in %s: indexed %d pods, full scan %d", sel, ns, len(got), len(want))
			}
		}
	}
}

func ptrTo[T any](v T) *T { return &v }
