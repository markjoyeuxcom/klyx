// Package record is the AI eval's delivered-evidence record (eval design
// §5): what one investigation actually sent to the model, evaluated against
// the scenario's predicates as each result is produced, and kept only as
// IDs, identities and flags. It holds no tool body, message, log line or
// hidden value. Chat calls into it only in the eval build; nothing here
// changes what an investigation reads, sends or labels.
package record

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Predicate kinds (eval design §5).
const (
	KindEvent     = "event"
	KindCondition = "condition"
	KindLog       = "log"
	KindDiff      = "diff"
)

// Reasons a predicate was not established (eval design §5's table).
const (
	NotRequested = "not-requested"
	NotPlanned   = "not-planned"
	ReadFailed   = "read-failed"
	NotSent      = "not-sent"
	TurnEnded    = "turn-ended"
	Capped       = "capped"
	NoMatch      = "no-match"
	// DeliveryUncertain: the request carrying the result was dispatched,
	// and the provider's call failed without a response that says it was
	// received (a transport error). Not assigned.
	DeliveryUncertain = "delivery-uncertain"
)

// MaxGrounding bounds the establishing reads and checks kept per predicate.
const MaxGrounding = 8

// Report states: an accepted report (tools: a report_findings call Klyx
// accepted; snapshot: a findings block that parsed), and the two ways one
// is missing (eval design §4).
const (
	ReportAccepted = "accepted"
	ReportRejected = "rejected"  // tools: every call was refused
	ReportMissing  = "missing"   // no call, or no block
	ReportMalform  = "malformed" // snapshot: a block that did not parse
)

// MaxReads is the record's bound per run (eval design §5).
const MaxReads = 64

// Subject is an object the scenario names, by identity.
type Subject struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

// ReadSpec is one read that can deliver a predicate: a tool and the
// arguments that select its subject. Only the named arguments are compared;
// a kind compares case-insensitively.
type ReadSpec struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args,omitempty"`
}

// Predicate is one planted fact the eval looks for in delivered evidence.
type Predicate struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"`
	Subject Subject    `json:"subject"`
	Reads   []ReadSpec `json:"reads"`
	// Pattern matches an event's or condition's message (event,
	// condition) or a changed path (diff).
	Pattern string `json:"pattern,omitempty"`
	// Type, Status and Reason select a condition.
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Pods are the plant's recorded pod UIDs a log may come from.
	Pods []string `json:"pods,omitempty"`
}

// Outcome is a predicate's result for the run.
type Outcome struct {
	Predicate   string `json:"predicate"`
	Established bool   `json:"established"`
	Read        string `json:"read,omitempty"` // the first read that established it
	// Reads are every delivered read that established it, Checks the
	// checks bound to it in them: a citation of any of them grounds a cause
	Reads    []string `json:"reads,omitempty"`
	Checks   []string `json:"checks,omitempty"`
	Reason   string   `json:"reason,omitempty"` // why it was not established
	Previous bool     `json:"previous,omitempty"`
	Capped   bool     `json:"capped,omitempty"`
	// Unsent: a result holding the planted evidence was acquired, and no
	// request carrying it was sent
	Unsent bool `json:"matchedNotSent,omitempty"`
}

// Facts is the record's result for one run.
type Facts struct {
	Mode       string    `json:"mode"`
	Report     string    `json:"report"`
	Predicates []Outcome `json:"predicates"`
	Reads      int       `json:"reads"`
	Overflow   bool      `json:"overflow,omitempty"`
}

type compiled struct {
	Predicate
	re *regexp.Regexp
}

type read struct {
	id        string
	tool      string
	failed    bool
	acquired  bool // an envelope was produced
	truncated bool
	delivered bool
	uncertain bool // dispatched; the call failed without a response
	refused   bool // carried by a request refused locally
	omitted   bool // left out by the snapshot fill
	relevant  map[int]bool
	matched   map[int]match
}

type match struct {
	checks   []string
	previous bool
}

// Recorder records one conversation. Events about another conversation are
// ignored, so a cleared investigation never reaches a later run.
type Recorder struct {
	mu       sync.Mutex
	conv     string
	preds    []compiled
	reads    []*read
	byID     map[string]*read
	mode     string
	accepted bool
	rejected bool
	snapshot string // a snapshot block's state, when one was answered
	overflow bool
}

// New is a recorder for conversation conv and the scenario's predicates.
func New(conv string, preds []Predicate) (*Recorder, error) {
	r := &Recorder{conv: conv, byID: map[string]*read{}, mode: "tools"}
	for _, p := range preds {
		c := compiled{Predicate: p}
		if p.Pattern != "" {
			re, err := regexp.Compile(p.Pattern)
			if err != nil {
				return nil, fmt.Errorf("predicate %s: %w", p.ID, err)
			}
			c.re = re
		}
		switch p.Kind {
		case KindEvent, KindCondition, KindDiff:
			if c.re == nil {
				return nil, fmt.Errorf("predicate %s: a %s predicate needs a pattern", p.ID, p.Kind)
			}
		case KindLog:
			if len(p.Pods) == 0 {
				return nil, fmt.Errorf("predicate %s: a log predicate needs the plant's pods", p.ID)
			}
		default:
			return nil, fmt.Errorf("predicate %s: unknown kind %q", p.ID, p.Kind)
		}
		r.preds = append(r.preds, c)
	}
	return r, nil
}

// Mode records the conversation's mode: tools or snapshot. A tools turn
// that falls back to snapshot records snapshot.
func (r *Recorder) Mode(conv, mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if conv == r.conv {
		r.mode = mode
	}
}

// Read records one read as Klyx produced it: the tool, the arguments it was
// asked or planned with, its read ID, whether it failed, and its final
// envelope (nil when there is none). The envelope is evaluated now and then
// dropped: only match flags and check IDs are kept.
func (r *Recorder) Read(conv, tool string, args json.RawMessage, readID string, failed bool, envelope []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if conv != r.conv {
		return
	}
	if len(r.reads) >= MaxReads {
		r.overflow = true
		return
	}
	rd := &read{id: readID, tool: tool, failed: failed, relevant: map[int]bool{}, matched: map[int]match{}}
	var a map[string]any
	_ = json.Unmarshal(args, &a)
	for i, p := range r.preds {
		for _, s := range p.Reads {
			if s.Tool == tool && argsMatch(s.Args, a) {
				rd.relevant[i] = true
			}
		}
	}
	if !failed && len(envelope) > 0 {
		var env envelopeView
		if json.Unmarshal(envelope, &env) == nil {
			rd.acquired = true
			rd.truncated = env.Truncated
			for i, p := range r.preds {
				if m, ok := evaluate(p, &env); ok {
					rd.matched[i] = m
					rd.relevant[i] = true
				}
			}
		}
	}
	r.reads = append(r.reads, rd)
	if readID != "" {
		r.byID[readID] = rd
	}
}

// Sent records a request the provider received, with the read IDs it
// carried: it answered, or replied with an HTTP status.
func (r *Recorder) Sent(conv string, readIDs []string) {
	r.mark(conv, readIDs, func(rd *read) { rd.delivered = true })
}

// SentUncertain records a request dispatched to the provider whose call
// failed without a response: whether it was received is not established.
func (r *Recorder) SentUncertain(conv string, readIDs []string) {
	r.mark(conv, readIDs, func(rd *read) { rd.uncertain = true })
}

// Refused records a request Klyx refused locally (for its size): nothing
// was sent.
func (r *Recorder) Refused(conv string, readIDs []string) {
	r.mark(conv, readIDs, func(rd *read) { rd.refused = true })
}

// Omitted records the reads a snapshot's fill left out.
func (r *Recorder) Omitted(conv string, readIDs []string) {
	r.mark(conv, readIDs, func(rd *read) { rd.omitted = true })
}

func (r *Recorder) mark(conv string, ids []string, f func(*read)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if conv != r.conv {
		return
	}
	for _, id := range ids {
		if rd, ok := r.byID[id]; ok {
			f(rd)
		}
	}
}

// ToolsReport records one report_findings call, accepted or refused.
func (r *Recorder) ToolsReport(conv string, accepted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if conv != r.conv {
		return
	}
	if accepted {
		r.accepted = true
	} else {
		r.rejected = true
	}
}

// SnapshotReport records a snapshot answer's findings block: found, and
// parsed or not.
func (r *Recorder) SnapshotReport(conv string, found, parsed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if conv != r.conv {
		return
	}
	switch {
	case !found:
		r.snapshot = ReportMissing
	case !parsed:
		r.snapshot = ReportMalform
	default:
		r.snapshot = ReportAccepted
	}
}

// Facts is the run's record: the report's state and each predicate's
// outcome. Deterministic: a delivered match establishes a predicate
// whatever came before it, and otherwise the furthest any relevant read
// got decides the reason.
func (r *Recorder) Facts() Facts {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := Facts{Mode: r.mode, Reads: len(r.reads), Overflow: r.overflow}
	switch {
	case r.mode == "snapshot" && r.snapshot != "":
		f.Report = r.snapshot
	case r.accepted:
		f.Report = ReportAccepted
	case r.rejected:
		f.Report = ReportRejected
	default:
		f.Report = ReportMissing
	}
	reads := r.ordered()
	for i, p := range r.preds {
		f.Predicates = append(f.Predicates, reduce(reads, i, p, r.mode))
	}
	return f
}

// ordered is the reads by read ID (r1, r2, ...): a batch's reads run at
// once and record in any order, so the IDs Klyx assigned decide "first".
// A read without an ID keeps its place after the numbered ones.
func (r *Recorder) ordered() []*read {
	out := append([]*read(nil), r.reads...)
	num := func(rd *read) int {
		if n, err := strconv.Atoi(strings.TrimPrefix(rd.id, "r")); err == nil && strings.HasPrefix(rd.id, "r") {
			return n
		}
		return int(^uint(0) >> 1)
	}
	sort.SliceStable(out, func(i, j int) bool { return num(out[i]) < num(out[j]) })
	return out
}

// reason ranks: the furthest a relevant read's delivery got. Delivery is
// decided on its own, never from whether the result matched: no-match and
// capped are for results delivered, the others for results never received.
var reasonRank = map[string]int{NotSent: 6, DeliveryUncertain: 5, TurnEnded: 4, Capped: 3, NoMatch: 2, ReadFailed: 1}

// reduce is one predicate's outcome over the ordered reads.
func reduce(reads []*read, i int, p compiled, mode string) Outcome {
	o := Outcome{Predicate: p.ID}
	for _, rd := range reads {
		m, ok := rd.matched[i]
		if !ok || !rd.delivered {
			continue
		}
		if !o.Established {
			o.Established, o.Read, o.Previous, o.Capped = true, rd.id, m.previous, rd.truncated
		}
		if len(o.Reads) < MaxGrounding {
			o.Reads = append(o.Reads, rd.id)
		}
		for _, c := range m.checks {
			if len(o.Checks) < 2*MaxGrounding {
				o.Checks = append(o.Checks, c)
			}
		}
	}
	if o.Established {
		return o
	}
	best := ""
	for _, rd := range reads {
		if !rd.relevant[i] {
			continue
		}
		var why string
		_, matched := rd.matched[i]
		switch {
		case rd.failed || !rd.acquired:
			why = ReadFailed
		case rd.delivered && rd.truncated:
			why = Capped
		case rd.delivered:
			why = NoMatch
		case rd.uncertain:
			why = DeliveryUncertain
		case rd.refused || rd.omitted:
			why = NotSent
		default:
			why = TurnEnded
		}
		if matched && !rd.delivered {
			o.Unsent = true
		}
		if rd.truncated {
			o.Capped = true
		}
		if reasonRank[why] > reasonRank[best] {
			best = why
		}
	}
	if best == "" {
		best = NotRequested
		if mode == "snapshot" {
			best = NotPlanned
		}
	}
	o.Reason = best
	return o
}

// argsMatch compares the spec's arguments with a call's.
func argsMatch(want map[string]string, got map[string]any) bool {
	for k, v := range want {
		g, ok := got[k]
		if !ok {
			return false
		}
		switch gv := g.(type) {
		case string:
			if k == "kind" {
				if !strings.EqualFold(gv, v) {
					return false
				}
			} else if gv != v {
				return false
			}
		case bool:
			if fmt.Sprint(gv) != v {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// envelopeView is the part of a chat envelope the predicates read. The
// shapes are chat's own (chat_tools.go): get_resource's object and warning
// events, get_events' list, get_pod_logs' log data and git_changes' objects.
type envelopeView struct {
	Tool      string          `json:"tool"`
	ReadID    string          `json:"readId"`
	Truncated bool            `json:"truncated"`
	Checks    []checkView     `json:"checks"`
	Data      json.RawMessage `json:"data"`
}

type checkView struct {
	ID        string  `json:"id"`
	Subject   Subject `json:"subject"`
	Statement string  `json:"statement"`
}

type eventView struct {
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	ObjectUID string `json:"objectUID"`
}

type conditionView struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type objectView struct {
	UID        string          `json:"uid"`
	Conditions []conditionView `json:"conditions"`
}

type getView struct {
	Object objectView  `json:"object"`
	Events []eventView `json:"warningEvents"`
}

type logView struct {
	PodUID   string `json:"podUID"`
	Instance string `json:"instance"`
	Lines    int    `json:"lines"`
	Text     string `json:"text"`
}

type changeView struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Visible   []struct {
		Path string `json:"path"`
	} `json:"visible"`
	Hidden []struct {
		Path string `json:"path"`
	} `json:"hidden"`
}

type changesView struct {
	Objects []changeView `json:"objects"`
}

// evaluate reports whether a delivered envelope establishes p, and the
// checks bound to it: a check is bound only when its own statement
// satisfies p, about p's subject.
func evaluate(p compiled, env *envelopeView) (match, bool) {
	var ok bool
	var m match
	switch p.Kind {
	case KindEvent:
		var evs []eventView
		if json.Unmarshal(env.Data, &evs) != nil {
			var g getView
			if json.Unmarshal(env.Data, &g) == nil {
				evs = g.Events
			}
		}
		for _, e := range evs {
			if p.Subject.UID != "" && e.ObjectUID == p.Subject.UID && p.re.MatchString(e.Message) {
				ok = true
			}
		}
	case KindCondition:
		var g getView
		if json.Unmarshal(env.Data, &g) == nil && g.Object.UID != "" && g.Object.UID == p.Subject.UID {
			for _, c := range g.Object.Conditions {
				if c.Type == p.Type && c.Status == p.Status && c.Reason == p.Reason && p.re.MatchString(c.Message) {
					ok = true
				}
			}
		}
	case KindLog:
		var l logView
		if json.Unmarshal(env.Data, &l) == nil && l.Instance == "previous" && l.Lines > 0 && strings.TrimSpace(l.Text) != "" {
			for _, uid := range p.Pods {
				if l.PodUID == uid {
					ok, m.previous = true, true
				}
			}
		}
	case KindDiff:
		var c changesView
		if json.Unmarshal(env.Data, &c) == nil {
			for _, o := range c.Objects {
				if !strings.EqualFold(o.Kind, p.Subject.Kind) || o.Namespace != p.Subject.Namespace || o.Name != p.Subject.Name {
					continue
				}
				for _, v := range o.Visible {
					ok = ok || p.re.MatchString(v.Path)
				}
				for _, h := range o.Hidden {
					ok = ok || p.re.MatchString(h.Path)
				}
			}
		}
	}
	if !ok {
		return match{}, false
	}
	if p.re != nil {
		for _, c := range env.Checks {
			if sameSubject(c.Subject, p.Subject) && p.re.MatchString(c.Statement) {
				m.checks = append(m.checks, c.ID)
			}
		}
		sort.Strings(m.checks)
	}
	return m, true
}

func sameSubject(a, b Subject) bool {
	return strings.EqualFold(a.Kind, b.Kind) && a.Namespace == b.Namespace && a.Name == b.Name
}
