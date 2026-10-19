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
	// State is found, not found, not established or not looked up (S8a §4).
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
	Fingerprint     string       `json:"fingerprint"`
	Name            string       `json:"name"`
	Severity        string       `json:"severity,omitempty"`
	Counted         bool         `json:"counted"`
	State           string       `json:"state"`
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

type AMAlertsRowsDTO struct {
	Source    string            `json:"source"`
	Note      string            `json:"note,omitempty"`
	Match     map[string]string `json:"match,omitempty"`
	ReadAt    string            `json:"readAt,omitempty"`
	Truncated bool              `json:"truncated"`
	Alerts    []AMAlertDTO      `json:"alerts"`
}

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
	d := AMSubjectDTO{Kind: s.Kind, Namespace: s.Namespace, Name: s.Name, Container: s.Container, Basis: s.Basis}
	for _, a := range s.AlsoNames {
		d.AlsoNames = append(d.AlsoNames, alertSubject(a))
	}
	return d
}

func amAlertDTO(r healthsrc.AlertRow) AMAlertDTO {
	a := r.Alert
	d := AMAlertDTO{Fingerprint: a.Fingerprint, Name: a.Name, Severity: a.Severity, Counted: a.Counted, State: a.State,
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
func alertsRows(rows any, _ time.Time) (any, error) {
	if rows == nil {
		return AMAlertsRowsDTO{Alerts: []AMAlertDTO{}}, nil
	}
	r, ok := rows.(healthsrc.AlertRows)
	if !ok {
		return nil, errRowsShape
	}
	out := AMAlertsRowsDTO{Source: r.Source, Note: r.Note, Match: r.Match, ReadAt: rfcOrEmpty(r.ReadAt), Truncated: r.Truncated, Alerts: make([]AMAlertDTO, 0, len(r.Alerts))}
	for _, a := range r.Alerts {
		out.Alerts = append(out.Alerts, amAlertDTO(a))
	}
	return out, nil
}

func init() { rowsConverters[healthsrc.TaskAlerts+"/alerts"] = alertsRows }
