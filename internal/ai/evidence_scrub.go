package ai

import (
	"strings"

	"github.com/moomora/klyx/internal/scrub"
)

// EvidenceFieldMax caps one free-text value that enters a prompt (spec
// 2026-10-13 §4.2).
const EvidenceFieldMax = 500

// ScrubEvidence removes credential shapes from one piece of cluster text and
// caps it at max characters (EvidenceFieldMax when max <= 0). It reports how
// many replacements and truncations it made, so the UI can say "scrubbed" -
// never "secret-free": a heuristic reduces, it does not prove.
func ScrubEvidence(s string, max int) (string, int) {
	if max <= 0 {
		max = EvidenceFieldMax
	}
	s, n := scrub.Credentials(s)
	// coverage S10 §8: cloud identifiers, before the cap, on every AI path
	s, m := scrub.Identifiers(s)
	n += m
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max])) + "…[truncated]"
		n++
	}
	return s, n
}
