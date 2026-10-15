package healthsrc

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/observe"
)

// The producer side of slice 2b §7: the real pods evaluator with and without
// its ledger role list.
//
//	go test ./internal/healthsrc -run '^$' -bench PodsRoles -benchmem

func benchPods(b *testing.B, n int) map[string][]any {
	b.Helper()
	objs := make([]any, 0, n)
	for i := 0; i < n; i++ {
		p, err := ProjectPod(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i), Namespace: "shop", UID: types.UID(fmt.Sprintf("u%d", i))},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}})
		if err != nil {
			b.Fatal(err)
		}
		objs = append(objs, p)
	}
	return map[string][]any{KindPods: objs}
}

func benchEvalPods(b *testing.B, n, roleCap int) {
	objs := benchPods(b, n)
	eval := evalPods("bench")
	c := observe.Capture{Now: time.Now(), Objects: objs, RoleCap: roleCap}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eval(c)
	}
}

func BenchmarkPodsRoles10k(b *testing.B)   { benchEvalPods(b, 10000, 100000) }
func BenchmarkPodsRoles50k(b *testing.B)   { benchEvalPods(b, 50000, 100000) }
func BenchmarkPodsNoRoles50k(b *testing.B) { benchEvalPods(b, 50000, 0) }
