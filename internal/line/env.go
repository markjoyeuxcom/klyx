// Package line is The Line's model (The Line spec §3,
// docs/superpowers/specs/2026-10-14-klyx-the-line-redesign.md): stations
// placed on tracks by environment rank, comparison units and their revision
// facts, the comparison key and revision, and each station's state, flags and
// held evidence. Pure: plain values in, plain values out, no clock.
package line

import (
	"regexp"
	"strings"
)

// Environment ranks, in line order. RankNone is a cluster with no
// recognised environment: it goes to the siding.
const (
	RankDev  = 0
	RankTest = 1
	RankStg  = 2
	RankPrd  = 3
	RankNone = -1
)

// envRanks is the one environment ranking (moved from FleetSpine's
// ENV_RANK; a parity test pins the frontend's copy until slice 4 reads
// ranks from LineDTO).
var envRanks = []struct {
	re   *regexp.Regexp
	rank int
}{
	{regexp.MustCompile(`^(dev|develop|development)$`), RankDev},
	{regexp.MustCompile(`^(test|testing|qa|int|integration|uat)$`), RankTest},
	{regexp.MustCompile(`^(stg|stage|staging|preprod|pre-prod)$`), RankStg},
	{regexp.MustCompile(`^(prd|prod|production)$`), RankPrd},
}

// EnvRank ranks an environment name, case- and space-insensitively;
// RankNone when it is not recognised.
func EnvRank(env string) int {
	e := strings.ToLower(strings.TrimSpace(env))
	for _, r := range envRanks {
		if r.re.MatchString(e) {
			return r.rank
		}
	}
	return RankNone
}
