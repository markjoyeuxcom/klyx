package appbridge

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/lb"
)

// Coverage S4: the Load balancers lens's rows (the Observer's lb task).
// Addresses, pool names and classes are infrastructure identity; controller
// messages arrive scrubbed and clipped. Lists are capped for display here,
// after the rules classified the whole input (S4 §2, §7).

const lbDisplayCap = 8

// The limits, always said (S4 §3, §5).
const (
	LBLimitAllocation = "An assigned address proves allocation, not that traffic reaches the Service."
	LBLimitStart      = "Its start was not observed: the time is a lower bound, from Klyx's first observation."
)

// LBCondDTO is one condition with its freshness.
type LBCondDTO struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Message            string     `json:"message,omitempty"`
	ObservedGeneration int64      `json:"observedGeneration,omitempty"`
	At                 *time.Time `json:"at,omitempty"`
	Current            bool       `json:"current"`
}

// LBCandidateDTO is one controller that may serve a Service.
type LBCandidateDTO struct {
	Controller string `json:"controller"`
	Detected   bool   `json:"detected"`
	Limit      string `json:"limit,omitempty"`
}

// LBExpectedDTO is the class's expected controllers (S4 §4).
type LBExpectedDTO struct {
	Class      string           `json:"class,omitempty"`
	Known      bool             `json:"known"`
	Candidates []LBCandidateDTO `json:"candidates"`
	Note       string           `json:"note,omitempty"`
}

// LBEventDTO is one Warning event joined by the Service's UID.
type LBEventDTO struct {
	Reason   string `json:"reason"`
	Message  string `json:"message,omitempty"`
	Count    int32  `json:"count,omitempty"`
	LastSeen int64  `json:"lastSeen,omitempty"`
}

// LBSvcLBDTO is a Service's ServiceLB evidence.
type LBSvcLBDTO struct {
	DaemonSet        string `json:"daemonSet,omitempty"`
	Pods             int    `json:"pods"`
	Ready            int    `json:"ready"`
	Pending          int    `json:"pending"`
	HostPortConflict bool   `json:"hostPortConflict,omitempty"`
	PendingReason    string `json:"pendingReason,omitempty"`
	Candidates       int    `json:"candidates,omitempty"`
}

// LBPoolRefDTO is the pool a Service names.
type LBPoolRefDTO struct {
	Name      string `json:"name"`
	State     string `json:"state"` // found | missing | unverified
	Namespace string `json:"namespace,omitempty"`
}

// LBDiagnosisDTO is a pending Service's evidence (S4 §4).
type LBDiagnosisDTO struct {
	Events     []LBEventDTO  `json:"events"`
	EventCount int           `json:"eventCount"`
	EventsRead string        `json:"eventsRead"` // complete | partial | unverified
	SvcLB      *LBSvcLBDTO   `json:"svclb,omitempty"`
	SvcLBRead  bool          `json:"svclbRead"`
	Cilium     *LBCondDTO    `json:"cilium,omitempty"`
	Request    []string      `json:"request"`
	Pool       *LBPoolRefDTO `json:"pool,omitempty"`
	Silent     string        `json:"silent,omitempty"`
	Summary    string        `json:"summary"`
}

// LBServiceDTO is one LoadBalancer Service row: the counted entity.
type LBServiceDTO struct {
	UID                   string          `json:"uid"`
	GVR                   string          `json:"gvr"`
	Namespace             string          `json:"namespace"`
	Name                  string          `json:"name"`
	Generation            int64           `json:"generation"`
	Created               *time.Time      `json:"created,omitempty"`
	Class                 string          `json:"class,omitempty"`
	ExternalTrafficPolicy string          `json:"externalTrafficPolicy,omitempty"`
	IPFamilies            []string        `json:"ipFamilies"`
	IPFamilyPolicy        string          `json:"ipFamilyPolicy,omitempty"`
	Allocated             bool            `json:"allocated"`
	IPs                   []string        `json:"ips"`
	Hostnames             []string        `json:"hostnames"`
	AddressCount          int             `json:"addressCount"`
	Entries               int             `json:"entries"`
	Families              []string        `json:"families"`
	PortError             bool            `json:"portError,omitempty"`
	PortsErrorCond        bool            `json:"portsErrorCond,omitempty"`
	MissingFamily         string          `json:"missingFamily,omitempty"`
	OneFamilyPreferred    bool            `json:"oneFamilyPreferred,omitempty"`
	Expected              LBExpectedDTO   `json:"expected"`
	PendingSince          *time.Time      `json:"pendingSince,omitempty"`
	PendingObserved       bool            `json:"pendingObserved,omitempty"`
	Diagnosis             *LBDiagnosisDTO `json:"diagnosis,omitempty"`
	Cilium                *LBCondDTO      `json:"cilium,omitempty"`
	Conditions            []LBCondDTO     `json:"conditions"`
	ConditionCount        int             `json:"conditionCount"`
	Limits                []string        `json:"limits"`
	Reason                string          `json:"reason,omitempty"`
	Rule                  RuleDTO         `json:"rule"`
}

func capStrings(xs []string) []string {
	if len(xs) > lbDisplayCap {
		xs = xs[:lbDisplayCap]
	}
	return nonNil(append([]string(nil), xs...))
}

func lbServiceCond(c *lb.ServiceCond) *LBCondDTO {
	if c == nil {
		return nil
	}
	return &LBCondDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, At: timePtrOf(c.At), Current: c.Current}
}

func lbServiceDTO(r healthsrc.Row[healthsrc.LBServiceRow]) LBServiceDTO {
	s, a := r.Data.Service, r.Data.Allocation
	d := LBServiceDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: s.Namespace, Name: s.Name, Generation: s.Generation, Created: timePtrOf(s.CreationTimestamp.Time),
		Class: lb.Class(s), ExternalTrafficPolicy: string(s.Spec.ExternalTrafficPolicy), IPFamilies: []string{}, Allocated: a.Allocated,
		IPs: capStrings(a.IPs), Hostnames: capStrings(a.Hostnames), AddressCount: len(a.IPs) + len(a.Hostnames), Entries: a.Entries, Families: []string{},
		PortError: a.PortError, PortsErrorCond: a.PortsErrorCond, MissingFamily: r.Data.MissingFamily, Cilium: lbServiceCond(r.Data.Cilium),
		Conditions: []LBCondDTO{}, ConditionCount: len(s.Status.Conditions), Limits: []string{LBLimitAllocation}, Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval)}
	for _, f := range s.Spec.IPFamilies {
		d.IPFamilies = append(d.IPFamilies, string(f))
	}
	if p := s.Spec.IPFamilyPolicy; p != nil {
		d.IPFamilyPolicy = string(*p)
		d.OneFamilyPreferred = *p == corev1.IPFamilyPolicyPreferDualStack && a.Allocated && !a.HostnameOnly() && len(a.Families) == 1
	}
	for _, f := range []string{lb.FamilyV4, lb.FamilyV6} {
		if a.Families[f] {
			d.Families = append(d.Families, f)
		}
	}
	for i, c := range s.Status.Conditions {
		if i == lbDisplayCap {
			break
		}
		d.Conditions = append(d.Conditions, LBCondDTO{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message,
			ObservedGeneration: c.ObservedGeneration, At: timePtrOf(c.LastTransitionTime.Time), Current: c.ObservedGeneration == s.Generation})
	}
	ex := r.Data.Expected
	d.Expected = LBExpectedDTO{Class: ex.Class, Known: ex.Known, Candidates: []LBCandidateDTO{}, Note: ex.Note}
	for _, c := range ex.Candidates {
		d.Expected.Candidates = append(d.Expected.Candidates, LBCandidateDTO{Controller: c.Controller, Detected: c.Detected, Limit: c.Limit})
	}
	if ep := r.Data.Episode; ep != nil {
		d.PendingSince, d.PendingObserved = timePtrOf(ep.Start), ep.Observed
		if !ep.Observed {
			d.Limits = append(d.Limits, LBLimitStart)
		}
	}
	if dg := r.Data.Diagnosis; dg != nil {
		dd := &LBDiagnosisDTO{Events: []LBEventDTO{}, EventCount: len(dg.Events), EventsRead: dg.EventsRead, SvcLBRead: dg.SvcLBRead,
			Cilium: lbServiceCond(dg.Cilium), Request: nonNil(dg.Request), Silent: dg.Silent, Summary: dg.Summary}
		for _, e := range dg.Events {
			dd.Events = append(dd.Events, LBEventDTO{Reason: e.Reason, Message: e.Message, Count: e.Count, LastSeen: e.LastSeen})
		}
		if v := dg.SvcLB; v != nil {
			dd.SvcLB = &LBSvcLBDTO{DaemonSet: v.DaemonSet, Pods: v.Pods, Ready: v.Ready, Pending: v.Pending, HostPortConflict: v.HostPortConflict,
				PendingReason: v.PendingReason, Candidates: v.Candidates}
		}
		if p := dg.Pool; p != nil {
			dd.Pool = &LBPoolRefDTO{Name: p.Name, State: p.State, Namespace: p.Namespace}
		}
		d.Diagnosis = dd
	}
	return d
}

// LBCountsDTO is a Cilium pool's counts as exact decimal strings (an IPv6
// count exceeds a JavaScript number), each with its state.
type LBCountsDTO struct {
	Total          string `json:"total,omitempty"`
	TotalState     string `json:"totalState,omitempty"`
	Available      string `json:"available,omitempty"`
	AvailableState string `json:"availableState,omitempty"`
	Used           string `json:"used,omitempty"`
	UsedState      string `json:"usedState,omitempty"`
}

func countDTO(c lb.Count) (string, string) {
	if c.Value == nil {
		return "", c.State
	}
	return c.Value.String(), ""
}

// LBMetalLBStatusDTO is a MetalLB pool's status counts; a nil field is not
// reported, never zero.
type LBMetalLBStatusDTO struct {
	AssignedIPv4  *int64 `json:"assignedIPv4,omitempty"`
	AvailableIPv4 *int64 `json:"availableIPv4,omitempty"`
	AssignedIPv6  *int64 `json:"assignedIPv6,omitempty"`
	AvailableIPv6 *int64 `json:"availableIPv6,omitempty"`
}

// LBPoolDTO is one pool row: a Cilium or a MetalLB pool.
type LBPoolDTO struct {
	Kind       string     `json:"kind"` // cilium | metallb
	UID        string     `json:"uid"`
	GVR        string     `json:"gvr"`
	Namespace  string     `json:"namespace,omitempty"`
	Name       string     `json:"name"`
	Generation int64      `json:"generation"`
	Created    *time.Time `json:"created,omitempty"`
	Blocks     []string   `json:"blocks"`
	BlockCount int        `json:"blockCount"`
	// Cilium
	Disabled        bool         `json:"disabled,omitempty"`
	ServiceSelector bool         `json:"serviceSelector,omitempty"`
	Counts          *LBCountsDTO `json:"counts,omitempty"`
	Conflict        *LBCondDTO   `json:"conflict,omitempty"`
	Conditions      []LBCondDTO  `json:"conditions"`
	// MetalLB
	Labels            map[string]string   `json:"labels,omitempty"`
	LabelsCut         bool                `json:"labelsCut,omitempty"`
	AutoAssign        *bool               `json:"autoAssign,omitempty"`
	ServiceAllocation bool                `json:"serviceAllocation,omitempty"`
	Status            *LBMetalLBStatusDTO `json:"status,omitempty"`
	SelectedBy        []string            `json:"selectedBy"`
	Unresolved        []string            `json:"unresolved"`
	AdsRead           string              `json:"adsRead,omitempty"`
	StatesRead        string              `json:"statesRead,omitempty"`
	Instance          string              `json:"instance,omitempty"`
	Invalid           []string            `json:"invalid"`
	Limits            []string            `json:"limits"`
	Reason            string              `json:"reason,omitempty"`
	Rule              RuleDTO             `json:"rule"`
}

func lbCond(c lb.Cond, gen int64) LBCondDTO {
	return LBCondDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, ObservedGeneration: c.ObservedGeneration,
		At: timePtrOf(c.At), Current: c.ObservedGeneration == gen}
}

func lbPoolDTO(r healthsrc.Row[healthsrc.LBPoolRow]) LBPoolDTO {
	d := LBPoolDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: r.Ref.Namespace, Name: r.Ref.Name, Generation: r.Ref.Generation,
		Blocks: []string{}, Conditions: []LBCondDTO{}, SelectedBy: []string{}, Unresolved: []string{}, Invalid: []string{}, Limits: []string{},
		Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval)}
	if p := r.Data.Cilium; p != nil {
		d.Kind, d.Created, d.Disabled, d.ServiceSelector = "cilium", timePtrOf(p.Created), p.Disabled, p.ServiceSelector
		d.Blocks, d.BlockCount = nonNil(append([]string(nil), p.BlockList...)), p.Blocks
		if n := r.Data.Counts; n != nil {
			c := &LBCountsDTO{}
			c.Total, c.TotalState = countDTO(n.Total)
			c.Available, c.AvailableState = countDTO(n.Available)
			c.Used, c.UsedState = countDTO(n.Used)
			d.Counts = c
		}
		if cf := r.Data.Conflict; cf != nil {
			cd := lbCond(*cf, p.Generation)
			d.Conflict = &cd
		}
		for _, c := range p.Conds {
			d.Conditions = append(d.Conditions, lbCond(c, p.Generation))
		}
		return d
	}
	p := r.Data.MetalLB
	if p == nil {
		return d
	}
	auto := p.AutoAssign
	d.Kind, d.Created, d.Labels, d.LabelsCut, d.AutoAssign, d.ServiceAllocation = "metallb", timePtrOf(p.Created), p.Labels, p.LabelsCut, &auto, p.ServiceAllocation
	d.Blocks, d.BlockCount = nonNil(append([]string(nil), p.AddressList...)), p.Addresses
	d.Status = &LBMetalLBStatusDTO{AssignedIPv4: p.Status.AssignedV4, AvailableIPv4: p.Status.AvailableV4, AssignedIPv6: p.Status.AssignedV6, AvailableIPv6: p.Status.AvailableV6}
	if sel := r.Data.Selection; sel != nil {
		d.SelectedBy, d.Unresolved = nonNil(sel.By), nonNil(sel.Unresolved)
	}
	d.AdsRead, d.StatesRead, d.Instance, d.Invalid = r.Data.AdsRead, r.Data.StatesRead, r.Data.Instance, nonNil(r.Data.Invalid)
	d.Limits = append(d.Limits, lb.LimitAdvertisement)
	if r.Data.StatesRead != "complete" || len(r.Data.Invalid) == 0 && d.Instance != "state" {
		d.Limits = append(d.Limits, lb.LimitConfigState)
	}
	if d.Instance == "workload" {
		d.Limits = append(d.Limits, "Its namespace is MetalLB's by its controller Deployment's; a --namespace flag or METALLB_NAMESPACE can move it.")
	}
	return d
}

// LBStateDTO is one MetalLB ConfigurationState row.
type LBStateDTO struct {
	UID            string      `json:"uid"`
	GVR            string      `json:"gvr"`
	Namespace      string      `json:"namespace"`
	Name           string      `json:"name"`
	Role           string      `json:"role,omitempty"` // controller | speaker
	Node           string      `json:"node,omitempty"`
	Result         string      `json:"result,omitempty"`
	Error          string      `json:"error,omitempty"`
	Created        *time.Time  `json:"created,omitempty"`
	Conditions     []LBCondDTO `json:"conditions"`
	ConditionCount int         `json:"conditionCount"`
	Reason         string      `json:"reason,omitempty"`
	Rule           RuleDTO     `json:"rule"`
}

func lbStateDTO(r healthsrc.Row[healthsrc.LBStateRow]) LBStateDTO {
	s := r.Data.State
	d := LBStateDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: s.Namespace, Name: s.Name, Role: s.Role(), Node: s.Node(), Result: s.Result,
		Error: s.Error, Created: timePtrOf(s.Created), Conditions: []LBCondDTO{}, ConditionCount: s.CondsN, Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval)}
	for _, c := range s.Conds {
		d.Conditions = append(d.Conditions, lbCond(c, s.Generation))
	}
	return d
}

func init() {
	rowsConverters[healthsrc.TaskLB+"/services"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.LBServiceRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]LBServiceDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, lbServiceDTO(r))
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name })
		return out, nil
	}
	rowsConverters[healthsrc.TaskLB+"/pools"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.LBPoolRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]LBPoolDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, lbPoolDTO(r))
		}
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Kind+out[i].Namespace+"/"+out[i].Name < out[j].Kind+out[j].Namespace+"/"+out[j].Name
		})
		return out, nil
	}
	rowsConverters[healthsrc.TaskLB+"/metallb"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.LBStateRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]LBStateDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, lbStateDTO(r))
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name })
		return out, nil
	}
}
