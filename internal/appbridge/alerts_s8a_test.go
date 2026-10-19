package appbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

var tAlert = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func alertRow(name, state string, counted bool, uid, subjectState string) healthsrc.AlertRow {
	return healthsrc.AlertRow{Alert: alertmanager.DisplayAlert{Fingerprint: "fp-" + name, Name: name, Severity: "critical", State: state, Counted: counted,
		Summary: name + " fired; token=hunter2-secret", StartsAt: tAlert.Add(-time.Hour), UpdatedAt: tAlert, EndsAt: tAlert.Add(time.Hour),
		Labels:  []alertmanager.Pair{{Name: "alertname", Value: name}},
		Mute:    alertmanager.DisplayMute{Kind: alertmanager.MutePartial, MutedFor: []string{"quiet"}, ActiveFor: []string{"lab"}},
		Subject: alertmanager.DisplaySubject{Kind: "Pod", Namespace: "ns", Name: "p", Basis: []string{"namespace", "pod"}}},
		SubjectGVR: "v1/pods", SubjectState: subjectState, SubjectUID: uid}
}

// Coverage S8a §7: the lens rows carry the display values, the subject as
// Klyx can state it, and absolute times; no read yet is an empty set.
func TestAlertsRowsConverter(t *testing.T) {
	created := tAlert.Add(-10 * time.Minute)
	r := alertRow("PodCrash", "active", true, "p-uid", healthsrc.SubjectFound)
	r.SubjectCreated, r.StartedBeforeCreated = &created, true
	dto, ok := toRowsDTO(observe.Rows{Task: healthsrc.TaskAlerts, Part: observe.Part{ID: "alerts"}, Rows: healthsrc.AlertRows{Source: "https://am.example:443", ReadAt: tAlert, Alerts: []healthsrc.AlertRow{r}}})
	if !ok {
		t.Fatal("no converter")
	}
	out := dto.Rows.([]AMAlertDTO)
	a := out[0]
	if dto.Facts["alertsSource"] != "https://am.example:443" || dto.Facts["alertsReadAt"] != "2026-10-19T12:00:00Z" || a.Name != "PodCrash" || !a.Counted ||
		a.Subject.State != "found" || a.Subject.UID != "p-uid" || !a.Subject.StartedBeforeCreated || a.Subject.CreatedAt != "2026-10-19T11:50:00Z" ||
		a.Mute == nil || a.Mute.Kind != "partial" || a.StartsAt != "2026-10-19T11:00:00Z" || a.EndsAt != "2026-10-19T13:00:00Z" || a.Subject.GVR != "v1/pods" {
		t.Fatalf("dto: %+v", out)
	}
	empty, ok := toRowsDTO(observe.Rows{Task: healthsrc.TaskAlerts, Part: observe.Part{ID: "alerts"}})
	if !ok || empty.Rows.([]AMAlertDTO) == nil {
		t.Fatalf("no read: %+v", empty)
	}
}

// Explain for an object carries its active alerts matched to it by UID,
// scrubbed, with the qualification that they are label matches; another
// object's alerts and suppressed ones stay out (S8a §7).
func TestExplainCarriesTheObjectsActiveAlerts(t *testing.T) {
	rows := healthsrc.AlertRows{ReadAt: tAlert, Alerts: []healthsrc.AlertRow{
		alertRow("Mine", "active", true, "p-uid", healthsrc.SubjectFound),
		alertRow("Silenced", "suppressed", false, "p-uid", healthsrc.SubjectFound),
		alertRow("Other", "active", true, "other-uid", healthsrc.SubjectFound),
	}}
	conn := &fakeExplainConn{
		details: map[string]crd.InstanceDetail{"/pods/ns/p": {Kind: "Pod", Namespace: "ns", Name: "p", UID: "p-uid"}},
		rows:    map[string]observe.Rows{healthsrc.TaskAlerts + "/alerts": {Task: healthsrc.TaskAlerts, Part: observe.Part{ID: "alerts"}, Rows: rows}},
	}
	cl := &fakeCompleter{reply: "ok"}
	s := newAI(cl, "local", false, false, conn)
	r := s.Explain("c", ExplainTargetDTO{Kind: "object", Group: "", Version: "v1", Plural: "pods", Namespace: "ns", Name: "p", UID: "p-uid"})
	if !r.OK {
		t.Fatalf("explain: %+v", r)
	}
	if !strings.Contains(cl.gotUser, `"name":"Mine"`) || strings.Contains(cl.gotUser, "Silenced") || strings.Contains(cl.gotUser, `"name":"Other"`) {
		t.Fatalf("alerts in the prompt: %s", cl.gotUser)
	}
	if strings.Contains(cl.gotUser, "hunter2-secret") || !strings.Contains(cl.gotUser, "matched to this object by label, not by UID") {
		t.Fatalf("scrubbed and qualified: %s", cl.gotUser)
	}
}

// The chat attention tool reports alerts apart from the objects: never a
// row, never in the total (S8a §6).
func TestChatAttentionCountsAlertsApart(t *testing.T) {
	f := newFakeChat()
	n := 1
	f.summary = &observe.Summary{EvaluatedAt: fixedNow(), Connection: "connected", Tasks: []observe.Task{
		{ID: "pods", Coverage: observe.Live, Parts: []observe.Part{{ID: "pods", Classifier: "verdict", Coverage: observe.Live,
			Counts: &observe.Counts{Total: 10, Fault: 1}, Top: []observe.Ref{{GVR: "v1/pods", Namespace: "shop", Name: "web-1", Role: "fault", Label: "CrashLoopBackOff"}}}}},
		{ID: healthsrc.TaskAlerts, Coverage: observe.Live, Parts: []observe.Part{{ID: "alerts", Classifier: "alerts", Coverage: observe.Live,
			Alerts: &observe.AlertCounts{Counted: 4, Critical: 1, Warning: 3}, Top: []observe.Ref{{GVR: "v1/pods", Namespace: "shop", Name: "web-1", Role: "critical", Label: "PodCrash", Fingerprint: "f1"}}}}},
	}, Namespaces: &n}
	env, read := run(t, f, toolAttention, `{}`)
	if env.Total == nil || env.Total.Count != 1 || len(read.Refs) != 1 {
		t.Fatalf("objects only in the total and the refs: %+v %+v", env.Total, read.Refs)
	}
	b, _ := json.Marshal(env.Data)
	if !strings.Contains(string(b), `"alerts":4`) || strings.Contains(string(b), `"label":"PodCrash"`) {
		t.Fatalf("data: %s", b)
	}
}
