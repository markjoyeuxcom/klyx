package karpenter

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/urlscrub"
)

// Bounds (coverage S10 §7). Every string kept is clipped and copied; the
// rules classify from the authoritative fields, computed from every
// condition before the display list is capped, so a decisive condition past
// the cap is never lost.
const (
	maxConds   = 8
	maxLabels  = 16
	maxName    = 253
	maxToken   = 128
	maxMessage = 512
	maxStatus  = 16
	maxUnknown = 8
	maxUID     = 64
	maxValue   = 63

	// MaxClassObject, MaxPoolObject and MaxClaimObject bound one projection
	// each: the product of every cap above, measured
	// (TestMaxObjectBoundsTheCapProduct: about 15.5, 16.0 and 2.7 KiB). No
	// object is left out for its shape, only for its kind's budget.
	MaxClassObject = 16 << 10
	MaxPoolObject  = 16 << 10
	MaxClaimObject = 8 << 10

	BudgetClasses = 128 << 10 // per class kind
	BudgetPools   = 256 << 10
	BudgetClaims  = 2 << 20
)

// Meta is an object's identity as the rules and the links read it.
type Meta struct {
	Name       string            `json:"name"`
	UID        string            `json:"uid"`
	RV         string            `json:"rv,omitempty"`
	Generation int64             `json:"generation"`
	Created    time.Time         `json:"created"`
	Deleting   bool              `json:"deleting,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	LabelsCut  bool              `json:"labelsCut,omitempty"`
}

// Cond is one condition. Reason is a Kubernetes reason token, checked
// against the CRDs' schema pattern; one outside it is withheld. Message is
// the native text for the local inspector only: no AI path reads it (§8).
type Cond struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	ReasonWithheld     bool      `json:"reasonWithheld,omitempty"`
	Message            string    `json:"message,omitempty"`
	ObservedGeneration int64     `json:"observedGeneration,omitempty"` // 0: not reported
	At                 time.Time `json:"at,omitempty"`
}

// Conds is the display list: at most 8 conditions, with the exact total.
type Conds struct {
	Display []Cond `json:"display,omitempty"`
	Total   int    `json:"total"`
	Cut     bool   `json:"cut,omitempty"`
}

// ClassRef is a nodeClassRef.
type ClassRef struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
}

// reasonToken is the schema's reason pattern (the karpenter.sh and provider
// CRDs): no space, slash or hyphen can appear in one.
var reasonToken = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)

// ReasonToken returns the reason when it is a token, else "" and false.
func ReasonToken(r string) (string, bool) {
	if r == "" {
		return "", true
	}
	if len(r) > maxToken || !reasonToken.MatchString(r) {
		return "", false
	}
	return strings.Clone(r), true
}

// clip clips s to n bytes on a rune boundary and copies what it keeps.
func clip(s string, n int) string {
	if len(s) <= n {
		return strings.Clone(s)
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// metaOf reads identity; labels only when the kind keeps them (a claim's
// labels are read into its fields instead).
func metaOf(u *unstructured.Unstructured, labelled bool) Meta {
	m := Meta{Name: clip(u.GetName(), maxName), UID: clip(string(u.GetUID()), maxUID), RV: clip(u.GetResourceVersion(), maxToken),
		Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().Time, Deleting: u.GetDeletionTimestamp() != nil}
	if labels := u.GetLabels(); labelled && len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m.Labels = map[string]string{}
		for _, k := range keys {
			if len(m.Labels) == maxLabels {
				m.LabelsCut = true
				break
			}
			v := labels[k]
			if len(k) > maxName || len(v) > maxValue {
				m.LabelsCut = true // never a clipped label: selectors read exact values
				continue
			}
			m.Labels[strings.Clone(k)] = strings.Clone(v)
		}
	}
	return m
}

// condsOf reads every condition of the whole object, in order.
func condsOf(u *unstructured.Unstructured) []Cond {
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	out := make([]Cond, 0, len(raw))
	for _, c := range raw {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if t == "" {
			continue
		}
		st, _ := m["status"].(string)
		reason, _ := m["reason"].(string)
		msg, _ := m["message"].(string)
		cond := Cond{Type: clip(t, maxToken), Status: clip(st, maxStatus), Message: urlscrub.Field(clip(msg, maxMessage*2)).Text}
		cond.Message = clip(cond.Message, maxMessage)
		cond.Reason, ok = ReasonToken(reason)
		cond.ReasonWithheld = !ok
		switch og := m["observedGeneration"].(type) {
		case int64:
			cond.ObservedGeneration = og
		case float64:
			cond.ObservedGeneration = int64(og)
		}
		if lt, ok := m["lastTransitionTime"].(string); ok {
			if ts, err := time.Parse(time.RFC3339, lt); err == nil {
				cond.At = ts
			}
		}
		out = append(out, cond)
	}
	return out
}

// display caps the list for the inspector; the rules never read it.
func display(all []Cond) Conds {
	c := Conds{Total: len(all)}
	n := len(all)
	if n > maxConds {
		n, c.Cut = maxConds, true
	}
	c.Display = append([]Cond(nil), all[:n]...)
	return c
}

// find returns the condition of type t, copied, and its index; nil and -1
// when absent.
func find(all []Cond, t string) (*Cond, int) {
	for i := range all {
		if all[i].Type == t {
			c := all[i]
			return &c, i
		}
	}
	return nil, -1
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func refOf(u *unstructured.Unstructured, path ...string) *ClassRef {
	m, ok, _ := unstructured.NestedMap(u.Object, path...)
	if !ok || m == nil {
		return nil
	}
	r := &ClassRef{}
	r.Group, _ = m["group"].(string)
	r.Kind, _ = m["kind"].(string)
	r.Name, _ = m["name"].(string)
	r.Group, r.Kind, r.Name = clip(r.Group, maxName), clip(r.Kind, maxToken), clip(r.Name, maxName)
	if r.Kind == "" && r.Name == "" && r.Group == "" {
		return nil
	}
	return r
}

// Size is the retained bytes: string lengths and a fixed allowance per
// field, so the caps bound every projection.

func condSize(c *Cond) int {
	if c == nil {
		return 0
	}
	return 48 + len(c.Type) + len(c.Status) + len(c.Reason) + len(c.Message)
}

// authSize is an authoritative copy: its struct always, its strings only
// when the display list does not already hold them (index past the cap).
func authSize(c *Cond, index int) int {
	if c == nil {
		return 0
	}
	if index < maxConds {
		return 48
	}
	return condSize(c)
}

func metaSize(m Meta) int {
	n := 96 + len(m.Name) + len(m.UID) + len(m.RV)
	for k, v := range m.Labels {
		n += 16 + len(k) + len(v)
	}
	return n
}

func condsSize(c Conds) int {
	n := 16
	for i := range c.Display {
		n += condSize(&c.Display[i])
	}
	return n
}

func refSize(r *ClassRef) int {
	if r == nil {
		return 0
	}
	return 24 + len(r.Group) + len(r.Kind) + len(r.Name)
}
