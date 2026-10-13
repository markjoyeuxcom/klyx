package ai

import (
	"errors"
	"regexp"
	"strings"
)

// scrubExcerptMax bounds any provider text Klyx shows: an error body is an
// excerpt, never a dump.
const scrubExcerptMax = 300

// secretShapes are credential shapes a provider error can echo back (a key in
// a "invalid x-api-key: sk-..." message, a header in a debug dump).
var secretShapes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)\b(x-api-key|x-goog-api-key|api[_-]?key|authorization)(["']?\s*[:=]\s*["']?)[^\s"',}]{6,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}`),                                      // Anthropic and OpenAI keys
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}`),                                     // Google API keys
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), // JWTs
}

// Scrub removes the given secrets (the active key) and anything shaped like a
// credential from s, then bounds it. Provider errors pass through this before
// they reach a DTO, the Activity log or a log line (spec §2.1).
func Scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 6 {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	for _, re := range secretShapes {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			// Keep a header name readable ("x-api-key: [redacted]").
			if sub := re.FindStringSubmatchIndex(m); len(sub) >= 6 && sub[4] >= 0 {
				return m[:sub[5]] + "[redacted]"
			}
			return "[redacted]"
		})
	}
	s = strings.TrimSpace(s)
	if len(s) > scrubExcerptMax {
		s = s[:scrubExcerptMax] + "…"
	}
	return s
}

// scrubbedError wraps err with its message scrubbed, keeping errors.Is/As
// working against the original.
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// scrubErr scrubs err's message of secrets. Typed outcome errors (refusal,
// truncation, ...) carry no secret and pass through unchanged.
func scrubErr(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	var refused *RefusedError
	if errors.As(err, &refused) || errors.Is(err, ErrTruncated) || errors.Is(err, ErrEmpty) || errors.Is(err, ErrUnexpectedOutput) {
		return err
	}
	return &scrubbedError{msg: Scrub(err.Error(), secrets...), err: err}
}
