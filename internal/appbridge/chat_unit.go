package appbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A1 §3.1: gitops_unit reads one Flux Kustomization or HelmRelease live,
// with Klyx's verdict for it, and parses a failed health check strictly
// (flux.ParseHealthCheck) from the raw Ready message, in process. Each object
// the health check names joins the unit's inventory, or its
// spec.healthChecks, by kind, namespace and name: by name, never by UID. The
// object of that name now is read as a separate fact, and says when it was
// created after the failure began. Nothing unparsed names an object.

const (
	toolUnit = "gitops_unit"

	chatUnitInventoryMax = 100 // inventory entries sent, counted beyond
	chatUnitEntriesMax   = 50  // health-check entries sent, counted beyond
	chatUnitCurrentMax   = 5   // named objects read as they are now
)

// The join of a health-check entry.
const (
	joinInventory    = "inventory"
	joinHealthChecks = "spec.healthChecks"
	joinAmbiguous    = "ambiguous: the inventory holds this kind and name in more than one API group"
	joinMissing      = "not in the inventory"
	joinNoInventory  = "the unit has no inventory"
)

// chatUnitCurrent is the object of a health-check entry's name now: a
// separate fact, never the entry's identity.
type chatUnitCurrent struct {
	GVR     string       `json:"gvr"`
	UID     string       `json:"uid,omitempty"`
	Created string       `json:"created,omitempty"`
	Verdict *chatVerdict `json:"verdict,omitempty"`
	Caveat  string       `json:"caveat,omitempty"`
	Error   string       `json:"error,omitempty"`
}

type chatUnitEntry struct {
	Kind         string           `json:"kind"`
	Namespace    string           `json:"namespace,omitempty"`
	Name         string           `json:"name"`
	Status       string           `json:"status,omitempty"`
	Undetermined bool             `json:"undetermined,omitempty"`
	ErrorUnread  bool             `json:"errorUnparsed,omitempty"`
	Join         string           `json:"join"`
	Group        string           `json:"group,omitempty"`
	Current      *chatUnitCurrent `json:"current,omitempty"`
}

type chatUnitHealth struct {
	Failure string `json:"failure"` // timeout | stalled
	// From is where the failure was read; FirstFailed, when this revision
	// first failed its health check, when the evidence says
	From        string          `json:"from"`
	FirstFailed string          `json:"firstFailed,omitempty"`
	Entries     []chatUnitEntry `json:"entries"`
	Total       int             `json:"total"`
	Unparsed    bool            `json:"unparsed,omitempty"`
	Note        string          `json:"note"`
}

type chatUnitReady struct {
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Since   string `json:"since,omitempty"`
	Message string `json:"message,omitempty"`
}

type chatUnitData struct {
	Kind               string          `json:"kind"`
	Namespace          string          `json:"namespace"`
	Name               string          `json:"name"`
	UID                string          `json:"uid"`
	Generation         int64           `json:"generation,omitempty"`
	ObservedGeneration int64           `json:"observedGeneration,omitempty"`
	Suspended          bool            `json:"suspended,omitempty"`
	Ready              *chatUnitReady  `json:"ready,omitempty"`
	AppliedRevision    string          `json:"lastAppliedRevision,omitempty"`
	AttemptedRevision  string          `json:"lastAttemptedRevision,omitempty"`
	Source             string          `json:"source,omitempty"`
	DependsOn          []string        `json:"dependsOn,omitempty"`
	Verdict            *chatVerdict    `json:"verdict,omitempty"`
	HealthCheck        *chatUnitHealth `json:"healthCheck,omitempty"`
	Inventory          []string        `json:"inventory,omitempty"`
	InventoryTotal     int             `json:"inventoryTotal"`
}

// unitResources are the two kinds gitops_unit reads, by Kind.
var unitResources = map[string]string{"Kustomization": "kustomizations", "HelmRelease": "helmreleases"}

func (t *chatTools) unit(ctx context.Context, kind, ns, name string) ChatToolResult {
	target := kind + " " + ns + "/" + name
	raw, err := t.conn.FluxObject(ctx, kind, ns, name)
	if err != nil {
		return t.failed(toolUnit, "api", target, err)
	}
	if raw.GetKind() != kind {
		return t.refuse(toolUnit, target, fmt.Errorf("the object read is a %s, not a %s", label(raw.GetKind()), kind))
	}
	gv, err := schema.ParseGroupVersion(raw.GetAPIVersion())
	if err != nil {
		return t.failed(toolUnit, "api", target, err)
	}
	gvr := gv.WithResource(unitResources[kind])
	// coverage S0b §3: the sanitiser runs before anything is read off it
	u := &unstructured.Unstructured{Object: crd.SanitizeEmbedded(gvr.Group, kind, raw.Object).Object}
	p := &projector{}
	d := flux.ParseDetail(u)
	uid := string(u.GetUID())
	data := chatUnitData{Kind: kind, Namespace: ns, Name: name, UID: uid, Generation: u.GetGeneration(),
		Suspended: d.Suspended, AppliedRevision: p.str(d.AppliedRevision, chatStringMax), AttemptedRevision: p.str(d.AttemptedRevision, chatStringMax),
		InventoryTotal: len(d.Inventory)}
	data.ObservedGeneration, _, _ = unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if src, ok := flux.BoundSource(u); ok {
		data.Source = p.str(strings.TrimSpace(src.Kind+" "+src.Namespace+"/"+src.Name), chatStringMax)
	}
	for _, dep := range d.DependsOn {
		data.DependsOn = append(data.DependsOn, p.str(strings.TrimPrefix(dep.Namespace+"/"+dep.Name, "/"), chatStringMax))
	}
	for i, e := range d.Inventory {
		if i == chatUnitInventoryMax {
			break
		}
		data.Inventory = append(data.Inventory, p.str(inventoryText(e), chatStringMax))
	}
	env := chatEnvelope{Tool: toolUnit, Source: "api", ReadAt: t.stamp(), Complete: true,
		Identity: &chatIdentity{GVR: gvrString(gvr), Namespace: ns, Name: name, UID: uid, Generation: data.Generation}}
	if data.Generation > data.ObservedGeneration {
		env.Note = fmt.Sprintf("The controller has not yet reconciled generation %d (it last observed %d): the status describes an earlier spec.", data.Generation, data.ObservedGeneration)
	}
	subj := ChatCheckSubject{Kind: kind, Namespace: ns, Name: name, UID: uid, Identity: identityUID}
	checks := conditionChecks(u, subj, env.ReadAt, p)

	// the raw conditions, read in process: a failure message is parsed here
	// and crosses only as the kind's message rule passes it, scrubbed
	for _, c := range d.Conditions {
		if c.Type == "Ready" {
			data.Ready = &chatUnitReady{Status: c.Status, Reason: p.str(c.Reason, chatCheckReasonMax), Message: p.str(c.Message, chatStringMax)}
			data.Ready.Since, _, _, _ = conditionRaw(u, "Ready")
		}
	}
	refs := []ChatRef{{GVR: gvrString(gvr), Namespace: ns, Name: name, UID: uid}}
	var current []ChatCheck
	if src, ok := t.healthFailure(ctx, u, ns, uid); ok {
		if hc, ok := flux.ParseHealthCheck(src.msg); ok {
			var hcChecks []ChatCheck
			data.HealthCheck, hcChecks, current, refs = t.healthCheck(ctx, u, d, hc, subj, src, p, refs)
			checks = append(checks, hcChecks...)
		}
	}
	if vs := t.verdictsFor(ctx, gvr, &p.redacted); vs != nil {
		if v, ok := vs.byUID[uid]; ok {
			data.Verdict = &v
			if c := verdictCheck(subj, v, p); c != nil {
				checks = append(checks, *c)
			}
		}
		if vs.problem != "" {
			env.Errors = append(env.Errors, vs.problem)
		}
	}
	// the unit's verdict before the named objects' (capChecks is stable)
	checks = append(checks, current...)
	env.Data = data
	env.Checks = capChecks(checks)
	return t.finish(env, ChatRead{Tool: toolUnit, Target: target, Outcome: "completed", Redacted: p.redacted, Refs: capRefs(refs)})
}

// conditionRaw is one condition's last transition, status, reason and raw
// message.
func conditionRaw(u *unstructured.Unstructured, typ string) (since, status, reason, msg string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		if m, ok := c.(map[string]any); ok && m["type"] == typ {
			since, _ = m["lastTransitionTime"].(string)
			status, _ = m["status"].(string)
			reason, _ = m["reason"].(string)
			msg, _ = m["message"].(string)
			return since, status, reason, msg
		}
	}
	return "", "", "", ""
}

// healthSource is where a unit's failed health check was read, and when.
type healthSource struct {
	msg string
	// from is said with the result: a condition, or Flux's event while the
	// unit retries
	from string
	// observed is when the evidence describes the world: the read for a
	// condition, the event's time for an event
	observed string
	// onset is when this revision first failed its health check, when the
	// evidence says
	onset time.Time
}

// healthFailure finds a unit's failed health check. While Flux retries, its
// Ready and Healthy conditions read Unknown, Progressing for the whole
// health-check run and the message is gone; Flux's own HealthCheckFailed
// event about this unit, by its UID, still holds it (AI A1 lab, kustomize-
// controller v1.9.6). When this revision first failed comes from
// status.history, else the condition's transition, else the event.
func (t *chatTools) healthFailure(ctx context.Context, u *unstructured.Unstructured, ns, uid string) (healthSource, bool) {
	var src healthSource
	first := firstFailedReconcile(u)
	for _, typ := range []string{"Ready", "Healthy"} {
		since, status, reason, msg := conditionRaw(u, typ)
		if status == "False" && reason == "HealthCheckFailed" && msg != "" {
			src = healthSource{msg: msg, from: "its " + typ + " condition", observed: t.stamp()}
			src.onset, _ = time.Parse(time.RFC3339, since)
			if !first.IsZero() {
				src.onset = first
			}
			return src, true
		}
	}
	evs, _, err := t.conn.ListEvents(ctx, ns)
	if err != nil {
		return src, false
	}
	var newest *workloads.EventSummary
	for i := range evs {
		e := &evs[i]
		if e.Type == "Warning" && e.Reason == "HealthCheckFailed" && e.UID == uid && uid != "" && (newest == nil || e.LastSeenUnix > newest.LastSeenUnix) {
			newest = e
		}
	}
	if newest == nil {
		return src, false
	}
	at := time.Unix(newest.LastSeenUnix, 0).UTC()
	src = healthSource{msg: newest.Message, from: fmt.Sprintf("Flux's HealthCheckFailed event at %s, while the unit retries", rfcOrEmpty(at)), observed: rfcOrEmpty(at), onset: first}
	if src.onset.IsZero() && newest.FirstSeenUnix > 0 {
		src.onset = time.Unix(newest.FirstSeenUnix, 0).UTC()
	}
	return src, true
}

// firstFailedReconcile is when the attempted revision first failed its
// health check, from status.history; zero when it does not say.
func firstFailedReconcile(u *unstructured.Unstructured) time.Time {
	attempted, _, _ := unstructured.NestedString(u.Object, "status", "lastAttemptedRevision")
	hist, _, _ := unstructured.NestedSlice(u.Object, "status", "history")
	var first time.Time
	for _, h := range hist {
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		rev, _, _ := unstructured.NestedString(m, "metadata", "revision")
		status, _ := m["lastReconciledStatus"].(string)
		at, _ := m["firstReconciled"].(string)
		tm, err := time.Parse(time.RFC3339, at)
		if rev == attempted && attempted != "" && status == "HealthCheckFailed" && err == nil && (first.IsZero() || tm.Before(first)) {
			first = tm
		}
	}
	return first
}

func inventoryText(e flux.InventoryEntry) string {
	k := e.Kind
	if e.Group != "" {
		k = e.Group + "/" + e.Kind
	}
	return k + " " + strings.TrimPrefix(e.Namespace+"/"+e.Name, "/")
}

// healthCheck builds the parsed entries, each joined by name, with their
// healthcheck checks about the unit and the current-object facts.
func (t *chatTools) healthCheck(ctx context.Context, u *unstructured.Unstructured, d flux.Detail, hc flux.HealthCheck, subj ChatCheckSubject,
	src healthSource, p *projector, refs []ChatRef) (*chatUnitHealth, []ChatCheck, []ChatCheck, []ChatRef) {
	onset := src.onset
	out := &chatUnitHealth{Failure: "timeout", Total: len(hc.Entries), Unparsed: hc.Unparsed, From: src.from, FirstFailed: rfcOrEmpty(onset),
		Note: "Each entry names an object by kind, namespace and name: by name. current is the object of that name now, a separate fact."}
	failure := "timed out"
	if hc.Stalled {
		out.Failure, failure = "stalled", "failed early on stalled resources"
	}
	if hc.Unparsed {
		out.Note += " Part of the message (an entry's error, or text after it) was not parsed: it names no object here."
	}
	declared := declaredHealthChecks(u)
	var checks, current []ChatCheck
	verdicts := map[string]*chatVerdicts{}
	read := 0
	for i, e := range hc.Entries {
		if i == chatUnitEntriesMax {
			break
		}
		ce := chatUnitEntry{Kind: e.Kind, Namespace: e.Namespace, Name: e.Name, Status: e.Status, Undetermined: e.Undetermined, ErrorUnread: e.HasError}
		var known bool
		ce.Join, ce.Group, known = joinEntry(e, d.Inventory, declared)
		named := ChatCheckSubject{Kind: e.Kind, Namespace: e.Namespace, Name: e.Name, Identity: identityName}
		status := "status " + e.Status
		if e.Undetermined {
			status = "status undetermined"
		}
		if e.HasError {
			status += ", with an error Klyx does not parse"
		}
		st := fmt.Sprintf("%s reports a failed health check (%s) naming %s, %s", subj.words(), failure, named.words(), status)
		if !known {
			st += "; " + ce.Join
		}
		st += " (from " + src.from + ")"
		checks = append(checks, ChatCheck{Class: checkHealthCheck, Subject: subj, Statement: st, Observed: src.observed, Onset: rfcOrEmpty(onset), Coverage: "live"})
		if known && e.Status != "Current" && read < chatUnitCurrentMax {
			read++
			cur, c, ref := t.currentOf(ctx, e, ce.Group, onset, verdicts, p)
			ce.Current = cur
			if c != nil {
				current = append(current, *c)
			}
			if ref != nil {
				refs = append(refs, *ref)
			}
		}
		out.Entries = append(out.Entries, ce)
	}
	return out, checks, current, refs
}

type declaredCheck struct{ group, kind, namespace, name string }

// declaredHealthChecks reads a Kustomization's spec.healthChecks, whose
// apiVersion gives each its group.
func declaredHealthChecks(u *unstructured.Unstructured) []declaredCheck {
	hcs, _, _ := unstructured.NestedSlice(u.Object, "spec", "healthChecks")
	var out []declaredCheck
	for _, h := range hcs {
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		var dc declaredCheck
		av, _ := m["apiVersion"].(string)
		if gv, err := schema.ParseGroupVersion(av); err == nil {
			dc.group = gv.Group
		}
		dc.kind, _ = m["kind"].(string)
		dc.namespace, _ = m["namespace"].(string)
		dc.name, _ = m["name"].(string)
		out = append(out, dc)
	}
	return out
}

// joinEntry joins an entry to the inventory, then to spec.healthChecks, by
// kind, namespace and name. A kind and name the inventory holds in two
// groups is ambiguous: not joined. known reports a single group.
func joinEntry(e flux.HealthCheckEntry, inv []flux.InventoryEntry, declared []declaredCheck) (join, group string, known bool) {
	groups := map[string]bool{}
	for _, ie := range inv {
		if ie.Kind == e.Kind && ie.Namespace == e.Namespace && ie.Name == e.Name {
			groups[ie.Group] = true
			group = ie.Group
		}
	}
	switch {
	case len(groups) == 1:
		return joinInventory, group, true
	case len(groups) > 1:
		return joinAmbiguous, "", false
	}
	for _, dc := range declared {
		if dc.kind == e.Kind && dc.namespace == e.Namespace && dc.name == e.Name {
			return joinHealthChecks, dc.group, true
		}
	}
	if len(inv) == 0 {
		return joinNoInventory, "", false
	}
	return joinMissing, "", false
}

// currentOf reads the object of an entry's name now, in its joined group,
// and states Klyx's verdict for it by its current UID, with the replacement
// caveat when it was created after the unit's Ready became False.
func (t *chatTools) currentOf(ctx context.Context, e flux.HealthCheckEntry, group string, onset time.Time, verdicts map[string]*chatVerdicts, p *projector) (*chatUnitCurrent, *ChatCheck, *ChatRef) {
	cur := &chatUnitCurrent{}
	ks, _, err := t.conn.ChatResolveKind(ctx, e.Kind, group)
	var k []int
	for i := range ks {
		if ks[i].GVR.Group == group && ks[i].Kind == e.Kind {
			k = append(k, i)
		}
	}
	switch {
	case err != nil:
		cur.Error = chatScrub(readError(err), &p.redacted)
		return cur, nil, nil
	case len(k) != 1:
		cur.Error = "the kind is not served in that group, or is ambiguous: not read"
		return cur, nil, nil
	}
	kind := ks[k[0]]
	cur.GVR = gvrString(kind.GVR)
	if kind.Namespaced != (e.Namespace != "") {
		cur.Error = "the entry's scope does not match the served kind: not read"
		return cur, nil, nil
	}
	obj, err := t.conn.ChatGet(ctx, kind.GVR, e.Namespace, e.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			cur.Error = "no object of that name now"
		} else {
			cur.Error = chatScrub(readError(err), &p.redacted)
		}
		return cur, nil, nil
	}
	cur.UID = string(obj.GetUID())
	created := obj.GetCreationTimestamp().Time
	cur.Created = rfcOrEmpty(created)
	switch {
	case onset.IsZero():
		cur.Caveat = "the health check's time is unknown: this may be a replacement of the object it named"
	case created.After(onset):
		cur.Caveat = fmt.Sprintf("created at %s, after the unit's Ready became False at %s: possibly a replacement of the object the health check named", cur.Created, rfcOrEmpty(onset))
	}
	ref := &ChatRef{GVR: cur.GVR, Namespace: e.Namespace, Name: e.Name, UID: cur.UID}
	key := cur.GVR
	vs, ok := verdicts[key]
	if !ok {
		vs = t.verdictsFor(ctx, kind.GVR, &p.redacted)
		verdicts[key] = vs
	}
	if vs == nil {
		return cur, nil, ref
	}
	if vs.problem != "" {
		cur.Error = vs.problem
	}
	v, ok := vs.byUID[cur.UID]
	if !ok {
		return cur, nil, ref
	}
	v.Kinds = nil // the unit's own verdict carries the part's kinds
	cur.Verdict = &v
	subj := ChatCheckSubject{Kind: e.Kind, Namespace: e.Namespace, Name: e.Name, UID: cur.UID, Identity: identityUID}
	c := verdictCheck(subj, v, p)
	if c != nil {
		c.Statement = strings.Replace(c.Statement, subj.words()+":", subj.words()+", the object of that name now:", 1)
		if cur.Caveat != "" {
			c.Statement += " (" + cur.Caveat + ")"
		}
	}
	return cur, c, ref
}

var errUnitKind = errors.New("kind must be Kustomization or HelmRelease")
