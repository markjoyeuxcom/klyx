// Package scrub removes credential shapes from cluster text. It is the shared
// low-level heuristic behind AI evidence (internal/ai) and the health
// summary's reasons (internal/health), so neither depends on the other: a
// heuristic reduces, it does not prove text secret-free.
package scrub

import "regexp"

// shapes are credential shapes cluster text can carry: a condition message
// quoting a URL with userinfo, an event echoing a header, a PEM block in an
// annotation-derived fact. Each match is replaced, never partially kept.
var shapes = []struct {
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

// Credentials replaces every credential shape in s and reports how many it
// replaced. It never truncates: callers cap after scrubbing, so a secret
// crossing a cap is replaced whole, never cut into a readable prefix.
func Credentials(s string) (string, int) {
	n := 0
	for _, shape := range shapes {
		s = shape.re.ReplaceAllStringFunc(s, func(m string) string {
			n++
			return shape.re.ReplaceAllString(m, shape.repl)
		})
	}
	return s, n
}

// identifiers are cloud identifier shapes (coverage S10 §8): providers write
// them into condition and event text (an Azure subnet's full ARM ID in
// SubnetsReady, an ARN in an access error), so excluding structured fields
// does not keep them out of a prompt. Each is replaced whole. A bare name
// (an instance profile's) has no shape: Karpenter's messages never cross at
// all, which no pattern could ensure.
var identifiers = []struct {
	re   *regexp.Regexp
	repl string
}{
	// an ARM resource ID, to the end of its path
	{regexp.MustCompile(`(?i)/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}[^\s"'<>,;)\]}]*`), "<azure-resource-id>"},
	// an ARN in any partition
	{regexp.MustCompile(`\barn:aws[a-z-]*:[^\s"'<>,;)\]}]+`), "<aws-arn>"},
	// an AWS resource ID of the kinds a node class resolves
	{regexp.MustCompile(`\b(subnet|sg|ami|vpc|eni|lt|cr|pg)-[0-9a-f]{8}([0-9a-f]{9})?\b`), "<aws-id>"},
}

// Identifiers replaces every cloud identifier shape in s and reports how
// many it replaced. Like Credentials it never truncates.
func Identifiers(s string) (string, int) {
	n := 0
	for _, id := range identifiers {
		s = id.re.ReplaceAllStringFunc(s, func(string) string {
			n++
			return id.repl
		})
	}
	return s, n
}
