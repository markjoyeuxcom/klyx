package appbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// AI A1 §3.5 and §8: get_alerts matches by label - the object, its current
// pods by controller UID, its namespace - and never makes a check about a
// UID, whatever S8a's SubjectUID says.

var alertSeen = time.Date(2026, 10, 21, 12, 6, 0, 0, time.UTC)

func owned(apiVersion, kind, ns, name, uid, ownerUID string) unstructured.Unstructured {
	u := uobj(map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns, "uid": uid,
		"labels": map[string]any{"app": "web"}}})
	if ownerUID != "" {
		_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "rs", "uid": ownerUID, "controller": true}},
			"metadata", "ownerReferences")
	}
	return *u
}

func amAlert(name, state string, subj alertmanager.Subject, uid string) healthsrc.AlertRow {
	return healthsrc.AlertRow{Key: "k-" + name, Alert: alertmanager.DisplayAlert{Name: name, StateClass: state, Severity: "critical",
		StartsAt: time.Date(2026, 10, 21, 12, 5, 0, 0, time.UTC), Summary: name + " summary",
		Labels: []alertmanager.Pair{{Name: "alertname", Value: name}, {Name: "pod", Value: subj.Name}, {Name: "team_secret", Value: "NeedleLabelValue"}}},
		Subject: subj, SubjectUID: uid}
}

func alertsChat(cov observe.KindCoverage, alerts ...healthsrc.AlertRow) *fakeChatConn {
	f := newFakeChat()
	f.kinds["node"] = []fleet.ChatKind{{GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Kind: "Node"}}
	f.objects[key(deployGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-d"},
		"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}})
	f.lists[replicaSetsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
		owned("apps/v1", "ReplicaSet", "shop", "web-1", "uid-rs1", ""), owned("apps/v1", "ReplicaSet", "shop", "lookalike-1", "uid-rsx", "")}}
	rs := f.lists[replicaSetsGVR.String()+"|shop"]
	_ = unstructured.SetNestedSlice(rs.Items[0].Object, []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "uid": "uid-d", "controller": true}}, "metadata", "ownerReferences")
	_ = unstructured.SetNestedSlice(rs.Items[1].Object, []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "lookalike", "uid": "uid-x", "controller": true}}, "metadata", "ownerReferences")
	f.lists[podsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
		owned("v1", "Pod", "shop", "web-1-a", "uid-p1", "uid-rs1"), owned("v1", "Pod", "shop", "lookalike-1-b", "uid-px", "uid-rsx")}}
	f.rows = map[string]observe.Rows{healthsrc.TaskAlerts + "/alerts": {Task: healthsrc.TaskAlerts,
		Part: observe.Part{ID: "alerts", Kinds: []observe.KindCoverage{cov}}, Rows: healthsrc.AlertRows{Alerts: alerts}}}
	return f
}

func pod(ns, name string) alertmanager.Subject {
	return alertmanager.Subject{Kind: "Pod", Namespace: ns, Name: name, Basis: []string{"namespace", "pod"}}
}

func TestAlertsMatchByLabelAndCheckNoUID(t *testing.T) {
	old := amAlert("PodOld", alertmanager.StateActive, pod("shop", "web-1-a"), "uid-p1")
	old.StartedBeforeCreated = true
	old.Alert.Description = "token ghp_abcdefghijklmnopqrstuvwxyz0123456789 leaked"
	f := alertsChat(observe.KindCoverage{Kind: healthsrc.SnapAlerts, Coverage: observe.Live, AsOf: &alertSeen},
		amAlert("Muted", alertmanager.StateSuppressed, alertmanager.Subject{Kind: "Namespace", Name: "shop", Basis: []string{"namespace"}}, ""),
		amAlert("WebDown", alertmanager.StateActive, alertmanager.Subject{Kind: "Deployment", Namespace: "shop", Name: "web", Basis: []string{"namespace", "deployment"}}, "uid-d"),
		old,
		amAlert("Lookalike", alertmanager.StateActive, pod("shop", "lookalike-1-b"), "uid-px"),
		amAlert("Elsewhere", alertmanager.StateActive, alertmanager.Subject{Kind: "Deployment", Namespace: "other", Name: "web"}, ""),
	)
	env, read := run(t, f, toolAlerts, `{"kind":"deployment","namespace":"shop","name":"web"}`)
	b, _ := json.Marshal(env)
	if strings.Contains(string(b), "NeedleLabelValue") || strings.Contains(string(b), "team_secret") || strings.Contains(string(b), "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatalf("labels outside the allowlist, and credentials, never cross: %s", b)
	}
	var d chatAlertsData
	db, _ := json.Marshal(env.Data)
	_ = json.Unmarshal(db, &d)
	var names []string
	for _, a := range d.Alerts {
		names = append(names, a.Name+":"+a.State)
	}
	if strings.Join(names, ",") != "WebDown:active,PodOld:active,Muted:suppressed" || d.Total != 3 || read.Outcome != "completed" || !env.Complete {
		t.Fatalf("the object's, its own pod's and its namespace's alerts, active first; never a lookalike's pod: %v %+v", names, env)
	}
	if !strings.HasPrefix(d.Alerts[1].MatchedBy, "one of its current pods") || d.Alerts[1].CurrentUID != "uid-p1" || !d.Alerts[1].StartedBeforeCreated {
		t.Fatalf("%+v", d.Alerts[1])
	}
	if len(env.Checks) != 2 {
		t.Fatalf("a check for each active alert only: %+v", env.Checks)
	}
	for _, c := range env.Checks {
		if c.Class != checkAlert || c.Subject.UID != "" || c.Subject.Identity != identityName || c.Observed != "2026-10-21T12:06:00Z" || c.Onset != "2026-10-21T12:05:00Z" {
			t.Fatalf("an alert check is by label, never about a UID: %+v", c)
		}
	}
	if env.Checks[1].Statement != "the current Alertmanager read holds alert PodOld, firing since 2026-10-21T12:05:00Z, naming Pod shop/web-1-a (by label); it began before the current object of that name was created" {
		t.Fatalf("%s", env.Checks[1].Statement)
	}
}

func TestAlertsFromAStaleSnapshotMakeNoCheck(t *testing.T) {
	last := alertSeen.Add(-time.Hour)
	f := alertsChat(observe.KindCoverage{Kind: healthsrc.SnapAlerts, Coverage: observe.Stale, LastGoodAt: &last},
		amAlert("WebDown", alertmanager.StateActive, alertmanager.Subject{Kind: "Deployment", Namespace: "shop", Name: "web"}, ""))
	env, _ := run(t, f, toolAlerts, `{"kind":"deployment","namespace":"shop","name":"web"}`)
	if len(env.Checks) != 0 || env.Complete || len(env.Errors) == 0 || !strings.Contains(env.Data.(map[string]any)["note"].(string), "none is called resolved") {
		t.Fatalf("%+v", env)
	}
	f.rows = nil
	env, read := run(t, f, toolAlerts, `{"kind":"node","name":"n1"}`)
	if read.Outcome != "failed" || !strings.Contains(env.Errors[0], "says nothing about whether alerts fire") {
		t.Fatalf("no snapshot is never no alerts: %+v", env)
	}
}

func TestAlertsForANodeAndItsNamespaceRule(t *testing.T) {
	f := alertsChat(observe.KindCoverage{Kind: healthsrc.SnapAlerts, Coverage: observe.Live, AsOf: &alertSeen},
		amAlert("NodeDown", alertmanager.StateActive, alertmanager.Subject{Kind: "Node", Name: "n1", Basis: []string{"node"}}, ""),
		amAlert("NsAlert", alertmanager.StateActive, alertmanager.Subject{Kind: "Namespace", Name: "", Basis: []string{"namespace"}}, ""))
	env, _ := run(t, f, toolAlerts, `{"kind":"node","namespace":"ignored","name":"n1"}`)
	var d chatAlertsData
	db, _ := json.Marshal(env.Data)
	_ = json.Unmarshal(db, &d)
	if d.Total != 1 || d.Alerts[0].Name != "NodeDown" || d.Alerts[0].Subject != "Node n1" {
		t.Fatalf("a cluster-scoped object has no namespace alerts: %+v", d)
	}
}
