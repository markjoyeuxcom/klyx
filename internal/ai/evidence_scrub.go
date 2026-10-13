package ai

import (
	"regexp"
	"strings"
)

// EvidenceFieldMax caps one free-text value that enters a prompt (spec
// 2026-10-13 §4.2).
const EvidenceFieldMax = 500

// evidenceSecretShapes are credential shapes that cluster text can carry: a
// condition message quoting a URL with userinfo, an event echoing a header, a
// PEM block in an annotation-derived fact. Each match is replaced, never
// partially kept.
var evidenceSecretShapes = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----[\s\S]*?(-----END [A-Z0-9 ]+-----|$)`), "[redacted PEM block]"},
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+(:[^/\s@]*)?@`), "${1}[redacted]@"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{6,}`), "${1} [redacted]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`), "[redacted]"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "[redacted]"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`), "[redacted]"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), "[redacted]"},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`), "[redacted]"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}`), "[redacted]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}`), "[redacted]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "[redacted]"},
	{regexp.MustCompile(`(?i)\b(password|passwd|pwd|token|secret|api[_-]?key|access[_-]?key|client[_-]?secret|account[_-]?key|private[_-]?key|sig|key)(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&)}\]]+)`), "${1}${2}[redacted]"},
}

// ScrubEvidence removes credential shapes from one piece of cluster text and
// caps it at max characters (EvidenceFieldMax when max <= 0). It reports how
// many replacements and truncations it made, so the UI can say "scrubbed" -
// never "secret-free": a heuristic reduces, it does not prove.
func ScrubEvidence(s string, max int) (string, int) {
	if max <= 0 {
		max = EvidenceFieldMax
	}
	n := 0
	for _, shape := range evidenceSecretShapes {
		s = shape.re.ReplaceAllStringFunc(s, func(m string) string {
			n++
			return shape.re.ReplaceAllString(m, shape.repl)
		})
	}
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max])) + "…[truncated]"
		n++
	}
	return s, n
}
