package velero

import (
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// fin finishes b at co (zero: no completion recorded), expiring at ex.
func fin(b *Backup, co, ex time.Time) *Backup { b.Completion, b.Expiration = co, ex; return b }

var (
	hzNow = day(12, 0, 0)
	later = hzNow.Add(720 * time.Hour)
)

func hzOf(t *testing.T, h History) Horizon {
	t.Helper()
	return judge(t, sched("0 2 * * *", lb), h, hzNow).Horizon
}

// Proven over named, by completion time, including a run that completed out
// of order; a newer PartiallyFailed is kept distinct (S2b §4).
func TestHorizonSelection(t *testing.T) {
	early := fin(owned(run("daily-a", day(1, 0, 0), day(1, 0, 0), "Completed")), day(3, 0, 0), later) // started first, finished last
	late := fin(owned(run("daily-b", day(2, 0, 0), day(2, 0, 0), "Completed")), day(2, 30, 0), later)
	named := fin(run("daily-c", day(4, 0, 0), day(4, 0, 0), "Completed"), day(4, 10, 0), later)
	hz := hzOf(t, complete(early, late, named))
	if hz.State != HorizonFound || hz.Success != early || !hz.Proven || hz.Untimed || hz.OrderUnknown || hz.Incomplete {
		t.Fatalf("%+v", hz)
	}
	// named only when no proven candidate is retained, and labelled
	hz = hzOf(t, complete(named))
	if hz.Success != named || hz.Proven {
		t.Fatalf("named: %+v", hz)
	}
	pf := fin(owned(run("daily-d", day(5, 0, 0), day(5, 0, 0), "PartiallyFailed")), day(5, 5, 0), later)
	older := fin(owned(run("daily-e", day(0, 10, 0), day(0, 10, 0), "PartiallyFailed")), day(0, 20, 0), later)
	hz = hzOf(t, complete(early, pf, older))
	if hz.Partial != pf || !hz.PartialProven || hz.PartialOrderUnknown {
		t.Fatalf("partial: %+v", hz)
	}
	if hz = hzOf(t, complete(early, older)); hz.Partial != nil {
		t.Fatalf("an older partial is not newer: %+v", hz)
	}
	// a name-matched partial is labelled as one
	npf := fin(run("daily-f", day(6, 0, 0), day(6, 0, 0), "PartiallyFailed"), day(6, 5, 0), later)
	if hz = hzOf(t, complete(early, npf)); hz.Partial != npf || hz.PartialProven {
		t.Fatalf("named partial: %+v", hz)
	}
	// no success: the newest partial is the fact
	if hz = hzOf(t, complete(pf)); hz.State != HorizonNone || hz.Partial != pf {
		t.Fatalf("no success: %+v", hz)
	}
}

// A synced success is never a candidate unless proven; a proven re-import
// is one (S2a's Attribute precedence).
func TestHorizonSynced(t *testing.T) {
	synced := fin(run("daily-x", day(9, 0, 0), day(1, 0, 0), "Completed"), day(1, 30, 0), later) // created 8h after it started
	if hz := hzOf(t, complete(synced)); hz.State != HorizonNone {
		t.Fatalf("synced counted: %+v", hz)
	}
	reimported := fin(owned(run("daily-y", day(9, 0, 0), day(1, 0, 0), "Completed")), day(1, 30, 0), later)
	if hz := hzOf(t, complete(reimported)); hz.Success != reimported || !hz.Proven {
		t.Fatalf("proven re-import: %+v", hz)
	}
	// an earlier schedule's success is not this one's
	prior := fin(run("daily-z", created.Add(-48*time.Hour), created.Add(-48*time.Hour), "Completed"), created.Add(-47*time.Hour), later)
	if hz := hzOf(t, complete(prior)); hz.State != HorizonNone {
		t.Fatalf("earlier incarnation: %+v", hz)
	}
}

// A missing completion time is said, never filled from the start (S2b §4,
// revision 1).
func TestHorizonMissingCompletion(t *testing.T) {
	timed := fin(owned(run("daily-a", day(2, 0, 0), day(2, 0, 0), "Completed")), day(2, 30, 0), later)
	after := fin(owned(run("daily-b", day(3, 0, 0), day(3, 0, 0), "Completed")), time.Time{}, later)
	before := fin(owned(run("daily-c", day(2, 10, 0), day(2, 10, 0), "Completed")), time.Time{}, later)

	if hz := hzOf(t, complete(timed, after)); hz.Success != after || !hz.Untimed || hz.OrderUnknown {
		t.Fatalf("started after: %+v", hz)
	}
	if hz := hzOf(t, complete(timed, before)); hz.Success != timed || hz.Untimed || !hz.OrderUnknown {
		t.Fatalf("started before: %+v", hz)
	}
	if hz := hzOf(t, complete(timed, after, before)); hz.Success != after || !hz.OrderUnknown {
		t.Fatalf("several untimed: %+v", hz)
	}
	// capped too: the same rule
	if hz := hzOf(t, capped(created.Add(-time.Hour), false, timed, before)); hz.Success != timed || !hz.OrderUnknown || hz.Incomplete {
		t.Fatalf("capped: %+v", hz)
	}
	// the newer-partial comparison: certain only by the rule
	pfAfter := fin(owned(run("daily-p", day(4, 0, 0), day(4, 0, 0), "PartiallyFailed")), time.Time{}, later)
	if hz := hzOf(t, complete(timed, pfAfter)); hz.Partial != pfAfter || hz.PartialOrderUnknown {
		t.Fatalf("partial after: %+v", hz)
	}
	pfOverlap := fin(owned(run("daily-q", day(2, 20, 0), day(2, 20, 0), "PartiallyFailed")), time.Time{}, later)
	if hz := hzOf(t, complete(timed, pfOverlap)); hz.Partial != pfOverlap || !hz.PartialOrderUnknown {
		t.Fatalf("partial unordered: %+v", hz)
	}
	// a success without a completion time cannot order a partial before it
	if hz := hzOf(t, complete(after, pfAfter)); hz.Partial != pfAfter || !hz.PartialOrderUnknown {
		t.Fatalf("untimed success: %+v", hz)
	}
}

// W orders by run time and never certifies completion order: capped, the
// selection is complete only when the schedule's whole history is held
// (S2b §4, revision 1); tier 0 certifies nothing but suppresses no
// retained success (revision 2).
func TestHorizonUnderTheCap(t *testing.T) {
	ok := fin(owned(run("daily-a", day(2, 0, 0), day(2, 0, 0), "Completed")), day(2, 30, 0), later)
	w := day(1, 0, 0) // after the schedule's creation: its history is not wholly held
	hz := hzOf(t, capped(w, false, ok))
	if hz.Success != ok || !hz.Incomplete || !hz.W.Equal(w) || hz.Tier0 {
		t.Fatalf("since W: %+v", hz)
	}
	if hz = hzOf(t, capped(w, false)); hz.State != HorizonNone || !hz.Incomplete {
		t.Fatalf("none since W: %+v", hz)
	}
	// the schedule's whole history is held: complete
	if hz = hzOf(t, capped(created.Add(-3*time.Minute), false, ok)); hz.Incomplete {
		t.Fatalf("own history held: %+v", hz)
	}
	if hz = hzOf(t, capped(created.Add(-90*time.Second), false, ok)); !hz.Incomplete {
		t.Fatalf("inside the margins: %+v", hz)
	}
	// tier 0: the retained success is selected and qualified, never suppressed
	if hz = hzOf(t, capped(time.Time{}, true, ok)); hz.Success != ok || !hz.Incomplete || !hz.Tier0 || !hz.W.IsZero() {
		t.Fatalf("tier 0: %+v", hz)
	}
	if hz = hzOf(t, History{Backups: []*Backup{ok}, Readable: true}); hz.Success != ok || !hz.Incomplete {
		t.Fatalf("unknown window: %+v", hz)
	}
	if hz = hzOf(t, History{}); hz.State != HorizonUnreadable {
		t.Fatalf("unreadable: %+v", hz)
	}
}

// Expiry decides eligibility at the capture's time; a GC-blocked expired
// record is counted, never selected, and the earliest candidate
// expiration is Next without changing the role (S2b §4, Clock).
func TestHorizonExpiry(t *testing.T) {
	s := sched("0 2 * * *", lb)
	n := fin(owned(run(nName, lb, lb, "Completed")), lb.Add(time.Minute), day(6, 0, 0))
	gone := fin(owned(run("daily-old", day(0, 0, 0), day(0, 0, 0), "Completed")), day(0, 10, 0), day(0, 30, 0)) // expired, GC blocked
	now := day(1, 0, 0)
	v := judge(t, s, complete(n, gone), now)
	want(t, v, health.OK, "Completed")
	if v.Horizon.Success != n || v.Horizon.Expired != 1 || !v.Horizon.NextExpiry.Equal(day(6, 0, 0)) {
		t.Fatalf("%+v", v.Horizon)
	}
	if v.Eval.Next == nil || !v.Eval.Next.Equal(day(2, 10, 0)) {
		t.Fatalf("next %v: the missed boundary is earlier than the expiry", v.Eval.Next)
	}
	// crossing the expiry with no API update: the success leaves the horizon
	// while the verdict's role is the classifier's alone
	at := day(6, 0, 0)
	s2 := sched("0 2 * * *", lb)
	s2.Cron = "0 2 1 1 *" // yearly: no missed boundary between
	v = judge(t, s2, complete(n, gone), now)
	if v.Eval.Next == nil || !v.Eval.Next.Equal(at) {
		t.Fatalf("expiry not in Next: %v", v.Eval.Next)
	}
	before := v.Eval.Role
	v = judge(t, s2, complete(n, gone), at)
	if v.Horizon.State != HorizonNone || v.Horizon.Expired != 2 || v.Eval.Role != before {
		t.Fatalf("after expiry: %+v role %v (was %v)", v.Horizon, v.Eval.Role, before)
	}
}

// The wording keeps the confidence (S2b §4).
func TestHorizonText(t *testing.T) {
	ok := fin(owned(run("daily-a", day(2, 0, 0), day(2, 0, 0), "Completed")), day(2, 30, 0), later)
	named := fin(run("daily-c", day(4, 0, 0), day(4, 0, 0), "Completed"), day(4, 10, 0), later)
	cases := []struct {
		h    History
		want string
	}{
		{complete(ok), "latest proven retained success, completed 2026-10-17 02:30:00Z"},
		{complete(named), "a Completed record matched by name (not proven to be this schedule's), completed 2026-10-17 04:10:00Z"},
		{complete(), "no successful backup retained"},
		{capped(day(1, 0, 0), false), "no successful backup retained since 2026-10-17 01:00:00Z"},
		{capped(day(1, 0, 0), false, ok), "latest proven retained success, completed 2026-10-17 02:30:00Z; a run that started before 2026-10-17 01:00:00Z and completed later may not be retained"},
		{capped(time.Time{}, true, ok), "latest proven retained success, completed 2026-10-17 02:30:00Z; the retained history may be missing finished runs"},
		{History{}, "runs not readable"},
	}
	for _, c := range cases {
		if got := hzOf(t, c.h).Text(); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
	for _, s := range []string{hzOf(t, complete(ok)).Text(), hzOf(t, complete(named)).Text()} {
		if strings.Contains(strings.ToLower(s), "restorable") {
			t.Fatal(s)
		}
	}
	pf := fin(run("daily-p", day(5, 0, 0), day(5, 0, 0), "PartiallyFailed"), day(5, 5, 0), later)
	if got := hzOf(t, complete(ok, pf)).PartialText(); got != "a newer record matched by name partially failed" {
		t.Fatal(got)
	}
}
