package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A1.1 §9: rollout_history says only what the retained history
// establishes, with each fact's own evidence, time and coverage.

var (
	rolloutSTSGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	rolloutDSGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
)

// Values that must never cross: env values, args, a command, the change
// cause's text.
const (
	rolloutEnvNew  = "mode-crash-zq81"
	rolloutEnvOld  = "mode-steady-zq81"
	rolloutArgTok  = "arg-token-zq81"
	rolloutCommand = "cmd-secret-zq81"
	rolloutCause   = "kubectl set env deploy/web TOKEN=cause-secret-zq81"
)

func podTemplate(image, mode string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
		"spec": map[string]any{"containers": []any{map[string]any{"name": "web", "image": image,
			"command": []any{"/bin/web", rolloutCommand},
			"args":    []any{"--token=" + rolloutArgTok + mode},
			"env":     []any{map[string]any{"name": "MODE", "value": mode}}}}},
	}
}

func withHash(tm map[string]any, h string) map[string]any {
	c := runtimeDeepCopy(tm)
	c["metadata"].(map[string]any)["labels"].(map[string]any)["pod-template-hash"] = h
	return c
}

func rsObj(name, uid, rev string, tm map[string]any, owner, created string, replicas, ready int64) unstructured.Unstructured {
	md := map[string]any{"name": name, "namespace": "shop", "uid": uid, "creationTimestamp": created, "generation": int64(1),
		"labels":          map[string]any{"app": "web"},
		"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "uid": owner, "controller": true}}}
	if rev != "" {
		md["annotations"] = map[string]any{"deployment.kubernetes.io/revision": rev}
	}
	return unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": md,
		"spec":   map[string]any{"replicas": replicas, "template": tm},
		"status": map[string]any{"observedGeneration": int64(1), "readyReplicas": ready, "availableReplicas": ready}}}
}

func rolloutPod(name, uid, ownerKind, ownerUID string, ready bool, labels map[string]any) unstructured.Unstructured {
	st := "False"
	if ready {
		st = "True"
	}
	l := map[string]any{"app": "web"}
	for k, v := range labels {
		l[k] = v
	}
	return unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": "shop", "uid": uid, "labels": l,
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": ownerKind, "name": "x", "uid": ownerUID, "controller": true}}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": st}}}}}
}

// podRows is the Observer's pod capture: each UID's verdict, at a time and
// a coverage.
func rolloutPodRows(at time.Time, cov observe.Coverage, kinds map[string]health.Kind) observe.Rows {
	var rows []healthsrc.Row[workloads.PodSummary]
	for uid, k := range kinds {
		rows = append(rows, healthsrc.Row[workloads.PodSummary]{Ref: healthsrc.RowRef{UID: uid, GVR: "v1/pods", Namespace: "shop", Name: uid},
			Data: workloads.PodSummary{Health: health.Verdict{Kind: k, Label: fmt.Sprint(k)}}})
	}
	return observe.Rows{Task: healthsrc.TaskPods, Incarnation: 3, EvaluatedAt: at,
		Part: observe.Part{ID: "pods", Classifier: "verdict", Coverage: cov, Ver: 7},
		Rows: healthsrc.RowSet[workloads.PodSummary]{Rows: rows}}
}

// deploymentLab: revision 4 (current template, crashing) and 3 (steady,
// ready); a ReplicaSet of another Deployment under the same selector, and
// one without the revision annotation.
func deploymentLab() *fakeChatConn {
	f := newFakeChat()
	cur := podTemplate("registry/web:1.3", rolloutEnvNew)
	f.objects[key(deployGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-d", "generation": int64(4),
			"annotations": map[string]any{"deployment.kubernetes.io/revision": "4"}},
		"spec":   map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "template": cur},
		"status": map[string]any{"observedGeneration": int64(4)}})
	rs4 := rsObj("web-new", "uid-rs4", "4", withHash(cur, "h4"), "uid-d", "2026-10-14T11:01:07Z", 2, 0)
	rs4.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": "4", "kubernetes.io/change-cause": rolloutCause})
	f.lists[replicaSetsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
		rs4,
		rsObj("web-old", "uid-rs3", "3", withHash(podTemplate("registry/web:1.2", rolloutEnvOld), "h3"), "uid-d", "2026-10-10T08:00:00Z", 2, 2),
		rsObj("other", "uid-rsx", "9", withHash(cur, "hx"), "uid-x", "2026-10-10T08:00:00Z", 1, 1),
		rsObj("nohist", "uid-rsn", "", withHash(cur, "hn"), "uid-d", "2026-10-10T08:00:00Z", 0, 0),
	}}
	f.lists[podsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
		rolloutPod("web-new-a", "uid-p4a", "ReplicaSet", "uid-rs4", false, nil),
		rolloutPod("web-new-b", "uid-p4b", "ReplicaSet", "uid-rs4", false, nil),
		rolloutPod("web-old-a", "uid-p3a", "ReplicaSet", "uid-rs3", true, nil),
		rolloutPod("web-old-b", "uid-p3b", "ReplicaSet", "uid-rs3", true, nil),
		rolloutPod("other-a", "uid-px", "ReplicaSet", "uid-rsx", true, nil),
	}}
	f.rows = map[string]observe.Rows{healthsrc.TaskPods + "/pods": rolloutPodRows(fixedNow().Add(-time.Minute), observe.Live,
		map[string]health.Kind{"uid-p4a": health.Fault, "uid-p4b": health.Fault, "uid-p3a": health.OK, "uid-p3b": health.OK, "uid-px": health.OK})}
	return f
}

type rolloutResult struct {
	res    ChatToolResult
	data   chatRolloutData
	checks []ChatCheck
	note   string
}

func runRollout(t *testing.T, tools *chatTools, kind, name string) rolloutResult {
	t.Helper()
	res := tools.as("r1").Run(context.Background(), toolRollout, json.RawMessage(fmt.Sprintf(`{"kind":%q,"namespace":"shop","name":%q}`, kind, name)))
	var env struct {
		Data   chatRolloutData `json:"data"`
		Checks []ChatCheck     `json:"checks"`
		Note   string          `json:"note"`
		Errors []string        `json:"errors"`
	}
	if err := json.Unmarshal(res.JSON, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Errors) > 0 {
		t.Fatalf("%v", env.Errors)
	}
	return rolloutResult{res: res, data: env.Data, checks: env.Checks, note: env.Note}
}

func (r rolloutResult) rev(t *testing.T, n int64) chatRolloutRevision {
	t.Helper()
	for _, rv := range r.data.Revisions {
		if rv.Revision == n {
			return rv
		}
	}
	t.Fatalf("no revision %d in %+v", n, r.data.Revisions)
	return chatRolloutRevision{}
}

func (r rolloutResult) check(t *testing.T, contains string) ChatCheck {
	t.Helper()
	for _, c := range r.checks {
		if strings.Contains(c.Statement, contains) {
			return c
		}
	}
	t.Fatalf("no check saying %q in %+v", contains, r.checks)
	return ChatCheck{}
}

func (r rolloutResult) noCheck(t *testing.T, contains string) {
	t.Helper()
	for _, c := range r.checks {
		if strings.Contains(c.Statement, contains) {
			t.Fatalf("an unexpected check: %s", c.Statement)
		}
	}
}

func TestRolloutHistoryOfADeployment(t *testing.T) {
	r := runRollout(t, newChatTools(deploymentLab(), fixedNow), "Deployment", "web")
	d := r.data
	if d.History.Held != 2 || d.History.Skipped != 1 || d.RevisionsTotal != 2 || len(d.Revisions) != 2 || d.Revisions[0].Revision != 4 {
		t.Fatalf("another Deployment's ReplicaSet is not history, one without its annotation is skipped: %+v", d.History)
	}
	if d.Desired.Revision != 4 || d.Desired.How == "" || d.Partner.Revision != 3 || d.Partner.Gap != "" {
		t.Fatalf("the desired template by equality, its nearest lower partner: %+v %+v", d.Desired, d.Partner)
	}
	if len(d.Markers) != 1 || d.Markers[0].Revision != 4 {
		t.Fatalf("the annotation is reported as a marker: %+v", d.Markers)
	}
	r4, r3 := r.rev(t, 4), r.rev(t, 3)
	if !r4.Desired || !r3.Partner || r4.Created != "2026-10-14T11:01:07Z" || !r4.ChangeCauseRecorded || r3.ChangeCauseRecorded {
		t.Fatalf("%+v %+v", r4, r3)
	}
	if len(r4.Images) != 1 || r4.Images[0].Image != "registry/web:1.3" || r4.Images[0].Container != "web" {
		t.Fatalf("images by container: %+v", r4.Images)
	}
	if r4.Pods.Listed != 2 || r4.Pods.CurrentFailing != 2 || r4.Pods.Ready != 0 || r3.Pods.Ready != 2 || r3.Pods.CurrentNotFailing != 2 || d.Pods.Listed != 4 {
		t.Fatalf("pods by revision, the other workload's pod not among them: %+v %+v %+v", r4.Pods, r3.Pods, d.Pods)
	}
	if r4.Replicas == nil || r4.Replicas.Desired != 2 || r4.Replicas.Ready != 0 {
		t.Fatalf("%+v", r4.Replicas)
	}
	ch := r4.Changes
	if ch == nil || ch.From != 3 {
		t.Fatalf("revision 4 compared with 3: %+v", ch)
	}
	var shown, hidden []string
	for _, v := range ch.Visible {
		shown = append(shown, v.Path+"="+v.Old+">"+v.New)
	}
	for _, h := range ch.Hidden {
		hidden = append(hidden, h.Path+" "+h.Change)
	}
	if !strings.Contains(strings.Join(shown, ";"), "registry/web:1.2>registry/web:1.3") || !strings.Contains(strings.Join(hidden, ";"), "env[MODE]") {
		t.Fatalf("the image shown, the env value a path marker: %v %v", shown, hidden)
	}
	failing := r.check(t, "2 of the 2 listed pods of Deployment shop/web's revision 4")
	if !strings.Contains(failing.Statement, "matches the Deployment's template") || failing.Observed != rfcAt(fixedNow().Add(-time.Minute)) || failing.Failing {
		t.Fatalf("the failing pods' revision, observed at the capture's time, never Failing: %+v", failing)
	}
	ready := r.check(t, "all Ready")
	verdicts := r.check(t, "none of the 2 listed pods")
	if ready.Observed != rfcAt(fixedNow()) || verdicts.Observed != rfcAt(fixedNow().Add(-time.Minute)) {
		t.Fatalf("the partner's two checks, each at its own source's time: %+v %+v", ready, verdicts)
	}
	change := r.check(t, "differs from revision 3")
	if change.Observed != rfcAt(fixedNow()) || !strings.Contains(change.Statement, "When revision 4 became active is not in the data") ||
		!strings.Contains(change.Statement, "env[MODE]") {
		t.Fatalf("the template comparison, observed when read: %+v", change)
	}
	for _, c := range r.checks {
		if c.Class != checkRollout || c.Subject.UID != "uid-d" || c.Subject.Identity != identityUID {
			t.Fatalf("%+v", c)
		}
	}
	body := string(r.res.JSON)
	for _, needle := range []string{rolloutEnvNew, rolloutEnvOld, rolloutArgTok, rolloutCommand, "cause-secret-zq81", "kubectl set env"} {
		if strings.Contains(body, needle) {
			t.Fatalf("%q crossed: %s", needle, body)
		}
	}
	if len(r.res.JSON) > chatResultMax {
		t.Fatalf("%d bytes", len(r.res.JSON))
	}
}

func rfcAt(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func TestALaggingDeploymentNeverCallsTheOldRevisionCurrent(t *testing.T) {
	f := deploymentLab()
	dep := f.objects[key(deployGVR, "shop", "web")]
	dep.SetGeneration(5)
	_ = unstructured.SetNestedField(dep.Object, podTemplate("registry/web:1.4", "mode-next"), "spec", "template")
	r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
	if r.data.Desired.Revision != 0 || !strings.Contains(r.data.Desired.NotEstablished, "no revision held") || r.data.GenerationLag == "" {
		t.Fatalf("the annotation lags the spec: nothing is current: %+v %q", r.data.Desired, r.data.GenerationLag)
	}
	if !strings.Contains(r.data.Markers[0].Note, "not observed") {
		t.Fatalf("the marker says the controller has not observed the spec: %+v", r.data.Markers)
	}
	failing := r.check(t, "revision 4")
	if strings.Contains(failing.Statement, "matches") || !strings.Contains(failing.Statement, "revision annotation") {
		t.Fatalf("the failing check names the marker, never a match: %s", failing.Statement)
	}
	r.noCheck(t, "differs from")
}

func TestARolledBackRevisionKeepsItsOldCreationTime(t *testing.T) {
	f := deploymentLab()
	steady := podTemplate("registry/web:1.2", rolloutEnvOld)
	dep := f.objects[key(deployGVR, "shop", "web")]
	_ = unstructured.SetNestedField(dep.Object, steady, "spec", "template")
	dep.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": "5"})
	// the rollback reused revision 3's object as revision 5
	l := f.lists[replicaSetsGVR.String()+"|shop"]
	l.Items[1].SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": "5"})
	r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
	r5 := r.rev(t, 5)
	if r.data.Desired.Revision != 5 || r.data.Partner.Revision != 4 || r5.Created != "2026-10-10T08:00:00Z" {
		t.Fatalf("%+v %+v %+v", r.data.Desired, r.data.Partner, r5)
	}
	change := r.check(t, "differs from revision 4")
	if change.Observed != rfcAt(fixedNow()) {
		t.Fatalf("observed when read, never at the reused object's creation: %+v", change)
	}
	// A1's chronology: a failure that began after the reused object was
	// created, but before this read, does not make the comparison earlier
	// evidence
	ledger := []ChatRead{r.res.Read}
	in, err := parseFindings(json.RawMessage(`{"findings":[{"type":"cause","statement":"the rollback","subject":{"kind":"Deployment","namespace":"shop","name":"web"},"cites":["` + change.ID + `"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	fd := labelFindings(in, ledger, anchorFacts{failing: true, start: "2026-10-12T00:00:00Z", subject: ChatCheckSubject{Kind: "Deployment", Namespace: "shop", Name: "web", UID: "uid-d", Identity: identityUID}})
	if len(fd.Findings[0].Checked) != 1 || fd.Findings[0].Checked[0].Before {
		t.Fatalf("%+v", fd.Findings[0])
	}
}

func TestHistoryGapsAndAmbiguity(t *testing.T) {
	f := deploymentLab()
	l := f.lists[replicaSetsGVR.String()+"|shop"]
	l.Items[1].SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": "2"})
	r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
	if r.data.Partner.Revision != 2 || !strings.Contains(r.data.Partner.Gap, "not held in this history read") {
		t.Fatalf("the nearest lower retained, with the gap said: %+v", r.data.Partner)
	}

	g := deploymentLab()
	gl := g.lists[replicaSetsGVR.String()+"|shop"]
	cur, _, _ := unstructured.NestedMap(gl.Items[0].Object, "spec", "template")
	_ = unstructured.SetNestedField(gl.Items[1].Object, cur, "spec", "template")
	a := runRollout(t, newChatTools(g, fixedNow), "Deployment", "web")
	if a.data.Desired.Revision != 0 || !strings.HasPrefix(a.data.Desired.NotEstablished, "ambiguous") {
		t.Fatalf("two matches are ambiguous: %+v", a.data.Desired)
	}
	a.noCheck(t, "differs from")
}

func TestTheRowBoundKeepsThePinnedRevisions(t *testing.T) {
	f := deploymentLab()
	cur := podTemplate("registry/web:1.3", rolloutEnvNew)
	items := []unstructured.Unstructured{}
	for n := 12; n >= 1; n-- {
		tm := podTemplate(fmt.Sprintf("registry/web:0.%d", n), "m")
		if n == 1 {
			tm = cur // the desired template is the oldest revision
		}
		items = append(items, rsObj(fmt.Sprintf("web-%d", n), fmt.Sprintf("uid-rs%d", n), fmt.Sprint(n), withHash(tm, fmt.Sprint(n)), "uid-d", "2026-10-10T08:00:00Z", 0, 0))
	}
	f.lists[replicaSetsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: items}
	r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
	if r.data.RevisionsTotal != 12 || len(r.data.Revisions) != rolloutRowsMax || r.data.Desired.Revision != 1 {
		t.Fatalf("%d of %d, desired %+v", len(r.data.Revisions), r.data.RevisionsTotal, r.data.Desired)
	}
	r.rev(t, 1)
	if r.data.Partner.NotEstablished == "" {
		t.Fatalf("revision 1 has no lower one: %+v", r.data.Partner)
	}
}

func TestAStatefulSetJoinsItsPodsByRevisionName(t *testing.T) {
	f := newFakeChat()
	cur := podTemplate("registry/db:2", "a")
	f.objects[key(rolloutSTSGVR, "shop", "db")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet",
		"metadata": map[string]any{"name": "db", "namespace": "shop", "uid": "uid-s", "generation": int64(2)},
		"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "template": cur},
		"status":   map[string]any{"observedGeneration": int64(2), "updateRevision": "db-2", "currentRevision": "db-1"}})
	cr := func(name, uid string, rev int64, tm map[string]any) unstructured.Unstructured {
		patch := runtimeDeepCopy(tm)
		patch["$patch"] = "replace"
		return unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "ControllerRevision",
			"metadata": map[string]any{"name": name, "namespace": "shop", "uid": uid, "labels": map[string]any{"app": "web"},
				"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "db", "uid": "uid-s", "controller": true}}},
			"revision": rev, "data": map[string]any{"spec": map[string]any{"template": patch}}}}
	}
	f.lists[controllerRevisionsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
		cr("db-2", "uid-cr2", 2, cur), cr("db-1", "uid-cr1", 1, podTemplate("registry/db:1", "b"))}}
	f.lists[podsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
		rolloutPod("db-0", "uid-db0", "StatefulSet", "uid-s", true, map[string]any{"controller-revision-hash": "db-2"}),
		rolloutPod("db-1", "uid-db1", "StatefulSet", "uid-s", true, map[string]any{"controller-revision-hash": "db-1"}),
		rolloutPod("db-x", "uid-dbx", "StatefulSet", "uid-s", true, map[string]any{"controller-revision-hash": "db-9"}),
	}}
	r := runRollout(t, newChatTools(f, fixedNow), "StatefulSet", "db")
	if r.data.Desired.Revision != 2 || r.data.Partner.Revision != 1 || r.data.History.Object != "ControllerRevision" {
		t.Fatalf("$patch removed, the template matches: %+v %+v", r.data.Desired, r.data.Partner)
	}
	if r.rev(t, 2).Pods.Listed != 1 || r.rev(t, 1).Pods.Listed != 1 || r.data.Pods.Unjoined != 1 {
		t.Fatalf("pods by the revision's name, one unjoined: %+v", r.data)
	}
	if len(r.data.Markers) != 2 || r.data.Markers[0].Revision != 2 || r.data.Markers[1].Revision != 1 {
		t.Fatalf("updateRevision and currentRevision, both markers: %+v", r.data.Markers)
	}
	if strings.Contains(string(r.res.JSON), "$patch") {
		t.Fatal("a ControllerRevision's raw data crossed")
	}
}

func TestADaemonSetJoinsByHashAndReportsTheHighestRetained(t *testing.T) {
	f := newFakeChat()
	cur := podTemplate("registry/agent:2", "a")
	f.objects[key(rolloutDSGVR, "shop", "agent")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "DaemonSet",
		"metadata": map[string]any{"name": "agent", "namespace": "shop", "uid": "uid-ds", "generation": int64(2)},
		"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "template": cur},
		"status":   map[string]any{"observedGeneration": int64(2)}})
	cr := func(hash string, rev int64, tm map[string]any) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "ControllerRevision",
			"metadata": map[string]any{"name": "agent-" + hash, "namespace": "shop", "uid": "uid-" + hash, "labels": map[string]any{"app": "web", "controller-revision-hash": hash},
				"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "DaemonSet", "name": "agent", "uid": "uid-ds", "controller": true}}},
			"revision": rev, "data": map[string]any{"spec": map[string]any{"template": tm}}}}
	}
	f.lists[controllerRevisionsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{cr("bbb", 2, cur), cr("aaa", 1, podTemplate("registry/agent:1", "b"))}}
	f.lists[podsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
		rolloutPod("agent-x", "uid-ax", "DaemonSet", "uid-ds", true, map[string]any{"controller-revision-hash": "bbb"})}}
	r := runRollout(t, newChatTools(f, fixedNow), "DaemonSet", "agent")
	if r.rev(t, 2).Pods.Listed != 1 || r.data.Markers[0].Revision != 2 || r.data.Markers[0].Note != "the highest retained in this read" {
		t.Fatalf("%+v %+v", r.data.Revisions, r.data.Markers)
	}
}

func TestPodHealthIsCurrentRetainedOrMissing(t *testing.T) {
	t.Run("a stale capture makes no failing or verdict check", func(t *testing.T) {
		f := deploymentLab()
		f.rows[healthsrc.TaskPods+"/pods"] = rolloutPodRows(fixedNow().Add(-time.Hour), observe.Stale,
			map[string]health.Kind{"uid-p4a": health.Fault, "uid-p4b": health.OK, "uid-p3a": health.OK, "uid-p3b": health.OK})
		r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
		r4 := r.rev(t, 4)
		if r4.Pods.RetainedFailing != 1 || r4.Pods.RetainedNotFailing != 1 || r4.Pods.CurrentFailing != 0 || r.data.Pods.VerdictsLive {
			t.Fatalf("%+v", r4.Pods)
		}
		r.noCheck(t, "failing by Klyx's verdict")
		r.check(t, "all Ready") // readiness is the pod list's, read now
	})
	t.Run("a pod without a verdict", func(t *testing.T) {
		f := deploymentLab()
		f.rows[healthsrc.TaskPods+"/pods"] = rolloutPodRows(fixedNow().Add(-time.Minute), observe.Live,
			map[string]health.Kind{"uid-p4a": health.Fault, "uid-p3a": health.OK})
		r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
		if r.rev(t, 4).Pods.Missing != 1 || r.rev(t, 3).Pods.Missing != 1 {
			t.Fatalf("%+v", r.data.Revisions)
		}
		r.check(t, "1 of the 2 listed pods of Deployment shop/web's revision 4 (ReplicaSet web-new) is failing by Klyx's verdict; 1 has no verdict")
		r.noCheck(t, "none of the 2 listed pods")
	})
	t.Run("no capture at all", func(t *testing.T) {
		f := deploymentLab()
		f.rows = nil
		r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
		if r.rev(t, 4).Pods.Missing != 2 || r.data.Pods.VerdictsNote == "" {
			t.Fatalf("%+v", r.data.Pods)
		}
		r.noCheck(t, "Klyx's verdict")
	})
	t.Run("a partner pod not Ready", func(t *testing.T) {
		f := deploymentLab()
		f.lists[podsGVR.String()+"|shop"].Items[2] = rolloutPod("web-old-a", "uid-p3a", "ReplicaSet", "uid-rs3", false, nil)
		r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
		r.noCheck(t, "all Ready")
		r.check(t, "none of the 2 listed pods") // not failing by verdict is a separate fact
	})
}

func TestThePartnersTwoTimesAreDatedApart(t *testing.T) {
	subj := ChatCheckSubject{Kind: "Deployment", Namespace: "shop", Name: "web", UID: "uid-d", Identity: identityUID}
	cases := []struct {
		name                 string
		capture, start       time.Time
		readyBefore, vBefore bool
	}{
		{"the failure between the capture and the read", fixedNow().Add(-time.Hour), fixedNow().Add(-30 * time.Minute), false, true},
		{"the failure between the read and the capture", fixedNow().Add(time.Hour), fixedNow().Add(30 * time.Minute), true, false},
		{"both after the failure", fixedNow().Add(-time.Hour), fixedNow().Add(-2 * time.Hour), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := deploymentLab()
			f.rows[healthsrc.TaskPods+"/pods"] = rolloutPodRows(c.capture, observe.Live,
				map[string]health.Kind{"uid-p4a": health.Fault, "uid-p4b": health.Fault, "uid-p3a": health.OK, "uid-p3b": health.OK})
			r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
			ready, verdicts := r.check(t, "all Ready"), r.check(t, "none of the 2 listed pods")
			a := anchorFacts{failing: true, start: rfcAt(c.start), subject: subj}
			for _, cite := range [][]string{{ready.ID, verdicts.ID}, {"r1"}} {
				raw, _ := json.Marshal(map[string]any{"findings": []any{map[string]any{"type": "observation", "statement": "the partner",
					"subject": map[string]any{"kind": "Deployment", "namespace": "shop", "name": "web"}, "cites": cite}}})
				in, err := parseFindings(raw)
				if err != nil {
					t.Fatal(err)
				}
				fd := labelFindings(in, []ChatRead{r.res.Read}, a)
				got := map[string]bool{}
				for _, ck := range fd.Findings[0].Checked {
					got[ck.ID] = ck.Before
				}
				if got[ready.ID] != c.readyBefore || got[verdicts.ID] != c.vBefore {
					t.Fatalf("citing %v: readiness before=%v, verdicts before=%v (%+v)", cite, got[ready.ID], got[verdicts.ID], fd.Findings[0].Checked)
				}
			}
		})
	}
}

func TestAReplacedAnchorMakesNoRolloutCheck(t *testing.T) {
	tools := newChatTools(deploymentLab(), fixedNow)
	tools.anchor = &ChatSeedDTO{Kind: "object", Group: "apps", Version: "v1", Plural: "deployments", Namespace: "shop", Name: "web", UID: "uid-the-old-one"}
	r := runRollout(t, tools, "Deployment", "web")
	if len(r.checks) != 0 || !strings.Contains(r.note, "possibly a replacement") {
		t.Fatalf("%+v %q", r.checks, r.note)
	}
	tools.anchor.UID = "uid-d"
	if r := runRollout(t, tools, "Deployment", "web"); len(r.checks) == 0 {
		t.Fatal("the anchor itself has its checks")
	}
}

func TestRolloutRefusals(t *testing.T) {
	tools := newChatTools(deploymentLab(), fixedNow)
	for args, why := range map[string]string{
		`{"kind":"Job","namespace":"shop","name":"web"}`:                  "kind must be Deployment, StatefulSet or DaemonSet",
		`{"kind":"Deployment","name":"web"}`:                              "namespace",
		`{"kind":"Deployment","namespace":"shop","name":"web","extra":1}`: "extra",
	} {
		res := tools.as("r1").Run(context.Background(), toolRollout, json.RawMessage(args))
		if res.Read.Outcome != "failed" || !strings.Contains(res.Read.Detail, why) {
			t.Fatalf("refused before any read, saying why: %s: %+v", args, res.Read)
		}
	}
}

func TestTheShrinkKeepsThePinnedRows(t *testing.T) {
	d := &chatRolloutData{}
	for n := int64(10); n >= 1; n-- {
		row := chatRolloutRevision{Revision: n, Object: "ReplicaSet x", Desired: n == 10, Partner: n == 9,
			Changes: &chatRolloutChange{From: n - 1}}
		for i := 0; i < 20; i++ {
			row.Changes.Hidden = append(row.Changes.Hidden, chatHiddenChange{Path: strings.Repeat("p", 120), Change: "changed (not shown)"})
		}
		d.Revisions = append(d.Revisions, row)
	}
	shrinkRollout(d, 12*1024)
	b, _ := json.Marshal(d)
	if len(b) > 12*1024 {
		t.Fatalf("%d bytes", len(b))
	}
	pinned := 0
	for _, r := range d.Revisions {
		if r.Desired || r.Partner {
			pinned++
		}
	}
	if pinned != 2 {
		t.Fatalf("the pinned rows stay: %+v", d.Revisions)
	}
	tiny := &chatRolloutData{Revisions: []chatRolloutRevision{{Revision: 2, Desired: true, Changes: &chatRolloutChange{Hidden: []chatHiddenChange{{Path: strings.Repeat("q", 4000)}}}}}}
	shrinkRollout(tiny, 1024)
	if !tiny.PinnedNotFit || tiny.Revisions[0].Changes != nil {
		t.Fatalf("pinned data that cannot fit is said, not claimed: %+v", tiny)
	}
}

func TestRolloutAtItsFullShape(t *testing.T) {
	fc := newFakeChat()
	rolloutFullShape(fc)
	r := runRollout(t, newChatTools(fc, fixedNow), "Deployment", "web")
	if len(r.res.JSON) > chatResultMax || r.data.RevisionsTotal != 50 || r.data.Desired.Revision != 50 || r.data.Partner.Revision != 49 {
		t.Fatalf("%d bytes, %d revisions, desired %+v partner %+v", len(r.res.JSON), r.data.RevisionsTotal, r.data.Desired, r.data.Partner)
	}
	pinned := 0
	for _, rv := range r.data.Revisions {
		if rv.Desired || rv.Partner {
			pinned++
		}
	}
	if pinned != 2 && !r.data.PinnedNotFit {
		t.Fatalf("the pinned rows are present or said not to fit: %+v", r.data.Revisions)
	}
	for _, needle := range []string{strings.Repeat("v", 200), strings.Repeat("a", 500)} {
		if strings.Contains(string(r.res.JSON), needle) {
			t.Fatal("an env value or args crossed")
		}
	}
}

// fencedRollout loses the cluster after the workload's read: the history
// and pod lists never run (A1 implementation review, finding 3).
type fencedRollout struct {
	*fakeChatConn
	lost  func()
	lists int
}

func (f *fencedRollout) ChatGet(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	u, err := f.fakeChatConn.ChatGet(ctx, gvr, ns, name)
	f.lost()
	return u, err
}

func (f *fencedRollout) ChatList(ctx context.Context, gvr schema.GroupVersionResource, ns, sel string, limit int64) (*unstructured.UnstructuredList, error) {
	f.lists++
	return f.fakeChatConn.ChatList(ctx, gvr, ns, sel, limit)
}

func TestARolloutReadStopsAtItsFence(t *testing.T) {
	f := newToolsFixture(t, calls(ai.ToolCall{ID: "r", Name: toolRollout, Args: json.RawMessage(`{"kind":"Deployment","namespace":"shop","name":"web"}`)}), answer("never"))
	lab := deploymentLab()
	lab.summary = f.conn.summary
	src := &fencedRollout{fakeChatConn: lab}
	src.lost = func() {
		f.mu.Lock()
		f.usable = false
		f.mu.Unlock()
	}
	f.mu.Lock()
	f.conn, f.cur = lab, src
	f.mu.Unlock()
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "what changed in web?", nil)
	if src.lists != 0 || st.Ended == "" || len(f.prov.reqs) != 1 {
		t.Fatalf("guard() before each read stops the lists: %d lists, ended %q, %d requests", src.lists, st.Ended, len(f.prov.reqs))
	}
}

func TestMoreThatNeverCrosses(t *testing.T) {
	f := deploymentLab()
	long := strings.Repeat("n", 253)
	l := f.lists[replicaSetsGVR.String()+"|shop"]
	for i := range l.Items[:2] {
		tm, _, _ := unstructured.NestedMap(l.Items[i].Object, "spec", "template")
		md := tm["metadata"].(map[string]any)
		md["annotations"] = map[string]any{"example.com/notes": fmt.Sprintf("annotation-secret-zq81-%d", i)}
		c := tm["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		c["livenessProbe"] = map[string]any{"exec": map[string]any{"command": []any{"/bin/check", fmt.Sprintf("probe-secret-zq81-%d", i)}}}
		_ = unstructured.SetNestedField(l.Items[i].Object, tm, "spec", "template")
	}
	l.Items[1].SetName(long)
	r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
	body := string(r.res.JSON)
	for _, needle := range []string{"annotation-secret-zq81", "probe-secret-zq81", rolloutEnvNew, rolloutEnvOld} {
		if strings.Contains(body, needle) {
			t.Fatalf("%q crossed", needle)
		}
	}
	if !strings.Contains(body, "annotations") || len(r.res.JSON) > chatResultMax {
		t.Fatalf("the annotation's change is a marker; %d bytes", len(r.res.JSON))
	}
}

func TestACappedHistoryIsPartial(t *testing.T) {
	f := newFakeChat()
	cur := podTemplate("registry/agent:99", "a")
	f.objects[key(rolloutDSGVR, "shop", "agent")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "DaemonSet",
		"metadata": map[string]any{"name": "agent", "namespace": "shop", "uid": "uid-ds", "generation": int64(2)},
		"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "template": cur},
		"status":   map[string]any{"observedGeneration": int64(2)}})
	l := &unstructured.UnstructuredList{}
	for n := 1; n <= chatReplicaSetsMax; n++ { // the page holds 1-50; the current one is beyond it
		l.Items = append(l.Items, unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "ControllerRevision",
			"metadata": map[string]any{"name": fmt.Sprintf("agent-%d", n), "namespace": "shop", "uid": fmt.Sprintf("uid-cr%d", n), "labels": map[string]any{"app": "web"},
				"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "DaemonSet", "name": "agent", "uid": "uid-ds", "controller": true}}},
			"revision": int64(n), "data": map[string]any{"spec": map[string]any{"template": podTemplate(fmt.Sprintf("registry/agent:%d", n), "a")}}}})
	}
	f.lists[controllerRevisionsGVR.String()+"|shop"] = l
	r := runRollout(t, newChatTools(f, fixedNow), "DaemonSet", "agent")
	if !r.data.History.Partial || r.data.Markers[0].Note != "the highest retained in this read" || r.data.Markers[0].Revision != 50 {
		t.Fatalf("%+v %+v", r.data.History, r.data.Markers)
	}
	if r.data.Desired.Revision != 0 || !strings.Contains(r.data.Desired.NotEstablished, "partial") {
		t.Fatalf("the current revision outside the page is not established: %+v", r.data.Desired)
	}
}

func TestTheCapsOnImagesComparisonsAndDegradedPods(t *testing.T) {
	f := deploymentLab()
	var cs []any
	for c := 0; c < rolloutImagesMax+3; c++ {
		cs = append(cs, map[string]any{"name": fmt.Sprintf("c%d", c), "image": fmt.Sprintf("img:%d", c)})
	}
	items := []unstructured.Unstructured{}
	for n := 6; n >= 1; n-- {
		tm := map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "web"}}, "spec": map[string]any{"containers": append([]any{map[string]any{"name": "web", "image": fmt.Sprintf("web:%d", n)}}, cs...)}}
		items = append(items, rsObj(fmt.Sprintf("web-%d", n), fmt.Sprintf("uid-rs%d", n), fmt.Sprint(n), withHash(tm, fmt.Sprint(n)), "uid-d", "2026-10-10T08:00:00Z", 0, 0))
	}
	cur, _, _ := unstructured.NestedMap(items[0].Object, "spec", "template")
	_ = unstructured.SetNestedField(f.objects[key(deployGVR, "shop", "web")].Object, normTemplate(cur), "spec", "template")
	f.lists[replicaSetsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: items}
	f.lists[podsGVR.String()+"|shop"] = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{rolloutPod("web-6-a", "uid-6a", "ReplicaSet", "uid-rs6", false, nil)}}
	f.rows[healthsrc.TaskPods+"/pods"] = rolloutPodRows(fixedNow(), observe.Live, map[string]health.Kind{"uid-6a": health.Degraded})
	r := runRollout(t, newChatTools(f, fixedNow), "Deployment", "web")
	r6 := r.rev(t, 6)
	if len(r6.Images) != rolloutImagesMax || r6.ImagesMore != 4 {
		t.Fatalf("%d images, %d more", len(r6.Images), r6.ImagesMore)
	}
	compared := 0
	for _, rv := range r.data.Revisions {
		if rv.Changes != nil {
			compared++
		}
	}
	if compared != rolloutCompareMax {
		t.Fatalf("%d comparisons", compared)
	}
	if r6.Pods.CurrentFailing != 1 {
		t.Fatalf("a degraded verdict is failing: %+v", r6.Pods)
	}
}
