package velero

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

func TestReviewS2aIncompleteOwnersDoNotProve(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1", "kind": "Backup",
		"metadata": map[string]any{"name": "manual", "namespace": "velero", "creationTimestamp": t0.Format("2006-01-02T15:04:05Z07:00"),
			"ownerReferences": []any{
				map[string]any{"apiVersion": "velero.io/v1", "kind": "Schedule", "name": "daily", "uid": sUID},
				map[string]any{"apiVersion": "velero.io/v1", "kind": "Schedule", "name": "other", "uid": "malformed"},
			}}, "spec": map[string]any{}, "status": map[string]any{"phase": "Failed", "startTimestamp": t0.Format("2006-01-02T15:04:05Z07:00")}}}
	b := ProjectBackup(u)
	if !b.OwnersIncomplete {
		t.Fatal("test needs incomplete owner set")
	}
	if got := Attribute(daily, b, "", nil); got != NotRun {
		t.Errorf("incomplete owner set proves run: %v; retained owners=%+v", got, b.Owners)
	}
	if m := Unscheduled([]*Backup{b}, []*Schedule{daily}, Servers{}, t0); !m[b].In {
		t.Errorf("failed Backup with incomplete owner set vanished from recent backups: %+v", m[b])
	}
}
