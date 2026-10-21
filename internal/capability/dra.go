package capability

import (
	"context"
	"encoding/json"
	"strings"
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
// nothing, so it is never read as absence: a served API with no object
// found and any list failed reads present, unverified, naming the kinds it
// could not list (the lens then says what it could not read).
func (d *Detector) detectDRA(ctx context.Context, served map[string]bool) DRACapability {
	out := DRACapability{Served: served["resource.k8s.io/v1"]}
	if !out.Served {
		return out
	}
	var failed []string
	for _, r := range []string{"resourceslices", "resourceclaims", "deviceclasses"} {
		raw, err := d.list(ctx, "/apis/resource.k8s.io/v1/"+r, map[string]string{"limit": "1"})
		var l struct {
			Items []json.RawMessage `json:"items"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &l)
		}
		if err != nil {
			failed = append(failed, r)
			continue
		}
		if len(l.Items) > 0 {
			out.Present = true
			return out
		}
	}
	if len(failed) > 0 {
		out.Present = true
		out.Base = Base{Tier: Degraded, Reason: "could not list " + strings.Join(failed, ", ")}
	}
	return out
}
