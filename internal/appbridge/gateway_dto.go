package appbridge

import "github.com/moomora/klyx/internal/gwapi"

type ListenerDTO struct {
	Name            string              `json:"name"`
	Protocol        string              `json:"protocol"`
	Hostname        string              `json:"hostname"`
	Port            int32               `json:"port"`
	CertificateRefs []CertificateRefDTO `json:"certificateRefs"`
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

func condDTO(c gwapi.Cond) CondDTO {
	return CondDTO{Status: c.Status, Reason: c.Reason, Message: c.Message, ObservedGeneration: c.ObservedGeneration}
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
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}
type MatchDTO struct {
	PathType  string `json:"pathType"`
	PathValue string `json:"pathValue"`
	Method    string `json:"method"`
}
type BackendDTO struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Port      int32  `json:"port"`
	Weight    int32  `json:"weight"`
	WeightSet bool   `json:"weightSet"` // false: unset, the Gateway API default of 1
	Rule      int    `json:"rule"`      // the spec.rules index listing it; weights split within one rule

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
	Namespace         string           `json:"namespace"`
	Name              string           `json:"name"`
	Hostnames         []string         `json:"hostnames"`
	Matches           []MatchDTO       `json:"matches"`
	ParentSectionName string           `json:"parentSectionName"`
	Accepted          bool             `json:"accepted"`
	ResolvedRefs      bool             `json:"resolvedRefs"`
	Backends          []BackendDTO     `json:"backends"`
	Services          []ServiceNodeDTO `json:"services"`
	Pods              PodCountDTO      `json:"pods"`
	Policies          []PolicyRefDTO   `json:"policies"`

	ParentFound      bool    `json:"parentFound"`
	AcceptedCond     CondDTO `json:"acceptedCond"`
	ResolvedRefsCond CondDTO `json:"resolvedRefsCond"`
	Generation       int64   `json:"generation"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
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
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

// GatewayRouteAttachmentDTO is one route x parent Gateway attachment with
// its captured evaluation.
type GatewayRouteAttachmentDTO struct {
	Parent string  `json:"parent"` // "namespace/name" of the parent Gateway
	Rule   RuleDTO `json:"rule"`
}

// GatewayRouteRowDTO is one HTTPRoute of the Gateways task's routes part
// (1c slice 3 §4a): counted once by UID, with its worst attachment's role,
// label and parent - the captured evaluation, never a new one - and every
// attachment. The topology stays the route's data path.
type GatewayRouteRowDTO struct {
	UID         string                      `json:"uid,omitempty"`
	GVR         string                      `json:"gvr,omitempty"`
	Generation  int64                       `json:"generation,omitempty"`
	Namespace   string                      `json:"namespace"`
	Name        string                      `json:"name"`
	Parent      string                      `json:"parent"` // the worst attachment's parent
	Rule        RuleDTO                     `json:"rule"`
	Attachments []GatewayRouteAttachmentDTO `json:"attachments"`
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
		Rule: toRuleDTO(gwapi.GatewayRule(g.AcceptedCond, g.ProgrammedCond, g.Generation, g.Writes))}
	for _, l := range g.Listeners {
		gd.Listeners = append(gd.Listeners, ListenerDTO{Name: l.Name, Protocol: l.Protocol, Hostname: l.Hostname, Port: l.Port, CertificateRefs: certificateRefDTOs(l.CertificateRefs)})
	}
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
	rd := RouteNodeDTO{Namespace: r.Namespace, Name: r.Name, Hostnames: r.Hostnames, ParentSectionName: r.ParentSectionName, Accepted: r.Accepted, ResolvedRefs: r.ResolvedRefs, Pods: PodCountDTO{Ready: r.Pods.Ready, Total: r.Pods.Total, Unknown: r.Pods.Unknown}, Policies: policyDTOs(r.Policies),
		ParentFound: r.ParentFound, AcceptedCond: condDTO(r.AcceptedCond), ResolvedRefsCond: condDTO(r.ResolvedRefsCond), Generation: r.Generation,
		Rule: toRuleDTO(gwapi.RouteRule(r, listeners))}
	// Slices always serialize as arrays: a route with no backendRefs (a
	// redirect-only HTTPRoute) used to reach the lens as backends: null, and
	// the lane code reads backends[0] (found by the 1c golden gate).
	rd.Hostnames = sliceOrEmpty(rd.Hostnames)
	rd.Matches = make([]MatchDTO, 0, len(r.Matches))
	rd.Backends = make([]BackendDTO, 0, len(r.Backends))
	rd.Services = make([]ServiceNodeDTO, 0, len(r.Services))
	for _, m := range r.Matches {
		rd.Matches = append(rd.Matches, MatchDTO{PathType: m.PathType, PathValue: m.PathValue, Method: m.Method})
	}
	for _, b := range r.Backends {
		bd := BackendDTO{Kind: b.Kind, Name: b.Name, Namespace: b.Namespace, Port: b.Port, Weight: b.Weight, WeightSet: b.WeightSet, Rule: b.Rule, State: string(b.State), Error: b.Error}
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
