package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Coverage S11b §4, §5: the dra task's rows for the DRA lens, the node
// inspector and the pod inspector.

// DRAReadingDTO is a reading in the lens's words.
type DRAReadingDTO struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Native  string `json:"native,omitempty"`
	Note    string `json:"note,omitempty"`
	Rule    string `json:"rule,omitempty"`
	// ReclassifyAt (unix): the reading's next time boundary, 0 when none.
	ReclassifyAt int64 `json:"reclassifyAtUnix,omitempty"`
}

func toDRAReading(v health.Verdict) DRAReadingDTO {
	d := DRAReadingDTO{Kind: v.Kind.String(), Label: v.Label, Reason: v.Reason, Message: v.Message, Native: v.Native, Note: v.Note, Rule: v.Rule}
	if v.ReclassifyAt != nil {
		d.ReclassifyAt = v.ReclassifyAt.Unix()
	}
	return d
}

type DRARequestDTO struct {
	Name        string   `json:"name"`
	Class       string   `json:"class,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Count       int64    `json:"count,omitempty"`
	AdminAccess bool     `json:"adminAccess,omitempty"`
	Subclasses  []string `json:"subclasses,omitempty"`
}

type DRAResultDTO struct {
	Request     string `json:"request"`
	Driver      string `json:"driver"`
	Pool        string `json:"pool"`
	Device      string `json:"device"`
	AdminAccess bool   `json:"adminAccess,omitempty"`
	ShareID     string `json:"shareId,omitempty"`
}

type DRAConsumerDTO struct {
	Resource string `json:"resource"`
	Name     string `json:"name"`
	UID      string `json:"uid"`
}

type DRACondDTO struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Message is the driver's own text: local only, never sent to a model.
	Message string `json:"message,omitempty"`
}

type DRADeviceStatusDTO struct {
	Driver     string       `json:"driver"`
	Pool       string       `json:"pool"`
	Device     string       `json:"device"`
	ShareID    string       `json:"shareId,omitempty"`
	Conditions []DRACondDTO `json:"conditions"`
}

type DRAConsumerReadingDTO struct {
	Pod    string `json:"pod"`
	Row    string `json:"row"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Reason string `json:"reason,omitempty"`
	// Meaning is the matched reason's documented meaning; Message the
	// scheduler's own text (local only).
	Meaning string `json:"meaning,omitempty"`
	Message string `json:"message,omitempty"`
}

// DRAClaimDTO is one ResourceClaim's row.
type DRAClaimDTO struct {
	UID              string                  `json:"uid"`
	Namespace        string                  `json:"namespace"`
	Name             string                  `json:"name"`
	Rule             RuleDTO                 `json:"rule"`
	Reading          DRAReadingDTO           `json:"reading"`
	Deleting         bool                    `json:"deleting,omitempty"`
	Finalizers       []string                `json:"finalizers,omitempty"`
	OwnerKind        string                  `json:"ownerKind,omitempty"`
	OwnerName        string                  `json:"ownerName,omitempty"`
	PodClaimName     string                  `json:"podClaimName,omitempty"`
	Extended         bool                    `json:"extended,omitempty"`
	Requests         []DRARequestDTO         `json:"requests"`
	Allocated        bool                    `json:"allocated"`
	Results          []DRAResultDTO          `json:"results"`
	ReservedCount    int                     `json:"reservedCount"`
	Reserved         []DRAConsumerDTO        `json:"reserved"`
	Devices          []DRADeviceStatusDTO    `json:"devices"`
	Consumers        []DRAConsumerReadingDTO `json:"consumers"`
	ConsumersOmitted int                     `json:"consumersOmitted,omitempty"`
}

// DRAVariantDTO is one scheduler's outcome for one view.
type DRAVariantDTO struct {
	Minor         int    `json:"minor"`
	Partitionable bool   `json:"partitionable"`
	Outcome       string `json:"outcome"`
	Devices       int    `json:"devices"`
	Have          int64  `json:"have,omitempty"`
	Want          int64  `json:"want,omitempty"`
	Rule          string `json:"rule,omitempty"`
}

// DRAViewDTO is one node's view of a pool ("" is a node no slice names).
type DRAViewDTO struct {
	Node     string          `json:"node"`
	Agreed   bool            `json:"agreed"`
	Kind     string          `json:"kind,omitempty"`
	Variants []DRAVariantDTO `json:"variants"`
}

// DRAPoolDTO is one pool's row: driver and pool are its identity.
type DRAPoolDTO struct {
	Key        string        `json:"key"` // driver/pool
	Driver     string        `json:"driver"`
	Pool       string        `json:"pool"`
	Generation int64         `json:"generation"`
	Slices     int           `json:"slices"`
	Rule       RuleDTO       `json:"rule"`
	Reading    DRAReadingDTO `json:"reading"`
	Views      []DRAViewDTO  `json:"views"`
	Obsolete   []string      `json:"obsolete,omitempty"`
	Assumed    string        `json:"assumed"`
	Unread     bool          `json:"unread,omitempty"`
}

// DRAClassDTO is one DeviceClass's facts.
type DRAClassDTO struct {
	UID                  string   `json:"uid"`
	Name                 string   `json:"name"`
	Selectors            int      `json:"selectors"`
	ConfigDrivers        []string `json:"configDrivers"`
	ExtendedResourceName string   `json:"extendedResourceName,omitempty"`
}

func toDRAClaimDTO(r healthsrc.Row[healthsrc.DRAClaimRow]) DRAClaimDTO {
	c := r.Data.Claim
	d := DRAClaimDTO{UID: c.UID, Namespace: c.Namespace, Name: c.Name, Rule: toRuleDTO(r.Eval), Reading: toDRAReading(r.Data.Verdict),
		Deleting: c.Deleting, Finalizers: c.Finalizers, OwnerKind: c.OwnerKind, OwnerName: c.OwnerName, PodClaimName: c.PodClaimName, Extended: c.Extended,
		Requests: []DRARequestDTO{}, Allocated: c.Allocated, Results: []DRAResultDTO{}, ReservedCount: c.ReservedCount, Reserved: []DRAConsumerDTO{},
		Devices: []DRADeviceStatusDTO{}, Consumers: []DRAConsumerReadingDTO{}, ConsumersOmitted: r.Data.ConsumersOmitted}
	for _, q := range c.Requests {
		d.Requests = append(d.Requests, DRARequestDTO{Name: q.Name, Class: q.Class, Mode: q.Mode, Count: q.Count, AdminAccess: q.AdminAccess, Subclasses: q.Subclasses})
	}
	for _, x := range c.Results {
		d.Results = append(d.Results, DRAResultDTO{Request: x.Request, Driver: x.Driver, Pool: x.Pool, Device: x.Device, AdminAccess: x.AdminAccess, ShareID: x.ShareID})
	}
	for _, x := range c.Reserved {
		d.Reserved = append(d.Reserved, DRAConsumerDTO{Resource: x.Resource, Name: x.Name, UID: x.UID})
	}
	for _, x := range c.Devices {
		ds := DRADeviceStatusDTO{Driver: x.Driver, Pool: x.Pool, Device: x.Device, ShareID: x.ShareID, Conditions: []DRACondDTO{}}
		for _, cond := range x.Conditions {
			ds.Conditions = append(ds.Conditions, DRACondDTO{Type: cond.Type, Status: cond.Status, Reason: cond.Reason, Message: cond.Message})
		}
		d.Devices = append(d.Devices, ds)
	}
	for _, x := range r.Data.Consumers {
		d.Consumers = append(d.Consumers, DRAConsumerReadingDTO{Pod: x.Pod, Row: x.Row, Kind: x.Kind.String(), Label: x.Label, Reason: x.Reason,
			Meaning: dra.Meaning(x.Reason), Message: x.Message})
	}
	return d
}

func toDRAPoolDTO(r healthsrc.Row[healthsrc.DRAPoolRow]) DRAPoolDTO {
	p := r.Data.Pool
	d := DRAPoolDTO{Key: r.Ref.Name, Driver: p.Driver, Pool: p.Pool, Generation: p.Generation, Slices: p.Slices, Rule: toRuleDTO(r.Eval),
		Reading: toDRAReading(r.Data.Verdict), Views: []DRAViewDTO{}, Obsolete: p.Obsolete, Assumed: p.Assumed(), Unread: r.Data.Unread}
	for _, v := range p.Views {
		vd := DRAViewDTO{Node: v.Node, Agreed: v.Agreed, Kind: v.Kind, Variants: []DRAVariantDTO{}}
		for _, o := range v.Outcomes {
			vd.Variants = append(vd.Variants, DRAVariantDTO{Minor: o.Variant.Minor, Partitionable: o.Variant.Partitionable, Outcome: o.Outcome.Kind,
				Devices: len(o.Outcome.Devices), Have: o.Outcome.Have, Want: o.Outcome.Want, Rule: o.Outcome.Rule})
		}
		d.Views = append(d.Views, vd)
	}
	return d
}

func init() {
	rowsConverters[healthsrc.TaskDRA+"/claims"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.DRAClaimRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]DRAClaimDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, toDRAClaimDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskDRA+"/pools"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.DRAPoolRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]DRAPoolDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, toDRAPoolDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskDRA+"/classes"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.DRAClassRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]DRAClassDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			c := r.Data.Class
			drivers := c.ConfigDrivers
			if drivers == nil {
				drivers = []string{}
			}
			out = append(out, DRAClassDTO{UID: c.UID, Name: c.Name, Selectors: c.Selectors, ConfigDrivers: drivers, ExtendedResourceName: c.ExtendedResourceName})
		}
		return out, nil
	}
}
