package capability

import (
	"context"
	"encoding/json"
)

// DRACapability is dynamic resource allocation (coverage S11b §5): the
// lens appears where resource.k8s.io/v1 is served and some DRA object
// exists. Every 1.34+ cluster serves the API, so serving alone does not
// make it present.
type DRACapability struct {
	Base
	Served  bool
	Present bool
}

// detectDRA lists one object of each DRA kind. A failed list confirms
// nothing: a served API whose lists all fail reads present, unverified
// (the lens then says what it could not read).
func (d *Detector) detectDRA(ctx context.Context, served map[string]bool) DRACapability {
	out := DRACapability{Served: served["resource.k8s.io/v1"]}
	if !out.Served {
		return out
	}
	failed := 0
	for _, r := range []string{"resourceslices", "resourceclaims", "deviceclasses"} {
		raw, err := d.list(ctx, "/apis/resource.k8s.io/v1/"+r, map[string]string{"limit": "1"})
		if err != nil {
			failed++
			continue
		}
		var l struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(raw, &l) == nil && len(l.Items) > 0 {
			out.Present = true
			return out
		}
	}
	if failed == 3 {
		out.Present = true
		out.Base = Base{Tier: Degraded, Reason: "the DRA objects could not be listed"}
	}
	return out
}
