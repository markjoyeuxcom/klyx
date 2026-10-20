package appbridge

import (
	"testing"

	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Coverage S11b §2.0: a pool row carries each view's device count, never
// the names, and what the row's bound left out.
func TestDRAPoolDTOCarriesCounts(t *testing.T) {
	var slices []dra.Slice
	for i, n := range []string{"n1", "n2", "n3"} {
		s := dra.Slice{Name: n, UID: n, Driver: "d", Pool: "p", Generation: 1, Count: 3, NodeName: n}
		for d := 0; d <= i; d++ {
			s.Devices = append(s.Devices, dra.Device{Name: n + "-g" + string(rune('0'+d))})
		}
		slices = append(slices, s)
	}
	p := dra.ReadPool(slices, 37).Kept(2)
	dto := toDRAPoolDTO(healthsrc.Row[healthsrc.DRAPoolRow]{Ref: healthsrc.RowRef{Name: "d/p"}, Data: healthsrc.DRAPoolRow{Pool: p}})
	if dto.Nodes != 3 || len(dto.Views) != 2 || dto.ViewsOmitted != 2 {
		t.Fatalf("3 nodes and the unnamed view, 2 kept, 2 counted: %+v", dto)
	}
	if v := dto.Views[0]; v.Node != "n1" || v.Variants[0].Devices != 1 {
		t.Fatalf("n1's view counts its one device: %+v", v)
	}
	if v := dto.Views[1]; v.Node != "n2" || v.Variants[0].Devices != 2 {
		t.Fatalf("n2's view counts its two devices: %+v", v)
	}
}
