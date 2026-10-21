package healthsrc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/healthnotify"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/safehttp"
)

var t0Alerts = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func item(name, sev, state string, counted bool, labels map[string]string, startedAgo time.Duration) alertmanager.Item {
	d := alertmanager.DisplayAlert{Fingerprint: "fp-" + name, Name: name, Severity: sev, State: state, Counted: counted, Summary: name + " summary",
		StartsAt: t0Alerts.Add(-startedAgo), StateClass: alertmanager.StateClassOf(state), SeverityClass: alertmanager.SeverityOf(map[string]string{"severity": sev})}
	s := alertmanager.SubjectOf(labels)
	d.Subject = alertmanager.DisplaySubject{Kind: s.Kind, Namespace: s.Namespace, Name: s.Name}
	return alertmanager.Item{Subject: s, Counted: counted, Key: "k-" + name, Display: d}
}

func capture(read alertmanager.Read, rows bool, complete map[string]bool, objs map[string][]any) observe.Capture {
	c := observe.Capture{Now: t0Alerts, Rows: rows, Objects: map[string][]any{SnapAlerts: {read}}, Complete: map[string]bool{}, Current: map[string]bool{}}
	for k, v := range objs {
		c.Objects[k] = v
	}
	for k, v := range complete {
		c.Complete[k], c.Current[k] = v, v
	}
	return c
}

// Coverage S8a §6: counts apart from object counts, active critical and
// warning counted, the rest classed; Top samples the counted, critical
// first then the longest firing, at most five.
func TestAlertsCountsAndTop(t *testing.T) {
	var items []alertmanager.Item
	for i := 0; i < 4; i++ {
		items = append(items, item(fmt.Sprintf("Warn%d", i), "warning", "active", true, map[string]string{"namespace": "team"}, time.Duration(i+1)*time.Minute))
	}
	items = append(items,
		item("CritNew", "critical", "active", true, map[string]string{"node": "n1"}, time.Minute),
		item("CritOld", "critical", "active", true, map[string]string{}, time.Hour),
		item("Info", "info", "active", false, nil, time.Minute),
		item("Watchdog", "none", "active", false, nil, time.Minute),
		item("Silenced", "critical", "suppressed", false, nil, time.Minute),
		item("Unproc", "critical", "unprocessed", false, nil, time.Minute),
		item("Odd", "critical", "pending-upstream", false, nil, time.Minute),
	)
	res := evalAlerts("c1", capture(alertmanager.Read{Identity: "id", Alerts: items, Truncated: true}, false, nil, nil))
	a := res.Alerts
	if a == nil || a.Counted != 6 || a.Critical != 2 || a.Warning != 4 || a.Uncounted != 2 || a.Suppressed != 1 || a.Unprocessed != 1 || a.Unknown != 1 || !a.Truncated {
		t.Fatalf("counts: %+v", a)
	}
	if res.Counts != nil || res.Native != nil || res.Evidence != nil || res.Rows != nil {
		t.Fatalf("alerts never use object counts, and no rows unless asked: %+v", res)
	}
	var labels []string
	for _, r := range res.Top {
		labels = append(labels, r.Label+":"+r.Role)
	}
	if fmt.Sprint(labels) != "[CritOld:critical CritNew:critical Warn3:warning Warn2:warning Warn1:warning]" {
		t.Fatalf("top: %v", labels)
	}
	if r := res.Top[1]; r.GVR != "v1/nodes" || r.Name != "n1" || r.Cluster != "c1" || r.Fingerprint != "k-CritNew" || r.Since == nil || r.Reason != "CritNew summary" {
		t.Fatalf("a ref: %+v", r)
	}
	if r := res.Top[0]; r.GVR != "" || r.Name != "" {
		t.Fatalf("a cluster alert's ref names no object: %+v", r)
	}
}

// S8a §4, §5: a subject is found by name; "not found" only for a held kind
// read live and complete; "not established" otherwise; "not looked up" for
// a kind the Observer does not hold; the started-before-created evidence.
func TestAlertsSubjectStates(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api-1", UID: types.UID("pod-uid"), CreationTimestamp: metav1.NewTime(t0Alerts.Add(-10 * time.Minute))}}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api", UID: "dep-uid", CreationTimestamp: metav1.NewTime(t0Alerts.Add(-24 * time.Hour))}}
	items := []alertmanager.Item{
		item("PodOld", "critical", "active", true, map[string]string{"namespace": "team", "pod": "api-1"}, time.Hour), // started before the pod
		item("PodGone", "critical", "active", true, map[string]string{"namespace": "team", "pod": "ghost-0"}, time.Minute),
		item("DepThere", "warning", "active", true, map[string]string{"namespace": "team", "deployment": "api"}, time.Minute),
		item("DepGone", "warning", "active", true, map[string]string{"namespace": "team", "deployment": "gone"}, time.Minute),
		item("Job", "warning", "active", true, map[string]string{"namespace": "team", "job_name": "backup-1"}, time.Minute),
		item("Cluster", "critical", "active", true, map[string]string{}, time.Minute),
	}
	read := alertmanager.Read{Identity: "id", Source: "src", Alerts: items}
	// pods live and complete; deployments not read complete
	res := evalAlerts("c1", capture(read, true, map[string]bool{KindPods: true, KindDeployments: false},
		map[string][]any{KindPods: {pod}, KindDeployments: {dep}}))
	rows := res.Rows.(AlertRows)
	got := map[string]AlertRow{}
	for _, r := range rows.Alerts {
		got[r.Alert.Name] = r
	}
	// a held object whose kind is not read live and complete is the object as
	// last observed, never current evidence (Codex review of 71c181c)
	for name, want := range map[string]string{"PodOld": SubjectFound, "PodGone": SubjectNotFound, "DepThere": SubjectLastObserved,
		"DepGone": SubjectNotEstablished, "Job": SubjectNotLookedUp, "Cluster": ""} {
		if got[name].SubjectState != want {
			t.Errorf("%s: %q, want %q", name, got[name].SubjectState, want)
		}
	}
	if r := got["PodOld"]; r.SubjectUID != "pod-uid" || !r.StartedBeforeCreated || r.SubjectGVR != "v1/pods" {
		t.Fatalf("found pod: %+v", r)
	}
	if r := got["DepThere"]; r.StartedBeforeCreated || r.SubjectGVR != "apps/v1/deployments" {
		t.Fatalf("found deployment: %+v", r)
	}
	if got["Job"].SubjectGVR != "batch/v1/jobs" || rows.Source != "src" || rows.Identity != "id" {
		t.Fatalf("rows: %+v", rows)
	}
	for _, r := range res.Top {
		if r.Label == "PodOld" && r.UID != "pod-uid" {
			t.Fatalf("a found subject's ref carries its UID: %+v", r)
		}
	}
}

// Codex review of 71c181c: counts, order and roles come from the classes,
// never the display text a credential could redact; refs and rows carry the
// alert's opaque key; the row facts carry the source's opaque key and
// neither the identity nor a read time.
func TestAlertsClassesKeysAndFacts(t *testing.T) {
	for _, secret := range []string{"active", "critical", "warning"} {
		raw := alertmanager.Reduced{Alert: alertmanager.Alert{Fingerprint: "fp", Labels: map[string]string{"alertname": "A", "severity": "critical"}}, State: alertmanager.StateActive}
		d := alertmanager.Display(raw, safehttp.NewRedactor(safehttp.Bearer(secret)))
		it := alertmanager.Item{Counted: raw.Counted(), Key: alertmanager.AlertKey("id", "fp"), Display: d}
		res := evalAlerts("c1", capture(alertmanager.Read{Identity: "id", Alerts: []alertmanager.Item{it}}, true, nil, nil))
		if res.Alerts.Counted != 1 || res.Alerts.Critical != 1 || len(res.Top) != 1 || res.Top[0].Role != "critical" {
			t.Errorf("a credential %q changed the verdict: %+v %+v", secret, res.Alerts, res.Top)
		}
		if res.Top[0].Fingerprint != it.Key || res.Rows.(AlertRows).Alerts[0].Key != it.Key {
			t.Errorf("refs and rows carry the key: %+v", res.Top[0])
		}
	}
	if alertmanager.AlertKey("A", "fp") == alertmanager.AlertKey("B", "fp") {
		t.Fatal("two sources' alerts sharing a fingerprint share a key")
	}
	f := AlertRows{Identity: "endpoint|https://am.example:443|tenant", Source: "s"}.RowFacts()
	if f["alertsSourceKey"] != alertmanager.SourceKey("endpoint|https://am.example:443|tenant") || f["alertsReadAt"] != nil || fmt.Sprint(f) == "" ||
		strings.Contains(fmt.Sprint(f), "endpoint|") {
		t.Fatalf("facts: %v", f)
	}
}

// No read yet: zero counts, which the consumers read under the part's
// coverage (checking, unreadable or absent).
func TestAlertsWithoutARead(t *testing.T) {
	res := evalAlerts("c1", observe.Capture{Objects: map[string][]any{}})
	if res.Alerts == nil || *res.Alerts != (observe.AlertCounts{}) || len(res.Top) != 0 {
		t.Fatalf("%+v", res)
	}
}

// recordingSink records the summary and every transition batch.
type recordingSink struct {
	sink
	mu      sync.Mutex
	batches []observe.TransitionBatch
}

func (r *recordingSink) Transitions(b observe.TransitionBatch) {
	r.mu.Lock()
	r.batches = append(r.batches, b)
	r.mu.Unlock()
}

// End to end through the Observer: an absent source is an absent part; a
// read counts; and no transition batch of the alerts part ever reaches the
// notification engine as an episode (S8a §4).
func TestAlertsThroughTheObserverNeverNotify(t *testing.T) {
	var mu sync.Mutex
	absent := true
	read := func(context.Context, string) (alertmanager.Read, error) {
		mu.Lock()
		defer mu.Unlock()
		if absent {
			return alertmanager.Read{}, fmt.Errorf("%w: none", observe.ErrSourceAbsent)
		}
		return alertmanager.Read{Identity: "", Alerts: []alertmanager.Item{item("Crit", "critical", "active", true, map[string]string{"namespace": "team", "pod": "api-1"}, time.Minute)}}, nil
	}
	snaps, domains := Alerts(AlertsDeps{Cluster: "c1", Read: read})
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kinds, _ := Core(CoreDeps{Cluster: "c1", Typed: fake.NewClientset(), Meta: metafake.NewSimpleMetadataClient(scheme)})
	s := &recordingSink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Snapshots: snaps, Domains: domains, NamespacesKind: KindNamespaces, Throttle: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	waitFor(t, "absent", func() bool {
		p, ok := nativePart(&s.sink, TaskAlerts, "alerts")
		return ok && p.Coverage == observe.Absent && p.Classifier == "alerts" && p.Alerts == nil
	})
	mu.Lock()
	absent = false
	mu.Unlock()
	o.Refresh(SnapAlerts)
	waitFor(t, "a read counted", func() bool {
		p, ok := nativePart(&s.sink, TaskAlerts, "alerts")
		return ok && p.Coverage == observe.Live && p.Alerts != nil && p.Alerts.Counted == 1 && len(p.Top) == 1
	})
	var sent int
	eng := healthnotify.New(time.Now, healthnotify.Hooks{Send: func(healthnotify.Notification) error { sent++; return nil }}, 100)
	eng.SetSettings(healthnotify.Settings{Enabled: true, Scope: healthnotify.ScopeAll})
	s.mu.Lock()
	for _, b := range s.batches {
		if b.Task == TaskAlerts {
			t.Errorf("the alerts part produced a transition batch: %+v", b)
		}
		eng.Apply(b)
	}
	s.mu.Unlock()
	eng.Flush()
	if sent != 0 {
		t.Fatalf("an alert reached a notification: %d", sent)
	}
}

// A kind complete but not current (absent, or not live) never proves a
// subject missing; an HPA's served version is autoscaling/v2.
func TestAlertsSubjectNeedsALiveCompleteKind(t *testing.T) {
	read := alertmanager.Read{Alerts: []alertmanager.Item{
		item("PodGone", "critical", "active", true, map[string]string{"namespace": "team", "pod": "ghost-0"}, time.Minute),
		item("HPA", "warning", "active", true, map[string]string{"namespace": "team", "horizontalpodautoscaler": "api"}, time.Minute),
	}}
	c := capture(read, true, nil, nil)
	c.Complete[KindPods], c.Current[KindPods] = true, false
	rows := evalAlerts("c1", c).Rows.(AlertRows)
	if rows.Alerts[0].SubjectState != SubjectNotEstablished {
		t.Fatalf("complete but not current: %q", rows.Alerts[0].SubjectState)
	}
	if rows.Alerts[1].SubjectGVR != "autoscaling/v2/horizontalpodautoscalers" || rows.Alerts[1].SubjectState != SubjectNotLookedUp {
		t.Fatalf("hpa: %+v", rows.Alerts[1])
	}
}
