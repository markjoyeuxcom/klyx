package sources

import "testing"

func TestALineageKeyMovesWithEveryField(t *testing.T) {
	base := Lineage{Kind: "metrics", Mode: "subscribed", Identity: "id", Installation: "i", InstRev: 1, SubRev: 2,
		Host: "prd", HostConn: 3, Access: "a", Tenant: "t", Match: "cluster=dev", CredRev: 4}
	again := base
	if base.Key() == "" || again.Key() != base.Key() || (Lineage{}).Key() != "" {
		t.Fatal("the key is not stable")
	}
	for name, l := range map[string]Lineage{
		"kind":         func() Lineage { l := base; l.Kind = "loki"; return l }(),
		"mode":         func() Lineage { l := base; l.Mode = "direct"; return l }(),
		"identity":     func() Lineage { l := base; l.Identity = "id2"; return l }(),
		"installation": func() Lineage { l := base; l.Installation = "j"; return l }(),
		"instRev":      func() Lineage { l := base; l.InstRev = 9; return l }(),
		"subRev":       func() Lineage { l := base; l.SubRev = 9; return l }(),
		"host":         func() Lineage { l := base; l.Host = "stg"; return l }(),
		"hostConn":     func() Lineage { l := base; l.HostConn = 9; return l }(),
		"access":       func() Lineage { l := base; l.Access = "b"; return l }(),
		"tenant":       func() Lineage { l := base; l.Tenant = "u"; return l }(),
		"match":        func() Lineage { l := base; l.Match = "cluster=prd"; return l }(),
		"credRev":      func() Lineage { l := base; l.CredRev = 9; return l }(),
		// a field's text never slides into the next
		"boundaries": func() Lineage { l := base; l.Identity, l.Installation = "id|i", ""; return l }(),
	} {
		if l.Key() == base.Key() {
			t.Errorf("%s: the key did not move", name)
		}
	}
}
