package healthsrc

import (
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/velero"
	"testing"
	"time"
)

func TestReviewS2bR1AbsentSchedulesAreKnownEmpty(t *testing.T) {
	now := time.Now()
	b := &velero.Backup{Meta: velero.Meta{Namespace: "velero", Name: "manual", UID: "b1", Created: now}, Phase: "Completed", Start: now}
	// publish.captureLocked represents a confirmed-absent kind as Complete
	// with no Objects entry (internal/observe/publish.go:291-294).
	c := observe.Capture{Now: now, Rows: true, Objects: map[string][]any{KindVeleroBackups: {b}}, Complete: map[string]bool{KindVeleroBackups: true, KindVeleroSchedules: true}}
	result := evalVeleroBackups("c")(c)
	rows := result.Rows.(RowSet[VeleroBackupRow]).Rows
	if len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
	attr := rows[0].Data.Attribution
	if attr.How != "" || attr.Coverage != "" {
		t.Fatalf("confirmed absent Schedules are known empty, got %+v", attr)
	}
}
