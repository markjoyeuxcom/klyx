package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/karpenter"
)

// Coverage S10's rows: the NodePools with their findings and class link,
// the NodeClaims' class reference, and the NodeClasses as fact rows, from
// the SAME evaluation as the parts' counts. Fields named native or message
// carry the provider's own words for the local inspector only: no AI or
// chat allowlist names them (§8).

// KarpenterRefDTO is a nodeClassRef.
type KarpenterRefDTO struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
}

// KarpenterCondDTO is one condition. Message is native, local only.
type KarpenterCondDTO struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	ReasonWithheld     bool       `json:"reasonWithheld,omitempty"`
	Message            string     `json:"message,omitempty"`
	ObservedGeneration int64      `json:"observedGeneration,omitempty"`
	At                 *time.Time `json:"at,omitempty"`
}

// KarpenterFindingDTO is one NodePool finding. Text is Klyx's wording;
// Native is the provider's, local only.
type KarpenterFindingDTO struct {
	Role        string `json:"role"`
	Label       string `json:"label"`
	Text        string `json:"text,omitempty"`
	Native      string `json:"native,omitempty"`
	Note        bool   `json:"note,omitempty"`
	Unversioned bool   `json:"unversioned,omitempty"`
}

// KarpenterReadingDTO is a class's own reading: display evidence, never a
// verdict. Native is local only; Note is Klyx's wording for a False
// condition reported for an older generation.
type KarpenterReadingDTO struct {
	State  string `json:"state"`
	Text   string `json:"text,omitempty"`
	Native string `json:"native,omitempty"`
	Note   string `json:"note,omitempty"`
}

// KarpenterLinkDTO is a NodePool's class link (§3).
type KarpenterLinkDTO struct {
	State   string `json:"state"`
	Adapter string `json:"adapter,omitempty"`
	Group   string `json:"group,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Version string `json:"version,omitempty"`
	// AsOf is a stale link's last good read.
	AsOf *time.Time `json:"asOf,omitempty"`
	// ClassUID names the linked class, when found or stale with a last good
	// one, so the inspector can open it.
	ClassUID string `json:"classUid,omitempty"`
}

// KarpenterClassFactsDTO is a class's provider facts: counts and names.
// Role and InstanceProfile are local only (§8).
type KarpenterClassFactsDTO struct {
	Subnets           *int   `json:"subnets,omitempty"`
	SecurityGroups    *int   `json:"securityGroups,omitempty"`
	AMIs              *int   `json:"amis,omitempty"`
	DeprecatedAMI     bool   `json:"deprecatedAmi,omitempty"`
	PodSubnets        bool   `json:"podSubnets,omitempty"`
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
	Images            *int   `json:"images,omitempty"`
	ImageFamily       string `json:"imageFamily,omitempty"`
	SubnetSet         bool   `json:"subnetSet,omitempty"`
	Role              string `json:"role,omitempty"`
	InstanceProfile   string `json:"instanceProfile,omitempty"`
}

// KarpenterClassDTO is one NodeClass row: a fact (no rule), with its reading
// and its users (§4).
type KarpenterClassDTO struct {
	UID             string                 `json:"uid"`
	GVR             string                 `json:"gvr"`
	Generation      int64                  `json:"generation"`
	Name            string                 `json:"name"`
	Group           string                 `json:"group"`
	Kind            string                 `json:"kind"`
	Adapter         string                 `json:"adapter"`
	Created         *time.Time             `json:"created,omitempty"`
	Deleting        bool                   `json:"deleting,omitempty"`
	Syncing         bool                   `json:"syncing,omitempty"`
	Reading         KarpenterReadingDTO    `json:"reading"`
	Conditions      []KarpenterCondDTO     `json:"conditions"`
	ConditionsTotal int                    `json:"conditionsTotal"`
	ConditionsCut   bool                   `json:"conditionsCut,omitempty"`
	Unknown         []string               `json:"unknown"`
	UnknownTotal    int                    `json:"unknownTotal,omitempty"`
	Facts           KarpenterClassFactsDTO `json:"facts"`
	Users           []string               `json:"users"`
	UsersComplete   bool                   `json:"usersComplete"`
}

func karpenterRefDTO(r *karpenter.ClassRef) *KarpenterRefDTO {
	if r == nil {
		return nil
	}
	return &KarpenterRefDTO{Group: r.Group, Kind: r.Kind, Name: r.Name}
}

func karpenterConds(c karpenter.Conds) []KarpenterCondDTO {
	out := make([]KarpenterCondDTO, 0, len(c.Display))
	for _, x := range c.Display {
		out = append(out, KarpenterCondDTO{Type: x.Type, Status: x.Status, Reason: x.Reason, ReasonWithheld: x.ReasonWithheld,
			Message: x.Message, ObservedGeneration: x.ObservedGeneration, At: timePtrOf(x.At)})
	}
	return out
}

func karpenterReading(r karpenter.ClassReading) KarpenterReadingDTO {
	return KarpenterReadingDTO{State: r.State, Text: r.Text, Native: r.Native, Note: r.Note}
}

// karpenterPoolRowDTO is a pool row with S10's link, findings and
// conditions beside the lens fields.
func karpenterPoolRowDTO(r healthsrc.Row[healthsrc.KarpenterPoolRow]) KarpenterPoolDTO {
	d := karpenterPoolFields(r.Data.Pool)
	d.Rule = toRuleDTO(r.Eval)
	d.UID, d.GVR, d.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
	p := r.Data.Pool
	d.NodeClassRef = karpenterRefDTO(p.Ref)
	d.NodeClassObservedGeneration = p.NodeClassObservedGeneration
	d.Conditions, d.ConditionsTotal, d.ConditionsCut = karpenterConds(p.Conds), p.Conds.Total, p.Conds.Cut
	l := r.Data.Link
	d.Link = &KarpenterLinkDTO{State: string(l.State), Adapter: string(l.Kind.Adapter), Group: l.Kind.Group, Kind: l.Kind.Kind,
		Version: l.Kind.Version, AsOf: timePtrOf(l.AsOf)}
	if l.Class != nil {
		d.Link.ClassUID = l.Class.Meta.UID
	}
	if r.Data.Class != nil {
		c := karpenterReading(*r.Data.Class)
		d.Class = &c
	}
	d.Handler = r.Data.Handler
	d.Findings = make([]KarpenterFindingDTO, 0, len(r.Data.Findings))
	for _, f := range r.Data.Findings {
		d.Findings = append(d.Findings, KarpenterFindingDTO{Role: f.Role.String(), Label: f.Label, Text: f.Text, Native: f.Native, Note: f.Note, Unversioned: f.Unversioned})
	}
	return d
}

func karpenterClassRowDTO(r healthsrc.Row[healthsrc.KarpenterClassRow]) KarpenterClassDTO {
	c := r.Data.Class
	f := c.Facts
	unknown := append([]string{}, c.Unknown...)
	return KarpenterClassDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Generation: r.Ref.Generation, Name: c.Meta.Name,
		Group: r.Data.Kind.Group, Kind: r.Data.Kind.Kind, Adapter: string(c.Adapter), Created: timePtrOf(c.Meta.Created),
		Deleting: c.Meta.Deleting, Syncing: c.Syncing, Reading: karpenterReading(r.Data.Reading),
		Conditions: karpenterConds(c.Conds), ConditionsTotal: c.Conds.Total, ConditionsCut: c.Conds.Cut,
		Unknown: unknown, UnknownTotal: c.UnknownTotal,
		Facts: KarpenterClassFactsDTO{Subnets: f.Subnets, SecurityGroups: f.SecurityGroups, AMIs: f.AMIs, DeprecatedAMI: f.DeprecatedAMI,
			PodSubnets: f.PodSubnets, KubernetesVersion: f.KubernetesVersion, Images: f.Images, ImageFamily: f.ImageFamily, SubnetSet: f.SubnetSet,
			Role: f.Role, InstanceProfile: f.InstanceProfile},
		Users: nonNil(r.Data.Users), UsersComplete: r.Data.UsersComplete}
}

func init() {
	rowsConverters[healthsrc.TaskKarpenter+"/nodeclasses"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.KarpenterClassRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]KarpenterClassDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, karpenterClassRowDTO(r))
		}
		return out, nil
	}
}
