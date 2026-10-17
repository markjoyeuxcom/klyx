package velero

import (
	"strings"
	"testing"
	"time"
)

var (
	utcServer = Server{ZoneResolved: true}
	v1184     = Server{Version: Version{1, 18, 4}, VersionResolved: true, ZoneResolved: true}
	v1172     = Server{Version: Version{1, 17, 2}, VersionResolved: true, ZoneResolved: true}
)

func at(t *testing.T, zone, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02T15:04:05", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNextRunAgreeingForms(t *testing.T) {
	base := at(t, "UTC", "2026-10-17T01:00:00")
	cases := []struct {
		spec string
		srv  Server
		want time.Time
	}{
		{"0 2 * * *", utcServer, at(t, "UTC", "2026-10-17T02:00:00")},
		{"@daily", utcServer, at(t, "UTC", "2026-10-18T00:00:00")},
		{"@every 90m", utcServer, base.Add(90 * time.Minute)},
		{"CRON_TZ=Europe/Amsterdam 0 2 * * *", utcServer, at(t, "Europe/Amsterdam", "2026-10-18T02:00:00")},
		// no prefix: the server's zone (its literal TZ)
		{"0 2 * * *", Server{Zone: "Europe/Amsterdam", ZoneResolved: true}, at(t, "Europe/Amsterdam", "2026-10-18T02:00:00")},
	}
	for _, c := range cases {
		d := NextRun(c.spec, base, c.srv)
		if d.Unknown != "" || !d.D.Equal(c.want) || d.Library != "both agree" {
			t.Errorf("%q: %+v, want %v", c.spec, d, c.want)
		}
	}
}

// Velero 1.18.3 moved to go-cron: a day of month with a day of week means
// both on go-cron, either on robfig (S2a §1).
func TestNextRunDomDowDependsOnVersion(t *testing.T) {
	base := at(t, "UTC", "2026-10-17T12:00:00") // a Saturday
	spec := "0 3 1 * 0"
	if d := NextRun(spec, base, utcServer); !strings.Contains(d.Unknown, "depends on Velero's version") {
		t.Fatalf("unresolved version: %+v", d)
	}
	if d := NextRun(spec, base, v1172); !d.D.Equal(at(t, "UTC", "2026-10-18T03:00:00")) || d.Library != LibraryRobfig {
		t.Fatalf("robfig (either): %+v", d)
	}
	if d := NextRun(spec, base, v1184); !d.D.Equal(at(t, "UTC", "2026-11-01T03:00:00")) || d.Library != LibraryGoCron {
		t.Fatalf("go-cron (both: Sunday 1 November): %+v", d)
	}
}

// A form only one library parses is never resolved from an Enabled phase
// (S2a design revision 1, finding 5).
func TestNextRunOneLibraryParses(t *testing.T) {
	base := at(t, "UTC", "2026-10-17T12:00:00")
	for _, spec := range []string{"* * * * 7", "@manual"} {
		if d := NextRun(spec, base, utcServer); !strings.Contains(d.Unknown, "depends on Velero's version") {
			t.Errorf("%q unresolved: %+v", spec, d)
		}
		if d := NextRun(spec, base, v1172); !strings.HasPrefix(d.Unknown, "Klyx cannot parse") {
			t.Errorf("%q on robfig: %+v", spec, d)
		}
	}
	if d := NextRun("* * * * 7", base, v1184); d.Unknown != "" || !d.D.Equal(at(t, "UTC", "2026-10-18T00:00:00")) {
		t.Fatalf("Sunday 7 on go-cron: %+v", d)
	}
	if d := NextRun("@manual", base, v1184); d.Unknown != "" || !d.Zero {
		t.Fatalf("@manual on go-cron is the zero time: %+v", d)
	}
}

// A spec that never matches gives the zero time in both libraries: Velero
// then starts a run on every check.
func TestNextRunNeverMatches(t *testing.T) {
	d := NextRun("0 0 31 2 *", at(t, "UTC", "2026-10-17T12:00:00"), utcServer)
	if !d.Zero || d.Unknown != "" || d.Library != "both agree" {
		t.Fatalf("31 February: %+v", d)
	}
}

// The spring-forward hour: go-cron runs at the matching minute after it,
// robfig drops that day's run (S2a §1).
func TestNextRunSpringForward(t *testing.T) {
	spec := "CRON_TZ=America/New_York 30 2 * * *"
	base := at(t, "America/New_York", "2026-03-08T00:00:00") // DST starts at 02:00
	if d := NextRun(spec, base, v1184); !d.D.Equal(at(t, "America/New_York", "2026-03-08T03:30:00")) {
		t.Fatalf("go-cron: %v", d.D)
	}
	if d := NextRun(spec, base, v1172); !d.D.Equal(at(t, "America/New_York", "2026-03-09T02:30:00")) {
		t.Fatalf("robfig: %v", d.D)
	}
	if d := NextRun(spec, base, utcServer); !strings.Contains(d.Unknown, "depends on Velero's version") {
		t.Fatalf("unresolved: %+v", d)
	}
}

// The repeated autumn hour: both libraries return both occurrences, so a
// schedule there fires twice; Klyx owes what Velero owes.
func TestNextRunFallBackRepeats(t *testing.T) {
	spec := "CRON_TZ=America/New_York 30 1 * * *"
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) // 01:30 EDT
	d := NextRun(spec, first, utcServer)
	if d.Unknown != "" || !d.D.Equal(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("the repeated 01:30 (EST): %+v", d)
	}
}

func TestNextRunUnresolvedFacts(t *testing.T) {
	base := at(t, "UTC", "2026-10-17T12:00:00")
	if d := NextRun("0 2 * * *", base, Server{}); d.Unknown != "the server's time zone is unresolved" {
		t.Fatalf("zone unresolved: %+v", d)
	}
	// a prefixed spec needs no server zone
	if d := NextRun("CRON_TZ=UTC 0 2 * * *", base, Server{}); d.Unknown != "" {
		t.Fatalf("prefixed: %+v", d)
	}
	old := Server{Version: Version{1, 14, 1}, VersionResolved: true, ZoneResolved: true}
	if d := NextRun("0 2 * * *", base, old); d.Unknown != "Velero before 1.15 uses another cron library" {
		t.Fatalf("1.14: %+v", d)
	}
	if d := NextRun(strings.Repeat("1", maxCron+1), base, utcServer); d.Unknown != "the cron is too long to read" {
		t.Fatalf("too long: %+v", d)
	}
	if d := NextRun("0 2 * * *", base, Server{Zone: "Mars/Olympus", ZoneResolved: true}); !strings.Contains(d.Unknown, "Mars/Olympus") {
		t.Fatalf("unknown zone: %+v", d)
	}
}

func TestBase(t *testing.T) {
	c, lb, ls := at(t, "UTC", "2026-10-01T00:00:00"), at(t, "UTC", "2026-10-10T02:00:00"), at(t, "UTC", "2026-10-12T09:00:00")
	if b := Base(time.Time{}, c, time.Time{}); !b.Equal(c) {
		t.Fatalf("no run yet: %v", b)
	}
	if b := Base(lb, c, time.Time{}); !b.Equal(lb) {
		t.Fatalf("lastBackup: %v", b)
	}
	if b := Base(lb, c, ls); !b.Equal(ls) {
		t.Fatalf("a later lastSkipped: %v", b)
	}
}

func TestVersionOfImage(t *testing.T) {
	for image, want := range map[string]string{
		"velero/velero:v1.18.4":                                   "1.18.4",
		"registry.example.com/mirror/velero/velero:v1.17.2":       "1.17.2",
		"velero/velero:v1.18.4@sha256:" + strings.Repeat("a", 64): "1.18.4",
		"docker.io/velero/velero:v1.18.0-rc.1":                    "1.18.0",
		"velero/velero@sha256:" + strings.Repeat("a", 64):         "",
		"velero/velero:main":                                      "",
		"registry.k8s.io/pause:3.10":                              "",
	} {
		v, ok := VersionOfImage(image)
		got := ""
		if ok {
			got = v.String()
		}
		if got != want {
			t.Errorf("%s: %q, want %q", image, got, want)
		}
	}
}

func TestParseError(t *testing.T) {
	if e := ParseError("", utcServer); e != "the cron is empty" {
		t.Fatal(e)
	}
	if e := ParseError("0 2 * *", v1184); !strings.HasPrefix(e, LibraryGoCron+": ") {
		t.Fatalf("go-cron's own error: %s", e)
	}
	if e := ParseError("0 2 * *", utcServer); !strings.Contains(e, LibraryRobfig) || !strings.Contains(e, LibraryGoCron) {
		t.Fatalf("both libraries' errors when unresolved: %s", e)
	}
	if e := ParseError("0 2 * * *", utcServer); e != "" {
		t.Fatalf("valid: %s", e)
	}
}
