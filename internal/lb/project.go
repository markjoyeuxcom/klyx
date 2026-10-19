// Package lb diagnoses LoadBalancer Services on clusters without a cloud
// load balancer, and the on-prem controllers that serve them (coverage S4):
// MetalLB, Cilium LB IPAM, k3s ServiceLB and kube-vip by class. Pure: the
// projections of MetalLB's and Cilium's objects, the Service and pool rules
// and the pending-episode tracker, with no client and no clock but the one
// passed in. An assigned address proves allocation, never working traffic.
package lb

import (
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/urlscrub"
)

// The APIs S4 reads (S4 §1).
const (
	GroupMetalLB   = "metallb.io"
	VersionMetalLB = "v1beta1"
	GroupCilium    = "cilium.io"
	VersionCilium  = "v2"

	ResourceMetalLBPools  = "ipaddresspools"
	ResourceL2Ads         = "l2advertisements"
	ResourceBGPAds        = "bgpadvertisements"
	ResourceConfigStates  = "configurationstates"
	ResourceCiliumPools   = "ciliumloadbalancerippools"
	advertisementL2       = "L2"
	advertisementBGP      = "BGP"
	configStateController = "controller"
	configStateSpeaker    = "speaker-"
)

// Projection limits (S4 §7).
const (
	maxList      = 8   // blocks, addresses, conditions shown
	maxPoolNames = 32  // an advertisement's ipAddressPools
	maxLabels    = 32  // a MetalLB pool's labels
	maxSelectors = 8   // an advertisement's ipAddressPoolSelectors
	maxRequires  = 16  // requirements in one selector
	maxMessage   = 400 // a condition message or error summary
	maxName      = 253
	maxLabelKey  = 317
	maxLabelVal  = 63
)

// Meta is a projected object's identity.
type Meta struct {
	// APIVersion is the version the object was served at (a ref's GVR).
	APIVersion string    `json:"av,omitempty"`
	Namespace  string    `json:"ns,omitempty"`
	Name       string    `json:"n"`
	UID        string    `json:"u"`
	Generation int64     `json:"g,omitempty"`
	Created    time.Time `json:"c"`
}

// Cond is one status condition, its message scrubbed under the URL rule and
// clipped.
type Cond struct {
	Type               string    `json:"t"`
	Status             string    `json:"s"`
	Reason             string    `json:"r,omitempty"`
	Message            string    `json:"m,omitempty"`
	ObservedGeneration int64     `json:"og,omitempty"`
	At                 time.Time `json:"at"`
}

// CiliumPool is one CiliumLoadBalancerIPPool (cluster-scoped).
type CiliumPool struct {
	Meta
	Disabled bool `json:"disabled,omitempty"`
	// Blocks counts spec.blocks (or the older spec.cidrs); BlockList is the
	// first maxList, as cidr or start-stop.
	Blocks          int      `json:"blocks"`
	BlockList       []string `json:"blockList,omitempty"`
	ServiceSelector bool     `json:"serviceSelector,omitempty"`
	// Conds holds the cilium.io conditions the pool rule reads, whole: the
	// conflict and the three counts, whose messages carry the numbers.
	Conds []Cond `json:"conds,omitempty"`
}

// PoolStatus is an IPAddressPool's status counts, each with its presence
// kept: absent is "not reported", never zero (S4 §5).
type PoolStatus struct {
	AssignedV4  *int64 `json:"a4,omitempty"`
	AvailableV4 *int64 `json:"v4,omitempty"`
	AssignedV6  *int64 `json:"a6,omitempty"`
	AvailableV6 *int64 `json:"v6,omitempty"`
}

// Reported reports whether any count is present.
func (s PoolStatus) Reported() bool {
	return s.AssignedV4 != nil || s.AvailableV4 != nil || s.AssignedV6 != nil || s.AvailableV6 != nil
}

// MetalLBPool is one IPAddressPool (namespaced).
type MetalLBPool struct {
	Meta
	// Labels are kept for advertisement selectors, capped at maxLabels;
	// LabelsCut makes selection by labels unresolved (S4 §7).
	Labels            map[string]string `json:"labels,omitempty"`
	LabelsCut         bool              `json:"labelsCut,omitempty"`
	Addresses         int               `json:"addresses"`
	AddressList       []string          `json:"addressList,omitempty"`
	AutoAssign        bool              `json:"autoAssign"`
	ServiceAllocation bool              `json:"serviceAllocation,omitempty"`
	Status            PoolStatus        `json:"status"`
}

// Advertisement is one L2Advertisement or BGPAdvertisement (namespaced).
type Advertisement struct {
	Meta
	Kind string `json:"kind"` // L2 | BGP
	// Pools are the named pools (ipAddressPools), the first maxPoolNames;
	// PoolsCut says the list was longer (revision 3).
	Pools    []string `json:"pools,omitempty"`
	PoolsCut bool     `json:"poolsCut,omitempty"`
	// Selectors are the ipAddressPoolSelectors kept whole; SelectorsCut
	// says one was left out (past the cap, or a key or value no pool label
	// can hold), so selection by selector is unresolved (S4 §5).
	Selectors     []metav1.LabelSelector `json:"selectors,omitempty"`
	SelectorsSet  bool                   `json:"selectorsSet,omitempty"`
	SelectorsCut  bool                   `json:"selectorsCut,omitempty"`
	NodeSelectors bool                   `json:"nodeSelectors,omitempty"`
	Peers         int                    `json:"peers,omitempty"`
}

// AllPools reports whether the advertisement selects every pool in its
// namespace: neither names nor selectors set.
func (a *Advertisement) AllPools() bool {
	return len(a.Pools) == 0 && !a.PoolsCut && !a.SelectorsSet
}

// ConfigState is one MetalLB ConfigurationState (namespaced): the
// controller's or one speaker's, separate writers (S4 §5).
type ConfigState struct {
	Meta
	Result string `json:"result,omitempty"` // Valid | Invalid | Unknown | ""
	Error  string `json:"error,omitempty"`
	Conds  []Cond `json:"conds,omitempty"`
	CondsN int    `json:"condsN,omitempty"`
}

// Role is "controller", "speaker" or "" for another name.
func (c *ConfigState) Role() string {
	switch {
	case c.Name == configStateController:
		return "controller"
	case strings.HasPrefix(c.Name, configStateSpeaker):
		return "speaker"
	}
	return ""
}

// Node is a speaker's node, from its name.
func (c *ConfigState) Node() string {
	if c.Role() != "speaker" {
		return ""
	}
	return strings.TrimPrefix(c.Name, configStateSpeaker)
}

// Project returns the projection for one S4 resource.
func Project(resource string) func(obj any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("lb: want *Unstructured, got %T", obj)
		}
		switch resource {
		case ResourceCiliumPools:
			return ProjectCiliumPool(u), nil
		case ResourceMetalLBPools:
			return ProjectMetalLBPool(u), nil
		case ResourceL2Ads:
			return ProjectAdvertisement(u, advertisementL2), nil
		case ResourceBGPAds:
			return ProjectAdvertisement(u, advertisementBGP), nil
		case ResourceConfigStates:
			return ProjectConfigState(u), nil
		}
		return nil, fmt.Errorf("lb: no projection for %q", resource)
	}
}

func metaOf(u *unstructured.Unstructured) Meta {
	return Meta{APIVersion: clip(u.GetAPIVersion(), 128), Namespace: clip(u.GetNamespace(), maxName), Name: clip(u.GetName(), maxName), UID: string(u.GetUID()),
		Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().Time}
}

// ProjectCiliumPool keeps the pool rule's inputs (S4 §7).
func ProjectCiliumPool(u *unstructured.Unstructured) *CiliumPool {
	p := &CiliumPool{Meta: metaOf(u)}
	p.Disabled, _, _ = unstructured.NestedBool(u.Object, "spec", "disabled")
	blocks, _, _ := unstructured.NestedSlice(u.Object, "spec", "blocks")
	if len(blocks) == 0 {
		blocks, _, _ = unstructured.NestedSlice(u.Object, "spec", "cidrs") // before v1.15
	}
	p.Blocks = len(blocks)
	for _, b := range blocks {
		if len(p.BlockList) == maxList {
			break
		}
		m, _ := b.(map[string]any)
		cidr, _ := m["cidr"].(string)
		start, _ := m["start"].(string)
		stop, _ := m["stop"].(string)
		switch {
		case cidr != "":
			p.BlockList = append(p.BlockList, clip(cidr, 64))
		case start != "":
			if stop == "" {
				stop = start
			}
			p.BlockList = append(p.BlockList, clip(start, 64)+"-"+clip(stop, 64))
		default:
			p.BlockList = append(p.BlockList, "(empty block)")
		}
	}
	if sel, ok, _ := unstructured.NestedMap(u.Object, "spec", "serviceSelector"); ok && sel != nil {
		p.ServiceSelector = true
	}
	for _, c := range conditions(u) {
		switch c.Type {
		case CondPoolConflict, CondIPsTotal, CondIPsAvailable, CondIPsUsed:
			p.Conds = append(p.Conds, c)
		}
	}
	return p
}

// ProjectMetalLBPool keeps the pool rule's inputs and the labels the
// advertisement selectors read (S4 §7).
func ProjectMetalLBPool(u *unstructured.Unstructured) *MetalLBPool {
	p := &MetalLBPool{Meta: metaOf(u), AutoAssign: true}
	if ls := u.GetLabels(); len(ls) > 0 {
		keys := make([]string, 0, len(ls))
		for k := range ls {
			keys = append(keys, k)
		}
		sort.Strings(keys) // the same labels kept on every read
		p.Labels = map[string]string{}
		for _, k := range keys {
			v := ls[k]
			if len(p.Labels) == maxLabels || len(k) > maxLabelKey || len(v) > maxLabelVal {
				p.LabelsCut = true
				continue
			}
			p.Labels[k] = v
		}
	}
	addrs, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "addresses")
	p.Addresses = len(addrs)
	for _, a := range addrs {
		if len(p.AddressList) == maxList {
			break
		}
		p.AddressList = append(p.AddressList, clip(a, 96))
	}
	if v, ok, _ := unstructured.NestedBool(u.Object, "spec", "autoAssign"); ok {
		p.AutoAssign = v
	}
	if sa, ok, _ := unstructured.NestedMap(u.Object, "spec", "serviceAllocation"); ok && sa != nil {
		p.ServiceAllocation = true
	}
	p.Status = PoolStatus{AssignedV4: count(u, "assignedIPv4"), AvailableV4: count(u, "availableIPv4"),
		AssignedV6: count(u, "assignedIPv6"), AvailableV6: count(u, "availableIPv6")}
	return p
}

// count reads one status count with its presence: a missing or negative
// field is not reported.
func count(u *unstructured.Unstructured, field string) *int64 {
	v, ok, err := unstructured.NestedInt64(u.Object, "status", field)
	if !ok || err != nil || v < 0 {
		return nil
	}
	return &v
}

// ProjectAdvertisement keeps an advertisement's pool selection whole, up to
// its caps (S4 §5, §7).
func ProjectAdvertisement(u *unstructured.Unstructured, kind string) *Advertisement {
	a := &Advertisement{Meta: metaOf(u), Kind: kind}
	names, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "ipAddressPools")
	for _, n := range names {
		if len(a.Pools) == maxPoolNames {
			a.PoolsCut = true
			break
		}
		if len(n) > maxName {
			a.PoolsCut = true // a name no pool can hold matches nothing it can prove
			continue
		}
		a.Pools = append(a.Pools, n)
	}
	sels, _, _ := unstructured.NestedSlice(u.Object, "spec", "ipAddressPoolSelectors")
	a.SelectorsSet = len(sels) > 0
	for _, raw := range sels {
		m, ok := raw.(map[string]any)
		if !ok || len(a.Selectors) == maxSelectors {
			a.SelectorsCut = true
			continue
		}
		sel, ok := selectorOf(m)
		if !ok {
			a.SelectorsCut = true
			continue
		}
		a.Selectors = append(a.Selectors, sel)
	}
	if ns, _, _ := unstructured.NestedSlice(u.Object, "spec", "nodeSelectors"); len(ns) > 0 {
		a.NodeSelectors = true
	}
	if peers, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "peers"); len(peers) > 0 {
		a.Peers = len(peers)
	}
	return a
}

// selectorOf decodes one LabelSelector within the caps; false when it is
// past them or holds a key or value no label can, so it stays unresolved.
func selectorOf(m map[string]any) (metav1.LabelSelector, bool) {
	var sel metav1.LabelSelector
	n := 0
	if ml, ok := m["matchLabels"].(map[string]any); ok {
		sel.MatchLabels = map[string]string{}
		for k, v := range ml {
			s, ok := v.(string)
			if !ok || len(k) > maxLabelKey || len(s) > maxLabelVal {
				return sel, false
			}
			sel.MatchLabels[k] = s
			n++
		}
	}
	if me, ok := m["matchExpressions"].([]any); ok {
		for _, raw := range me {
			e, ok := raw.(map[string]any)
			if !ok {
				return sel, false
			}
			key, _ := e["key"].(string)
			op, _ := e["operator"].(string)
			if len(key) > maxLabelKey {
				return sel, false
			}
			r := metav1.LabelSelectorRequirement{Key: key, Operator: metav1.LabelSelectorOperator(op)}
			if vs, ok := e["values"].([]any); ok {
				for _, v := range vs {
					s, ok := v.(string)
					if !ok || len(s) > maxLabelVal {
						return sel, false
					}
					r.Values = append(r.Values, s)
				}
			}
			sel.MatchExpressions = append(sel.MatchExpressions, r)
			n++
		}
	}
	return sel, n <= maxRequires
}

// ProjectConfigState keeps a ConfigurationState's result, its error summary
// and its conditions, scrubbed and clipped.
func ProjectConfigState(u *unstructured.Unstructured) *ConfigState {
	c := &ConfigState{Meta: metaOf(u)}
	c.Result, _, _ = unstructured.NestedString(u.Object, "status", "result")
	c.Result = clip(c.Result, 32)
	if e, _, _ := unstructured.NestedString(u.Object, "status", "errorSummary"); e != "" {
		c.Error = disp(e, maxMessage)
	}
	cs := conditions(u)
	c.CondsN = len(cs)
	if len(cs) > maxList {
		cs = cs[:maxList]
	}
	c.Conds = cs
	return c
}

func conditions(u *unstructured.Unstructured) []Cond {
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	out := make([]Cond, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		c := Cond{}
		c.Type, _ = m["type"].(string)
		c.Status, _ = m["status"].(string)
		c.Reason, _ = m["reason"].(string)
		msg, _ := m["message"].(string)
		c.Type, c.Status, c.Reason = clip(c.Type, maxLabelKey), clip(c.Status, 16), clip(c.Reason, 128)
		c.Message = disp(msg, maxMessage)
		switch og := m["observedGeneration"].(type) {
		case int64:
			c.ObservedGeneration = og
		case float64:
			c.ObservedGeneration = int64(og)
		}
		if s, ok := m["lastTransitionTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				c.At = t
			}
		}
		out = append(out, c)
	}
	return out
}

// disp scrubs a controller message under the URL rule and clips it.
func disp(s string, n int) string { return clip(urlscrub.Text(s).Text, n) }

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
