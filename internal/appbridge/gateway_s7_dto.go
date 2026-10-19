package appbridge

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Coverage S7a's DTOs: a listener's status and allowedRoutes (§3), a
// Gateway's allowedListeners (§4), and the ListenerSets part's rows.

// ListenerStatusDTO is one listener's status as its controller reported it.
type ListenerStatusDTO struct {
	SupportedKinds []RouteKindDTO         `json:"supportedKinds"`
	AttachedRoutes int32                  `json:"attachedRoutes"`
	Conditions     []ListenerConditionDTO `json:"conditions"`
}

// ListenerConditionDTO is one listener condition. Stale: it describes an
// older generation of the object, so it is shown, never judged.
type ListenerConditionDTO struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
	ObservedGeneration int64  `json:"observedGeneration"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"` // RFC3339
	Stale              bool   `json:"stale"`
}

// RouteKindDTO is a route group and kind; group "" is the Gateway API's.
type RouteKindDTO struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
}

// NamespaceAllowanceDTO is a namespaces allowance as written: set says the
// field was written at all, from is "" when unset (its default applies),
// and selector is the label selector in kubectl's form.
type NamespaceAllowanceDTO struct {
	Set             bool   `json:"set"`
	From            string `json:"from"`
	Selector        string `json:"selector,omitempty"`
	SelectorInvalid bool   `json:"selectorInvalid,omitempty"`
}

// AllowedRoutesDTO is a listener's allowedRoutes as written.
type AllowedRoutesDTO struct {
	Namespaces NamespaceAllowanceDTO `json:"namespaces"`
	Kinds      []RouteKindDTO        `json:"kinds"`
}

// ListenerParentDTO is a ListenerSet's parentRef, defaults applied.
type ListenerParentDTO struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// AllowanceDTO is Klyx's own reading of the parent's allowedListeners:
// evidence beside the controller's verdict. allowed is "yes", "no" or ""
// (not established).
type AllowanceDTO struct {
	From      string `json:"from"`
	Allowed   string `json:"allowed"`
	Text      string `json:"text"`
	Disagrees bool   `json:"disagrees"`
}

// ListenerSetRowDTO is one ListenerSet of the Gateways task's ListenerSets
// part (S7 §4), with its captured verdict. parentState is found, not found
// (proven by a complete read), not established, or unsupported (the
// reference names another kind). precedence is its place among the
// ListenerSets read for the same parent, after the Gateway's own listeners.
type ListenerSetRowDTO struct {
	UID                string            `json:"uid,omitempty"`
	GVR                string            `json:"gvr,omitempty"`
	Generation         int64             `json:"generation,omitempty"`
	Namespace          string            `json:"namespace"`
	Name               string            `json:"name"`
	Created            string            `json:"created,omitempty"` // RFC3339
	Parent             ListenerParentDTO `json:"parent"`
	ParentState        string            `json:"parentState"`
	ParentCurrent      bool              `json:"parentCurrent"` // a found parent is from a current read
	Listeners          []ListenerDTO     `json:"listeners"`
	AcceptedCond       CondDTO           `json:"acceptedCond"`
	ProgrammedCond     CondDTO           `json:"programmedCond"`
	Stale              bool              `json:"stale"`
	ObservedGeneration int64             `json:"observedGeneration"`
	Allowance          AllowanceDTO      `json:"allowance"`
	Precedence         int               `json:"precedence"`
	Siblings           int               `json:"siblings"`
	PrecedenceComplete bool              `json:"precedenceComplete"`
	Rule               RuleDTO           `json:"rule"`
}

func listenerDTOs(ls []gwapi.Listener, generation int64) []ListenerDTO {
	out := make([]ListenerDTO, 0, len(ls))
	for _, l := range ls {
		out = append(out, listenerDTO(l, generation))
	}
	return out
}

func listenerDTO(l gwapi.Listener, generation int64) ListenerDTO {
	d := ListenerDTO{Name: l.Name, Protocol: l.Protocol, Hostname: l.Hostname, Port: l.Port, CertificateRefs: certificateRefDTOs(l.CertificateRefs), TLSMode: l.TLSMode}
	if ar := l.AllowedRoutes; ar != nil {
		d.AllowedRoutes = &AllowedRoutesDTO{Namespaces: allowanceDTO(ar.Namespaces), Kinds: routeKindDTOs(ar.Kinds)}
	}
	if st := l.Status; st != nil {
		sd := &ListenerStatusDTO{SupportedKinds: routeKindDTOs(st.SupportedKinds), AttachedRoutes: st.AttachedRoutes, Conditions: make([]ListenerConditionDTO, 0, len(st.Conditions))}
		for _, c := range st.Conditions {
			cd := ListenerConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: gwapi.SafeMessage(c.Message), ObservedGeneration: c.ObservedGeneration,
				Stale: generation > 0 && c.ObservedGeneration < generation}
			if !c.LastTransitionTime.IsZero() {
				cd.LastTransitionTime = c.LastTransitionTime.UTC().Format(time.RFC3339)
			}
			sd.Conditions = append(sd.Conditions, cd)
		}
		d.Status = sd
	}
	return d
}

func routeKindDTOs(ks []gwapi.RouteKind) []RouteKindDTO {
	out := make([]RouteKindDTO, 0, len(ks))
	for _, k := range ks {
		out = append(out, RouteKindDTO{Group: k.Group, Kind: k.Kind})
	}
	return out
}

func allowanceDTO(a gwapi.NamespaceAllowance) NamespaceAllowanceDTO {
	d := NamespaceAllowanceDTO{Set: a.Set, From: a.From, SelectorInvalid: a.SelectorInvalid}
	if a.Selector != nil {
		d.Selector = metav1.FormatLabelSelector(a.Selector)
	}
	return d
}

// listenerSetRowDTO is one row of the ListenerSets part; the rule is the
// captured evaluation.
func listenerSetRowDTO(r healthsrc.Row[healthsrc.ListenerSetRow]) ListenerSetRowDTO {
	ls := r.Data.Set
	d := ListenerSetRowDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Generation: r.Ref.Generation, Namespace: ls.Namespace, Name: ls.Name,
		Parent:      ListenerParentDTO{Group: ls.Parent.Group, Kind: ls.Parent.Kind, Namespace: ls.Parent.Namespace, Name: ls.Parent.Name},
		ParentState: string(r.Data.ParentState), ParentCurrent: r.Data.ParentCurrent, Listeners: listenerDTOs(ls.Listeners, ls.Generation),
		AcceptedCond: condDTO(ls.AcceptedCond), ProgrammedCond: condDTO(ls.ProgrammedCond), Stale: ls.Stale, ObservedGeneration: ls.ObservedGeneration,
		Allowance:  AllowanceDTO{From: r.Data.Allowance.From, Allowed: r.Data.Allowance.Allowed, Text: r.Data.Allowance.Text, Disagrees: r.Data.Allowance.Disagrees},
		Precedence: r.Data.Precedence, Siblings: r.Data.Siblings, PrecedenceComplete: r.Data.PrecedenceComplete, Rule: toRuleDTO(r.Eval)}
	if !ls.Created.IsZero() {
		d.Created = ls.Created.UTC().Format(time.RFC3339)
	}
	return d
}

// routeRowDTO is one row of the routes part (S7b §5): the reduced
// attachment's captured verdict, the route's kind and hostnames, and every
// attachment with its evidence.
func routeRowDTO(r healthsrc.Row[healthsrc.RouteRow]) GatewayRouteRowDTO {
	a := r.Data.Attached
	dto := GatewayRouteRowDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Generation: r.Ref.Generation, Kind: a.Kind,
		Namespace: a.Namespace, Name: a.Name, Parent: a.Parent, ParentLabel: a.ParentLabel, Gateway: a.Gateway,
		Hostnames: sliceOrEmpty(r.Data.Hostnames), HostnameCount: r.Data.HostnameCount,
		Rule:        RuleDTO{Included: a.Included, Role: a.Role, Label: a.Label},
		Attachments: make([]GatewayRouteAttachmentDTO, 0, len(a.Attachments))}
	for i, at := range a.Attachments {
		ad := GatewayRouteAttachmentDTO{Parent: at.Parent, ParentLabel: at.ParentLabel, Gateway: at.Gateway,
			Rule: RuleDTO{Included: at.Included, Role: at.Role, Label: at.Label}}
		if p := at.ParentRef; p != nil {
			ad.ParentRef = &ParentRefDTO{Group: p.Group, Kind: p.Kind, Namespace: p.Namespace, Name: p.Name, SectionName: p.SectionName, Port: p.Port}
		}
		if i < len(r.Data.Evidence) {
			ev := r.Data.Evidence[i]
			ed := &AttachmentEvDTO{Controller: ev.Controller, ControllerWhy: ev.ControllerWhy, Features: ev.Features, Disagreements: ev.Disagreements,
				Allowance: AllowanceDTO{Allowed: ev.Allowance.Allowed, Text: ev.Allowance.Text}}
			for _, o := range ev.Others {
				ed.Others = append(ed.Others, OtherStatusDTO{Controller: o.Controller, Accepted: o.Accepted, ResolvedRefs: o.ResolvedRefs})
			}
			for _, g := range ev.Grants {
				ed.Grants = append(ed.Grants, GrantDTO{Rule: g.Rule, Backend: g.Backend, Allowed: g.Allowed, By: g.By, Text: g.Text})
			}
			ad.Evidence = ed
		}
		dto.Attachments = append(dto.Attachments, ad)
	}
	return dto
}

// routeNodeS7b fills a topology lane's S7b fields (§5, §9): its kind and
// attachment, the attachment rule's verdict when the read evaluated it,
// the controllers, the evidence and rule names.
func routeNodeS7b(rd *RouteNodeDTO, r gwapi.RouteNode) {
	rd.Kind, rd.APIVersion = r.Kind, r.APIVersion
	if rd.Kind == "" {
		rd.Kind = gwapi.KindHTTPRoute
	}
	rd.Attachment, rd.AttachmentLabel = r.Attachment, r.AttachmentLabel
	if r.Attachment != "" {
		p := r.ParentRef
		rd.ParentRef = &ParentRefDTO{Group: p.Group, Kind: p.Kind, Namespace: p.Namespace, Name: p.Name, SectionName: p.SectionName, Port: p.Port}
	}
	if r.Verdict != nil {
		rd.Rule = toRuleDTO(*r.Verdict)
	}
	rd.Controller, rd.ControllerWhy = r.Controller, r.ControllerWhy
	for _, o := range r.OtherControllers {
		rd.Others = append(rd.Others, OtherStatusDTO{Controller: o.ControllerName, Accepted: o.Accepted.Status, ResolvedRefs: o.ResolvedRefs.Status})
	}
	if ev := r.Evidence; ev != nil {
		ed := &AttachmentEvDTO{Controller: r.Controller, ControllerWhy: r.ControllerWhy, Others: rd.Others, Features: ev.Features, Disagreements: ev.Disagreements,
			Allowance: AllowanceDTO{Allowed: ev.Allowance.Allowed, Text: ev.Allowance.Text}}
		for _, g := range ev.Grants {
			ed.Grants = append(ed.Grants, GrantDTO{Rule: g.Rule, Backend: g.Backend, Allowed: g.Allowed, By: g.By, Text: g.Text})
		}
		rd.Evidence = ed
	}
	rd.RuleNames = sliceOrEmpty(r.RuleNames)
	rd.HostnameCount = r.HostnameCount
	if rd.HostnameCount == 0 {
		rd.HostnameCount = len(r.Hostnames)
	}
}
