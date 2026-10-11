package appbridge

import "github.com/moomora/klyx/internal/health"

// RuleDTO is a relocated lens rule's verdict for one object (1c addendum
// §2): the lens's inclusion predicate and presentation role, computed once
// in Go so the lens and the health summary can never disagree. The lens reads
// these instead of computing its own.
type RuleDTO struct {
	Included bool   `json:"included"`
	Role     string `json:"role"`
	Label    string `json:"label"`
}

func toRuleDTO(e health.NativeEval) RuleDTO {
	return RuleDTO{Included: e.Included, Role: e.Role.String(), Label: e.Label}
}
