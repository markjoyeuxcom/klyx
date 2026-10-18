package appbridge

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/healthsrc"
)

// The Explain prompt contract (spec 2026-10-13 §4.2). Exactly the fields
// declared here cross into a prompt; every string among them is scrubbed of
// credential shapes and capped; the whole document is capped; and the model
// is told all of it is untrusted data written by the cluster.

// evidenceTotalMax caps the serialized evidence document.
const evidenceTotalMax = 24 << 10 // 24 KiB

// evidenceListMax caps every list in the evidence (events, related objects,
// a lens field's array).
const evidenceListMax = 10

// allow is an allowlist node. A key mapped to nil admits a scalar (or a list
// of scalars); a key mapped to a non-nil allow admits an object - or a list of
// objects - holding only the listed keys. Anything not listed is dropped.
type allow map[string]allow

var (
	condAllow  = allow{"status": nil, "reason": nil, "message": nil, "observedGeneration": nil}
	argoCond   = allow{"type": nil, "status": nil, "reason": nil, "message": nil}
	attachment = allow{"parent": nil, "rule": allow{"included": nil, "role": nil, "label": nil}}
)

// aiEvidenceFields is the allowlist of lens row fields per part (the lens's
// own DTO, from the same capture as Klyx's verdict). Identity (uid, gvr), the
// evaluation (health, rule - carried separately as the anchor) and anything
// not needed to explain a state (pod IPs, ownership UIDs) are left out.
var aiEvidenceFields = map[string]allow{
	healthsrc.TaskCerts + "/certificates": {"ready": nil, "reason": nil, "message": nil, "issuerKind": nil, "issuerName": nil, "secretName": nil,
		"dnsNames": nil, "notAfterUnix": nil, "expiresInSeconds": nil, "renewalUnix": nil, "stale": nil, "generation": nil, "observedGeneration": nil},
	healthsrc.TaskCerts + "/issuers": {"kind": nil, "ready": nil, "reason": nil, "message": nil, "generation": nil},
	healthsrc.TaskFlux + "/reconciliations": {"kind": nil, "ready": nil, "reason": nil, "message": nil, "revision": nil, "readySinceUnix": nil, "stale": nil,
		"generation": nil, "observedGeneration": nil, "suspended": nil, "sourceKind": nil, "sourceName": nil, "sourceNamespace": nil},
	healthsrc.TaskFlux + "/sources": {"kind": nil, "ready": nil, "reason": nil, "message": nil, "revision": nil, "url": nil, "suspended": nil, "generation": nil},
	healthsrc.TaskArgo + "/applications": {"project": nil, "syncStatus": nil, "healthStatus": nil, "broken": nil, "revision": nil, "repoURL": nil, "path": nil,
		"chart": nil, "targetRevision": nil, "destNamespace": nil, "destServer": nil, "destName": nil, "applicationSet": nil, "autoSync": nil,
		"opPhase": nil, "opMessage": nil, "conditions": argoCond,
		"resources": allow{"group": nil, "kind": nil, "namespace": nil, "name": nil, "status": nil, "health": nil}},
	healthsrc.TaskArgo + "/applicationsets": {"generators": nil, "templateName": nil, "project": nil, "repoURL": nil, "path": nil, "targetRevision": nil,
		"destNamespace": nil, "generatedApps": nil, "healthy": nil, "conditions": argoCond},
	healthsrc.TaskKarpenter + "/nodepools": {"ready": nil, "reason": nil, "message": nil, "weight": nil, "cpuLimit": nil, "cpuUsed": nil, "memLimit": nil,
		"memUsed": nil, "nodes": nil},
	healthsrc.TaskKarpenter + "/nodeclaims": {"pool": nil, "nodeName": nil, "ready": nil, "failingType": nil, "failingStatus": nil, "failingReason": nil,
		"failingMessage": nil, "drifted": nil, "consolidatable": nil, "capacityType": nil, "instanceType": nil, "ageSeconds": nil},
	healthsrc.TaskGateway + "/gateways": {"className": nil, "listeners": allow{"name": nil, "protocol": nil, "hostname": nil, "port": nil},
		"accepted": nil, "programmed": nil, "stale": nil, "generation": nil, "observedGeneration": nil, "acceptedCond": condAllow, "programmedCond": condAllow},
	healthsrc.TaskGateway + "/routes": {"parent": nil, "attachments": attachment, "generation": nil},
	healthsrc.TaskNodes + "/nodes": {"roles": nil, "ready": nil, "unschedulable": nil, "problems": nil, "version": nil, "os": nil, "arch": nil,
		"taintCount": nil, "cpuCapacity": nil, "cpuAllocatable": nil, "memCapacity": nil, "memAllocatable": nil, "podCapacity": nil,
		"ageSeconds": nil, "pool": nil, "zone": nil},
	healthsrc.TaskPods + "/pods": {"ownerKind": nil, "ownerName": nil, "node": nil, "ready": nil, "restarts": nil,
		"containers": allow{"name": nil, "init": nil, "ready": nil, "restarts": nil, "state": nil}},
	healthsrc.TaskWorkloads + "/workloads": {"kind": nil, "desired": nil, "ready": nil, "available": nil, "updated": nil, "restarts": nil,
		"reason": nil, "stale": nil, "generation": nil, "observedGeneration": nil, "paused": nil,
		"pods":   allow{"name": nil, "ready": nil, "restarts": nil, "reason": nil, "node": nil, "ageSeconds": nil},
		"gitops": allow{"kind": nil, "namespace": nil, "name": nil}},
	healthsrc.TaskESO + "/externalsecrets": {"state": nil, "reason": nil, "message": nil, "storeKind": nil, "storeName": nil, "targetSecret": nil,
		"refreshIntervalSeconds": nil, "lastRefreshAgeSeconds": nil, "refreshed": nil, "stale": nil, "generation": nil},
	healthsrc.TaskESO + "/stores": {"kind": nil, "state": nil, "reason": nil, "message": nil, "provider": nil, "generation": nil},
	// Coverage S1b §6: verdicts, reasons and messages (after the rules),
	// images, tags and digests, policy type and range, times and commit
	// SHAs; never the author's email or the selector.
	healthsrc.TaskFlux + "/images": {"kind": nil,
		"repository": allow{"ready": nil, "reason": nil, "message": nil, "image": nil, "canonicalImage": nil, "provider": nil, "interval": nil,
			"scanned": nil, "tagCount": nil, "scanTime": nil, "latestTags": nil, "suspended": nil, "generation": nil, "observedGeneration": nil},
		"policy": allow{"ready": nil, "reason": nil, "message": nil, "repositoryNamespace": nil, "repositoryName": nil, "policyType": nil,
			"policyValue": nil, "digestReflection": nil, "latest": imageRefAllow, "previous": imageRefAllow, "blockedBy": nil, "suspended": nil,
			"generation": nil, "observedGeneration": nil},
		"automation": allow{"ready": nil, "reason": nil, "message": nil, "sourceKind": nil, "sourceName": nil, "sourceNamespace": nil,
			"interval": nil, "pushBranch": nil, "updateStrategy": nil, "updatePath": nil, "lastRun": nil, "lastPushTime": nil,
			"lastPushCommit": nil, "observedSourceRevision": nil, "suspended": nil, "generation": nil, "observedGeneration": nil,
			"uses": allow{"namespace": nil, "name": nil, "resolved": imageRefAllow, "observed": imageRefAllow, "state": nil}}},
	// Coverage S1c §6: kinds, verdicts, reasons and messages (after the
	// rules), names, revisions and digests, an artifact's origin (scheme
	// and host) and the producer block; never a URL path, producer
	// metadata, a webhook path or a Secret's name.
	healthsrc.TaskFlux + "/composition": {"kind": nil,
		"generator": allow{"ready": nil, "reason": nil, "message": nil, "suspended": nil, "resume": nil, "generation": nil, "readyGeneration": nil,
			"sources": allow{"kind": nil, "alias": nil, "namespace": nil, "name": nil}, "outputs": allow{"name": nil, "revision": nil},
			"generated": allow{"namespace": nil, "name": nil, "found": nil, "label": nil, "role": nil}, "generatedState": nil},
		"artifact": allow{"ready": nil, "reason": nil, "message": nil, "hasArtifact": nil, "revision": nil, "digest": nil, "origin": nil,
			"originRevision": nil, "lastUpdate": nil, "size": nil, "sourceRef": allow{"kind": nil, "namespace": nil, "name": nil},
			"producer": allow{"state": nil, "kind": nil, "namespace": nil, "name": nil, "label": nil, "role": nil, "failing": nil}}},
	healthsrc.TaskFlux + "/receivers": {"type": nil, "ready": nil, "reason": nil, "suspended": nil, "events": nil, "meaning": nil, "stalled": nil,
		"resources": allow{"kind": nil, "name": nil, "namespace": nil}, "resourceCount": nil, "filterCount": nil,
		"generation": nil, "observedGeneration": nil, "readyGeneration": nil},
	// Coverage S2a §6: kinds, verdicts, phases, the cron and zone, times,
	// counts, a location's name, provider, bucket and prefix, and Klyx's
	// classes; never a config value, a Secret's name, a hook or a selector.
	healthsrc.TaskVelero + "/schedules": {"cron": nil, "zone": nil, "library": nil, "paused": nil, "phase": nil, "useOwnerReferences": nil,
		"lastBackup": nil, "lastSkipped": nil, "due": nil, "dueZero": nil, "dueUnknown": nil, "validation": nil, "nState": nil,
		"n": veleroRunAllow, "deciding": veleroRunAllow, "previous": veleroRunAllow, "previousNamed": nil, "blocker": veleroRunAllow,
		"runs": veleroRunAllow, "newer": veleroRunAllow, "synced": nil, "resumed": nil, "reason": nil,
		"template": allow{"storageLocation": nil, "ttl": nil, "snapshotVolumes": nil, "defaultVolumesToFsBackup": nil, "snapshotMoveData": nil,
			"includedCount": nil, "excludedCount": nil, "hooks": nil},
		"location": allow{"state": nil, "name": nil, "byDefault": nil, "label": nil, "role": nil, "validated": nil},
		// S2b §5: the horizon, with its wording, so its confidence travels
		"horizon": allow{"state": nil, "success": veleroRunAllow, "at": nil, "proven": nil, "untimed": nil, "orderUnknown": nil,
			"incomplete": nil, "window": nil, "tier0": nil, "partial": veleroRunAllow, "partialProven": nil, "partialOrderUnknown": nil,
			"expired": nil, "nextExpiry": nil, "text": nil, "partialText": nil}},
	healthsrc.TaskVelero + "/locations": {"provider": nil, "bucket": nil, "prefix": nil, "default": nil, "accessMode": nil, "phase": nil,
		"lastValidation": nil, "lastSynced": nil, "validationFrequency": nil, "syncPeriod": nil, "message": nil, "messageWithheld": nil,
		"configCount": nil, "schedules": allow{"name": nil, "label": nil, "role": nil}, "schedulesState": nil, "expired": nil,
		"expiredState": nil, "gcFailures": nil, "severalDefaults": nil, "reason": nil},
	healthsrc.TaskVelero + "/backups": {"phase": nil, "created": nil, "started": nil, "completed": nil, "expires": nil, "scheduleLabel": nil,
		"storageLocation": nil, "errors": nil, "warnings": nil, "itemsTotal": nil, "itemsBackedUp": nil, "failure": nil, "validation": nil,
		"validationCount": nil, "hooksFailed": nil, "operationsFailed": nil, "membership": nil, "disputed": nil, "reason": nil,
		// S2b §5: whether the row is counted, and its attribution
		"counted": nil, "schedule": nil, "attribution": nil, "attributionDisputed": nil, "attributionCoverage": nil, "expired": nil, "gcFailure": nil},
}

// Coverage S3a §8: names, kinds, roles, failure policies, backend states and
// counts; never a URL, a rule list, an expression or a warning's text.
var admissionBindingAllow = allow{"name": nil, "policyName": nil, "policyState": nil, "actions": nil,
	"paramRef": allow{"name": nil, "namespace": nil, "selector": nil, "notFoundAction": nil}}

func init() {
	aiEvidenceFields[healthsrc.TaskAdmission+"/webhooks"] = allow{"kind": nil, "webhookCount": nil, "cut": nil, "unavailable": nil, "unverified": nil, "reason": nil,
		"webhooks": allow{"name": nil, "failurePolicy": nil, "failClosed": nil, "sideEffects": nil, "timeoutSeconds": nil, "serviceNamespace": nil,
			"serviceName": nil, "servicePort": nil, "url": nil, "caBundle": nil, "ruleCount": nil, "namespaceSelector": nil, "objectSelector": nil,
			"matchConditions": nil, "role": nil, "reason": nil,
			"backend": allow{"state": nil, "label": nil, "reason": nil, "endpointRouting": nil, "clusterRouting": nil, "terminating": nil, "selectorless": nil}}}
	aiEvidenceFields[healthsrc.TaskAdmission+"/policies"] = allow{"entity": nil, "kind": nil, "failurePolicy": nil, "failClosed": nil,
		"paramKind": allow{"apiVersion": nil, "kind": nil, "state": nil, "namespaced": nil}, "matchRuleCount": nil, "validations": nil, "mutations": nil,
		"variables": nil, "matchConditions": nil, "auditAnnotations": nil, "behind": nil, "warningCount": nil, "warningFields": nil,
		"bindings": admissionBindingAllow, "binding": admissionBindingAllow, "reason": nil}
}

var veleroRunAllow = allow{"name": nil, "phase": nil, "at": nil, "created": nil, "started": nil, "completed": nil, "proven": nil,
	"timing": nil, "errors": nil, "warnings": nil, "class": nil}

var imageRefAllow = allow{"name": nil, "tag": nil, "digest": nil, "text": nil}

// helmEvidenceFields is the allowlist for a Helm release (it has no row).
var helmEvidenceFields = allow{"chart": nil, "appVersion": nil, "status": nil, "revision": nil, "updated": nil,
	"history": allow{"revision": nil, "status": nil, "updated": nil, "description": nil}}

// scrubber counts what was scrubbed while projecting.
type scrubber struct{ n int }

func (sc *scrubber) text(s string) string {
	out, n := ai.ScrubEvidence(s, ai.EvidenceFieldMax)
	sc.n += n
	return out
}

// project keeps only the allowed keys of v (decoded JSON), scrubbing every
// string and capping every list.
func (sc *scrubber) project(v any, a allow) any {
	switch t := v.(type) {
	case map[string]any:
		if a == nil {
			return nil // an object where a scalar was declared: dropped
		}
		out := map[string]any{}
		for k, sub := range a {
			if val, ok := t[k]; ok && val != nil {
				if p := sc.project(val, sub); p != nil {
					out[k] = p
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		var out []any
		for _, el := range t {
			if len(out) == evidenceListMax {
				sc.n++
				break
			}
			if p := sc.project(el, a); p != nil {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if a != nil {
			return nil
		}
		return sc.text(t)
	default: // numbers and booleans
		if a != nil {
			return nil
		}
		return t
	}
}

// evidenceVerdict is Klyx's own conclusion for the object - the anchor the
// model is asked to explain.
type evidenceVerdict struct {
	Classifier string `json:"classifier"` // verdict | native
	// verdict parts
	Kind    string `json:"kind,omitempty"`
	Label   string `json:"label,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Rule    string `json:"rule,omitempty"`
	// native parts: the presentation role and, separately, whether the lens
	// counts the object for attention. Included never means "failed".
	Role                string `json:"role,omitempty"`
	IncludedInAttention *bool  `json:"includedInAttention,omitempty"`
	EvaluatedAt         string `json:"evaluatedAt"`
	Generation          int64  `json:"generation,omitempty"`
}

// AIAnchorDTO is where Klyx's verdict came from (spec §4.1): the capture's
// identity and its coverage, so a reader can tell a live verdict from a
// last-observed one, and two captures apart.
type AIAnchorDTO struct {
	Source      string          `json:"source"` // observer | helm-read | none
	Task        string          `json:"task,omitempty"`
	Part        string          `json:"part,omitempty"`
	Incarnation uint64          `json:"incarnation,omitempty"`
	Ver         uint64          `json:"ver,omitempty"`
	UID         string          `json:"uid,omitempty"`
	GVR         string          `json:"gvr,omitempty"`
	Generation  int64           `json:"generation,omitempty"`
	EvaluatedAt string          `json:"evaluatedAt,omitempty"`
	Coverage    string          `json:"coverage,omitempty"`
	Kinds       []AICoverageDTO `json:"kinds,omitempty"`
	// FetchedAt is when the object's conditions, facts and events were read -
	// independently of, and after, the capture.
	FetchedAt string `json:"fetchedAt,omitempty"`
}

// AICoverageDTO is one kind's coverage in the capture.
type AICoverageDTO struct {
	Kind     string `json:"kind"`
	Role     string `json:"role"` // counted | dependency
	Coverage string `json:"coverage"`
	AsOf     string `json:"asOf,omitempty"` // when it left live, or the read time of a snapshot
	Error    string `json:"error,omitempty"`
}

type evidenceCondition struct {
	Type string `json:"type"`
	// Scope names the sub-object a nested condition belongs to (a Gateway
	// listener, an HTTPRoute parent), so conflicting conditions stay
	// attributable (Codex review of 0825767).
	Scope   string `json:"scope,omitempty"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type evidenceFact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type evidenceEvent struct {
	Reason   string `json:"reason"`
	Count    int32  `json:"count,omitempty"`
	Message  string `json:"message,omitempty"`
	LastSeen string `json:"lastSeen,omitempty"`
}

type evidenceObject struct {
	Kind       string              `json:"kind"`
	Namespace  string              `json:"namespace,omitempty"`
	Name       string              `json:"name"`
	Relation   string              `json:"relation,omitempty"`
	Conditions []evidenceCondition `json:"conditions,omitempty"`
	Facts      []evidenceFact      `json:"facts,omitempty"`
}

// explainEvidence is the whole evidence document.
type explainEvidence struct {
	Target         evidenceObject   `json:"target"`
	KlyxVerdict    *evidenceVerdict `json:"klyxVerdict,omitempty"`
	Anchor         AIAnchorDTO      `json:"anchor"`
	NoVerdict      string           `json:"noVerdict,omitempty"`
	LensFields     map[string]any   `json:"lensFields,omitempty"`
	WarningEvents  []evidenceEvent  `json:"warningEvents,omitempty"`
	Related        []evidenceObject `json:"related,omitempty"`
	IssuanceChain  *evidenceChain   `json:"issuanceChain,omitempty"`
	Operators      *operatorHealth  `json:"operators,omitempty"`
	Qualifications []string         `json:"qualifications,omitempty"`
	Truncated      bool             `json:"truncated,omitempty"`
}

// operators projects controller health through the scrubber: a controller's
// Reason can carry API or transport error text (Codex review of 0825767).
func (sc *scrubber) operators(oh *operatorHealth) *operatorHealth {
	if oh == nil {
		return nil
	}
	view := func(v *operatorView) *operatorView {
		if v == nil {
			return nil
		}
		cp := *v
		cp.Reason = sc.text(v.Reason)
		return &cp
	}
	return &operatorHealth{Flux: view(oh.Flux), Argo: view(oh.Argo), CertManager: view(oh.CertManager),
		ExternalSecrets: view(oh.ExternalSecrets), Reloader: view(oh.Reloader), Gateway: view(oh.Gateway)}
}

// bundle scrubs and caps Suggest-fix's resolved bundle the way Explain's
// evidence is: every free-text value through the scrubber, lists capped.
func (sc *scrubber) bundle(b resourceBundle) resourceBundle {
	view := func(v resourceView) resourceView {
		out := resourceView{Kind: v.Kind, Namespace: v.Namespace, Name: v.Name, Relation: v.Relation}
		for i, c := range v.Conditions {
			if i == 2*evidenceListMax {
				sc.n++
				break
			}
			out.Conditions = append(out.Conditions, conditionView{Type: sc.text(c.Type), Status: sc.text(c.Status), Reason: sc.text(c.Reason), Message: sc.text(c.Message), Scope: sc.text(c.Scope)})
		}
		for i, f := range v.Facts {
			if i == 2*evidenceListMax {
				sc.n++
				break
			}
			out.Facts = append(out.Facts, factView{Group: sc.text(f.Group), Label: sc.text(f.Label), Value: sc.text(f.Value), Tone: f.Tone})
		}
		return out
	}
	out := resourceBundle{Root: view(b.Root)}
	for _, r := range b.Related {
		out.Related = append(out.Related, view(r))
	}
	return out
}

// chain projects a resolved issuance chain (cert-manager) for the evidence:
// identity, ready state, scrubbed reason and message, children bounded.
func (sc *scrubber) chain(c certmanager.Chain) *evidenceChain {
	var node func(n chainNodeView, depth int) chainNodeView
	node = func(n chainNodeView, depth int) chainNodeView {
		out := chainNodeView{Kind: n.Kind, Namespace: n.Namespace, Name: n.Name, Ready: n.Ready, Reason: sc.text(n.Reason), Message: sc.text(n.Message)}
		if depth < 3 {
			for i, ch := range n.Children {
				if i == evidenceListMax {
					sc.n++
					break
				}
				out.Children = append(out.Children, node(ch, depth+1))
			}
		}
		return out
	}
	ev := &evidenceChain{}
	if c.Issuer != nil {
		bi := node(chainNodeView{Kind: string(c.Issuer.Kind), Namespace: c.Issuer.Namespace, Name: c.Issuer.Name, Ready: string(c.Issuer.Ready), Reason: c.Issuer.Reason, Message: c.Issuer.Message}, 0)
		ev.BoundIssuer = &bi
	}
	for i, r := range c.Requests {
		if i == evidenceListMax {
			sc.n++
			break
		}
		ev.Requests = append(ev.Requests, node(toChainNodeView(r), 0))
	}
	if c.FailingLink != nil {
		ev.FailingLink = string(c.FailingLink.Kind) + "/" + c.FailingLink.Name + " (ready=" + string(c.FailingLink.Ready) + ")"
	}
	for i, w := range c.Warnings {
		if i == evidenceListMax {
			break
		}
		ev.Warnings = append(ev.Warnings, sc.text(w))
	}
	return ev
}

// evidenceChain is a certificate's issuance chain: the bound issuer, the
// CertificateRequest -> Order -> Challenge tree, and Klyx's deepest-stuck
// node.
type evidenceChain struct {
	BoundIssuer *chainNodeView  `json:"boundIssuer,omitempty"`
	Requests    []chainNodeView `json:"requests,omitempty"`
	FailingLink string          `json:"failingLink,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
	FetchedAt   string          `json:"fetchedAt,omitempty"` // when the chain was resolved
}

// detailObject projects a resolved object's conditions and facts - never its
// YAML, labels, annotations or secret keys.
func (sc *scrubber) detailObject(d crd.InstanceDetail, relation string) evidenceObject {
	o := evidenceObject{Kind: d.Kind, Namespace: d.Namespace, Name: d.Name, Relation: relation}
	for _, c := range d.Conditions {
		if len(o.Conditions) == 2*evidenceListMax {
			sc.n++
			break
		}
		o.Conditions = append(o.Conditions, evidenceCondition{Type: sc.text(c.Type), Scope: sc.text(c.Scope), Status: sc.text(c.Status), Reason: sc.text(c.Reason), Message: sc.text(c.Message)})
	}
	for _, f := range d.Facts {
		if len(o.Facts) == 2*evidenceListMax {
			break
		}
		o.Facts = append(o.Facts, evidenceFact{Label: sc.text(f.Label), Value: sc.text(f.Value)})
	}
	return o
}

// warningEvents keeps the newest Warning events, scrubbed.
func (sc *scrubber) warningEvents(evs []crd.Event) []evidenceEvent {
	var w []crd.Event
	for _, e := range evs {
		if e.Type == "Warning" {
			w = append(w, e)
		}
	}
	sort.SliceStable(w, func(i, j int) bool { return w[i].Last.After(w[j].Last) })
	var out []evidenceEvent
	for _, e := range w {
		if len(out) == evidenceListMax {
			break
		}
		ev := evidenceEvent{Reason: sc.text(e.Reason), Count: e.Count, Message: sc.text(e.Message)}
		if !e.Last.IsZero() {
			ev.LastSeen = e.Last.UTC().Format(time.RFC3339)
		}
		out = append(out, ev)
	}
	return out
}

// marshalCapped serializes the evidence within evidenceTotalMax, shedding the
// least essential parts first: related objects, then events, then facts, then
// lens fields, then all but three conditions. The anchor and the target's
// identity always stay. Evidence that still does not fit is refused, never
// cut mid-document.
func marshalCapped(ev explainEvidence) ([]byte, bool, error) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, false, err
	}
	if len(raw) <= evidenceTotalMax {
		return raw, false, nil
	}
	ev.Truncated = true
	steps := []func(){
		func() { ev.Related = nil },
		func() { ev.WarningEvents = nil },
		func() { ev.Target.Facts = nil },
		func() { ev.LensFields = nil },
		func() {
			if len(ev.Target.Conditions) > 3 {
				ev.Target.Conditions = ev.Target.Conditions[:3]
			}
		},
	}
	for _, step := range steps {
		step()
		if raw, err = json.Marshal(ev); err != nil {
			return nil, true, err
		}
		if len(raw) <= evidenceTotalMax {
			return raw, true, nil
		}
	}
	return nil, true, errEvidenceTooLarge
}

var errEvidenceTooLarge = errors.New("the evidence for this object is too large to send, even trimmed; nothing was sent")
