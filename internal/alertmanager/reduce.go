package alertmanager

import (
	"sort"
	"strings"
)

// Klyx's states after Reduce (S8a §3). Unknown states are kept as written.
const (
	StateActive      = "active"
	StateSuppressed  = "suppressed"
	StateUnprocessed = "unprocessed"
	// StateOther is the class of a state Klyx does not know.
	StateOther = "other"
)

// StateClassOf is a state's class: one of Klyx's states, or StateOther. It
// is read from the raw state, never from a display value.
func StateClassOf(state string) string {
	switch state {
	case StateActive, StateSuppressed, StateUnprocessed:
		return state
	}
	return StateOther
}

// MuteKind is what the groups established about an active alert's mutes.
type MuteKind string

const (
	// MuteNone: every expected route was read, and none muted the alert.
	MuteNone MuteKind = "none"
	// MutePartial: every expected route was read; some muted it, some did
	// not. The alert is active: a route still notifying is not quiet.
	MutePartial MuteKind = "partial"
	// MuteIncomplete: the groups do not cover the routes the alert is
	// expected in (fewer, more, other, or none). Nothing is established.
	MuteIncomplete MuteKind = "incomplete"
)

// Mute is an active alert's mute evidence.
type Mute struct {
	Kind      MuteKind
	MutedFor  []string // partial: the receivers whose routes muted it
	ActiveFor []string // partial: the receivers whose routes did not
	Expected  []string // incomplete: the flat list's receivers
	Shown     []string // incomplete: the receivers the groups show it under
}

// Reduced is an alert with its state decided.
type Reduced struct {
	Alert
	// State is Klyx's: active, suppressed, unprocessed, or an unknown value
	// as written. Suppressed carries its causes.
	State        string
	SilencedBy   []string
	InhibitedBy  []string
	MutedBy      []string // a fully muted alert's time intervals
	Mute         Mute     // an active alert's mute evidence
	Disagreement string   // duplicate copies whose states differed
}

// Counted reports whether the alert counts in Needs you (S8a §6): active
// after Reduce, with a critical or warning severity.
func (r Reduced) Counted() bool {
	if r.State != StateActive {
		return false
	}
	s := SeverityOf(r.Labels)
	return s == SeverityCritical || s == SeverityWarning
}

// Reduce decides each alert's state from the flat list (membership, state,
// silences, inhibitions) and the groups (mutes, per route) - S8a §3:
//   - membership is the flat list's; a fingerprint only in the groups is
//     ignored;
//   - duplicate copies collapse: active if any copy is active, causes
//     united, a disagreement kept;
//   - silenced or inhibited never consult the groups;
//   - an active alert is muted only when its group appearances, as a
//     multiset of receivers, equal its flat receivers and every appearance
//     is muted; some muted is active (partial); any mismatch, an empty
//     expectation or no appearance is incomplete, and stays active.
//
// The output keeps the flat list's order of first appearance.
func Reduce(alerts []Alert, groups []Group) []Reduced {
	type entry struct {
		copies []Alert
	}
	var order []string
	byFP := map[string]*entry{}
	for _, a := range alerts {
		e := byFP[a.Fingerprint]
		if e == nil {
			e = &entry{}
			byFP[a.Fingerprint] = e
			order = append(order, a.Fingerprint)
		}
		e.copies = append(e.copies, a)
	}
	appearances := map[string][]appearance{}
	for _, g := range groups {
		for _, ga := range g.Alerts {
			appearances[ga.Fingerprint] = append(appearances[ga.Fingerprint], appearance{receiver: g.Receiver, mutedBy: ga.MutedBy})
		}
	}
	out := make([]Reduced, 0, len(order))
	for _, fp := range order {
		r := merge(byFP[fp].copies)
		if r.State == StateActive {
			decideMute(&r, appearances[fp])
		}
		out = append(out, r)
	}
	return out
}

type appearance struct {
	receiver string
	mutedBy  []string
}

// stateRank orders a merge: active wins (a copy that may notify is not
// quiet), then unprocessed (suppression not established), then suppressed,
// then unknown values.
func stateRank(s string) int {
	switch s {
	case StateActive:
		return 0
	case StateUnprocessed:
		return 1
	case StateSuppressed:
		return 2
	}
	return 3
}

// merge collapses duplicate copies of one fingerprint. The raw fields are
// the latest copy's; the state is the highest-ranked; the causes are united.
func merge(copies []Alert) Reduced {
	latest := copies[0]
	states := map[string]bool{}
	var silenced, inhibited []string
	best := copies[0].State
	for _, c := range copies {
		if c.UpdatedAt.After(latest.UpdatedAt) {
			latest = c
		}
		states[c.State] = true
		if stateRank(c.State) < stateRank(best) || (stateRank(c.State) == stateRank(best) && c.State < best) {
			best = c.State
		}
		silenced = append(silenced, c.SilencedBy...)
		inhibited = append(inhibited, c.InhibitedBy...)
	}
	r := Reduced{Alert: latest, State: best}
	if best == StateSuppressed {
		r.SilencedBy, r.InhibitedBy = uniq(silenced), uniq(inhibited)
	}
	if len(states) > 1 {
		names := make([]string, 0, len(states))
		for s := range states {
			names = append(names, s)
		}
		sort.Strings(names)
		r.Disagreement = "copies disagreed: " + strings.Join(names, " and ")
	}
	return r
}

// decideMute applies the coverage rule to an active alert.
func decideMute(r *Reduced, apps []appearance) {
	expected := append([]string(nil), r.Receivers...)
	shown := make([]string, 0, len(apps))
	for _, a := range apps {
		shown = append(shown, a.receiver)
	}
	sort.Strings(expected)
	sort.Strings(shown)
	// An empty expectation is never covered: with no appearance it is
	// incomplete below, and any appearance cannot equal it - so "every
	// appearance muted" is never vacuously true (Codex, S8a design accept).
	if len(apps) == 0 || !equalStrings(expected, shown) {
		r.Mute = Mute{Kind: MuteIncomplete, Expected: expected, Shown: shown}
		return
	}
	var mutedFor, activeFor, intervals []string
	for _, a := range apps {
		if len(a.mutedBy) > 0 {
			mutedFor = append(mutedFor, a.receiver)
			intervals = append(intervals, a.mutedBy...)
		} else {
			activeFor = append(activeFor, a.receiver)
		}
	}
	switch {
	case len(activeFor) == 0:
		r.State, r.MutedBy = StateSuppressed, uniq(intervals)
	case len(mutedFor) > 0:
		sort.Strings(mutedFor)
		sort.Strings(activeFor)
		r.Mute = Mute{Kind: MutePartial, MutedFor: mutedFor, ActiveFor: activeFor}
	default:
		r.Mute = Mute{Kind: MuteNone}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func uniq(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
