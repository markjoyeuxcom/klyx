package appbridge

import "github.com/moomora/klyx/internal/gwapi"

type ListenerDTO struct {
	Name            string              `json:"name"`
	Protocol        string              `json:"protocol"`
	Hostname        string              `json:"hostname"`
	Port            int32               `json:"port"`
	CertificateRefs []CertificateRefDTO `json:"certificateRefs"`
	// S7a §3: the TLS mode and allowedRoutes as written, and the status its
	// controller reported (absent when none was).
	TLSMode       string             `json:"tlsMode,omitempty"`
	AllowedRoutes *AllowedRoutesDTO  `json:"allowedRoutes,omitempty"`
	Status        *ListenerStatusDTO `json:"status,omitempty"`
}
type CertificateRefDTO struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}
type GatewayAddressDTO struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}
type PolicyDetailDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type PolicyRefDTO struct {
	Kind              string            `json:"kind"`
	Namespace         string            `json:"namespace"`
	Name              string            `json:"name"`
	TargetKind        string            `json:"targetKind"`
	TargetNamespace   string            `json:"targetNamespace"`
	TargetName        string            `json:"targetName"`
	TargetSectionName string            `json:"targetSectionName"`
	Summary           string            `json:"summary"`
	Details           []PolicyDetailDTO `json:"details"`
	Inferred          bool              `json:"inferred"`
	Match             string            `json:"match"`
}

// CondDTO is one status condition as observed: status "True", "False",
// "Unknown", or "" when absent (no controller has written it).
type CondDTO struct {
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
	ObservedGeneration int64  `json:"observedGeneration"`
}

// condDTO's message is made safe here too (coverage S7a, Codex's review
// finding 1): a Cond built elsewhere than condOf reaches no DTO unscrubbed.
func condDTO(c gwapi.Cond) CondDTO {
	return CondDTO{Status: c.Status, Reason: c.Reason, Message: gwapi.SafeMessage(c.Message), ObservedGeneration: c.ObservedGeneration}
}

// EndpointCountDTO is one Service's EndpointSlice endpoints (not pods:
// nonPod counts endpoints without a pod TargetRef). error = unreadable.
type EndpointCountDTO struct {
	Ready  int    `json:"ready"`
	Total  int    `json:"total"`
	NonPod int    `json:"nonPod"`
	Error  string `json:"error,omitempty"`
}

type GatewayNodeDTO struct {
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	ClassName  string              `json:"className"`
	Listeners  []ListenerDTO       `json:"listeners"`
	Addresses  []GatewayAddressDTO `json:"addresses"`
	Accepted   bool                `json:"accepted"`
	Programmed bool                `json:"programmed"`
	Policies   []PolicyRefDTO      `json:"policies"`

	AcceptedCond   CondDTO `json:"acceptedCond"`
	ProgrammedCond CondDTO `json:"programmedCond"`
	Generation     int64   `json:"generation"`
	// S7a §4: allowedListeners as written, and the controller's count of
	// attached ListenerSets (null when unreported).
	AllowedListeners     NamespaceAllowanceDTO `json:"allowedListeners"`
	AttachedListenerSets *int32                `json:"attachedListenerSets"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}
type MatchDTO struct {
	PathType  string `json:"pathType"`
	PathValue string `json:"pathValue"`
	Method    string `json:"method"`
	Rule      int    `json:"rule"` // the spec.rules index it belongs to
	// GRPCRoute's method match (S7b §5)
	GRPCService string `json:"grpcService,omitempty"`
	GRPCMethod  string `json:"grpcMethod,omitempty"`
}

// RouteRuleDTO is one spec.rules entry with its verdict and each backend's
// own (The Line slice 8d): the data path draws a branch per rule, a bar per
// backend for its configured weight, and a failing backend in danger.
type RouteRuleDTO struct {
	Index       int                   `json:"index"`
	Fingerprint string                `json:"fingerprint"` // matches, backends and weights (slice 7b): joins measured traffic to this rule
	Rule        RuleDTO               `json:"rule"`
	Matches     []MatchDTO            `json:"matches"`
	Backends    []RouteRuleBackendDTO `json:"backends"`
}

// RouteRuleBackendDTO is a backendRef of one rule: its configured weight
// (an unset weight counts as 1) and its own verdict.
type RouteRuleBackendDTO struct {
	Kind      string  `json:"kind"`
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	Port      int32   `json:"port"`
	Weight    int32   `json:"weight"` // the effective configured weight: 1 when unset
	WeightSet bool    `json:"weightSet"`
	Rule      RuleDTO `json:"rule"`
}
type BackendDTO struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Port      int32  `json:"port"`
	Weight    int32  `json:"weight"`
	WeightSet bool   `json:"weightSet"`           // false: unset, the Gateway API default of 1
	Rule      int    `json:"rule"`                // the spec.rules index listing it; weights split within one rule
	Transport string `json:"transport,omitempty"` // TCP, or UDP for a UDPRoute (S7b §5)

	// state: resolved | not-found | unreadable | unsupported
	State     string            `json:"state"`
	Error     string            `json:"error,omitempty"`
	Endpoints *EndpointCountDTO `json:"endpoints,omitempty"`
}
type PodCountDTO struct {
	Ready   int  `json:"ready"`
	Total   int  `json:"total"`
	Unknown bool `json:"unknown"`
}
type ServiceNodeDTO struct {
	Namespace       string         `json:"namespace"`
	Name            string         `json:"name"`
	Type            string         `json:"type"`
	Port            int32          `json:"port"`
	Resolved        bool           `json:"resolved"`
	Global          bool           `json:"global"`
	MeshClusters    []string       `json:"meshClusters"`
	MeshUnconfirmed bool           `json:"meshUnconfirmed"`
	Policies        []PolicyRefDTO `json:"policies"`
	CNPs            []PolicyRefDTO `json:"cnps"`
}
type RouteNodeDTO struct {
	UID               string           `json:"uid"` // the route read: measured traffic joins only this UID
	Namespace         string           `json:"namespace"`
	Name              string           `json:"name"`
	Hostnames         []string         `json:"hostnames"`
	Matches           []MatchDTO       `json:"matches"`
	ParentSectionName string           `json:"parentSectionName"`
	Accepted          bool             `json:"accepted"`
	ResolvedRefs      bool             `json:"resolvedRefs"`
	Backends          []BackendDTO     `json:"backends"`
	Rules             []RouteRuleDTO   `json:"rules"` // every spec.rules entry, judged (slice 8d)
	Services          []ServiceNodeDTO `json:"services"`
	Pods              PodCountDTO      `json:"pods"`
	Policies          []PolicyRefDTO   `json:"policies"`

	ParentFound      bool    `json:"parentFound"`
	AcceptedCond     CondDTO `json:"acceptedCond"`
	ResolvedRefsCond CondDTO `json:"resolvedRefsCond"`
	Generation       int64   `json:"generation"`
	// Rule is the relocated lens rule (1c addendum §2); for a lane the
	// topology read evaluated, the attachment rule's verdict (S7b §5).
	Rule RuleDTO `json:"rule"`
	// Coverage S7b: the route's kind; the attachment the lane draws (its
	// identity, its label, the reference); the controller expected to judge
	// it (or why not established) and other controllers' entries; the
	// evidence; rule names by index; the hostname count.
	Kind            string           `json:"kind"`
	APIVersion      string           `json:"apiVersion,omitempty"` // the version read: opening the route uses it
	Attachment      string           `json:"attachment"`
	AttachmentLabel string           `json:"attachmentLabel"`
	ParentRef       *ParentRefDTO    `json:"parentRef,omitempty"`
	Controller      string           `json:"controller,omitempty"`
	ControllerWhy   string           `json:"controllerWhy,omitempty"`
	Others          []OtherStatusDTO `json:"others,omitempty"`
	Evidence        *AttachmentEvDTO `json:"evidence,omitempty"`
	RuleNames       []string         `json:"ruleNames"`
	HostnameCount   int              `json:"hostnameCount"`
}
type TopologyDTO struct {
	Gateway         GatewayNodeDTO `json:"gateway"`
	Routes          []RouteNodeDTO `json:"routes"`
	ClusterPolicies []PolicyRefDTO `json:"clusterPolicies,omitempty"`
	Warnings        []string       `json:"warnings,omitempty"`
	// Error = the topology could not be read at all (the Gateway GET
	// failed); nothing else in the DTO is evidence.
	Error string `json:"error,omitempty"`
	// RoutesComplete: the HTTPRoute list succeeded, so an absent route is
	// proven not attached. RoutesError names the failure otherwise.
	RoutesComplete bool   `json:"routesComplete"`
	RoutesError    string `json:"routesError,omitempty"`
}
type GatewayRefDTO struct {
	UID        string              `json:"uid,omitempty"` // identity (1c slice 3 §2a)
	GVR        string              `json:"gvr,omitempty"`
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	ClassName  string              `json:"className"`
	Addresses  []GatewayAddressDTO `json:"addresses"`
	Listeners  []ListenerDTO       `json:"listeners"`
	Accepted   bool                `json:"accepted"`
	Programmed bool                `json:"programmed"`
	// Stale: the spec changed and the controller has not observed it yet -
	// Accepted/Programmed reflect the previous spec; the row renders as
	// transitioning.
	Stale              bool    `json:"stale"`
	Generation         int64   `json:"generation"`
	AcceptedCond       CondDTO `json:"acceptedCond"`
	ProgrammedCond     CondDTO `json:"programmedCond"`
	ObservedGeneration int64   `json:"observedGeneration"`
	// S7a §4, as GatewayNodeDTO's.
	AllowedListeners     NamespaceAllowanceDTO `json:"allowedListeners"`
	AttachedListenerSets *int32                `json:"attachedListenerSets"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

// GatewayRouteAttachmentDTO is one attachment - one parentRef (coverage S7b
// §5) - with its captured evaluation and evidence. parent is its identity
// (the whole normalised reference); parentLabel names it; gateway is
// "namespace/name" of the Gateway it lands on ("" when not established).
type GatewayRouteAttachmentDTO struct {
	Parent      string           `json:"parent"`
	ParentLabel string           `json:"parentLabel"`
	ParentRef   *ParentRefDTO    `json:"parentRef,omitempty"`
	Gateway     string           `json:"gateway"`
	Rule        RuleDTO          `json:"rule"`
	Evidence    *AttachmentEvDTO `json:"evidence,omitempty"`
}

// ParentRefDTO is a parentRef with its defaults applied.
type ParentRefDTO struct {
	Group       string `json:"group"`
	Kind        string `json:"kind"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	SectionName string `json:"sectionName,omitempty"`
	Port        int32  `json:"port,omitempty"`
}

// AttachmentEvDTO is an attachment's evidence (S7b §5, §6): the expected
// controller (or why it is not established), other controllers' entries
// for the same reference (shown, never judged), and Klyx's own readings.
type AttachmentEvDTO struct {
	Controller    string           `json:"controller,omitempty"`
	ControllerWhy string           `json:"controllerWhy,omitempty"`
	Others        []OtherStatusDTO `json:"others,omitempty"`
	Allowance     AllowanceDTO     `json:"allowance"`
	Grants        []GrantDTO       `json:"grants,omitempty"`
	Features      []string         `json:"features,omitempty"`
	Disagreements []string         `json:"disagreements,omitempty"`
}

type OtherStatusDTO struct {
	Controller   string `json:"controller"`
	Accepted     string `json:"accepted"`
	ResolvedRefs string `json:"resolvedRefs"`
}

// GrantDTO is one cross-namespace backend's ReferenceGrant evidence.
type GrantDTO struct {
	Rule    int    `json:"rule"`
	Backend string `json:"backend"`
	Allowed string `json:"allowed"`
	By      string `json:"by,omitempty"`
	Text    string `json:"text"`
}

// GatewayRouteRowDTO is one HTTPRoute of the Gateways task's routes part
// (1c slice 3 §4a): counted once by UID, with its worst attachment's role,
// label and parent - the captured evaluation, never a new one - and every
// attachment. The topology stays the route's data path.
type GatewayRouteRowDTO struct {
	UID        string `json:"uid,omitempty"`
	GVR        string `json:"gvr,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	// Kind is the route's kind (S7b §5): identity is kind/namespace/name.
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Parent, ParentLabel and Gateway are the worst attachment's.
	Parent        string                      `json:"parent"`
	ParentLabel   string                      `json:"parentLabel"`
	Gateway       string                      `json:"gateway"`
	Hostnames     []string                    `json:"hostnames"`
	HostnameCount int                         `json:"hostnameCount"`
	Rule          RuleDTO                     `json:"rule"`
	Attachments   []GatewayRouteAttachmentDTO `json:"attachments"`
}

// RouteMetricDTO is a route's traffic readout. Nil fractions serialize as JSON
// null (UI renders "—"), never 0. ErrRate is a fraction 0..1.
type RouteMetricDTO struct {
	RPS     *float64 `json:"rps"`
	P50     *float64 `json:"p50"` // ms
	P99     *float64 `json:"p99"` // ms
	ErrRate *float64 `json:"errRate"`
}

type RouteMetricsStatusDTO struct {
	Available bool   `json:"available"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updatedAt"` // RFC3339; "" when never succeeded
	// Sentence says where the metrics came from and what makes them this
	// cluster's; Lineage keys that source, so a view refuses a late reply
	// read under another (Settings S3b §3.2, §3.4)
	Sentence string `json:"sentence,omitempty"`
	Lineage  string `json:"lineage,omitempty"`
}

type RouteMetricsResultDTO struct {
	Status RouteMetricsStatusDTO     `json:"status"`
	Routes map[string]RouteMetricDTO `json:"routes"`
}

func policyDTOs(ps []gwapi.PolicyRef) []PolicyRefDTO {
	out := make([]PolicyRefDTO, 0, len(ps))
	for _, p := range ps {
		details := make([]PolicyDetailDTO, 0, len(p.Details))
		for _, d := range p.Details {
			details = append(details, PolicyDetailDTO{Key: d.Key, Value: d.Value})
		}
		out = append(out, PolicyRefDTO{
			Kind: p.Kind, Namespace: p.Namespace, Name: p.Name,
			TargetKind: p.TargetKind, TargetNamespace: p.TargetNamespace, TargetName: p.TargetName, TargetSectionName: p.TargetSectionName,
			Summary: p.Summary, Details: details, Inferred: p.Inferred, Match: string(p.Match),
		})
	}
	return out
}

func certificateRefDTOs(refs []gwapi.CertificateRef) []CertificateRefDTO {
	out := make([]CertificateRefDTO, 0, len(refs))
	for _, r := range refs {
		out = append(out, CertificateRefDTO{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name})
	}
	return out
}

func toTopologyDTO(t gwapi.Topology) TopologyDTO {
	g := t.Gateway
	gd := GatewayNodeDTO{Namespace: g.Namespace, Name: g.Name, ClassName: g.ClassName, Accepted: g.Accepted, Programmed: g.Programmed, Policies: policyDTOs(g.Policies),
		AcceptedCond: condDTO(g.AcceptedCond), ProgrammedCond: condDTO(g.ProgrammedCond), Generation: g.Generation,
		AllowedListeners: allowanceDTO(g.AllowedListeners), AttachedListenerSets: g.AttachedListenerSets,
		Rule: toRuleDTO(gwapi.GatewayVerdict(g.AcceptedCond, g.ProgrammedCond, g.Generation, g.Listeners))}
	gd.Listeners = listenerDTOs(g.Listeners, g.Generation)
	for _, a := range g.Addresses {
		gd.Addresses = append(gd.Addresses, GatewayAddressDTO{Type: a.Type, Value: a.Value})
	}
	out := TopologyDTO{Gateway: gd, Warnings: t.Warnings, ClusterPolicies: policyDTOs(t.ClusterPolicies), RoutesComplete: t.RoutesComplete, RoutesError: t.RoutesError}
	for _, r := range t.Routes {
		out.Routes = append(out.Routes, toRouteNodeDTO(r, g.Listeners))
	}
	return out
}

// toRouteNodeDTO maps one route as it attaches to a Gateway; listeners are
// that Gateway's (the route rule checks the parent section exists).
func toRouteNodeDTO(r gwapi.RouteNode, listeners []gwapi.Listener) RouteNodeDTO {
	rd := RouteNodeDTO{UID: r.UID, Namespace: r.Namespace, Name: r.Name, Hostnames: r.Hostnames, ParentSectionName: r.ParentSectionName, Accepted: r.Accepted, ResolvedRefs: r.ResolvedRefs, Pods: PodCountDTO{Ready: r.Pods.Ready, Total: r.Pods.Total, Unknown: r.Pods.Unknown}, Policies: policyDTOs(r.Policies),
		ParentFound: r.ParentFound, AcceptedCond: condDTO(r.AcceptedCond), ResolvedRefsCond: condDTO(r.ResolvedRefsCond), Generation: r.Generation,
		Rule: toRuleDTO(gwapi.RouteRule(r, listeners))}
	routeNodeS7b(&rd, r)
	// Slices always serialize as arrays: a route with no backendRefs (a
	// redirect-only HTTPRoute) used to reach the lens as backends: null, and
	// the lane code reads backends[0] (found by the 1c golden gate).
	rd.Hostnames = sliceOrEmpty(rd.Hostnames)
	rd.Matches = make([]MatchDTO, 0, len(r.Matches))
	rd.Backends = make([]BackendDTO, 0, len(r.Backends))
	rd.Services = make([]ServiceNodeDTO, 0, len(r.Services))
	for _, m := range r.Matches {
		rd.Matches = append(rd.Matches, MatchDTO{PathType: m.PathType, PathValue: m.PathValue, Method: m.Method, Rule: m.Rule, GRPCService: m.GRPCService, GRPCMethod: m.GRPCMethod})
	}
	rd.Rules = make([]RouteRuleDTO, 0, r.RuleCount)
	for _, rv := range gwapi.RuleVerdicts(r) {
		rr := RouteRuleDTO{Index: rv.Index, Rule: toRuleDTO(rv.Eval), Matches: []MatchDTO{}, Backends: make([]RouteRuleBackendDTO, 0, len(rv.Backends))}
		if rv.Index < len(r.Fingerprints) {
			rr.Fingerprint = r.Fingerprints[rv.Index]
		}
		for _, m := range rd.Matches {
			if m.Rule == rv.Index {
				rr.Matches = append(rr.Matches, m)
			}
		}
		for _, b := range rv.Backends {
			w := b.Weight
			if !b.WeightSet {
				w = 1
			}
			rr.Backends = append(rr.Backends, RouteRuleBackendDTO{Kind: b.Kind, Namespace: b.Namespace, Name: b.Name, Port: b.Port, Weight: w, WeightSet: b.WeightSet, Rule: toRuleDTO(b.Eval)})
		}
		rd.Rules = append(rd.Rules, rr)
	}
	for _, b := range r.Backends {
		bd := BackendDTO{Kind: b.Kind, Name: b.Name, Namespace: b.Namespace, Port: b.Port, Weight: b.Weight, WeightSet: b.WeightSet, Rule: b.Rule, Transport: b.Transport, State: string(b.State), Error: b.Error}
		if b.Endpoints != nil {
			bd.Endpoints = &EndpointCountDTO{Ready: b.Endpoints.Ready, Total: b.Endpoints.Total, NonPod: b.Endpoints.NonPod, Error: b.Endpoints.Error}
		}
		rd.Backends = append(rd.Backends, bd)
	}
	for _, s := range r.Services {
		rd.Services = append(rd.Services, ServiceNodeDTO{Namespace: s.Namespace, Name: s.Name, Type: s.Type, Port: s.Port, Resolved: s.Resolved, Global: s.Global, MeshClusters: []string{}, Policies: policyDTOs(s.Policies), CNPs: policyDTOs(s.CNPs)})
	}
	return rd
}

// ReasonRateDTO is one Hubble drop reason's rate (flows/s).
type ReasonRateDTO struct {
	Reason   string  `json:"reason"`
	Protocol string  `json:"protocol"`
	Rate     float64 `json:"rate"`
}

// HubbleStatsDTO is the Hubble flow/drop triage readout. Nil pointer fields
// mean "that metric set is not enabled/scraped" and render as absent - never a
// fabricated zero. Status reuses the route-metrics status discipline.
type HubbleStatsDTO struct {
	ForwardRate   *float64              `json:"forwardRate"`
	DropRate      *float64              `json:"dropRate"`
	DropReasons   []ReasonRateDTO       `json:"dropReasons"`
	DNSErrorRate  *float64              `json:"dnsErrorRate"`
	DNSTotalRate  *float64              `json:"dnsTotalRate"`
	HTTPErrorRate *float64              `json:"httpErrorRate"`
	HTTPTotalRate *float64              `json:"httpTotalRate"`
	Status        RouteMetricsStatusDTO `json:"status"`
}
