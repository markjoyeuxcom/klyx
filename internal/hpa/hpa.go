// Package hpa reads HorizontalPodAutoscalers for coverage S9 (§4, §10):
// a bounded projection, the standalone HPA rule, and the joins KEDA's
// ScaledObject rule reads - the controller reference and the target.
package hpa

import (
	"fmt"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Bounds (S9 §10). Size is the retained bytes - every string's length and
// a fixed allowance per field - not an encoding, so a hostile message is
// counted at its length. MaxObject holds the worst projection by
// construction: six conditions with 512-byte messages, the target, the
// controller reference and eight metric types come to under 6 KiB. The
// HPA controller writes four condition types, so six is never a cut in
// practice; a cut is flagged.
const (
	MaxObject  = 8 << 10
	Budget     = 512 << 10
	maxConds   = 6
	maxMetrics = 8
	maxMessage = 512
	maxShort   = 128 // a condition's type and reason, a metric type
	maxName    = 253
	maxAPI     = 256 // group/version
	maxKind    = 63
	maxUID     = 64
	// Grace is how long a new HPA may go without conditions (§4 rule 1).
	Grace = 60 * time.Second
)

// The conditions the HPA controller writes.
const (
	CondAbleToScale    = "AbleToScale"
	CondScalingActive  = "ScalingActive"
	CondScalingLimited = "ScalingLimited"
	ReasonScalingOff   = "ScalingDisabled"
	ReasonAmbiguous    = "AmbiguousSelector"
	ReasonTooMany      = "TooManyReplicas"
)

// Owner is an object reference with its UID.
type Owner struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}

// Group is the reference's API group ("" for the core group).
func (o Owner) Group() string { return groupOf(o.APIVersion) }

// Target is the object an HPA scales.
type Target struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// Group is the target's API group.
func (t Target) Group() string { return groupOf(t.APIVersion) }

// Cond is one condition, its message scrubbed and clipped.
type Cond struct {
	Type    string    `json:"type"`
	Status  string    `json:"status"`
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

// HPA is a HorizontalPodAutoscaler as S9 reads it (§10).
type HPA struct {
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	UID        string    `json:"uid"`
	Generation int64     `json:"generation"`
	Created    time.Time `json:"created"`
	// ObservedGeneration keeps presence: nil is "not reported", which an
	// older HPA controller never writes (§4 rule 4).
	ObservedGeneration *int64    `json:"observedGeneration,omitempty"`
	Controller         *Owner    `json:"controller,omitempty"`
	Target             Target    `json:"target"`
	Min                *int32    `json:"min,omitempty"`
	Max                int32     `json:"max"`
	Current            int32     `json:"current"`
	Desired            int32     `json:"desired"`
	LastScale          time.Time `json:"lastScale,omitempty"`
	Metrics            int       `json:"metrics"`
	MetricTypes        []string  `json:"metricTypes,omitempty"`
	Conds              []Cond    `json:"conds,omitempty"`
	CondsCut           bool      `json:"condsCut,omitempty"`
	Size               int       `json:"-"`
}

// Project is the HPA kind's projection (S9 §10).
func Project(obj any) (any, error) {
	h, ok := obj.(*autoscalingv2.HorizontalPodAutoscaler)
	if !ok {
		return nil, fmt.Errorf("hpa: want *HorizontalPodAutoscaler, got %T", obj)
	}
	out := &HPA{Namespace: clip(h.Namespace, maxName), Name: clip(h.Name, maxName), UID: clip(string(h.UID), maxUID), Generation: h.Generation,
		Created: h.CreationTimestamp.Time, Max: h.Spec.MaxReplicas, Current: h.Status.CurrentReplicas,
		Desired: h.Status.DesiredReplicas, Metrics: len(h.Spec.Metrics),
		Target: Target{APIVersion: clip(h.Spec.ScaleTargetRef.APIVersion, maxAPI), Kind: clip(h.Spec.ScaleTargetRef.Kind, maxKind), Name: clip(h.Spec.ScaleTargetRef.Name, maxName)}}
	if h.Status.ObservedGeneration != nil {
		g := *h.Status.ObservedGeneration
		out.ObservedGeneration = &g
	}
	if h.Spec.MinReplicas != nil {
		m := *h.Spec.MinReplicas
		out.Min = &m
	}
	if h.Status.LastScaleTime != nil {
		out.LastScale = h.Status.LastScaleTime.Time
	}
	for _, r := range h.OwnerReferences {
		if r.Controller != nil && *r.Controller {
			out.Controller = &Owner{APIVersion: clip(r.APIVersion, maxAPI), Kind: clip(r.Kind, maxKind), Name: clip(r.Name, maxName), UID: clip(string(r.UID), maxUID)}
			break
		}
	}
	for i, m := range h.Spec.Metrics {
		if i == maxMetrics {
			break
		}
		out.MetricTypes = append(out.MetricTypes, clip(string(m.Type), maxShort))
	}
	for _, c := range h.Status.Conditions {
		if len(out.Conds) == maxConds {
			out.CondsCut = true
			break
		}
		out.Conds = append(out.Conds, Cond{Type: clip(string(c.Type), maxShort), Status: clip(string(c.Status), 16), Reason: clip(c.Reason, maxShort),
			Message: Display(c.Message, maxMessage), At: c.LastTransitionTime.Time})
	}
	out.Size = size(out)
	return out, nil
}

// SizeOf is the budget's Size function.
func SizeOf(v any) int {
	if h, ok := v.(*HPA); ok {
		return h.Size
	}
	return MaxObject + 1
}

// size is the retained bytes: string lengths plus a fixed allowance per
// field and per condition.
func size(h *HPA) int {
	n := 256 + len(h.Namespace) + len(h.Name) + len(h.UID) + len(h.Target.APIVersion) + len(h.Target.Kind) + len(h.Target.Name)
	if h.Controller != nil {
		n += 64 + len(h.Controller.APIVersion) + len(h.Controller.Kind) + len(h.Controller.Name) + len(h.Controller.UID)
	}
	for _, m := range h.MetricTypes {
		n += 16 + len(m)
	}
	for _, c := range h.Conds {
		n += 64 + len(c.Type) + len(c.Status) + len(c.Reason) + len(c.Message)
	}
	return n
}

// Cond returns the named condition, nil when absent.
func (h *HPA) Cond(t string) *Cond {
	for i := range h.Conds {
		if h.Conds[i].Type == t {
			return &h.Conds[i]
		}
	}
	return nil
}

// KEDAOwner is the ScaledObject that controls the HPA, when its controller
// reference names one (§2): such an HPA is that ScaledObject's dependency.
func (h *HPA) KEDAOwner() (Owner, bool) {
	if h.Controller != nil && h.Controller.Group() == "keda.sh" && h.Controller.Kind == "ScaledObject" {
		return *h.Controller, true
	}
	return Owner{}, false
}

// Scales reports whether the HPA scales the object (group, kind, name) in
// its namespace.
func (h *HPA) Scales(namespace, group, kind, name string) bool {
	return h.Namespace == namespace && h.Target.Group() == group && h.Target.Kind == kind && h.Target.Name == name
}

// Failure is the HPA's own proven failure (§3's HPA result, §4 rules 2 and
// 3): AbleToScale=False, or ScalingActive=False with any reason but
// ScalingDisabled. ok is false when neither holds.
func (h *HPA) Failure() (c Cond, verb string, ok bool) {
	if a := h.Cond(CondAbleToScale); a != nil && a.Status == "False" {
		return *a, "change its target's scale", true
	}
	if s := h.Cond(CondScalingActive); s != nil && s.Status == "False" && s.Reason != ReasonScalingOff {
		return *s, "compute replicas", true
	}
	return Cond{}, "", false
}

// Lag is a reported positive lag (§4 rule 4): observedGeneration present,
// positive and behind the generation. Absent or zero reports nothing.
func (h *HPA) Lag() bool {
	return h.ObservedGeneration != nil && *h.ObservedGeneration > 0 && *h.ObservedGeneration < h.Generation
}

// GenerationReported says whether the controller reports which generation
// it observed (§4 rule 4's stated limit when not).
func (h *HPA) GenerationReported() bool {
	return h.ObservedGeneration != nil && *h.ObservedGeneration > 0
}

// FailureText is the label text of a proven failure.
func FailureText(c Cond, verb string) string {
	t := "the HPA cannot " + verb + ": " + c.Reason
	if c.Message != "" {
		t += ": " + c.Message
	}
	return t
}

// Rule judges a standalone HPA (S9 §4; first match wins). others names the
// other autoscalers on the same target, for AmbiguousSelector.
func Rule(h *HPA, now time.Time, others []string) health.NativeEval {
	if len(h.Conds) == 0 {
		deadline := h.Created.Add(Grace)
		if now.Before(deadline) {
			return health.NativeEval{Role: health.Progressing, Label: "Waiting for the HPA controller", Next: health.At(deadline)}
		}
		return health.NativeEval{Role: health.Unknown, Label: "Not reported", Boundary: health.At(deadline),
			Reason: health.CapReason("Not reported", "the HPA controller has not reported")}
	}
	if c, verb, ok := h.Failure(); ok {
		msg := FailureText(c, verb)
		if c.Reason == ReasonAmbiguous && len(others) > 0 {
			msg += "; also scaling this target: " + strings.Join(others, ", ")
		}
		label := "Cannot scale"
		if verb == "compute replicas" {
			label = "Cannot compute replicas"
		}
		return health.NativeEval{Included: true, Role: health.Fault, Label: label,
			Cond: health.CondAt(c.Type, c.Status, c.At), Reason: health.CapReason(c.Reason, msg)}
	}
	if h.Lag() {
		return health.NativeEval{Role: health.Progressing, Label: "Syncing…",
			Reason: health.CapReason("Syncing", fmt.Sprintf("generation %d, observed %d", h.Generation, *h.ObservedGeneration))}
	}
	if s := h.Cond(CondScalingActive); s != nil && s.Status == "False" && s.Reason == ReasonScalingOff {
		return health.NativeEval{Role: health.OK, Label: "Scaling disabled: target at 0"}
	}
	return health.NativeEval{Role: health.OK, Label: OKLabel(h)}
}

// OKLabel is a healthy HPA's label: its replicas, and the capacity fact
// "at maximum" (§4 rule 5), never attention.
func OKLabel(h *HPA) string {
	min := int32(1)
	if h.Min != nil {
		min = *h.Min
	}
	l := fmt.Sprintf("%d replicas (min %d, max %d, wants %d)", h.Current, min, h.Max, h.Desired)
	if c := h.Cond(CondScalingLimited); c != nil && c.Status == "True" && c.Reason == ReasonTooMany {
		l += fmt.Sprintf("; at maximum, wants %d", h.Desired)
	}
	return l
}

// Display is a free string for display: the URL rule first, then the clip.
func Display(s string, n int) string { return clip(urlscrub.Field(s).Text, n) }

// clip cuts s to n bytes on a rune boundary, marked. The concatenation
// copies, so the cut owns its bytes and a long field is not kept alive
// behind it.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func groupOf(apiVersion string) string {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return "\x00invalid"
	}
	return gv.Group
}
