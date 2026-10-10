package appbridge

import "github.com/moomora/klyx/internal/health"

// HealthDTO is the shared verdict (internal/health) on every classified row.
// The frontend maps Kind to tone, lamp and order and never derives health
// itself; Label heads both the list row and the inspector.
type HealthDTO struct {
	Kind    string `json:"kind"` // fault | degraded | unknown | progressing | paused | ok
	Label   string `json:"label"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
	Native  string `json:"native,omitempty"`
	Note    string `json:"note,omitempty"`
	Rule    string `json:"rule"`
	Rank    int    `json:"rank,omitempty"`
	// ReclassifyAtUnix is the next time-threshold crossing that could change
	// the verdict with no object change; 0 when none is pending.
	ReclassifyAtUnix int64 `json:"reclassifyAtUnix,omitempty"`
}

func toHealthDTO(v health.Verdict) HealthDTO {
	d := HealthDTO{
		Kind: v.Kind.String(), Label: v.Label, Reason: v.Reason, Message: v.Message,
		Native: v.Native, Note: v.Note, Rule: v.Rule, Rank: v.Rank,
	}
	if v.ReclassifyAt != nil {
		d.ReclassifyAtUnix = v.ReclassifyAt.Unix()
	}
	return d
}
