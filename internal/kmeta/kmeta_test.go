package kmeta

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestWritesOf(t *testing.T) {
	at := func(h int) *metav1.Time {
		v := metav1.NewTime(time.Date(2026, 10, 14, h, 0, 0, 0, time.UTC))
		return &v
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{}}
	if w := WritesOf(u); w.Known {
		t.Fatalf("no managedFields: unknown, got %+v", w)
	}
	u.SetManagedFields([]metav1.ManagedFieldsEntry{
		{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply, Time: at(9)},
		{Manager: "flux", Operation: metav1.ManagedFieldsOperationApply, Time: at(10)},
		{Manager: "controller", Operation: metav1.ManagedFieldsOperationUpdate, Subresource: "status", Time: at(11)},
		{Manager: "no-time", Operation: metav1.ManagedFieldsOperationUpdate},
	})
	w := WritesOf(u)
	if !w.Known || !w.Main.Equal(at(10).Time) || !w.Status.Equal(at(11).Time) {
		t.Fatalf("got %+v", w)
	}
}

func TestUndisturbed(t *testing.T) {
	l := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	before, after := l.Add(-time.Hour), l.Add(time.Hour)
	cases := []struct {
		name string
		gen  int64
		w    Writes
		want bool
	}{
		{"never edited", 1, Writes{}, true},
		{"edited, no managedFields", 2, Writes{}, false},
		{"edited before the transition, transition was the last status write", 2, Writes{Main: before, Status: l, Known: true}, true},
		{"a spec write after the transition", 2, Writes{Main: after, Status: l, Known: true}, false},
		{"a spec write in the same second", 2, Writes{Main: l, Status: l, Known: true}, false},
		{"a status write after the transition (re-observed without a transition)", 2, Writes{Main: before, Status: after, Known: true}, false},
	}
	for _, c := range cases {
		if got := Undisturbed(c.gen, c.w, l); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	if Undisturbed(1, Writes{}, time.Time{}) {
		t.Error("no transition time: never undisturbed")
	}
	if !SpecSettled(2, Writes{Main: before, Status: after, Known: true}, l) || SpecSettled(2, Writes{Main: after, Known: true}, l) {
		t.Error("SpecSettled ignores status writes and refuses later spec writes")
	}
}
