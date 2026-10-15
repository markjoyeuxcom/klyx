package line

import (
	"sort"
	"time"
)

// Read states, per station (slice 3 note §2).
const (
	ReadOK       = "ok"
	ReadPartial  = "partial"
	ReadStale    = "stale"
	ReadError    = "error"
	ReadChecking = "checking"
)

// Station states, in precedence order (§3.3). Git ordering (§3.4) replaces
// matches/differs per station in slice 6.
const (
	StateChecking       = "checking"
	StateNotCompared    = "not compared"
	StateAppliedUnknown = "applied unknown"
	StateMixed          = "mixed"
	StateMatches        = "matches"
	StateDiffers        = "differs"
)

// How a tie for R was broken: Git and Argo chart keys by the newest source
// artifact ("tie broken by fetch time, not commit order"); a recorded-name
// Flux chart key by the version string's lexical order - deterministic, not
// version recency (the current source's fetch time says nothing about a
// historical record).
const (
	TieByFetchTime     = "fetch time"
	TieByVersionString = "lexical version string"
)

// How the comparison key was chosen.
const (
	KeyByMostStations = "most stations"
	KeyByOverride     = "override"
	KeyBySelector     = "selector"
)

// Input is the whole fleet's line input.
type Input struct {
	Stations []StationInput
	Read     map[string]string // per cluster; missing reads as checking
	Override *Key              // fleet.yaml line.source
	Selector *Key              // the board's session choice
}

// KeyCount is one key and the stations holding it, each counted once.
type KeyCount struct {
	Key      Key
	Stations int
}

// Flags are a station's facts that hold independently of its state (§3.3).
type Flags struct {
	Failing       bool
	FailSince     *time.Time // the earliest failure among E and its sources
	FailBasis     string     // how FailSince is known: rule | observed | sampled | derived | unknown
	Compared      string     // "compared X, applied Y": X, when a compared fact differs from applied
	ComparedVs    string     // Y
	Syncing       bool
	Suspended     bool
	SourceChanged bool
	ValuesDiffer  bool // a chart unit at R's version with different values (configDigest)
}

// StationLine is one station's place and comparison.
type StationLine struct {
	Placed
	Read     string
	State    string
	E        int      // eligible units on the chosen key
	A        []string // the distinct known applied revisions (sha or version)
	U        int      // eligible units whose applied revision is unknown
	Single   string   // the one applied revision, when every eligible unit has the same known one
	Flags    Flags
	Held     bool   // attempted R and failing (§3.3)
	HeldWhy  string // the evidence
	Excluded []Exclusion
	Units    []Unit // every unit of the station, on any key
}

// Line is the computed board.
type Line struct {
	Placement   Placement
	Keys        []KeyCount
	Key         *Key
	KeyHow      string
	KeyStations int
	// R is the comparison revision: the single revision of the most stations
	// that have one. A reference, never a rollout target.
	R          string
	RStations  int
	RTieBroken bool   // a tie was broken, by RTieBy
	RTieBy     string // TieByFetchTime or TieByVersionString
	Stations   []StationLine
	Siding     []StationLine
	// The headline: R applied on Applied of Compared stations, plus the
	// not-compared and checking counts.
	Applied, Compared, NotCompared, Checking int
}

// Build computes the line (§3.2-3.3).
func Build(in Input) Line {
	var st []Station
	units := map[string][]Unit{}
	excl := map[string][]Exclusion{}
	for _, s := range in.Stations {
		st = append(st, s.Station)
		units[s.Cluster], excl[s.Cluster] = Units(s)
	}
	l := Line{Placement: Place(st), Keys: keyCounts(units)}
	l.Key, l.KeyHow, l.KeyStations = chooseKey(l.Keys, in.Override, in.Selector)

	read := func(c string) string {
		if r, ok := in.Read[c]; ok {
			return r
		}
		return ReadChecking
	}
	build := func(p Placed) StationLine {
		sl := StationLine{Placed: p, Read: read(p.Cluster), Excluded: excl[p.Cluster], Units: units[p.Cluster]}
		facts(&sl, l.Key)
		return sl
	}
	for _, p := range l.Placement.Placed {
		l.Stations = append(l.Stations, build(p))
	}
	for _, p := range l.Placement.Siding {
		l.Siding = append(l.Siding, build(p))
	}
	l.R, l.RStations, l.RTieBroken = chooseR(append(append([]StationLine(nil), l.Stations...), l.Siding...), l.Key)
	if l.RTieBroken {
		l.RTieBy = TieByFetchTime
		if l.Key.Kind == KeyChartName {
			l.RTieBy = TieByVersionString
		}
	}
	for _, sls := range [][]StationLine{l.Stations, l.Siding} {
		for i := range sls {
			finish(&sls[i], &l)
		}
	}
	return l
}

// keyCounts counts each key's stations, each station once (§3.2); most
// stations first, ties lexically.
func keyCounts(units map[string][]Unit) []KeyCount {
	count := map[Key]int{}
	for _, us := range units {
		seen := map[Key]bool{}
		for _, u := range us {
			if !seen[u.Key] {
				seen[u.Key] = true
				count[u.Key]++
			}
		}
	}
	out := make([]KeyCount, 0, len(count))
	for k, n := range count {
		out = append(out, KeyCount{Key: k, Stations: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stations != out[j].Stations {
			return out[i].Stations > out[j].Stations
		}
		return out[i].Key.String() < out[j].Key.String()
	})
	return out
}

// chooseKey: the selector, else the override, else the Git key held by the
// most stations (ties lexically first). A default needs a key two stations
// share; with none, nothing is compared (§3.2).
func chooseKey(counts []KeyCount, override, selector *Key) (*Key, string, int) {
	find := func(k Key) (int, bool) {
		for _, c := range counts {
			if c.Key == k {
				return c.Stations, true
			}
		}
		return 0, false
	}
	if selector != nil {
		if n, ok := find(*selector); ok {
			k := *selector
			return &k, KeyBySelector, n
		}
	}
	if override != nil {
		k := *override
		n, _ := find(k)
		return &k, KeyByOverride, n
	}
	for _, c := range counts {
		if c.Key.Kind == KeyGit && c.Stations >= 2 {
			k := c.Key
			return &k, KeyByMostStations, c.Stations
		}
	}
	return nil, "", 0
}

// facts computes a station's E/A/U, flags and pre-R state.
func facts(sl *StationLine, key *Key) {
	if key == nil {
		sl.State = StateNotCompared
		if sl.Read != ReadOK {
			sl.State = StateChecking
		}
		return
	}
	known := map[string]bool{}
	for _, u := range sl.Units {
		if u.Key != *key {
			continue
		}
		sl.E++
		if u.Applied == nil {
			sl.U++
		} else if !known[u.Applied.id()] {
			known[u.Applied.id()] = true
			sl.A = append(sl.A, u.Applied.id())
		}
		f := &sl.Flags
		if u.Failing {
			f.Failing = true
			if u.FailSince != nil && (f.FailSince == nil || u.FailSince.Before(*f.FailSince)) {
				t := *u.FailSince
				f.FailSince, f.FailBasis = &t, u.FailBasis
			} else if f.FailSince == nil && f.FailBasis == "" {
				f.FailBasis = u.FailBasis
			}
		}
		if u.Compared != nil && u.Applied != nil && !sameRev(u.Compared, u.Applied) && f.Compared == "" {
			f.Compared, f.ComparedVs = u.Compared.id(), u.Applied.id()
		}
		f.Syncing = f.Syncing || u.Syncing
		f.Suspended = f.Suspended || u.Suspended
		f.SourceChanged = f.SourceChanged || u.SourceChanged
	}
	sort.Strings(sl.A)
	unread := false
	for _, u := range sl.Units {
		if u.Key == *key && u.Read != ReadOK && u.Read != KindAbsent {
			unread = true
		}
	}
	switch {
	case sl.Read != ReadOK || unread: // §3.3 rule 1: the station, or any eligible unit, is not read ok
		sl.State = StateChecking
	case sl.E == 0:
		sl.State = StateNotCompared
	case len(sl.A) == 0:
		sl.State = StateAppliedUnknown
	case len(sl.A) > 1 || sl.U > 0:
		sl.State = StateMixed
	default:
		sl.Single = sl.A[0]
		sl.State = StateMatches // provisional: matches or differs once R is known
	}
}

// chooseR: the single revision of the most stations with one; mixed,
// unknown, checking and not-compared stations never vote. Ties go to the
// newest source artifact time, disclosed (§3.3).
func chooseR(sls []StationLine, key *Key) (string, int, bool) {
	votes := map[string]int{}
	newest := map[string]time.Time{}
	for _, sl := range sls {
		if sl.Single == "" || sl.Read != ReadOK || key == nil {
			continue
		}
		votes[sl.Single]++
		for _, u := range sl.Units {
			// Only the key's eligible units break a tie: an unrelated unit
			// with the same revision string says nothing about this key.
			if u.Key == *key && u.Applied != nil && u.Applied.id() == sl.Single && u.ArtifactTime.After(newest[sl.Single]) {
				newest[sl.Single] = u.ArtifactTime
			}
		}
	}
	best, n, tie := "", 0, false
	for rev, c := range votes {
		switch {
		case c > n:
			best, n, tie = rev, c, false
		case c == n:
			tie = true
			byVersion := key != nil && key.Kind == KeyChartName
			if (byVersion && rev < best) || (!byVersion && (newest[rev].After(newest[best]) || (newest[rev].Equal(newest[best]) && rev < best))) {
				best = rev
			}
		}
	}
	return best, n, tie && n > 0
}

// Settle computes one station against a given key and comparison revision
// R: its units, facts, state and held, as Build would with that reference.
// The line's own history commits a station's state on every accepted change
// against the last build's reference (slice 3 note §5).
func Settle(in StationInput, read string, key *Key, r string) StationLine {
	sl := StationLine{Placed: Placed{Station: in.Station}, Read: read}
	sl.Units, sl.Excluded = Units(in)
	facts(&sl, key)
	settle(&sl, key, r, "")
	return sl
}

// settle settles matches/differs against R, held and values-differ (rd: the
// configDigest most stations at R carry, or "").
func settle(sl *StationLine, key *Key, r, rd string) {
	if sl.State == StateMatches && sl.Single != r {
		sl.State = StateDiffers
	}
	if r == "" && sl.State == StateMatches {
		sl.State = StateDiffers // no comparison revision: nothing is lit
	}
	if key == nil || r == "" {
		return
	}
	var rDigest string
	for _, u := range sl.Units {
		if u.Key != *key {
			continue
		}
		// Held needs evidence tied to R: an eligible unit attempted R and
		// is failing (§3.3). An unrelated failure stays "differs". Never
		// for a recorded-name chart: a historical failed attempt plus a
		// current failure does not prove the current rollout is held.
		if key.Kind != KeyChartName && u.Failing && u.Attempted != nil && u.Attempted.id() == r {
			sl.Held, sl.HeldWhy = true, "attempted "+r+", failing"
		}
		if u.Applied != nil && u.Applied.id() == r && u.Applied.Digest != "" {
			if rDigest == "" {
				rDigest = u.Applied.Digest
			} else if rDigest != u.Applied.Digest {
				sl.Flags.ValuesDiffer = true
			}
		}
	}
	if rDigest != "" && rd != "" && rDigest != rd {
		sl.Flags.ValuesDiffer = true
	}
}

// finish settles a station against the line's R and adds it to the
// headline counts.
func finish(sl *StationLine, l *Line) {
	settle(sl, l.Key, l.R, l.rDigest())
	switch sl.State {
	case StateMatches:
		l.Applied++
		l.Compared++
	case StateDiffers, StateMixed, StateAppliedUnknown:
		l.Compared++
	case StateNotCompared:
		l.NotCompared++
	case StateChecking:
		l.Checking++
	}
}

// rDigest is the configDigest most stations at R carry (ties lexically
// first): the values a chart unit at R's version is compared against.
func (l *Line) rDigest() string {
	// Chart keys compare configDigest: for a recorded-name key it reads
	// "recorded values differ", never equal chart content or desired values.
	if l.Key == nil || (l.Key.Kind != KeyChart && l.Key.Kind != KeyChartName) {
		return ""
	}
	count := map[string]int{}
	for _, sls := range [][]StationLine{l.Stations, l.Siding} {
		for _, sl := range sls {
			if sl.Single != l.R {
				continue
			}
			for _, u := range sl.Units {
				if u.Key == *l.Key && u.Applied != nil && u.Applied.Digest != "" {
					count[u.Applied.Digest]++
					break
				}
			}
		}
	}
	best, n := "", 0
	for d, c := range count {
		if c > n || (c == n && d < best) {
			best, n = d, c
		}
	}
	return best
}
