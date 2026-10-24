package appbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/healthsrc"
)

// AI A1 §3.5: get_alerts reads the Observer's Alertmanager snapshot for one
// object: alerts whose labels name it, name one of its current pods (listed
// by controller UID), or name its namespace. Every match is by label, never
// by UID. S8a's SubjectUID - the current object found at the labels' names -
// travels as a navigation fact, with "the alert began before this object was
// created" when S8a says so. No alert check is ever about a UID.

const (
	toolAlerts        = "get_alerts"
	chatAlertsMax     = 20
	chatAlertTextMax  = 300
	chatAlertLabelMax = 200
)

// chatAlertLabels are the labels sent; all others are dropped.
var chatAlertLabels = map[string]bool{"alertname": true, "severity": true, "namespace": true, "pod": true, "container": true,
	"job": true, "service": true, "cluster": true}

type chatAlert struct {
	Name                 string            `json:"name"`
	State                string            `json:"state"` // active | unprocessed | suppressed | other
	Severity             string            `json:"severity,omitempty"`
	StartedAt            string            `json:"startedAt,omitempty"`
	Labels               map[string]string `json:"labels,omitempty"`
	Summary              string            `json:"summary,omitempty"`
	Description          string            `json:"description,omitempty"`
	MatchedBy            string            `json:"matchedBy"`
	Subject              string            `json:"subject"` // what the labels name
	CurrentUID           string            `json:"currentUID,omitempty"`
	StartedBeforeCreated bool              `json:"startedBeforeCreated,omitempty"`
}

type chatAlertsData struct {
	Alerts []chatAlert `json:"alerts"`
	Total  int         `json:"total"`
	SeenAt string      `json:"seenAt,omitempty"`
	Note   string      `json:"note"`
}

func subjectText(s alertmanager.Subject) string {
	if s.Kind == "" {
		return "the cluster"
	}
	return s.Kind + " " + strings.TrimPrefix(s.Namespace+"/"+s.Name, "/")
}

func (t *chatTools) alerts(ctx context.Context, kind, ns, name string) ChatToolResult {
	k, err := t.resolve(ctx, kind, "")
	if err != nil {
		return t.refuse(toolAlerts, kind+" "+name, err)
	}
	if k.Namespaced && ns == "" {
		return t.refuse(toolAlerts, k.Kind+" "+name, fmt.Errorf("%s is namespaced: give a namespace", k.Kind))
	}
	if !k.Namespaced {
		ns = ""
	}
	target := k.Kind + " " + strings.TrimPrefix(ns+"/"+name, "/")
	rows, ok := t.conn.HealthRowsNow(ctx, healthsrc.TaskAlerts, "alerts")
	if !ok {
		return t.failed(toolAlerts, "observer", target, errors.New("Klyx reads no Alertmanager for this cluster, or its alerts are not available yet; this says nothing about whether alerts fire"))
	}
	ar, ok := rows.Rows.(healthsrc.AlertRows)
	if !ok {
		return t.failed(toolAlerts, "observer", target, errors.New("the alerts snapshot could not be read"))
	}
	seen, stale := alertsSeenAt(rows.Part)
	env := chatEnvelope{Tool: toolAlerts, Source: "observer", ReadAt: t.stamp(), Complete: !ar.Truncated && !stale}
	p := &projector{}
	note := "Alerts are Alertmanager's, matched to the object by label, never by UID."

	// the object's current pods, by controller UID, for a workload
	pods := map[string]bool{}
	if workloadKinds[k.GVR.Group+"/"+k.Kind] {
		w, err := t.workloadPods(ctx, k, ns, name)
		switch {
		case err != nil:
			env.Errors = append(env.Errors, "the workload's pods: "+chatScrub(readError(err), &p.redacted))
			env.Complete = false
		default:
			for _, pod := range w.Pods {
				pods[pod.Name] = true
			}
			note += fmt.Sprintf(" Pod alerts are those naming the %d pods it has now (by controller UID); a pod gone before this read is not included.", len(w.Pods))
			if w.Partial {
				note += " The pod list was capped."
				env.Complete = false
			}
		}
	}
	matches := func(s alertmanager.Subject) string {
		switch {
		case strings.EqualFold(s.Kind, k.Kind) && s.Namespace == ns && s.Name == name:
			return "the object, by label " + strings.Join(s.Basis, "/")
		case s.Kind == "Pod" && s.Namespace == ns && pods[s.Name]:
			return "one of its current pods, by label " + strings.Join(s.Basis, "/")
		case s.Kind == "Namespace" && ns != "" && s.Name == ns:
			return "its namespace, by label " + strings.Join(s.Basis, "/")
		}
		return ""
	}
	var found []healthsrc.AlertRow
	matchedBy := map[int]string{}
	for _, a := range ar.Alerts {
		m := matches(a.Subject)
		for _, also := range a.Subject.AlsoNames {
			if m != "" {
				break
			}
			m = matches(also)
		}
		if m == "" {
			continue
		}
		matchedBy[len(found)] = m
		found = append(found, a)
	}
	order := map[string]int{alertmanager.StateActive: 0, alertmanager.StateUnprocessed: 1, alertmanager.StateSuppressed: 2}
	idx := make([]int, len(found))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, b := found[idx[i]].Alert.StateClass, found[idx[j]].Alert.StateClass
		ra, oka := order[a]
		rb, okb := order[b]
		if !oka {
			ra = 3
		}
		if !okb {
			rb = 3
		}
		return ra < rb
	})
	data := chatAlertsData{Total: len(found), SeenAt: seen}
	var checks []ChatCheck
	for _, i := range idx {
		if len(data.Alerts) == chatAlertsMax {
			break
		}
		a := found[i]
		ca := chatAlert{Name: p.str(a.Alert.Name, chatAlertLabelMax), State: a.Alert.StateClass, Severity: p.str(a.Alert.Severity, chatAlertLabelMax),
			StartedAt: rfcOrEmpty(a.Alert.StartsAt), Summary: p.str(a.Alert.Summary, chatAlertTextMax), Description: p.str(a.Alert.Description, chatAlertTextMax),
			MatchedBy: matchedBy[i], Subject: p.str(subjectText(a.Subject), chatStringMax), CurrentUID: a.SubjectUID, StartedBeforeCreated: a.StartedBeforeCreated}
		for _, l := range a.Alert.Labels {
			if chatAlertLabels[l.Name] {
				if ca.Labels == nil {
					ca.Labels = map[string]string{}
				}
				ca.Labels[l.Name] = p.str(l.Value, chatAlertLabelMax)
			}
		}
		data.Alerts = append(data.Alerts, ca)
		// a check only on a live read: a stale snapshot cannot say what
		// fires now
		if stale || a.Alert.StateClass != alertmanager.StateActive {
			continue
		}
		subj := ChatCheckSubject{Kind: a.Subject.Kind, Namespace: a.Subject.Namespace, Name: a.Subject.Name, Identity: identityName}
		st := fmt.Sprintf("the current Alertmanager read holds alert %s, firing since %s, naming %s (by label)", ca.Name, ca.StartedAt, p.str(subjectText(a.Subject), chatStringMax))
		if a.StartedBeforeCreated {
			st += "; it began before the current object of that name was created"
		}
		checks = append(checks, ChatCheck{Class: checkAlert, Subject: subj, Statement: st, Observed: seen, Onset: ca.StartedAt, Coverage: "live"})
	}
	switch {
	case stale:
		note += " The alerts reads are failing: each alert was last seen at the last good read, " + seen + "; none is called resolved, and no check is made."
		env.Errors = append(env.Errors, "alerts: the snapshot is stale")
	case ar.Truncated:
		note += " Alertmanager returned more alerts than Klyx keeps: this list may be incomplete."
	}
	if data.Total > len(data.Alerts) {
		env.Complete = false
	}
	data.Note = note
	env.Total = &chatTotal{Count: data.Total, Exact: env.Complete}
	env.Data = data
	env.Checks = capChecks(checks)
	// alerts match by label: the target and each alert's subject by name
	about := []ChatCheckSubject{byName(k.Kind, ns, name)}
	for _, i := range idx {
		s := byName(found[i].Subject.Kind, found[i].Subject.Namespace, found[i].Subject.Name)
		if len(about) < chatReadAboutMax && s.Name != "" && !slices.Contains(about, s) {
			about = append(about, s)
		}
	}
	return t.finish(env, ChatRead{Tool: toolAlerts, Target: target, Outcome: "completed", Redacted: p.redacted,
		Detail: fmt.Sprintf("%d alerts", data.Total), About: about})
}
