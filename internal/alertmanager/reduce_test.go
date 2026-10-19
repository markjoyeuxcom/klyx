package alertmanager

import (
	"reflect"
	"testing"
	"time"
)

func active(fp string, receivers ...string) Alert {
	return Alert{Fingerprint: fp, Labels: map[string]string{"alertname": fp, "severity": "critical"}, State: StateActive, Receivers: receivers}
}

func group(receiver string, apps ...GroupAlert) Group { return Group{Receiver: receiver, Alerts: apps} }

func muted(fp string, intervals ...string) GroupAlert {
	return GroupAlert{Fingerprint: fp, State: StateSuppressed, MutedBy: intervals}
}

func live(fp string) GroupAlert { return GroupAlert{Fingerprint: fp, State: StateActive} }

func one(t *testing.T, alerts []Alert, groups []Group) Reduced {
	t.Helper()
	r := Reduce(alerts, groups)
	if len(r) != 1 {
		t.Fatalf("reduced to %d", len(r))
	}
	return r[0]
}

// S8a §3: an active alert is muted only when its group appearances, as a
// multiset of receivers, equal its flat receivers and every one is muted.
func TestReduceMuteCoverage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		receivers []string
		groups    []Group
		state     string
		kind      MuteKind
		counted   bool
	}{
		{"covered, all muted", []string{"A"}, []Group{group("A", muted("f", "always"))}, StateSuppressed, "", false},
		{"covered, two routes all muted", []string{"A", "B"}, []Group{group("A", muted("f", "nights")), group("B", muted("f", "always"))}, StateSuppressed, "", false},
		{"covered, some muted", []string{"A", "B"}, []Group{group("A", muted("f", "always")), group("B", live("f"))}, StateActive, MutePartial, true},
		{"covered, none muted", []string{"A"}, []Group{group("A", live("f"))}, StateActive, MuteNone, true},
		{"[A, B], only A shown, muted", []string{"A", "B"}, []Group{group("A", muted("f", "always"))}, StateActive, MuteIncomplete, true},
		{"[A, A], both muted", []string{"A", "A"}, []Group{group("A", muted("f", "always")), group("A", muted("f", "always"))}, StateSuppressed, "", false},
		{"[A, A], one muted", []string{"A", "A"}, []Group{group("A", muted("f", "always")), group("A", live("f"))}, StateActive, MutePartial, true},
		{"[A, A], only one shown", []string{"A", "A"}, []Group{group("A", muted("f", "always"))}, StateActive, MuteIncomplete, true},
		{"unexpected receiver", []string{"A"}, []Group{group("A", muted("f", "always")), group("C", muted("f", "always"))}, StateActive, MuteIncomplete, true},
		{"other receiver only", []string{"A"}, []Group{group("C", muted("f", "always"))}, StateActive, MuteIncomplete, true},
		{"no appearance", []string{"A"}, nil, StateActive, MuteIncomplete, true},
		{"empty expectation, no appearance", nil, nil, StateActive, MuteIncomplete, true},
		{"empty expectation, a muted appearance", nil, []Group{group("A", muted("f", "always"))}, StateActive, MuteIncomplete, true},
	} {
		r := one(t, []Alert{active("f", tc.receivers...)}, tc.groups)
		if r.State != tc.state || r.Mute.Kind != tc.kind || r.Counted() != tc.counted {
			t.Errorf("%s: state %s mute %+v counted %v", tc.name, r.State, r.Mute, r.Counted())
		}
	}
	r := one(t, []Alert{active("f", "A", "B")}, []Group{group("A", muted("f", "nights")), group("B", muted("f", "always"))})
	if !reflect.DeepEqual(r.MutedBy, []string{"always", "nights"}) {
		t.Fatalf("intervals: %v", r.MutedBy)
	}
	r = one(t, []Alert{active("f", "B", "A")}, []Group{group("A", muted("f", "always")), group("B", live("f"))})
	if !reflect.DeepEqual(r.Mute.MutedFor, []string{"A"}) || !reflect.DeepEqual(r.Mute.ActiveFor, []string{"B"}) {
		t.Fatalf("partial: %+v", r.Mute)
	}
	r = one(t, []Alert{active("f", "A", "B")}, []Group{group("A", muted("f", "always"))})
	if !reflect.DeepEqual(r.Mute.Expected, []string{"A", "B"}) || !reflect.DeepEqual(r.Mute.Shown, []string{"A"}) {
		t.Fatalf("incomplete: %+v", r.Mute)
	}
}

// Silenced and inhibited alerts never consult the groups; unprocessed and
// unknown states are kept; membership is the flat list's.
func TestReduceStates(t *testing.T) {
	sil := Alert{Fingerprint: "s", Labels: map[string]string{"severity": "critical"}, State: StateSuppressed, SilencedBy: []string{"sil-1"}, Receivers: []string{"A"}}
	inh := Alert{Fingerprint: "i", Labels: map[string]string{"severity": "warning"}, State: StateSuppressed, InhibitedBy: []string{"fp-crit"}, Receivers: []string{"A"}}
	unp := Alert{Fingerprint: "u", Labels: map[string]string{"severity": "critical"}, State: StateUnprocessed}
	odd := Alert{Fingerprint: "o", Labels: map[string]string{"severity": "critical"}, State: "pending-upstream"}
	groups := []Group{group("A", muted("s", "always"), muted("i", "always"), muted("ghost", "always"))}
	got := Reduce([]Alert{sil, inh, unp, odd}, groups)
	if len(got) != 4 {
		t.Fatalf("the fingerprint only in the groups was kept: %d", len(got))
	}
	if got[0].State != StateSuppressed || !reflect.DeepEqual(got[0].SilencedBy, []string{"sil-1"}) || got[0].MutedBy != nil || got[0].Counted() {
		t.Fatalf("silenced: %+v", got[0])
	}
	if got[1].State != StateSuppressed || !reflect.DeepEqual(got[1].InhibitedBy, []string{"fp-crit"}) || got[1].MutedBy != nil {
		t.Fatalf("inhibited: %+v", got[1])
	}
	if got[2].State != StateUnprocessed || got[2].Counted() {
		t.Fatalf("unprocessed: %+v", got[2])
	}
	if got[3].State != "pending-upstream" || got[3].Counted() {
		t.Fatalf("unknown: %+v", got[3])
	}
}

// Duplicate copies (a front merging HA replicas) collapse: active if any
// copy is, causes united, a disagreement kept; the latest copy's fields.
func TestReduceDuplicates(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	a := active("f", "A")
	a.UpdatedAt, a.Annotations = t0, map[string]string{"summary": "old"}
	b := active("f", "A")
	b.UpdatedAt, b.Annotations = t0.Add(time.Minute), map[string]string{"summary": "new"}
	r := one(t, []Alert{a, b}, []Group{group("A", live("f"))})
	if r.Disagreement != "" || r.Annotations["summary"] != "new" || r.State != StateActive {
		t.Fatalf("agreeing copies: %+v", r)
	}
	s := a
	s.State, s.SilencedBy = StateSuppressed, []string{"sil-1"}
	r = one(t, []Alert{s, b}, []Group{group("A", live("f"))})
	if r.State != StateActive || r.Disagreement != "copies disagreed: active and suppressed" || !r.Counted() {
		t.Fatalf("disagreeing copies: %+v", r)
	}
	// unprocessed outranks suppressed: suppression is not established
	u := b
	u.State = StateUnprocessed
	r = one(t, []Alert{s, u}, nil)
	if r.State != StateUnprocessed || r.Disagreement != "copies disagreed: suppressed and unprocessed" || r.SilencedBy != nil {
		t.Fatalf("unprocessed and suppressed copies: %+v", r)
	}
	s2 := b
	s2.State, s2.SilencedBy = StateSuppressed, []string{"sil-2"}
	r = one(t, []Alert{s, s2}, nil)
	if r.State != StateSuppressed || !reflect.DeepEqual(r.SilencedBy, []string{"sil-1", "sil-2"}) {
		t.Fatalf("united causes: %+v", r)
	}
}

// Counted is active with a critical or warning severity only.
func TestCounted(t *testing.T) {
	for sev, want := range map[string]bool{"critical": true, "warning": true, "info": false, "none": false, "": false} {
		a := active("f", "A")
		a.Labels["severity"] = sev
		if got := one(t, []Alert{a}, []Group{group("A", live("f"))}).Counted(); got != want {
			t.Errorf("%q: %v", sev, got)
		}
	}
}

func TestReduceKeepsOrder(t *testing.T) {
	got := Reduce([]Alert{active("c"), active("a"), active("c"), active("b")}, nil)
	var fps []string
	for _, r := range got {
		fps = append(fps, r.Fingerprint)
	}
	if !reflect.DeepEqual(fps, []string{"c", "a", "b"}) {
		t.Fatalf("order: %v", fps)
	}
}
