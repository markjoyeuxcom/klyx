package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/healthsrc"
)

// The Alerts lens's rows (coverage S8a §7): display values only - every
// string was made by alertmanager.Display with the reading transport's
// redactor - with each alert's subject as Klyx can state it.

type AMPairDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type AMSubjectDTO struct {
	Kind      string         `json:"kind,omitempty"` // "" for a cluster alert
	GVR       string         `json:"gvr,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	Name      string         `json:"name,omitempty"`
	Container string         `json:"container,omitempty"`
	Basis     []string       `json:"basis,omitempty"` // the label names matched
	AlsoNames []AMSubjectDTO `json:"alsoNames,omitempty"`
	// Exact: namespace and name are shown as written (not redacted or
	// clipped); only then may the lens compare them with an object's name.
	Exact bool `json:"exact"`
	// State is found, not found, not established, not looked up or last
	// observed (S8a §4, revision 1).
	State                string `json:"state,omitempty"`
	UID                  string `json:"uid,omitempty"`
	CreatedAt            string `json:"createdAt,omitempty"`
	StartedBeforeCreated bool   `json:"startedBeforeCreated,omitempty"`
}

type AMMuteDTO struct {
	Kind      string   `json:"kind"` // none | partial | incomplete
	MutedFor  []string `json:"mutedFor,omitempty"`
	ActiveFor []string `json:"activeFor,omitempty"`
	Expected  []string `json:"expected,omitempty"`
	Shown     []string `json:"shown,omitempty"`
}

type AMAlertDTO struct {
	// Key is the alert's opaque key (source identity and fingerprint,
	// hashed): the lens selects and reveals by it.
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
	Severity    string `json:"severity,omitempty"`
	// SeverityClass and StateClass are Klyx's classes from the raw alert;
	// Severity and State are the display values as written.
	SeverityClass   string       `json:"severityClass"` // critical | warning | other
	Counted         bool         `json:"counted"`
	State           string       `json:"state"`
	StateClass      string       `json:"stateClass"` // active | suppressed | unprocessed | other
	Summary         string       `json:"summary,omitempty"`
	Description     string       `json:"description,omitempty"`
	RunbookURL      string       `json:"runbookURL,omitempty"`
	GeneratorURL    string       `json:"generatorURL,omitempty"`
	Labels          []AMPairDTO  `json:"labels"`
	LabelsMore      int          `json:"labelsMore,omitempty"`
	Annotations     []AMPairDTO  `json:"annotations"`
	AnnotationsMore int          `json:"annotationsMore,omitempty"`
	Receivers       []string     `json:"receivers,omitempty"`
	SilencedBy      []string     `json:"silencedBy,omitempty"`
	InhibitedBy     []string     `json:"inhibitedBy,omitempty"`
	MutedBy         []string     `json:"mutedBy,omitempty"`
	Mute            *AMMuteDTO   `json:"mute,omitempty"`
	Disagreement    string       `json:"disagreement,omitempty"`
	Subject         AMSubjectDTO `json:"subject"`
	StartsAt        string       `json:"startsAt,omitempty"`
	UpdatedAt       string       `json:"updatedAt,omitempty"`
	EndsAt          string       `json:"endsAt,omitempty"` // the end as last reported, never a state
}

// rowUID keys the alerts rows' deltas: the alert's opaque key.
func (a AMAlertDTO) rowUID() string { return a.Key }

func rfcOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func alertPairs(ps []alertmanager.Pair) []AMPairDTO {
	out := make([]AMPairDTO, 0, len(ps))
	for _, p := range ps {
		out = append(out, AMPairDTO{Name: p.Name, Value: p.Value})
	}
	return out
}

func alertSubject(s alertmanager.DisplaySubject) AMSubjectDTO {
	d := AMSubjectDTO{Kind: s.Kind, Namespace: s.Namespace, Name: s.Name, Container: s.Container, Basis: s.Basis, Exact: s.Exact}
	for _, a := range s.AlsoNames {
		d.AlsoNames = append(d.AlsoNames, alertSubject(a))
	}
	return d
}

func amAlertDTO(r healthsrc.AlertRow) AMAlertDTO {
	a := r.Alert
	d := AMAlertDTO{Key: r.Key, Fingerprint: a.Fingerprint, Name: a.Name, Severity: a.Severity, SeverityClass: string(a.SeverityClass),
		Counted: a.Counted, State: a.State, StateClass: a.StateClass,
		Summary: a.Summary, Description: a.Description, RunbookURL: a.RunbookURL, GeneratorURL: a.GeneratorURL,
		Labels: alertPairs(a.Labels), LabelsMore: a.LabelsMore, Annotations: alertPairs(a.Annotations), AnnotationsMore: a.AnnotationsMore,
		Receivers: a.Receivers, SilencedBy: a.SilencedBy, InhibitedBy: a.InhibitedBy, MutedBy: a.MutedBy, Disagreement: a.Disagreement,
		Subject: alertSubject(a.Subject), StartsAt: rfcOrEmpty(a.StartsAt), UpdatedAt: rfcOrEmpty(a.UpdatedAt), EndsAt: rfcOrEmpty(a.EndsAt)}
	if a.Mute.Kind != "" {
		d.Mute = &AMMuteDTO{Kind: string(a.Mute.Kind), MutedFor: a.Mute.MutedFor, ActiveFor: a.Mute.ActiveFor, Expected: a.Mute.Expected, Shown: a.Mute.Shown}
	}
	d.Subject.GVR, d.Subject.State, d.Subject.UID, d.Subject.StartedBeforeCreated = r.SubjectGVR, r.SubjectState, r.SubjectUID, r.StartedBeforeCreated
	if r.SubjectCreated != nil {
		d.Subject.CreatedAt = rfcOrEmpty(*r.SubjectCreated)
	}
	return d
}

// alertsRows converts the alerts part's rows; no read yet is an empty set.
// The read's source line travels as the rows' facts (AlertRows.RowFacts).
func alertsRows(rows any, _ time.Time) (any, error) {
	if rows == nil {
		return []AMAlertDTO{}, nil
	}
	r, ok := rows.(healthsrc.AlertRows)
	if !ok {
		return nil, errRowsShape
	}
	out := make([]AMAlertDTO, 0, len(r.Alerts))
	for _, a := range r.Alerts {
		out = append(out, amAlertDTO(a))
	}
	return out, nil
}

func init() { rowsConverters[healthsrc.TaskAlerts+"/alerts"] = alertsRows }
