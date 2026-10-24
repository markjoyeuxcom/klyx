package evalrun

import (
	"regexp"
	"strings"

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
	grounding := map[string]bool{}
	for _, o := range facts.Predicates {
		if o.Established {
			grounding[o.Read] = true
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
		if early(fd) {
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

// early: every check a finding rests on was observed before the failure
// was reported. Context, not an error (eval design §6).
func early(fd appbridge.FindingDTO) bool {
	if len(fd.Checked) == 0 {
		return false
	}
	for _, c := range fd.Checked {
		if !c.Before {
			return false
		}
	}
	return true
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

// onlyLookalikes: every citation is to a read about a lookalike.
func (s *Scenario) onlyLookalikes(cites []string, reads map[string]appbridge.ChatRead) bool {
	if len(cites) == 0 || len(s.Lookalikes) == 0 {
		return false
	}
	for _, c := range cites {
		id, _, _ := strings.Cut(c, ".")
		r, ok := reads[id]
		if !ok || !s.isLookalike(r) {
			return false
		}
	}
	return true
}

// isLookalike: the read is about a lookalike, by the resource and name
// its references carry.
func (s *Scenario) isLookalike(r appbridge.ChatRead) bool {
	for _, ref := range r.Refs {
		for _, l := range s.Lookalikes {
			if !strings.HasSuffix("/"+ref.GVR, "/"+l.Resource) || ref.Namespace != l.Namespace {
				continue
			}
			if (l.Name != "" && ref.Name == l.Name) || (l.NamePrefix != "" && strings.HasPrefix(ref.Name, l.NamePrefix)) {
				return true
			}
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
