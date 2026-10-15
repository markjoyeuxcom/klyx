package lastwrite

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func at(min int) *metav1.Time {
	t := metav1.NewTime(time.Date(2026, 10, 15, 20, min, 0, 0, time.UTC))
	return &t
}

func TestReadPicksTheLatestAndTheLatestNonStatus(t *testing.T) {
	w := Read([]metav1.ManagedFieldsEntry{
		{Manager: "kubectl-client-side-apply", Operation: metav1.ManagedFieldsOperationUpdate, Time: at(1)},
		{Manager: "kustomize-controller", Operation: metav1.ManagedFieldsOperationApply, Time: at(5)},
		{Manager: "kube-controller-manager", Operation: metav1.ManagedFieldsOperationUpdate, Subresource: "status", Time: at(9)},
		{Manager: "no-time", Operation: metav1.ManagedFieldsOperationUpdate},
	})
	if w.Last == nil || w.Last.Manager != "kube-controller-manager" || !w.Last.StatusUpdate() || !w.Last.Time.Equal(at(9).Time) {
		t.Fatalf("last %+v", w.Last)
	}
	if w.LastSpec == nil || w.LastSpec.Manager != "kustomize-controller" || w.LastSpec.Operation != "Apply" || w.LastSpec.StatusUpdate() {
		t.Fatalf("last spec %+v", w.LastSpec)
	}
}

func TestNoRecord(t *testing.T) {
	for name, es := range map[string][]metav1.ManagedFieldsEntry{
		"absent":  nil,
		"reset":   {},
		"no time": {{Manager: "m", Operation: metav1.ManagedFieldsOperationUpdate}},
	} {
		if w := Read(es); w.Last != nil || w.LastSpec != nil {
			t.Errorf("%s: %+v", name, w)
		}
	}
	// Only status updates: a last record, and none for change proximity.
	w := Read([]metav1.ManagedFieldsEntry{{Manager: "m", Subresource: "status", Time: at(3)}})
	if w.Last == nil || w.LastSpec != nil {
		t.Fatalf("status only %+v", w)
	}
}
