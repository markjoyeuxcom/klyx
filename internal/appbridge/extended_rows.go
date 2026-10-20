package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/extres"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Coverage S11a §2, §6: the extended task's rows. Figures cross as decimal
// strings ("-" when not established, figureDTO).

// ReservedDTO is one entry of a node's reservations.
type ReservedDTO struct {
	Name          string `json:"name"`
	Advertised    bool   `json:"advertised,omitempty"`
	Requested     string `json:"requested"`
	ServedByDRA   string `json:"servedByDra"`
	NotAdvertised string `json:"notAdvertised"`
}

// ReservationDTO is one node's reservations, joined to its node row by UID.
type ReservationDTO struct {
	UID     string        `json:"uid"`
	Node    string        `json:"node"`
	Entries []ReservedDTO `json:"entries"`
	// Omitted counts entries past the row's bound; Untracked says the
	// node's not-advertised and DRA-served figures of untracked names are
	// not established (more than 256 distinct names).
	Omitted   int  `json:"omitted,omitempty"`
	Untracked bool `json:"untracked,omitempty"`
}

// ExtendedTotalDTO is one accelerator name's cluster total.
type ExtendedTotalDTO struct {
	Name        string `json:"name"`
	Capacity    string `json:"capacity"`
	Allocatable string `json:"allocatable"`
	Requested   string `json:"requested"`
}

func qtyDTO(q extres.Qty) string { return figureDTO(&q) }

func init() {
	rowsConverters[healthsrc.TaskExtended+"/reservations"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.ReservationRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ReservationDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			d := ReservationDTO{UID: r.Ref.UID, Node: r.Data.Node, Entries: make([]ReservedDTO, 0, len(r.Data.Entries)), Omitted: r.Data.Omitted, Untracked: r.Data.Untracked}
			for _, e := range r.Data.Entries {
				d.Entries = append(d.Entries, ReservedDTO{Name: e.Name, Advertised: e.Advertised, Requested: qtyDTO(e.Requested),
					ServedByDRA: qtyDTO(e.ServedByDRA), NotAdvertised: qtyDTO(e.NotAdvertised)})
			}
			out = append(out, d)
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskExtended+"/totals"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.TotalRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ExtendedTotalDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, ExtendedTotalDTO{Name: r.Data.Name, Capacity: qtyDTO(r.Data.Capacity), Allocatable: qtyDTO(r.Data.Allocatable), Requested: qtyDTO(r.Data.Requested)})
		}
		return out, nil
	}
}
