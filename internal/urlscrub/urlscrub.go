// Package urlscrub removes credentials from URLs in display text (coverage
// S0b §2 and §4). It is conservative: an incomplete or ambiguous URL is
// treated as credential-bearing, never as proof of safety - kubectl cuts long
// annotation lines at 138 bytes plus "...", so a URL can lose its "@" - and a
// candidate it cannot delimit withholds its whole line. There is no raw
// fallback.
package urlscrub

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// WithheldLine replaces a line holding a candidate that cannot be safely
// delimited.
const WithheldLine = "<line withheld: possible credential>"

// WithheldURL replaces a URL field whose value cannot be safely delimited.
const WithheldURL = "<withheld: unparseable URL>"

// Redacted replaces an authority that may carry credentials.
const Redacted = "<redacted>"

// Result is one scrub: the text, how many URLs lost user info or were
// redacted, and how many lines were withheld.
type Result struct {
	Text     string
	Removed  int
	Withheld int
}

// Changed reports whether anything was removed or withheld.
func (r Result) Changed() bool { return r.Removed > 0 || r.Withheld > 0 }

var (
	scheme = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://`)
	// scp is user@host:path with no scheme, at a token start.
	scp = regexp.MustCompile(`(^|[\s'"(\[<,;=])([A-Za-z0-9._%+~-]+)@([A-Za-z0-9.-]+\.[A-Za-z]{2,}|[A-Za-z0-9-]+):`)
)

// hardEnd reports whether r ends a URL authority under any reading: the
// path, query and fragment delimiters, space, and characters a URL can never
// hold. RFC 3986 lets user info carry the sub-delimiters ' , ; ( ), so those
// do not end it (Codex's S0b implementation review, finding 1).
func hardEnd(r rune) bool {
	switch r {
	case '/', '?', '#', '"', '<', '>', '`', '{', '}', '|', '\\':
		return true
	}
	return unicode.IsSpace(r)
}

// softEnd reports whether r ends a host[:port] in prose: a sub-delimiter
// that cannot be part of a host name. It is used only when the span holds
// no "@", so no user info can precede it.
func softEnd(r rune) bool {
	switch r {
	case '\'', ',', ';', '(', ')':
		return true
	}
	return false
}

// Text scrubs multi-line text, line by line.
func Text(s string) Result {
	if !strings.Contains(s, "://") && !strings.Contains(s, "@") {
		return Result{Text: s}
	}
	lines := strings.Split(s, "\n")
	var out Result
	for i, line := range lines {
		l, removed, withheld := scrubLine(line)
		lines[i] = l
		out.Removed += removed
		if withheld {
			out.Withheld++
		}
	}
	out.Text = strings.Join(lines, "\n")
	return out
}

// Field scrubs one URL-valued field. A value that cannot be safely delimited
// is replaced whole by WithheldURL.
func Field(s string) Result {
	r := Text(s)
	if r.Withheld > 0 && !strings.Contains(s, "\n") {
		return Result{Text: WithheldURL, Removed: r.Removed, Withheld: r.Withheld}
	}
	return r
}

// scrubLine applies the policy to one line. withheld is true when the whole
// line was replaced.
func scrubLine(line string) (string, int, bool) {
	removed := 0
	var b strings.Builder
	rest := line
	for {
		loc := scheme.FindStringIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:loc[1]])
		after := rest[loc[1]:]
		end := strings.IndexFunc(after, hardEnd)
		if end < 0 {
			end = len(after)
		}
		safe, ok, n := span(after[:end], after[end:])
		if !ok {
			return WithheldLine, removed, true
		}
		removed += n
		b.WriteString(safe)
		rest = after[end:]
	}
	out := b.String()
	// SCP-style user@host:path, outside any scheme URL already handled.
	out = scp.ReplaceAllStringFunc(out, func(m string) string {
		sub := scp.FindStringSubmatch(m)
		if sub[2] == "git" {
			return m
		}
		removed++
		return sub[1] + sub[3] + ":"
	})
	return out, removed, false
}

// span returns what to print for the text between a scheme and the next
// hard end. With an "@" (raw or encoded) anywhere in it, the user info runs to
// the last one, whatever sub-delimiters it holds. Without one, the host[:port]
// ends at the first soft end; a candidate that fails there, or whose rest
// holds another ":", is redacted whole, never in part.
func span(sp, tail string) (string, bool, int) {
	if strings.Contains(sp, "@") || strings.Contains(strings.ToLower(sp), "%40") {
		return authority(sp, tail)
	}
	if truncated(sp, tail) {
		return Redacted, true, 1
	}
	soft := strings.IndexFunc(sp, softEnd)
	if soft < 0 {
		return authority(sp, tail)
	}
	safe, ok, n := authority(sp[:soft], sp[soft:]+tail)
	if !ok {
		return "", false, 0
	}
	if n > 0 || strings.Contains(sp[soft:], ":") {
		return Redacted, true, 1
	}
	return safe + sp[soft:], true, 0
}

// truncated reports a span kubectl (or anything else) cut short: it ends in
// an ellipsis, or one follows it.
func truncated(raw, tail string) bool {
	return strings.HasSuffix(raw, "...") || strings.HasSuffix(raw, "…") ||
		strings.HasPrefix(tail, "...") || strings.HasPrefix(tail, "…")
}

// authority returns the authority to print. ok is false when it cannot be
// safely delimited (the caller withholds the line); n counts removals.
func authority(raw, tail string) (string, bool, int) {
	if raw == "" {
		return raw, true, 0
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", false, 0
		}
	}
	dec, err := url.PathUnescape(raw)
	if err != nil {
		return "", false, 0
	}
	cut := truncated(raw, tail)
	if at := strings.LastIndex(dec, "@"); at >= 0 {
		user := dec[:at]
		if user == "git" && !cut {
			return raw, true, 0
		}
		// remove everything up to the last raw "@" (or its encoded form)
		rawAt := strings.LastIndex(raw, "@")
		if rawAt < 0 {
			rawAt = strings.LastIndex(strings.ToLower(raw), "%40") + 2
		}
		host := raw[rawAt+1:]
		if cut {
			return Redacted, true, 1
		}
		return host, true, 1
	}
	if cut {
		return Redacted, true, 1
	}
	// IPv6 literal: balanced brackets, then an optional numeric port.
	if strings.HasPrefix(dec, "[") {
		close := strings.Index(dec, "]")
		if close < 0 {
			return "", false, 0
		}
		port := dec[close+1:]
		if port == "" || (strings.HasPrefix(port, ":") && digits(port[1:])) {
			return raw, true, 0
		}
		return Redacted, true, 1
	}
	if c := strings.LastIndex(dec, ":"); c >= 0 && !digits(dec[c+1:]) {
		return Redacted, true, 1
	}
	return raw, true, 0
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
