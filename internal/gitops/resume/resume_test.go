package resume

import (
	"testing"
	"time"
)

// ResumedAt is the first enabled capture of a suspension, kept across
// later captures, never the last paused one (coverage S2a §4.2, revision
// 1), and a new suspension voids it.
func TestResumedAt(t *testing.T) {
	tr := NewTracker()
	t0 := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	fp := ReadyFingerprint("True", "x", "", "")
	paused := Sample{Kind: "schedules", UID: "u", Disabled: true, Fingerprint: fp, HasReady: true}
	enabled := paused
	enabled.Disabled = false

	tr.Observe(t0, paused)
	if _, ok := tr.ResumedAt("u"); ok {
		t.Fatal("paused: no resume yet")
	}
	// a long quiet pause, then a fresh unpause
	r1 := t0.Add(6 * time.Hour)
	if got := tr.Observe(r1, enabled); got != Awaiting {
		t.Fatalf("resume: %v", got)
	}
	tr.Observe(r1.Add(time.Minute), enabled)
	if at, ok := tr.ResumedAt("u"); !ok || !at.Equal(r1) {
		t.Fatalf("resumed at %v %v, want %v", at, ok, r1)
	}
	// a new suspension voids it
	tr.Observe(r1.Add(2*time.Minute), paused)
	if _, ok := tr.ResumedAt("u"); ok {
		t.Fatal("a new suspension keeps no resume time")
	}
	// evidence ends the resume
	tr.Observe(r1.Add(3*time.Minute), enabled)
	changed := enabled
	changed.Fingerprint = ReadyFingerprint("True", "y", "", "")
	if got := tr.Observe(r1.Add(4*time.Minute), changed); got != Current {
		t.Fatalf("evidence: %v", got)
	}
	if _, ok := tr.ResumedAt("u"); ok {
		t.Fatal("an ended resume has no resume time")
	}
}
