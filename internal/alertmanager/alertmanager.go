// Package alertmanager reads Alertmanager's v2 API for coverage S8a: the
// flat alert list and the aggregation groups parsed strictly, one Reduce that
// decides every alert's state, the label correlation to Kubernetes objects,
// the counting class, and the display values that leave the package. Pure:
// no transport, no clock.
package alertmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Alert is one alert of the flat list (GET /api/v2/alerts), its values raw:
// they serve match, deduplication, Reduce and correlation, and nothing is
// shown from them without Display.
type Alert struct {
	Fingerprint  string
	Labels       map[string]string
	Annotations  map[string]string
	StartsAt     time.Time
	UpdatedAt    time.Time
	EndsAt       time.Time
	State        string // as written: active, suppressed, unprocessed, or another value
	SilencedBy   []string
	InhibitedBy  []string
	Receivers    []string // one per matching route: a multiset
	GeneratorURL string
}

// Group is one aggregation group (GET /api/v2/alerts/groups): its receiver,
// its labels, and its alerts with *this group's* status.
type Group struct {
	Receiver string
	Labels   map[string]string
	Alerts   []GroupAlert
}

// GroupAlert is an alert's appearance in one group.
type GroupAlert struct {
	Fingerprint string
	State       string
	SilencedBy  []string
	InhibitedBy []string
	MutedBy     []string
}

// Status is GET /api/v2/status, as Settings' Test reports it.
type Status struct {
	Version       string
	ClusterStatus string // ready, settling or disabled
	Peers         int
}

type wireStatus struct {
	State       *string  `json:"state"`
	SilencedBy  []string `json:"silencedBy"`
	InhibitedBy []string `json:"inhibitedBy"`
	MutedBy     []string `json:"mutedBy"`
}

type wireAlert struct {
	Fingerprint *string            `json:"fingerprint"`
	Labels      *map[string]string `json:"labels"`
	Annotations map[string]string  `json:"annotations"`
	StartsAt    string             `json:"startsAt"`
	UpdatedAt   string             `json:"updatedAt"`
	EndsAt      string             `json:"endsAt"`
	Status      *wireStatus        `json:"status"`
	Receivers   []struct {
		Name string `json:"name"`
	} `json:"receivers"`
	GeneratorURL string `json:"generatorURL"`
}

// ErrShape: the response is not the shape Alertmanager's v2 API returns. The
// read fails whole, never a partial list (S8a §3).
var ErrShape = errors.New("not an Alertmanager v2 response")

func shape(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrShape, fmt.Sprintf(format, a...))
}

// checkStatus validates a status object: a state is required.
func checkStatus(s *wireStatus, i int) (string, error) {
	if s == nil || s.State == nil || *s.State == "" {
		return "", shape("alert %d has no status.state", i)
	}
	return *s.State, nil
}

func parseTime(v, field string, i int) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, shape("alert %d has an unreadable %s", i, field)
	}
	return t, nil
}

// ParseAlerts parses the flat list. Every alert needs a fingerprint, labels
// and a status state; a time that does not parse fails the read.
func ParseAlerts(body []byte) ([]Alert, error) {
	var wire []wireAlert
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, shape("the alert list does not decode")
	}
	out := make([]Alert, 0, len(wire))
	for i, w := range wire {
		if w.Fingerprint == nil || *w.Fingerprint == "" {
			return nil, shape("alert %d has no fingerprint", i)
		}
		if w.Labels == nil {
			return nil, shape("alert %d has no labels", i)
		}
		state, err := checkStatus(w.Status, i)
		if err != nil {
			return nil, err
		}
		a := Alert{Fingerprint: *w.Fingerprint, Labels: *w.Labels, Annotations: w.Annotations, State: state,
			SilencedBy: w.Status.SilencedBy, InhibitedBy: w.Status.InhibitedBy, GeneratorURL: w.GeneratorURL}
		if a.StartsAt, err = parseTime(w.StartsAt, "startsAt", i); err != nil {
			return nil, err
		}
		if a.UpdatedAt, err = parseTime(w.UpdatedAt, "updatedAt", i); err != nil {
			return nil, err
		}
		if a.EndsAt, err = parseTime(w.EndsAt, "endsAt", i); err != nil {
			return nil, err
		}
		for _, r := range w.Receivers {
			a.Receivers = append(a.Receivers, r.Name)
		}
		out = append(out, a)
	}
	return out, nil
}

// ParseGroups parses the aggregation groups. Every group needs a receiver;
// every appearance a fingerprint and a status state.
func ParseGroups(body []byte) ([]Group, error) {
	var wire []struct {
		Labels   map[string]string `json:"labels"`
		Receiver *struct {
			Name string `json:"name"`
		} `json:"receiver"`
		Alerts []wireAlert `json:"alerts"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, shape("the alert groups do not decode")
	}
	out := make([]Group, 0, len(wire))
	for gi, w := range wire {
		if w.Receiver == nil || w.Receiver.Name == "" {
			return nil, shape("group %d has no receiver", gi)
		}
		g := Group{Receiver: w.Receiver.Name, Labels: w.Labels}
		for i, a := range w.Alerts {
			if a.Fingerprint == nil || *a.Fingerprint == "" {
				return nil, shape("group %d alert %d has no fingerprint", gi, i)
			}
			state, err := checkStatus(a.Status, i)
			if err != nil {
				return nil, err
			}
			g.Alerts = append(g.Alerts, GroupAlert{Fingerprint: *a.Fingerprint, State: state,
				SilencedBy: a.Status.SilencedBy, InhibitedBy: a.Status.InhibitedBy, MutedBy: a.Status.MutedBy})
		}
		out = append(out, g)
	}
	return out, nil
}

// ParseStatus parses GET /api/v2/status.
func ParseStatus(body []byte) (Status, error) {
	var w struct {
		VersionInfo *struct {
			Version string `json:"version"`
		} `json:"versionInfo"`
		Cluster *struct {
			Status string `json:"status"`
			Peers  []any  `json:"peers"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(body, &w); err != nil || w.VersionInfo == nil || w.Cluster == nil {
		return Status{}, shape("the status does not decode")
	}
	return Status{Version: w.VersionInfo.Version, ClusterStatus: w.Cluster.Status, Peers: len(w.Cluster.Peers)}, nil
}

// FilterParams are match as Alertmanager filter matchers, one per label,
// sorted, each value quoted with its quotes and backslashes escaped.
func FilterParams(match map[string]string) []string {
	names := make([]string, 0, len(match))
	for k := range match {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(match[k])
		out = append(out, k+`="`+v+`"`)
	}
	return out
}

// Matches reports whether the alert carries every match label: the check
// Klyx repeats on what the endpoint returns, whatever its filter did.
func Matches(labels, match map[string]string) bool {
	for k, v := range match {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// Severity is an alert's counting class (S8a §6).
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityOther    Severity = "other" // info, none, any other value, or no severity
)

// SeverityOf reads the severity label, case-insensitively.
func SeverityOf(labels map[string]string) Severity {
	switch strings.ToLower(labels["severity"]) {
	case "critical":
		return SeverityCritical
	case "warning":
		return SeverityWarning
	}
	return SeverityOther
}
