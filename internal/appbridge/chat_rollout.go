package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// AI A1.1 (docs/superpowers/specs/2026-10-24-klyx-ai-a1-1-rollout-history.md):
// rollout_history reads one workload's retained revisions, its pods and
// Klyx's verdicts for them, and says only what that history establishes.
// Kubernetes numbers revisions in the order each became the target, and a
// rollback reuses an old object under the next number: an object's
// creation time dates neither a rollout nor a change, a revision missing
// from the read is not held, and the only revision called current is the
// one whose pod template equals the workload's (FindNewReplicaSet's rule).

const (
	toolRollout = "rollout_history"

	rolloutRowsMax     = 10 // revisions listed, the pinned ones always among them
	rolloutImagesMax   = 8  // images per revision, counted beyond
	rolloutCompareMax  = 3  // template comparisons per read
	rolloutFailNames   = 3  // failing pods named per revision
	rolloutFailChecks  = 3  // failing-pods checks per read
	rolloutStmtPaths   = 5  // paths in a change check's statement
	rolloutRevisionAnn = "deployment.kubernetes.io/revision"
	rolloutCauseAnn    = "kubernetes.io/change-cause"
	rolloutHashLabel   = "pod-template-hash"
	rolloutRevHash     = "controller-revision-hash"
)

var (
	controllerRevisionsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
	rolloutResources       = map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets", "DaemonSet": "daemonsets"}
)

type chatRolloutData struct {
	Kind               string `json:"kind"`
	Namespace          string `json:"namespace"`
	Name               string `json:"name"`
	UID                string `json:"uid"`
	Generation         int64  `json:"generation,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	// GenerationLag: the controller has not observed the latest spec, so
	// no marker speaks for it
	GenerationLag string              `json:"generationLag,omitempty"`
	Markers       []chatRolloutMarker `json:"markers,omitempty"`
	// Desired is the revision whose pod template equals the workload's, or
	// why none is established
	Desired chatRolloutPick `json:"desiredTemplate"`
	Partner chatRolloutPick `json:"nearestLowerRetained"`
	History chatRolloutHist `json:"history"`
	Pods    chatRolloutPods `json:"pods"`
	// Revisions are newest revision number first
	Revisions      []chatRolloutRevision `json:"revisions"`
	RevisionsTotal int                   `json:"revisionsTotal"`
	// PinnedNotFit: the pinned revisions' data could not fit the result
	PinnedNotFit bool `json:"pinnedDataNotFit,omitempty"`
}

// chatRolloutMarker is what the controller wrote, reported as it is.
type chatRolloutMarker struct {
	Marker string `json:"marker"`
	Names  string `json:"names"`
	// Revision is the history revision it names, 0 when the read holds none
	Revision int64  `json:"revision,omitempty"`
	Note     string `json:"note,omitempty"`
}

type chatRolloutPick struct {
	Revision int64  `json:"revision,omitempty"`
	Object   string `json:"object,omitempty"`
	How      string `json:"how,omitempty"`
	// NotEstablished says why there is no revision
	NotEstablished string `json:"notEstablished,omitempty"`
	Gap            string `json:"gap,omitempty"`
}

type chatRolloutHist struct {
	Object  string `json:"object"` // ReplicaSet | ControllerRevision
	Held    int    `json:"held"`
	Skipped int    `json:"skipped,omitempty"` // a ReplicaSet without a revision
	Partial bool   `json:"partial,omitempty"`
	ReadAt  string `json:"readAt"`
	Note    string `json:"note,omitempty"`
}

type chatRolloutPods struct {
	Listed   int    `json:"listed"`
	Unjoined int    `json:"unjoined,omitempty"`
	Partial  bool   `json:"partial,omitempty"`
	ReadAt   string `json:"readAt"`
	// Verdicts is the Observer's capture: its time, and whether its
	// coverage is live (only a live capture's verdicts are current)
	VerdictsAt   string `json:"verdictsEvaluatedAt,omitempty"`
	VerdictsLive bool   `json:"verdictsLive"`
	VerdictsNote string `json:"verdictsNote,omitempty"`
}

type chatRolloutRevision struct {
	Revision   int64              `json:"revision"`
	Object     string             `json:"object"`
	UID        string             `json:"uid"`
	Created    string             `json:"objectCreated,omitempty"` // metadata only: a reused object keeps it
	Markers    []string           `json:"markers,omitempty"`
	Desired    bool               `json:"matchesDesiredTemplate,omitempty"`
	Partner    bool               `json:"nearestLowerRetained,omitempty"`
	Images     []chatRolloutImage `json:"images,omitempty"`
	ImagesMore int                `json:"imagesMore,omitempty"`
	Replicas   *chatRolloutReps   `json:"replicas,omitempty"`
	Pods       chatRolloutRevPods `json:"pods"`
	// ChangeCauseRecorded: kubernetes.io/change-cause is set. Its text is
	// never sent: kubectl's --record writes the whole command line
	ChangeCauseRecorded bool               `json:"changeCauseRecorded,omitempty"`
	TemplateUnreadable  bool               `json:"templateNotReadable,omitempty"`
	Changes             *chatRolloutChange `json:"changesFromNearestLower,omitempty"`
}

type chatRolloutImage struct {
	Container string `json:"container"`
	Image     string `json:"image"`
}

type chatRolloutReps struct {
	Desired   int64  `json:"desired"`
	Ready     int64  `json:"ready"`
	Available int64  `json:"available"`
	StatusLag string `json:"statusLag,omitempty"`
}

type chatRolloutRevPods struct {
	Listed int `json:"listed"`
	Ready  int `json:"ready"`
	// current verdicts (a live capture), retained ones (last observed, in a
	// capture that is not live) and missing ones
	CurrentFailing     int      `json:"currentFailing"`
	CurrentNotFailing  int      `json:"currentNotFailing"`
	RetainedFailing    int      `json:"retainedFailing,omitempty"`
	RetainedNotFailing int      `json:"retainedNotFailing,omitempty"`
	Missing            int      `json:"noVerdict,omitempty"`
	Failing            []string `json:"failing,omitempty"`
}

type chatRolloutChange struct {
	From       int64               `json:"from"`
	Visible    []chatVisibleChange `json:"visible,omitempty"`
	Hidden     []chatHiddenChange  `json:"hidden,omitempty"`
	PathsTotal int                 `json:"pathsTotal"`
}

// rolloutRev is one held revision before projection.
type rolloutRev struct {
	num     int64
	obj     *unstructured.Unstructured
	tmpl    map[string]any // the pod template, normalised; nil when unreadable
	hash    string         // a ControllerRevision's own hash label
	pods    []*unstructured.Unstructured
	markers []string
	desired bool
	partner bool
	changes *chatRolloutChange
}

func (t *chatTools) rollout(ctx context.Context, kind, ns, name string) ChatToolResult {
	target := kind + " " + ns + "/" + name
	res := rolloutResources[kind]
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: res}
	p := &projector{}
	w, err := t.conn.ChatGet(ctx, gvr, ns, name)
	if err != nil {
		return t.failed(toolRollout, "api", target, err)
	}
	uid := string(w.GetUID())
	data := chatRolloutData{Kind: kind, Namespace: ns, Name: name, UID: uid, Generation: w.GetGeneration()}
	data.ObservedGeneration, _, _ = unstructured.NestedInt64(w.Object, "status", "observedGeneration")
	if data.ObservedGeneration < data.Generation {
		data.GenerationLag = fmt.Sprintf("the controller has observed generation %d; the spec is at %d", data.ObservedGeneration, data.Generation)
	}
	selector, err := selectorOf(w)
	if err != nil {
		return t.refuse(toolRollout, target, err)
	}
	sel := selector.String()
	wtmpl, _, _ := unstructured.NestedMap(w.Object, "spec", "template")
	wtmpl = normTemplate(wtmpl)

	// the revisions: one page, kept by controller UID
	histGVR, histObj := replicaSetsGVR, "ReplicaSet"
	if kind != "Deployment" {
		histGVR, histObj = controllerRevisionsGVR, "ControllerRevision"
	}
	list, err := t.conn.ChatList(ctx, histGVR, ns, sel, chatReplicaSetsMax)
	if err != nil {
		return t.failed(toolRollout, "api", target, err)
	}
	data.History = chatRolloutHist{Object: histObj, ReadAt: t.stamp(), Partial: len(list.Items) >= chatReplicaSetsMax || list.GetContinue() != ""}
	var revs []*rolloutRev
	for i := range list.Items {
		o := &list.Items[i]
		if controllerUID(o) != uid {
			continue
		}
		r := &rolloutRev{obj: o}
		if kind == "Deployment" {
			n, perr := strconv.ParseInt(o.GetAnnotations()[rolloutRevisionAnn], 10, 64)
			if perr != nil {
				data.History.Skipped++ // a ReplicaSet without the revision annotation is not history
				continue
			}
			r.num = n
			if tm, ok, _ := unstructured.NestedMap(o.Object, "spec", "template"); ok {
				r.tmpl = normTemplate(tm)
			}
		} else {
			r.num, _, _ = unstructured.NestedInt64(o.Object, "revision")
			r.hash = o.GetLabels()[rolloutRevHash]
			if tm, ok, _ := unstructured.NestedMap(o.Object, "data", "spec", "template"); ok {
				r.tmpl = normTemplate(tm)
			}
		}
		revs = append(revs, r)
	}
	sort.SliceStable(revs, func(i, j int) bool { return revs[i].num > revs[j].num })
	data.History.Held = len(revs)
	if data.History.Partial {
		data.History.Note = fmt.Sprintf("the history read is one page of %d; revisions outside it are not held", chatReplicaSetsMax)
	}

	// the pods: one page, joined to their revision
	pods, err := t.conn.ChatList(ctx, podsGVRv1, ns, sel, chatWorkloadPodsMax)
	if err != nil {
		return t.failed(toolRollout, "api", target, err)
	}
	data.Pods = chatRolloutPods{ReadAt: t.stamp(), Partial: len(pods.Items) >= chatWorkloadPodsMax || pods.GetContinue() != ""}
	byRSUID := map[string]*rolloutRev{}
	for _, r := range revs {
		byRSUID[string(r.obj.GetUID())] = r
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := controllerUID(pod)
		var r *rolloutRev
		switch {
		case kind == "Deployment":
			r = byRSUID[owner]
			if r == nil {
				if owner != "" {
					// a pod of a ReplicaSet this read does not hold, or of
					// another workload under the same selector
					if _, ours := ownedBy(pod, list, uid); ours {
						data.Pods.Unjoined++
						data.Pods.Listed++
					}
				}
				continue
			}
		case owner == uid:
			h := pod.GetLabels()[rolloutRevHash]
			for _, cand := range revs {
				if h != "" && (h == cand.obj.GetName() || h == cand.hash) {
					r = cand
					break
				}
			}
			if r == nil {
				data.Pods.Unjoined++
				data.Pods.Listed++
				continue
			}
		default:
			continue
		}
		data.Pods.Listed++
		r.pods = append(r.pods, pod)
	}

	// Klyx's verdicts for the pods: the Observer's capture, with its own
	// time and coverage
	verdicts := t.verdictsFor(ctx, podsGVRv1, &p.redacted)
	switch {
	case verdicts == nil:
		data.Pods.VerdictsNote = "Klyx classifies no pods"
	case verdicts.byUID == nil:
		data.Pods.VerdictsNote = verdicts.problem
	default:
		data.Pods.VerdictsLive = verdicts.problem == ""
		data.Pods.VerdictsNote = verdicts.problem
		for _, v := range verdicts.byUID {
			if v.EvaluatedAt > data.Pods.VerdictsAt {
				data.Pods.VerdictsAt = v.EvaluatedAt
			}
		}
	}

	// markers, as markers
	markers(kind, w, revs, &data)

	// the desired template, by equality only
	var desired, partner *rolloutRev
	var matches []*rolloutRev
	for _, r := range revs {
		if r.tmpl != nil && wtmpl != nil && reflect.DeepEqual(r.tmpl, wtmpl) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		data.Desired.NotEstablished = "no revision held in this read has the workload's pod template"
		if data.History.Partial {
			data.Desired.NotEstablished += "; the history read is partial"
		}
	case 1:
		desired = matches[0]
		desired.desired = true
		data.Desired = chatRolloutPick{Revision: desired.num, Object: histObj + " " + desired.obj.GetName(), How: "its pod template equals the workload's"}
	default:
		data.Desired.NotEstablished = fmt.Sprintf("ambiguous: %d revisions held have the workload's pod template", len(matches))
	}
	if desired != nil {
		for _, r := range revs {
			if r.num < desired.num {
				partner = r
				break
			}
		}
		if partner == nil {
			data.Partner.NotEstablished = "no lower revision is held in this read"
		} else {
			partner.partner = true
			data.Partner = chatRolloutPick{Revision: partner.num, Object: histObj + " " + partner.obj.GetName(), How: "the nearest lower revision retained in this read"}
			if partner.num != desired.num-1 {
				data.Partner.Gap = "the revisions between them are not held in this history read; why they are absent is not established"
			}
		}
	} else {
		data.Partner.NotEstablished = "no desired-template revision to compare with"
	}

	// comparisons: the desired revision with its partner, then the newest
	// two others with their nearest lower ones
	compared := 0
	compare := func(newer, older *rolloutRev) {
		if compared >= rolloutCompareMax || newer == nil || older == nil || newer.tmpl == nil || older.tmpl == nil || newer.changes != nil {
			return
		}
		compared++
		newer.changes = templateChanges(older, newer, ns, p)
	}
	compare(desired, partner)
	for i, r := range revs {
		if compared >= rolloutCompareMax {
			break
		}
		if r == desired || i+1 >= len(revs) {
			continue
		}
		compare(r, revs[i+1])
	}

	// rows: the pinned first, then the newest others, newest number first
	var rows []*rolloutRev
	for _, r := range revs {
		if r.desired || r.partner {
			rows = append(rows, r)
		}
	}
	for _, r := range revs {
		if len(rows) >= rolloutRowsMax {
			break
		}
		if !r.desired && !r.partner {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].num > rows[j].num })
	data.RevisionsTotal = len(revs)
	for _, r := range rows {
		data.Revisions = append(data.Revisions, revisionRow(r, histObj, verdicts, p))
	}

	checks := rolloutChecks(t, &data, revs, desired, partner, verdicts, w)
	replaced := t.anchorReplaced(gvr, ns, name, uid)
	note := "Creation times are object metadata: a rollback reuses an old object under the next revision number, so they date neither a rollout nor a change."
	if replaced {
		checks = nil
		note = "possibly a replacement: this " + kind + " is not the investigated one (its UID differs); no rollout check is made. " + note
	}
	shrinkRollout(&data, chatResultMax-2048)
	env := chatEnvelope{Tool: toolRollout, Source: "api", ReadAt: t.stamp(), Complete: !data.History.Partial && !data.Pods.Partial,
		Identity: &chatIdentity{GVR: gvrString(gvr), Namespace: ns, Name: name, UID: uid, Generation: data.Generation},
		Data:     data, Checks: capChecks(checks), Note: note}
	read := ChatRead{Tool: toolRollout, Target: target, Outcome: "completed", Redacted: p.redacted,
		Refs:   []ChatRef{{GVR: gvrString(gvr), Namespace: ns, Name: name, UID: uid}},
		Detail: fmt.Sprintf("%d revisions held, %d pods listed", data.History.Held, data.Pods.Listed)}
	return t.finish(env, read)
}

// ownedBy: a pod whose controller is a ReplicaSet in the list owned by the
// workload (held or skipped), or one the list did not hold at all.
func ownedBy(pod *unstructured.Unstructured, list *unstructured.UnstructuredList, uid string) (string, bool) {
	owner := controllerUID(pod)
	for i := range list.Items {
		if string(list.Items[i].GetUID()) == owner {
			return owner, controllerUID(&list.Items[i]) == uid
		}
	}
	// a ReplicaSet outside the page: its pod joins nothing, and is counted
	return owner, true
}

// normTemplate is a pod template as compared: without the Deployment
// controller's hash label, a ControllerRevision's patch directive, or a
// null creation time.
func normTemplate(tm map[string]any) map[string]any {
	if tm == nil {
		return nil
	}
	c := runtimeDeepCopy(tm)
	delete(c, "$patch")
	if md, ok := c["metadata"].(map[string]any); ok {
		if l, ok := md["labels"].(map[string]any); ok {
			delete(l, rolloutHashLabel)
			if len(l) == 0 {
				delete(md, "labels")
			}
		}
		if v, ok := md["creationTimestamp"]; ok && v == nil {
			delete(md, "creationTimestamp")
		}
		if len(md) == 0 {
			delete(c, "metadata")
		}
	}
	return c
}

func runtimeDeepCopy(m map[string]any) map[string]any {
	return (&unstructured.Unstructured{Object: m}).DeepCopy().Object
}

// markers reports the controller's own revision markers, and the revision
// each names in this read.
func markers(kind string, w *unstructured.Unstructured, revs []*rolloutRev, data *chatRolloutData) {
	find := func(match func(*rolloutRev) bool) *rolloutRev {
		for _, r := range revs {
			if match(r) {
				return r
			}
		}
		return nil
	}
	add := func(marker, names string, r *rolloutRev, tag string) {
		m := chatRolloutMarker{Marker: marker, Names: names}
		if r == nil {
			m.Note = "not in the history read"
		} else {
			m.Revision = r.num
			r.markers = append(r.markers, tag)
		}
		data.Markers = append(data.Markers, m)
	}
	switch kind {
	case "Deployment":
		if v, ok := w.GetAnnotations()[rolloutRevisionAnn]; ok {
			n, _ := strconv.ParseInt(v, 10, 64)
			add("the Deployment's "+rolloutRevisionAnn+" annotation", "revision "+v, find(func(r *rolloutRev) bool { return r.num == n && n > 0 }), "named by the Deployment's revision annotation")
		}
	case "StatefulSet":
		for _, f := range []string{"updateRevision", "currentRevision"} {
			if v, _, _ := unstructured.NestedString(w.Object, "status", f); v != "" {
				add("status."+f, v, find(func(r *rolloutRev) bool { return r.obj.GetName() == v }), "status."+f)
			}
		}
	case "DaemonSet":
		m := chatRolloutMarker{Marker: "none", Names: "a DaemonSet's status names no revision"}
		if len(revs) > 0 {
			m.Revision = revs[0].num
			m.Note = "the highest retained in this read"
			revs[0].markers = append(revs[0].markers, "highest retained in this read")
		}
		data.Markers = append(data.Markers, m)
	}
	if data.GenerationLag != "" {
		for i := range data.Markers {
			data.Markers[i].Note = strings.TrimPrefix(data.Markers[i].Note+"; the controller has not observed the latest spec", "; ")
		}
	}
}

// templateChanges compares two revisions' pod templates as two objects of
// kind ReplicaSet in group apps, so chat's projection decides what is shown
// and every other difference is a path marker (A1 §3.2).
func templateChanges(older, newer *rolloutRev, ns string, p *projector) *chatRolloutChange {
	wrap := func(r *rolloutRev) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet",
			"metadata": map[string]any{"name": "revision", "namespace": ns}, "spec": map[string]any{"template": runtimeDeepCopy(r.tmpl)}}}
	}
	k := renderKey{group: "apps", kind: "ReplicaSet", ns: ns, name: "revision"}
	resolve := func(schema.GroupVersion, string) schema.GroupVersionResource { return replicaSetsGVR }
	visible, hidden, _ := objectChanges(wrap(older), wrap(newer), k, resolve, p)
	c := &chatRolloutChange{From: older.num, PathsTotal: len(visible) + len(hidden)}
	for _, v := range visible {
		if len(c.Visible)+len(c.Hidden) < renderPathsMax {
			c.Visible = append(c.Visible, v)
		}
	}
	for _, h := range hidden {
		if len(c.Visible)+len(c.Hidden) < renderPathsMax {
			c.Hidden = append(c.Hidden, h)
		}
	}
	return c
}

func revisionRow(r *rolloutRev, histObj string, verdicts *chatVerdicts, p *projector) chatRolloutRevision {
	row := chatRolloutRevision{Revision: r.num, Object: histObj + " " + p.str(r.obj.GetName(), chatStringMax), UID: string(r.obj.GetUID()),
		Markers: r.markers, Desired: r.desired, Partner: r.partner, Changes: r.changes, TemplateUnreadable: r.tmpl == nil}
	if ts := r.obj.GetCreationTimestamp(); !ts.IsZero() {
		row.Created = ts.UTC().Format("2006-01-02T15:04:05Z")
	}
	if _, ok := r.obj.GetAnnotations()[rolloutCauseAnn]; ok {
		row.ChangeCauseRecorded = true
	}
	if r.tmpl != nil {
		spec, _ := r.tmpl["spec"].(map[string]any)
		n := 0
		for _, key := range []string{"initContainers", "containers"} {
			cs, _ := spec[key].([]any)
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				name, _ := cm["name"].(string)
				img, _ := cm["image"].(string)
				if n < rolloutImagesMax {
					row.Images = append(row.Images, chatRolloutImage{Container: p.str(name, chatStringMax), Image: p.str(img, chatStringMax)})
				} else {
					row.ImagesMore++
				}
				n++
			}
		}
	}
	if histObj == "ReplicaSet" {
		reps := &chatRolloutReps{}
		reps.Desired, _, _ = unstructured.NestedInt64(r.obj.Object, "spec", "replicas")
		reps.Ready, _, _ = unstructured.NestedInt64(r.obj.Object, "status", "readyReplicas")
		reps.Available, _, _ = unstructured.NestedInt64(r.obj.Object, "status", "availableReplicas")
		if og, _, _ := unstructured.NestedInt64(r.obj.Object, "status", "observedGeneration"); og < r.obj.GetGeneration() {
			reps.StatusLag = fmt.Sprintf("its status is for generation %d; it is at %d", og, r.obj.GetGeneration())
		}
		row.Replicas = reps
	}
	row.Pods = podCounts(r, verdicts, p)
	return row
}

// podCounts splits a revision's listed pods by readiness and by the
// verdict evidence Klyx holds for each.
func podCounts(r *rolloutRev, verdicts *chatVerdicts, p *projector) chatRolloutRevPods {
	var c chatRolloutRevPods
	live := verdicts != nil && verdicts.byUID != nil && verdicts.problem == ""
	for _, pod := range r.pods {
		c.Listed++
		if podReady(pod) {
			c.Ready++
		}
		var v chatVerdict
		ok := verdicts != nil && verdicts.byUID != nil
		if ok {
			v, ok = verdicts.byUID[string(pod.GetUID())]
		}
		failing := ok && (v.Kind == "fault" || v.Kind == "degraded")
		switch {
		case !ok:
			c.Missing++
		case live && failing:
			c.CurrentFailing++
			if len(c.Failing) < rolloutFailNames {
				c.Failing = append(c.Failing, p.str(pod.GetName(), chatStringMax))
			}
		case live:
			c.CurrentNotFailing++
		case failing:
			c.RetainedFailing++
		default:
			c.RetainedNotFailing++
		}
	}
	return c
}

func podReady(pod *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(pod.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" && m["status"] == "True" {
			return true
		}
	}
	return false
}

// rolloutChecks are A1.1 §6's checks. None sets Failing: a rollout check
// attributes, and each carries its own sources' times and coverage.
func rolloutChecks(t *chatTools, data *chatRolloutData, revs []*rolloutRev, desired, partner *rolloutRev, verdicts *chatVerdicts, w *unstructured.Unstructured) []ChatCheck {
	subj := ChatCheckSubject{Kind: data.Kind, Namespace: data.Namespace, Name: data.Name, UID: data.UID, Identity: identityUID}
	who := subj.Kind + " " + data.Namespace + "/" + data.Name
	partial := ""
	if data.History.Partial {
		partial = " (" + data.History.Note + ")"
	}
	histObj := data.History.Object
	var out []ChatCheck
	fails := 0
	for _, r := range revs {
		if fails == rolloutFailChecks {
			break
		}
		c := podCounts(r, verdicts, &projector{})
		if c.CurrentFailing == 0 || data.Pods.VerdictsAt == "" {
			continue
		}
		fails++
		st := fmt.Sprintf("%d of the %d listed pods of %s's revision %d (%s %s) %s failing by Klyx's verdict", c.CurrentFailing, c.Listed, who, r.num, histObj, r.obj.GetName(), plural(c.CurrentFailing, "is", "are"))
		if c.Missing > 0 {
			st += fmt.Sprintf("; %d %s no verdict", c.Missing, plural(c.Missing, "has", "have"))
		}
		switch {
		case r.desired:
			st += fmt.Sprintf(". Revision %d matches the %s's template", r.num, data.Kind)
		case len(r.markers) > 0:
			st += fmt.Sprintf(". Revision %d is the one %s names", r.num, strings.Join(r.markers, " and "))
		}
		out = append(out, ChatCheck{Class: checkRollout, Subject: subj, Statement: st + partial, Observed: data.Pods.VerdictsAt, Coverage: "live"})
	}
	if partner != nil {
		c := podCounts(partner, verdicts, &projector{})
		podCov := "live"
		if data.Pods.Partial {
			podCov = "partial"
		}
		if c.Listed > 0 && c.Ready == c.Listed {
			out = append(out, ChatCheck{Class: checkRollout, Subject: subj, Observed: data.Pods.ReadAt, Coverage: podCov,
				Statement: fmt.Sprintf("%s's revision %d (%s %s), the nearest lower retained revision, has %d listed pods, all Ready%s", who, partner.num, histObj, partner.obj.GetName(), c.Listed, partial)})
		}
		if c.Listed > 0 && c.CurrentNotFailing == c.Listed && data.Pods.VerdictsAt != "" {
			out = append(out, ChatCheck{Class: checkRollout, Subject: subj, Observed: data.Pods.VerdictsAt, Coverage: "live",
				Statement: fmt.Sprintf("none of the %d listed pods of %s's revision %d (%s %s) is failing by Klyx's verdict%s", c.Listed, who, partner.num, histObj, partner.obj.GetName(), partial)})
		}
	}
	if desired != nil && partner != nil && desired.changes != nil {
		ch := desired.changes
		var parts []string
		for _, v := range ch.Visible {
			s := v.Path
			switch {
			case v.Note != "" && v.Old == "" && v.New == "":
				s += ", " + v.Note
			case v.Old != "" || v.New != "":
				s += ", " + v.Old + " to " + v.New
			}
			parts = append(parts, s)
		}
		for _, h := range ch.Hidden {
			parts = append(parts, h.Path+", "+h.Change)
		}
		more := ch.PathsTotal - len(parts)
		if len(parts) > rolloutStmtPaths {
			more += len(parts) - rolloutStmtPaths
			parts = parts[:rolloutStmtPaths]
		}
		diff := "no field"
		if len(parts) > 0 {
			diff = strings.Join(parts, "; ")
		}
		if more > 0 {
			diff += fmt.Sprintf("; +%d more", more)
		}
		out = append(out, ChatCheck{Class: checkRollout, Subject: subj, Observed: data.History.ReadAt, Coverage: "live",
			Statement: fmt.Sprintf("the pod template of %s's revision %d, which matches its current template, differs from revision %d, the nearest lower retained revision, in: %s. When revision %d became active is not in the data%s",
				who, desired.num, partner.num, diff, desired.num, partial)})
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// shrinkRollout keeps the result within budget: the unpinned revisions'
// changes go first, then the unpinned revisions, oldest first. When the
// pinned rows still do not fit, their changes go and the result says so.
func shrinkRollout(d *chatRolloutData, budget int) {
	size := func() int {
		b, _ := json.Marshal(d)
		return len(b)
	}
	if size() <= budget {
		return
	}
	for i := len(d.Revisions) - 1; i >= 0 && size() > budget; i-- {
		if !d.Revisions[i].Desired && !d.Revisions[i].Partner {
			d.Revisions[i].Changes = nil
		}
	}
	for i := len(d.Revisions) - 1; i >= 0 && size() > budget; i-- {
		if !d.Revisions[i].Desired && !d.Revisions[i].Partner {
			d.Revisions = append(d.Revisions[:i], d.Revisions[i+1:]...)
		}
	}
	if size() > budget {
		for i := range d.Revisions {
			d.Revisions[i].Changes = nil
			d.Revisions[i].Images, d.Revisions[i].ImagesMore = nil, 0
		}
		d.PinnedNotFit = true
	}
}

// anchorReplaced: this workload is the investigation's anchor by name, and
// not by UID (A1's anchor rule).
func (t *chatTools) anchorReplaced(gvr schema.GroupVersionResource, ns, name, uid string) bool {
	a := t.anchor
	return a != nil && a.Kind == "object" && a.Group == gvr.Group && a.Plural == gvr.Resource && a.Namespace == ns && a.Name == name && a.UID != uid
}
