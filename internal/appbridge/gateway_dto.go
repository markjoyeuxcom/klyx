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
type GatewayNodeDTO struct {
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	ClassName  string              `json:"className"`
	Listeners  []ListenerDTO       `json:"listeners"`
	Addresses  []GatewayAddressDTO `json:"addresses"`
	Accepted   bool                `json:"accepted"`
	Programmed bool                `json:"programmed"`
	Policies   []PolicyRefDTO      `json:"policies"`
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
}
type TopologyDTO struct {
	Gateway         GatewayNodeDTO `json:"gateway"`
	Routes          []RouteNodeDTO `json:"routes"`
	ClusterPolicies []PolicyRefDTO `json:"clusterPolicies,omitempty"`
	Warnings        []string       `json:"warnings,omitempty"`
	Error           string         `json:"error,omitempty"`
}
type GatewayRefDTO struct {
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	ClassName  string              `json:"className"`
	Addresses  []GatewayAddressDTO `json:"addresses"`
	Listeners  []ListenerDTO       `json:"listeners"`
	Accepted   bool                `json:"accepted"`
	Programmed bool                `json:"programmed"`
}
type GatewayListDTO struct {
	GatewayAPIServed bool            `json:"gatewayAPIServed"`
	Gateways         []GatewayRefDTO `json:"gateways"`
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
	gd := GatewayNodeDTO{Namespace: g.Namespace, Name: g.Name, ClassName: g.ClassName, Accepted: g.Accepted, Programmed: g.Programmed, Policies: policyDTOs(g.Policies)}
	for _, l := range g.Listeners {
		gd.Listeners = append(gd.Listeners, ListenerDTO{Name: l.Name, Protocol: l.Protocol, Hostname: l.Hostname, Port: l.Port, CertificateRefs: certificateRefDTOs(l.CertificateRefs)})
	}
	for _, a := range g.Addresses {
		gd.Addresses = append(gd.Addresses, GatewayAddressDTO{Type: a.Type, Value: a.Value})
	}
	out := TopologyDTO{Gateway: gd, Warnings: t.Warnings, ClusterPolicies: policyDTOs(t.ClusterPolicies)}
	for _, r := range t.Routes {
		rd := RouteNodeDTO{Namespace: r.Namespace, Name: r.Name, Hostnames: r.Hostnames, ParentSectionName: r.ParentSectionName, Accepted: r.Accepted, ResolvedRefs: r.ResolvedRefs, Pods: PodCountDTO{Ready: r.Pods.Ready, Total: r.Pods.Total, Unknown: r.Pods.Unknown}, Policies: policyDTOs(r.Policies)}
		for _, m := range r.Matches {
			rd.Matches = append(rd.Matches, MatchDTO{PathType: m.PathType, PathValue: m.PathValue, Method: m.Method})
		}
		for _, b := range r.Backends {
			rd.Backends = append(rd.Backends, BackendDTO{Kind: b.Kind, Name: b.Name, Namespace: b.Namespace, Port: b.Port, Weight: b.Weight})
		}
		for _, s := range r.Services {
			rd.Services = append(rd.Services, ServiceNodeDTO{Namespace: s.Namespace, Name: s.Name, Type: s.Type, Port: s.Port, Resolved: s.Resolved, Global: s.Global, MeshClusters: []string{}, Policies: policyDTOs(s.Policies), CNPs: policyDTOs(s.CNPs)})
		}
		out.Routes = append(out.Routes, rd)
	}
	return out
}
