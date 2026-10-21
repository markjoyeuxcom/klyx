package dra

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The two annotations a generated claim carries (S11b §1.4).
const (
	AnnotationPodClaimName = "resource.kubernetes.io/pod-claim-name"
	AnnotationExtended     = "resource.kubernetes.io/extended-resource-claim"
)

// The claim projection's bounds (S11b §2.0). The lists' bounds are the
// API's (resource.k8s.io/v1, 1.37): a list past one is cut and the claim
// marked Truncated, which no reading takes as complete.
const (
	MaxReservedKept   = 8
	MaxConditions     = 8
	MaxMessageBytes   = 256
	MaxFinalizersKept = 8
	// MaxTokenBytes clips a condition's type and reason (the API allows
	// 316 and 1024 bytes); "Ready" is never clipped
	MaxTokenBytes     = 128
	maxResults        = 32
	maxRequests       = 32
	maxSubrequests    = 8
	maxDeviceStatuses = 32
)

// Request is one of a claim's requests.
type Request struct {
	Name, Class string
	Mode        string
	Count       int64
	AdminAccess bool
	// Subclasses are a firstAvailable request's subrequests' classes.
	Subclasses []string
}

// Result is one allocated device.
type Result struct {
	Request, Driver, Pool, Device string
	AdminAccess                   bool
	ShareID                       string
}

// Consumer is one reservedFor entry.
type Consumer struct {
	Resource, Name, UID string
}

// Cond is a driver-reported device condition. Message is local only.
type Cond struct {
	Type, Status, Reason, Message string
}

// DeviceStatus is a driver's status of one allocated device.
type DeviceStatus struct {
	Driver, Pool, Device, ShareID string
	Conditions                    []Cond
}

// Claim is a projected ResourceClaim: every field a claim rule reads.
type Claim struct {
	Namespace, Name, UID string
	Deleting             bool
	DeletionTime         time.Time
	Finalizers           []string
	FinalizersOmitted    int
	// Owner is the controller owner reference, when there is one.
	OwnerKind, OwnerName, OwnerUID string
	PodClaimName                   string
	Extended                       bool
	Requests                       []Request
	Allocated                      bool
	Results                        []Result
	ReservedCount                  int
	Reserved                       []Consumer
	Devices                        []DeviceStatus
	Truncated                      bool
}

// ProjectClaim keeps the claim ledger of S11b §2.0. Selectors, configs and
// tolerations are dropped, in spec and in the allocation's status alike;
// device data and networkData are never read.
func ProjectClaim(u *unstructured.Unstructured) Claim {
	o := u.Object
	c := Claim{Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID())}
	fins := u.GetFinalizers()
	if len(fins) > MaxFinalizersKept {
		c.FinalizersOmitted, fins = len(fins)-MaxFinalizersKept, append([]string(nil), fins[:MaxFinalizersKept]...)
	}
	c.Finalizers = fins
	if dt := u.GetDeletionTimestamp(); dt != nil {
		c.Deleting, c.DeletionTime = true, dt.Time
	}
	for _, r := range u.GetOwnerReferences() {
		if r.Controller != nil && *r.Controller {
			c.OwnerKind, c.OwnerName, c.OwnerUID = r.Kind, r.Name, string(r.UID)
		}
	}
	ann := u.GetAnnotations()
	// an annotation value is unbounded; a pod claim name is a DNS label
	c.PodClaimName = Clip(ann[AnnotationPodClaimName], 253)
	c.Extended = ann[AnnotationExtended] == "true"
	reqs, _, _ := unstructured.NestedSlice(o, "spec", "devices", "requests")
	if len(reqs) > maxRequests {
		c.Truncated, reqs = true, reqs[:maxRequests]
	}
	for _, x := range reqs {
		m, _ := x.(map[string]any)
		r := Request{Name: str(m, "name"), Class: str(m, "exactly", "deviceClassName"), Mode: str(m, "exactly", "allocationMode"),
			Count: i64(m, "exactly", "count"), AdminAccess: boolean(m, "exactly", "adminAccess")}
		subs, _, _ := unstructured.NestedSlice(m, "firstAvailable")
		if len(subs) > maxSubrequests {
			c.Truncated, subs = true, subs[:maxSubrequests]
		}
		for _, y := range subs {
			sm, _ := y.(map[string]any)
			r.Subclasses = append(r.Subclasses, str(sm, "deviceClassName"))
		}
		c.Requests = append(c.Requests, r)
	}
	if _, ok, _ := unstructured.NestedMap(o, "status", "allocation"); ok {
		c.Allocated = true
	}
	results, _, _ := unstructured.NestedSlice(o, "status", "allocation", "devices", "results")
	if len(results) > maxResults {
		c.Truncated, results = true, results[:maxResults]
	}
	for _, x := range results {
		m, _ := x.(map[string]any)
		c.Results = append(c.Results, Result{Request: str(m, "request"), Driver: str(m, "driver"), Pool: str(m, "pool"), Device: str(m, "device"),
			AdminAccess: boolean(m, "adminAccess"), ShareID: str(m, "shareID")})
	}
	reserved, _, _ := unstructured.NestedSlice(o, "status", "reservedFor")
	c.ReservedCount = len(reserved)
	for i, x := range reserved {
		if i == MaxReservedKept {
			break
		}
		m, _ := x.(map[string]any)
		c.Reserved = append(c.Reserved, Consumer{Resource: str(m, "resource"), Name: str(m, "name"), UID: str(m, "uid")})
	}
	devs, _, _ := unstructured.NestedSlice(o, "status", "devices")
	if len(devs) > maxDeviceStatuses {
		c.Truncated, devs = true, devs[:maxDeviceStatuses]
	}
	for _, x := range devs {
		m, _ := x.(map[string]any)
		d := DeviceStatus{Driver: str(m, "driver"), Pool: str(m, "pool"), Device: str(m, "device"), ShareID: str(m, "shareID")}
		conds, _, _ := unstructured.NestedSlice(m, "conditions")
		for i, y := range conds {
			if i == MaxConditions {
				break
			}
			cm, _ := y.(map[string]any)
			msg := Clip(Printable(str(cm, "message")), MaxMessageBytes)
			d.Conditions = append(d.Conditions, Cond{Type: Clip(str(cm, "type"), MaxTokenBytes), Status: Clip(str(cm, "status"), MaxTokenBytes),
				Reason: Clip(str(cm, "reason"), MaxTokenBytes), Message: msg})
		}
		c.Devices = append(c.Devices, d)
	}
	return c
}

// Classes are every device class the claim's requests name.
func (c Claim) Classes() []string {
	var out []string
	for _, r := range c.Requests {
		if r.Class != "" {
			out = append(out, r.Class)
		}
		out = append(out, r.Subclasses...)
	}
	return out
}

// The results of resourceclaim.Name, kept apart (S11b §3.5).
const (
	RefResolved   = "resolved"
	RefNoneNeeded = "none-needed" // a status entry with no name: upstream skips it on purpose
	RefNotCreated = "not-created" // a template with no status entry yet
)

// PodClaimRef is one claim a pod references, mapped as the kubelet and the
// scheduler map it.
type PodClaimRef struct {
	PodClaim string
	// Source: "claim", "template" or "extended".
	Source string
	Claim  string
	State  string
}

// PodClaims maps a pod's claims (dynamic-resource-allocation/resourceclaim
// Name): a direct name as it is; a template through the pod's status; and
// the extended-resource claim. A template's claim counts only when the pod
// is its controller, checked against the claim itself (OwnedBy).
func PodClaims(p *corev1.Pod) []PodClaimRef {
	var out []PodClaimRef
	for _, pc := range p.Spec.ResourceClaims {
		switch {
		case pc.ResourceClaimName != nil:
			out = append(out, PodClaimRef{PodClaim: pc.Name, Source: "claim", Claim: *pc.ResourceClaimName, State: RefResolved})
		case pc.ResourceClaimTemplateName != nil:
			ref := PodClaimRef{PodClaim: pc.Name, Source: "template", State: RefNotCreated}
			for _, st := range p.Status.ResourceClaimStatuses {
				if st.Name != pc.Name {
					continue
				}
				if st.ResourceClaimName == nil {
					ref.State = RefNoneNeeded
				} else {
					ref.Claim, ref.State = *st.ResourceClaimName, RefResolved
				}
			}
			out = append(out, ref)
		}
	}
	if st := p.Status.ExtendedResourceClaimStatus; st != nil && st.ResourceClaimName != "" {
		out = append(out, PodClaimRef{PodClaim: "", Source: "extended", Claim: st.ResourceClaimName, State: RefResolved})
	}
	return out
}

// OwnedBy reports whether a template's claim belongs to the pod: the pod is
// its controller (IsForPod).
func (c Claim) OwnedBy(p *corev1.Pod) bool {
	return c.OwnerKind == "Pod" && c.OwnerUID == string(p.UID)
}

// Clip keeps at most n bytes of s, cut on a rune boundary and marked with
// "…", and copies what it keeps, so a projection never retains the object's
// own string.
func Clip(s string, n int) string {
	if len(s) <= n {
		return strings.Clone(s)
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// Printable replaces control characters with spaces: a driver's message is
// shown, never interpreted, and its JSON size stays at most twice its bytes.
func Printable(s string) string {
	if strings.IndexFunc(s, isControl) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return ' '
		}
		return r
	}, s)
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
