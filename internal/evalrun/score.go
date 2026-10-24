package evalrun

import (
	"regexp"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/evalrun/record"
)

// Run statuses (eval design §4). Only completed runs are scored.
const (
	StatusNotReady      = "not-ready"
	StatusSUTNotReady   = "sut-not-ready"
	StatusDrift         = "drift"
	StatusSUTFailure    = "sut-failure"
	StatusModelFailure  = "model-failure"
	StatusReportFailure = "report-failure"
	StatusCompleted     = "completed"
)

// Statuses in the report's order.
var Statuses = []string{StatusCompleted, StatusNotReady, StatusSUTNotReady, StatusDrift, StatusSUTFailure, StatusModelFailure, StatusReportFailure}

// RunFacts is what a run observed, in the order it happened.
type RunFacts struct {
	Before      Plant
	Ready       bool
	ReadyReason string
	StartError  string // StartInvestigation refused
	AnchorRead  bool   // the first read is the anchor's, by its pinned UID
	Outcome     string // the exchange's: answered | failed | cancelled ("" when none)
	Error       string
	Report      string // the record's report state
	After       Plant
}

// Classify gives a run its one status, and why (eval design §4). A changed
// plant comes before Klyx's or the model's failures: a replaced anchor
// fails the read, and the lab caused it.
func Classify(f RunFacts) (string, string) {
	switch {
	case !f.Before.OK:
		return StatusNotReady, f.Before.Failed
	case !f.Ready:
		return StatusSUTNotReady, f.ReadyReason
	case !f.After.OK:
		return StatusDrift, "after the run: " + f.After.Failed
	}
	if ch := f.Before.Changed(f.After); len(ch) > 0 {
		return StatusDrift, "changed during the run: " + strings.Join(ch, ", ")
	}
	switch {
	case f.StartError != "":
		return StatusSUTFailure, "the investigation did not start: " + f.StartError
	case !f.AnchorRead:
		return StatusSUTFailure, "the anchor was not read: " + f.Error
	case f.Outcome != "answered":
		return StatusModelFailure, strings.TrimSpace(f.Outcome + ": " + f.Error)
	case f.Report != record.ReportAccepted:
		return StatusReportFailure, "the model's report was " + f.Report
	}
	return StatusCompleted, ""
}

// Marks are a completed run's automatic marks (eval design §6), each named
// for what it verifies.
type Marks struct {
	Evidence            []record.Outcome `json:"evidence"`
	GroundedCause       bool             `json:"groundedCause"`
	CitedLookalike      bool             `json:"citedLookalike"`
	UnsupportedCauses   int              `json:"unsupportedCauses"`
	UnsupportedFindings int              `json:"unsupportedFindings"`
	EarlyEvidence       int              `json:"earlyEvidence"`
	EmptyReport         bool             `json:"explicitEmptyReport"`
	// Control is set for a control scenario: whether it passed.
	Control *bool `json:"control,omitempty"`
	// Proxies are lexical, never accuracy: cause statements matching the
	// scenario's cause pattern, and those mentioning the lookalike.
	ProxyCause     int `json:"proxyCause"`
	ProxyLookalike int `json:"proxyLookalike"`
}

const (
	labelSuspected   = "suspected"
	labelUnsupported = "unsupported"
	typeCause        = "cause"
	windowNoFailure  = "no failure to date"
)

// Score marks a completed run from Klyx's labelled findings, the reads
// they cite and the record.
func Score(s *Scenario, ex appbridge.ChatExchangeDTO, facts record.Facts) Marks {
	m := Marks{Evidence: facts.Predicates}
	f := ex.Findings
	if f == nil {
		f = &appbridge.FindingsDTO{}
	}
	// every delivered read that established a predicate grounds a cause,
	// and every check bound to it: not only the first
	grounding := map[string]bool{}
	for _, o := range facts.Predicates {
		if o.Established {
			grounding[o.Read] = true
			for _, id := range o.Reads {
				grounding[id] = true
			}
			for _, c := range o.Checks {
				grounding[c] = true
			}
		}
	}
	reads := map[string]appbridge.ChatRead{}
	for _, r := range ex.Reads {
		reads[r.ID] = r
	}
	causeRe, lookRe := compileOr(s.Proxies.Cause), compileOr(s.Proxies.Lookalike)
	causes := 0
	for _, fd := range f.Findings {
		if fd.Label == labelUnsupported {
			m.UnsupportedFindings++
		}
		if early(fd, f.Window, ex.Reads) {
			m.EarlyEvidence++
		}
		if fd.Type != typeCause {
			continue
		}
		causes++
		if fd.Label == labelUnsupported {
			m.UnsupportedCauses++
		}
		if fd.Label == labelSuspected && s.about(fd.Subject) {
			for _, c := range fd.Cites {
				if grounding[c] {
					m.GroundedCause = true
				}
			}
		}
		if s.onlyLookalikes(fd.Cites, reads) {
			m.CitedLookalike = true
		}
		if causeRe != nil && causeRe.MatchString(fd.Statement) {
			m.ProxyCause++
		}
		if lookRe != nil && lookRe.MatchString(fd.Statement) {
			m.ProxyLookalike++
		}
	}
	m.EmptyReport = facts.Report == record.ReportAccepted && len(f.Findings) == 0
	if s.Control {
		pass := ex.Outcome == "answered" && facts.Report == record.ReportAccepted && f.Window == windowNoFailure && causes == 0
		m.Control = &pass
	}
	return m
}

// early: everything a finding cites was observed before the failure was
// reported, by A1's own rule (chat_findings.go labelFindings): a check by
// its observation time; a whole read by its historical interval, which must
// end before the failure; a live read, or a read whose time is unknown, is
// never early. Context, not an error (eval design §6).
func early(fd appbridge.FindingDTO, window string, ledger []appbridge.ChatRead) bool {
	start, ok := parseTime(window)
	if !ok || len(fd.Cites) == 0 {
		return false
	}
	reads := map[string]appbridge.ChatRead{}
	checks := map[string]string{}
	for _, r := range ledger {
		reads[r.ID] = r
		for _, c := range r.Checks {
			checks[c.ID] = c.Observed
		}
	}
	for _, cite := range fd.Cites {
		if obs, ok := checks[cite]; ok {
			if t, ok := parseTime(obs); !ok || !t.Before(start) {
				return false
			}
			continue
		}
		r, ok := reads[cite]
		if !ok || r.ObservedUnknown || r.ObservedTo == "" {
			return false
		}
		if t, ok := parseTime(r.ObservedTo); !ok || !t.Before(start) {
			return false
		}
	}
	return true
}

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// about: the finding's subject is the anchor or one of the scenario's
// subjects.
func (s *Scenario) about(sub appbridge.ChatCheckSubject) bool {
	if s.Anchor.Names(sub.Kind, sub.Namespace, sub.Name) {
		return true
	}
	for _, r := range s.Subjects {
		if r.Names(sub.Kind, sub.Namespace, sub.Name) {
			return true
		}
	}
	return false
}

// onlyLookalikes: everything every citation is about is a lookalike. A
// check is about its own subject; a whole read is about all its references
// and all its checks' subjects, so a mixed read is not a lookalike's. A
// citation about nothing Klyx can name is not a lookalike's either.
func (s *Scenario) onlyLookalikes(cites []string, reads map[string]appbridge.ChatRead) bool {
	if len(cites) == 0 || len(s.Lookalikes) == 0 {
		return false
	}
	for _, c := range cites {
		subjects := citedSubjects(c, reads)
		if len(subjects) == 0 {
			return false
		}
		for _, sub := range subjects {
			if !s.lookalike(sub) {
				return false
			}
		}
	}
	return true
}

// citedSubject is what a citation names: a reference by group, resource,
// namespace and name, or a check's subject by kind, namespace and name.
type citedSubject struct {
	group, resource, kind, ns, name string
	byRef                           bool
}

func citedSubjects(cite string, reads map[string]appbridge.ChatRead) []citedSubject {
	id, _, isCheck := strings.Cut(cite, ".")
	r, ok := reads[id]
	if !ok {
		return nil
	}
	var out []citedSubject
	for _, c := range r.Checks {
		if (isCheck && c.ID == cite) || !isCheck {
			out = append(out, citedSubject{kind: c.Subject.Kind, ns: c.Subject.Namespace, name: c.Subject.Name})
		}
	}
	if isCheck {
		return out
	}
	for _, ref := range r.Refs {
		parts := strings.Split(ref.GVR, "/")
		if len(parts) < 2 {
			continue
		}
		out = append(out, citedSubject{group: strings.Join(parts[:len(parts)-2], "/"), resource: parts[len(parts)-1], ns: ref.Namespace, name: ref.Name, byRef: true})
	}
	return out
}

// lookalike: the subject is one of the scenario's lookalikes, by its full
// identity: a reference's group and resource, a check's kind.
func (s *Scenario) lookalike(sub citedSubject) bool {
	for _, l := range s.Lookalikes {
		if sub.ns != l.Namespace {
			continue
		}
		if sub.byRef && (sub.group != l.Group || sub.resource != l.Resource) {
			continue
		}
		if !sub.byRef && !strings.EqualFold(sub.kind, l.Kind) {
			continue
		}
		if (l.Name != "" && sub.name == l.Name) || (l.NamePrefix != "" && strings.HasPrefix(sub.name, l.NamePrefix)) {
			return true
		}
	}
	return false
}

func compileOr(p string) *regexp.Regexp {
	if p == "" {
		return nil
	}
	return regexp.MustCompile(p)
}
