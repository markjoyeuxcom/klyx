package velero

import (
	"strings"
	"testing"
	"time"
)

func TestReviewS2bMissingBothTimesIsStillASuccess(t *testing.T) {
	b := fin(owned(run("daily-no-times", day(2, 0, 0), time.Time{}, "Completed")), time.Time{}, later)
	h := hzOf(t, complete(b))
	if h.Success != b || h.State != HorizonFound {
		t.Fatalf("lost success: %+v", h)
	}
	if strings.Contains(h.Text(), "0001") {
		t.Fatalf("invented a start time: %s", h.Text())
	}
}
