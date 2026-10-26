package appbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/observe"
)

// Settings slice F2b: notifications run from their running copy; a hand
// edit is drift until "Use the file's values", which writes nothing.

type notifyFixture struct {
	t      *testing.T
	s      *NotificationsService
	path   string
	offset time.Duration
	inc    uint64
	sent   []string
}

const notifyFleet = "clusters:\n  - name: prd\n    context: prd\n"

func newNotifyFixture(t *testing.T, doc string) *notifyFixture {
	t.Helper()
	f := &notifyFixture{t: t, path: filepath.Join(t.TempDir(), "fleet.yaml")}
	f.write(doc)
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	prev := notifyClock
	notifyClock = func() time.Time { return start.Add(f.offset) }
	t.Cleanup(func() { notifyClock = prev })
	f.s = NewNotificationsService(f.path, cfg, func(string) (config.ClusterConfig, bool) { return config.ClusterConfig{}, false },
		func(_, _, body string, _ map[string]any) error { f.sent = append(f.sent, body); return nil }, nil, nil)
	return f
}

func (f *notifyFixture) write(doc string) {
	f.t.Helper()
	if err := os.WriteFile(f.path, []byte(doc), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *notifyFixture) file() string {
	f.t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// onsetSent drives one new fault on a fresh connection of prd through the
// engine and says whether it was notified: what the engine runs, not what
// the service reports.
func (f *notifyFixture) onsetSent() bool {
	f.inc++
	before := len(f.sent)
	l := NotificationsListener(f.s)
	base := observe.TransitionBatch{Cluster: "prd", Incarnation: f.inc, Task: "pods", Part: "pods", Source: "pods", GVR: "v1/pods"}
	b := base
	b.Kind, b.Items = observe.BatchBaseline, nil
	l.OnTransitions(b)
	b = base
	b.Kind, b.Items = observe.BatchChanges, []observe.Transition{{Key: "u" + string(rune('0'+f.inc)), NS: "shop", Name: "web", From: "ok", To: "fault", Label: "CrashLoopBackOff"}}
	l.OnTransitions(b)
	f.offset += 10 * time.Minute
	f.s.engine.Flush()
	return len(f.sent) > before
}

const notifyOnAll = notifyFleet + "notifications:\n  enabled: true\n  scope: all\n  recoveries: false\n"

func TestANotificationHandEditIsAdoptedFromTheFile(t *testing.T) {
	f := newNotifyFixture(t, notifyFleet)
	f.write(notifyOnAll)
	st := f.s.Settings()
	if !st.Diverged || st.InUse.Enabled || st.FileError != "" {
		t.Fatalf("a hand edit is drift, not in use: %+v", st)
	}
	if f.onsetSent() {
		t.Fatal("the engine ran the file's settings before they were adopted")
	}
	// a toggle starts from what runs: it would replace the edit, so it is refused
	if r := f.s.ApplyNotificationsQuiet(NotificationSettingsDTO{Enabled: true, Scope: "protected"}); r.OK || !strings.Contains(r.Error, "use the file's values first") || f.file() != notifyOnAll {
		t.Fatalf("%+v\n%s", r, f.file())
	}
	p := f.s.PlanNotificationsFromFile()
	if p.Refused != "" || p.Writes || !p.Activates || p.Kind != planFile || len(p.Ends) != 0 || p.Interlock != nil {
		t.Fatalf("plan %+v", p)
	}
	if len(p.Changes) != 2 || p.Changes[0].ID != "notifications.enabled" || p.Changes[0].To != "on" || p.Changes[1].ID != "notifications.scope" || p.Changes[1].To != "every cluster" {
		t.Fatalf("changes %+v", p.Changes)
	}
	if r := f.s.ApplyNotifications(p.Digest); !r.OK {
		t.Fatalf("%+v", r)
	}
	if f.file() != notifyOnAll {
		t.Fatalf("Use the file's values wrote:\n%s", f.file())
	}
	if st := f.s.Settings(); st.Diverged || !st.InUse.Enabled || st.InUse.Scope != "all" {
		t.Fatalf("adopted %+v", st)
	}
	if !f.onsetSent() {
		t.Fatal("the engine does not run the adopted settings")
	}
	// consumed: the same plan never applies twice
	if r := f.s.ApplyNotifications(p.Digest); r.OK || !strings.Contains(r.Error, "already applied") {
		t.Fatalf("%+v", r)
	}
	// once adopted, a toggle writes again
	if r := f.s.ApplyNotificationsQuiet(NotificationSettingsDTO{Enabled: false, Scope: "all"}); !r.OK {
		t.Fatalf("%+v", r)
	}
	if c, _ := config.Load(f.path); c.Notifications == nil || c.Notifications.Enabled || f.onsetSent() {
		t.Fatalf("off: %+v", c.Notifications)
	}
}

func TestANotificationPlanRefusesAMovedFileOrRevision(t *testing.T) {
	f := newNotifyFixture(t, notifyFleet)
	f.write(notifyOnAll)
	p := f.s.PlanNotificationsFromFile()
	// the file moves while the plan is open
	f.write(notifyFleet + "notifications:\n  enabled: true\n  scope: all\n  recoveries: true\n")
	r := f.s.ApplyNotifications(p.Digest)
	if r.OK || r.Plan == nil || r.Plan.Digest == p.Digest || r.Plan.FileVersion == p.FileVersion || f.s.Settings().InUse.Enabled {
		t.Fatalf("a moved file applied: %+v", r)
	}
	// the replacement adopts the file as it is now, never the planned values
	if c := r.Plan.Changes; len(c) != 3 || c[2].ID != "notifications.recoveries" || c[2].To != "on" {
		t.Fatalf("the replacement: %+v", c)
	}
	// what runs moves, and the file comes back to the planned bytes
	p2 := f.s.PlanNotificationsFromFile()
	if r := f.s.ApplyNotifications(p2.Digest); !r.OK {
		t.Fatalf("%+v", r)
	}
	f.write(notifyFleet + "notifications:\n  enabled: false\n  scope: protected\n  recoveries: false\n")
	if r := f.s.ApplyNotifications(f.s.PlanNotificationsFromFile().Digest); !r.OK {
		t.Fatalf("%+v", r)
	}
	f.write(notifyOnAll)
	// the same file version and the same changes as p: only the revision moved
	r = f.s.ApplyNotifications(p.Digest)
	if r.OK || r.Plan == nil || r.Plan.FileVersion != p.FileVersion || r.Plan.Runtime["notifications"] == p.Runtime["notifications"] {
		t.Fatalf("a plan from an earlier revision applied: %+v", r)
	}
	if r := f.s.ApplyNotifications(r.Plan.Digest); !r.OK || !f.s.Settings().InUse.Enabled {
		t.Fatalf("the replacement: %+v", r)
	}
}

// A hand edit between the re-plan and the write refuses the toggle: the
// write is held to the version planned from.
func TestANotificationToggleWritesAgainstItsVersion(t *testing.T) {
	f := newNotifyFixture(t, notifyFleet)
	edited := "# edited by hand\n" + notifyFleet
	f.s.save = func(p, version string, c config.NotificationsConfig) (string, error) {
		f.write(edited)
		return config.SaveNotificationsAt(p, version, c)
	}
	r := f.s.ApplyNotificationsQuiet(NotificationSettingsDTO{Enabled: true})
	if r.OK || !strings.Contains(r.Error, "changed while this was applied") || r.Plan == nil || f.file() != edited || f.s.Settings().InUse.Enabled {
		t.Fatalf("%+v\n%s", r, f.file())
	}
	if f.onsetSent() {
		t.Fatal("a refused toggle reached the engine")
	}
}
