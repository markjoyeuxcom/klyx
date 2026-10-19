package fleet

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// Implementation review 2 (S5 #2): VisitKind reports a confirmed-absent
// kind with Coverage Absent and complete false (it visits nothing). That
// is a complete, empty source; stale, checking and unreadable stay
// incomplete.
type absentKinds struct {
	fakeIdentityObserver
	absent map[string]bool
}

func (h absentKinds) VisitKind(k string, f func(any) bool) (observe.KindState, bool, bool) {
	if h.absent[k] {
		return observe.KindState{Coverage: observe.Absent}, false, true
	}
	return h.fakeIdentityObserver.VisitKind(k, f)
}

func TestReview2AbsentKindIsKnownEmpty(t *testing.T) {
	s := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "external-secrets.io/v1", "kind": "SecretStore", "metadata": map[string]any{"namespace": "app", "name": "s", "uid": "u"}}}
	live := fakeIdentityObserver{models: map[string][]any{healthsrc.KindSecretStores: {s}}, live: map[string]bool{healthsrc.KindSecretStores: true}}
	h := absentKinds{live, map[string]bool{healthsrc.KindClusterSecretStores: true}}
	if stores, read := esoStores(h); !read || len(stores) != 1 {
		t.Fatalf("a live SecretStore beside an absent ClusterSecretStore API: %v %d", read, len(stores))
	}
	both := absentKinds{fakeIdentityObserver{}, map[string]bool{healthsrc.KindSecretStores: true, healthsrc.KindClusterSecretStores: true}}
	if stores, read := esoStores(both); !read || len(stores) != 0 {
		t.Fatalf("both absent: known empty: %v %d", read, len(stores))
	}
	if !visitLive(both, healthsrc.KindSecretStores, func(any) {}) {
		t.Fatal("an absent kind is a complete, empty read")
	}
	checking := absentKinds{fakeIdentityObserver{models: map[string][]any{healthsrc.KindSecretStores: {s}}, live: map[string]bool{}}, map[string]bool{healthsrc.KindClusterSecretStores: true}}
	if _, read := esoStores(checking); read {
		t.Fatal("a checking SecretStore read stays incomplete")
	}
}
