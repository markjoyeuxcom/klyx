package healthsrc

import (
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/velero"
	"testing"
	"time"
)

func TestReviewS2bAttributionFromIncompleteSchedules(t *testing.T) {
	for _, readable := range []bool{false, true} {
		t.Run(map[bool]string{false: "unreadable", true: "capped"}[readable], func(t *testing.T) {
			b := &velero.Backup{Meta: velero.Meta{Namespace: "velero", Name: "daily-1", UID: "b1", Created: time.Now()}, ScheduleLabel: "daily", Phase: "Completed", Start: time.Now()}
			objects := map[string][]any{KindVeleroBackups: {b}}
			if readable {
				objects[KindVeleroSchedules] = []any{}
			}
			c := observe.Capture{Now: time.Now(), Rows: true, Objects: objects, Complete: map[string]bool{KindVeleroBackups: true, KindVeleroSchedules: false}}
			result := evalVeleroBackups("c")(c)
			rows := result.Rows.(RowSet[VeleroBackupRow]).Rows
			if len(rows) != 1 {
				t.Fatalf("rows %d", len(rows))
			}
			if rows[0].Data.Attribution.How == "" {
				t.Fatal("classified as not a run of a current schedule while Schedules are incomplete")
			}
		})
	}
}
