// Package observe is the health slice 1c Observer core
// (docs/superpowers/specs/2026-10-11-klyx-health-1c-cluster-summary.md):
// one Observer per cluster owns refcounted watch leases, Klyx-owned
// reflector models with a per-kind freshness machine, per-part versions with
// atomic capture, a throttled publisher, time-driven reclassification and a
// terminal Final publish. It knows no domain: domains plug in as specs.
package observe

import "time"

// Coverage is how much of a kind, part or task Klyx currently observes.
type Coverage string

const (
	Live       Coverage = "live"       // a complete replacement list applied since the last error
	Stale      Coverage = "stale"      // last-good evidence retained; the watch or read is failing
	Checking   Coverage = "checking"   // no complete list yet
	Unreadable Coverage = "unreadable" // never read; no evidence
	Absent     Coverage = "absent"     // not installed (a successful discovery lacks it)
)

// coverageRank orders the non-absent states worst first.
var coverageRank = map[Coverage]int{Unreadable: 0, Stale: 1, Checking: 2, Live: 3}

// rollup is the worst of a set of coverages: unreadable > stale > checking >
// live, and absent only when every member is absent.
func rollup(cs []Coverage) Coverage {
	worst, any := Live, false
	for _, c := range cs {
		if c == Absent {
			continue
		}
		if !any || coverageRank[c] < coverageRank[worst] {
			worst = c
		}
		any = true
	}
	if !any {
		return Absent
	}
	return worst
}

// Error classes recorded on a kind or snapshot.
const (
	ClassForbidden    = "forbidden"
	ClassUnauthorized = "unauthorized"
	ClassNotFound     = "notFound"
	ClassDiscovery    = "discovery"
	ClassTransport    = "transport"
	ClassTimeout      = "timeout"
	ClassOverdue      = "overdue"
	ClassCLIMissing   = "cliMissing"
	ClassOther        = "other"
)

// KindCoverage is one watched kind or snapshot as a part sees it.
type KindCoverage struct {
	Kind     string   `json:"kind"`
	Role     string   `json:"role"` // counted | dependency
	Scope    string   `json:"scope"`
	Coverage Coverage `json:"coverage"`
	Class    string   `json:"class,omitempty"`
	Error    string   `json:"error,omitempty"`
	Objects  int      `json:"objects"`
	// Truncated: a capped snapshot (Events); informers are never truncated.
	Truncated bool `json:"truncated,omitempty"`
	// AsOf is nil while live (current as of the summary's EvaluatedAt), the
	// moment the kind left live when stale, and the read time for snapshots.
	AsOf       *time.Time `json:"asOf,omitempty"`
	LastGoodAt *time.Time `json:"lastGoodAt,omitempty"`
	CadenceSec int64      `json:"cadenceSec,omitempty"`
	// Discovery names a failing re-probe while the kind's data stays as
	// observed (a live reflector keeps running; spec §3.3). A consumer
	// treats it as a capability gap, never as data staleness.
	Discovery string `json:"discovery,omitempty"`
}

// Counts is a complete partition of a verdict part by verdict.
type Counts struct {
	Total       int `json:"total"`
	Fault       int `json:"fault"`
	Degraded    int `json:"degraded"`
	Unknown     int `json:"unknown"`
	Progressing int `json:"progressing"`
	Paused      int `json:"paused"`
	OK          int `json:"ok"`
}

// NativeCounts carries a relocated lens rule: the role partition plus the
// lens's own inclusion count and wording (spec §2).
type NativeCounts struct {
	Total    int    `json:"total"`
	Roles    Counts `json:"roles"`
	Included int    `json:"included"`
	Word     string `json:"word"`
}

// Ref points at one object worth opening (Top refs, worst first).
type Ref struct {
	Cluster    string `json:"cluster"`
	GVR        string `json:"gvr"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	UID        string `json:"uid,omitempty"`
	Parent     string `json:"parent,omitempty"` // a route's worst parent ("gateway infra/eg")
	Role       string `json:"role"`
	Label      string `json:"label"`
	Generation int64  `json:"generation,omitempty"`
	Route      string `json:"route,omitempty"` // nav registry destination id
	// Since is when the current role began, when the rule proves it
	// (health.NativeEval.Since); Condition the deciding condition's own
	// transition, qualified evidence that never dates the role; Reason the
	// rule's capped reason and message (The Line §8.1). Absent otherwise.
	Since     *time.Time `json:"since,omitempty"`
	Condition *Condition `json:"condition,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

// Condition is one condition's own transition: it has had Status since
// Since ("Ready=False since 09:00, condition time").
type Condition struct {
	Type   string    `json:"type"`
	Status string    `json:"status"`
	Since  time.Time `json:"since"`
}

// Part is one counted entity set inside a task.
type Part struct {
	ID         string         `json:"id"`
	Entity     string         `json:"entity"`
	Scope      string         `json:"scope"`
	Classifier string         `json:"classifier"` // verdict | native | evidence
	Coverage   Coverage       `json:"coverage"`
	Kinds      []KindCoverage `json:"kinds"`
	Counts     *Counts        `json:"counts,omitempty"`
	Native     *NativeCounts  `json:"native,omitempty"`
	Evidence   *int           `json:"evidence,omitempty"`
	Top        []Ref          `json:"top,omitempty"`
	Obs        uint64         `json:"obs"` // data watermark (informational)
	Ver        uint64         `json:"ver"` // part version: orders counts and rows
	AsOf       *time.Time     `json:"asOf,omitempty"`
	Next       *time.Time     `json:"next,omitempty"`
}

// ControllerIssue is a task's controller health, a separate identity that is
// never added to a part count.
type ControllerIssue struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Task is one navigable area.
type Task struct {
	ID         string           `json:"id"`
	Parts      []Part           `json:"parts"`
	Controller *ControllerIssue `json:"controller,omitempty"`
	Coverage   Coverage         `json:"coverage"`
	AsOf       *time.Time       `json:"asOf,omitempty"`
}

// Summary is one cluster's published observation.
type Summary struct {
	Cluster     string    `json:"cluster"`
	Incarnation uint64    `json:"incarnation"`
	Seq         uint64    `json:"seq"`
	EvaluatedAt time.Time `json:"evaluatedAt"`
	Connection  string    `json:"connection"`
	Namespaces  *int      `json:"namespaces,omitempty"`
	Tasks       []Task    `json:"tasks"`
	Final       bool      `json:"final,omitempty"`
}

// Rows is one part's rows with the counts from the same capture.
type Rows struct {
	Cluster     string    `json:"cluster"`
	Incarnation uint64    `json:"incarnation"`
	Task        string    `json:"task"`
	EvaluatedAt time.Time `json:"evaluatedAt"`
	Part        Part      `json:"part"`
	Rows        any       `json:"rows"`
}
