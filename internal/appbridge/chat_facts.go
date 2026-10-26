package appbridge

import (
	"encoding/json"
	"strconv"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The action facts (AI A3 §2.2): what Klyx retains from its own reads to
// decide whether a proposed step is eligible. They are extracted as a tool
// result is built, from the object Klyx read, never from model text, and
// they are never sent. The ledger keeps names and checks only.

const (
	actionFactsMax     = 64   // subjects per conversation
	actionFactBytesMax = 1024 // one subject's record, as JSON
)

// actionFact is one subject's record, by UID.
type actionFact struct {
	Kind  string `json:"kind"`
	Group string `json:"group,omitempty"`
	// Version and Resource are the API Klyx read it through, so a step's
	// review and run read the same object again (AI A3 §4)
	Version   string `json:"version,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	// Object is the latest read of the object itself; History the latest
	// rollout_history of a workload. A newer read of either kind replaces
	// that part only.
	Object  *objectFacts  `json:"object,omitempty"`
	History *historyFacts `json:"history,omitempty"`
}

// objectFacts are the fields a matrix verb's eligibility reads.
type objectFacts struct {
	ReadID     string `json:"readId"`
	ReadAt     string `json:"readAt"`
	Generation int64  `json:"generation,omitempty"`
	// acquired orders Klyx's reads of the object itself, taken as the
	// object is read, not when its tool's composite finished: a part is
	// replaced only by a later acquisition, never by an older read
	// completing late (A3 stage 2 review, finding 2). A sequence, not a
	// clock, so two reads never tie.
	acquired uint64
	// a workload's spec.replicas (nil: not a scalable workload, or unset
	// with no default Klyx can state), its pause, update strategy,
	// StatefulSet partition and desired pods
	Replicas  *int64 `json:"replicas,omitempty"`
	Paused    bool   `json:"paused,omitempty"`
	Strategy  string `json:"strategy,omitempty"`
	Partition int64  `json:"partition,omitempty"`
	Desired   *int64 `json:"desired,omitempty"`
	// a Flux unit's spec.suspend and spec.interval
	Suspended bool   `json:"suspended,omitempty"`
	Interval  string `json:"interval,omitempty"`
	// a pod's controller owner
	OwnerKind string `json:"ownerKind,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	OwnerUID  string `json:"ownerUid,omitempty"`
	// a node's spec.unschedulable
	Unschedulable bool `json:"unschedulable,omitempty"`
	// a KEDA scaler's autoscaling.keda.sh/paused annotation: whether it is
	// set and its value (KEDA 2.13 and later read the value, 2.12 the
	// presence), and another pause annotation set beside it
	KEDAPauseSet   bool   `json:"kedaPauseSet,omitempty"`
	KEDAPause      string `json:"kedaPause,omitempty"`
	KEDAOtherPause string `json:"kedaOtherPause,omitempty"`
}

// kedaPausedTrue: the paused annotation's value is true, which pauses on
// every KEDA version that applies it.
func (o *objectFacts) kedaPausedTrue() bool {
	b, err := strconv.ParseBool(o.KEDAPause)
	return o.KEDAPauseSet && err == nil && b
}

// historyFacts are what rollout_history established (AI A1.1 §8): the
// desired-template revision and its nearest lower retained partner, with
// the partner's revision object and template digest an undo is pinned to.
type historyFacts struct {
	ReadID           string `json:"readId"`
	ReadAt           string `json:"readAt"`
	acquired         uint64
	DesiredRevision  int64  `json:"desiredRevision,omitempty"`
	PartnerRevision  int64  `json:"partnerRevision,omitempty"`
	PartnerObjectUID string `json:"partnerObjectUid,omitempty"`
	PartnerObject    string `json:"partnerObject,omitempty"` // its name
	PartnerDigest    string `json:"partnerDigest,omitempty"`
}

// factOf extracts an object's facts from Klyx's read of it, by its GVR. A
// kind no matrix verb acts on carries its identity only.
func factOf(gvr schema.GroupVersionResource, u *unstructured.Unstructured, readID, readAt string, acquired uint64) actionFact {
	f := actionFact{Kind: u.GetKind(), Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID())}
	o := &objectFacts{ReadID: readID, ReadAt: readAt, Generation: u.GetGeneration(), acquired: acquired}
	obj := u.Object
	switch gvr.Group + "/" + gvr.Resource {
	case "apps/deployments":
		o.Replicas = replicasOf(obj, true)
		o.Desired = o.Replicas
		o.Paused, _, _ = unstructured.NestedBool(obj, "spec", "paused")
		o.Strategy, _, _ = unstructured.NestedString(obj, "spec", "strategy", "type")
	case "apps/statefulsets":
		o.Replicas = replicasOf(obj, true)
		o.Desired = o.Replicas
		o.Strategy, _, _ = unstructured.NestedString(obj, "spec", "updateStrategy", "type")
		o.Partition, _, _ = unstructured.NestedInt64(obj, "spec", "updateStrategy", "rollingUpdate", "partition")
	case "apps/daemonsets":
		o.Strategy, _, _ = unstructured.NestedString(obj, "spec", "updateStrategy", "type")
		if n, ok, _ := unstructured.NestedInt64(obj, "status", "desiredNumberScheduled"); ok {
			o.Desired = &n
		}
	case "kustomize.toolkit.fluxcd.io/kustomizations", "helm.toolkit.fluxcd.io/helmreleases":
		o.Suspended, _, _ = unstructured.NestedBool(obj, "spec", "suspend")
		o.Interval, _, _ = unstructured.NestedString(obj, "spec", "interval")
	case "/pods":
		for _, r := range u.GetOwnerReferences() {
			if r.Controller != nil && *r.Controller {
				o.OwnerKind, o.OwnerName, o.OwnerUID = r.Kind, r.Name, string(r.UID)
			}
		}
	case "/nodes":
		o.Unschedulable, _, _ = unstructured.NestedBool(obj, "spec", "unschedulable")
	case "keda.sh/scaledobjects", "keda.sh/scaledjobs":
		ann := u.GetAnnotations()
		o.KEDAPause, o.KEDAPauseSet = ann["autoscaling.keda.sh/paused"]
		if len(o.KEDAPause) > 16 {
			o.KEDAPause = o.KEDAPause[:16]
		}
		for _, other := range []string{"autoscaling.keda.sh/paused-replicas", "autoscaling.keda.sh/paused-scale-in", "autoscaling.keda.sh/paused-scale-out"} {
			if _, ok := ann[other]; ok && o.KEDAOtherPause == "" {
				o.KEDAOtherPause = other
			}
		}
	}
	f.Object = o
	return f
}

// replicasOf is a workload's spec.replicas; absent, the API's default of 1
// when def.
func replicasOf(obj map[string]any, def bool) *int64 {
	if n, ok, _ := unstructured.NestedInt64(obj, "spec", "replicas"); ok {
		return &n
	}
	if def {
		one := int64(1)
		return &one
	}
	return nil
}

// actionFacts is a conversation's record, at most actionFactsMax subjects,
// the oldest dropped first.
type actionFacts struct {
	byUID map[string]*actionFact
	order []string // UIDs, oldest first
}

func newActionFacts() *actionFacts { return &actionFacts{byUID: map[string]*actionFact{}} }

// add records f, merging its part into the subject's record. A record
// above actionFactBytesMax is not kept: the subject is then ineligible.
func (a *actionFacts) add(f actionFact) {
	if a == nil || f.UID == "" {
		return
	}
	cur, ok := a.byUID[f.UID]
	if !ok {
		cur = &actionFact{}
	}
	merged := *cur
	merged.Kind, merged.Group, merged.Namespace, merged.Name, merged.UID = f.Kind, f.Group, f.Namespace, f.Name, f.UID
	if f.Resource != "" {
		merged.Version, merged.Resource = f.Version, f.Resource
	}
	if f.Object != nil && newerObject(f.Object, cur.Object) {
		merged.Object = f.Object
	}
	if f.History != nil && (cur.History == nil || f.History.acquired > cur.History.acquired) {
		merged.History = f.History
	}
	if b, err := json.Marshal(merged); err != nil || len(b) > actionFactBytesMax {
		return
	}
	*cur = merged
	if ok {
		a.touch(f.UID)
		return
	}
	a.byUID[f.UID] = cur
	a.order = append(a.order, f.UID)
	for len(a.order) > actionFactsMax {
		delete(a.byUID, a.order[0])
		a.order = a.order[1:]
	}
}

// newerObject: n replaces cur only when Klyx acquired it later and its
// generation did not go back; a late completion of an older read, or a tie,
// keeps what is held.
func newerObject(n, cur *objectFacts) bool {
	if cur == nil {
		return true
	}
	return n.acquired > cur.acquired && n.Generation >= cur.Generation
}

var acquisitions atomic.Uint64

// acquisition is the next read's place in the order every fact part is
// compared by.
func acquisition() uint64 { return acquisitions.Add(1) }

// touch moves uid to the newest end: a newer read keeps a subject.
func (a *actionFacts) touch(uid string) {
	for i, u := range a.order {
		if u == uid {
			a.order = append(append(a.order[:i:i], a.order[i+1:]...), uid)
			return
		}
	}
}

// named are the subjects recorded at kind namespace/name: more than one
// means the name held another object since, and the step is refused.
func (a *actionFacts) named(kind, namespace, name string) []actionFact {
	if a == nil {
		return nil
	}
	var out []actionFact
	for _, uid := range a.order {
		f := a.byUID[uid]
		if f.Kind == kind && f.Namespace == namespace && f.Name == name {
			out = append(out, *f)
		}
	}
	return out
}

// len is how many subjects are held.
func (a *actionFacts) len() int {
	if a == nil {
		return 0
	}
	return len(a.order)
}
