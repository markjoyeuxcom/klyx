package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/health"
)

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

// RuleStepDTO is the verdict a snapshot row takes from AtUnix on with no new
// read: a time-only boundary of the rule (Helm's pending deadline),
// evaluated in Go at read time. The lens switches on its clock; it never
// computes a rule of its own.
type RuleStepDTO struct {
	AtUnix int64   `json:"atUnix"`
	Rule   RuleDTO `json:"rule"`
}

// ruleStep evaluates a time-dependent rule at now and, when it names a
// boundary, at that boundary: the one step the verdict takes without data.
func ruleStep(rule func(time.Time) health.NativeEval, now time.Time) (RuleDTO, *RuleStepDTO) {
	e := rule(now)
	if e.Next == nil {
		return toRuleDTO(e), nil
	}
	return toRuleDTO(e), &RuleStepDTO{AtUnix: e.Next.Unix(), Rule: toRuleDTO(rule(*e.Next))}
}
