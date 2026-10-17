package urlscrub

import (
	"regexp"
	"strings"
)

// Origins is coverage S1c §6's rule (revision 3) for messages that can
// carry a signed artifact URL: an ExternalArtifact's URL is copied into its
// consumers' fetches, and the fetcher prints it whole in its errors.
// Nothing in a message proves where a URL ends - the URL is a producer's
// string, a request accepts literal spaces, Go's errors quote control
// characters, kubectl prints a line break raw - so Origins never looks for
// the end:
//
//   - the text before the first "<scheme>://" candidate is kept;
//   - the candidate shows as "<scheme>://<host>" only when its authority is
//     well formed: the authority runs to the first "/", "?" or "#", or the
//     message's end; user info up to its last "@" or "%40" is dropped; what
//     remains must be a host name, an IPv4 address or a bracketed IPv6
//     address, with an optional all-digit port. Anything else shows as
//     "<scheme>://<withheld>";
//   - everything after the authority, to the end of the message, is
//     withheld: the path, the query, later lines and any later URL.
//
// A URL need not be well formed to be fetched or printed (Codex's Stage 0
// review, finding 2): the ExternalArtifact CRD declares its URL a string,
// and Go's HTTP stack prints whatever target it was given. So a request
// echo is a candidate too, whatever follows it - url.Error's
// `<Op> "<target>"` (an HTTP method in title case, or "parse") and
// retryablehttp's `<METHOD> <target> giving up` - as is "http:" or
// "https:" without "//". From such a candidate everything is withheld; an
// echo whose target is itself a "<scheme>://" candidate keeps its origin.
//
// It works on a whole message, never on a line.
func Origins(msg string) (string, bool) {
	at, loc := first(msg)
	if at < 0 {
		return msg, false
	}
	if loc == nil {
		return msg[:at] + OriginsRest, true
	}
	sch := msg[loc[0] : loc[1]-len("://")]
	rest := msg[loc[1]:]
	// its own output passes unchanged: the rule is idempotent
	if h, ok := strings.CutSuffix(rest, " "+OriginsRest); ok && (h == OriginWithheld || originHost.MatchString(h)) {
		return msg, false
	}
	if rest == OriginWithheld {
		return msg, false
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	host := rest[:end]
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.LastIndex(strings.ToLower(host), "%40"); i >= 0 {
		host = host[i+3:]
	}
	if !originHost.MatchString(host) {
		host = OriginWithheld
	}
	out := msg[:loc[0]] + sch + "://" + host
	if end < len(rest) {
		out += " " + OriginsRest
	}
	return out, out != msg
}

// The withheld parts of an Origins rewrite.
const (
	OriginWithheld = "<withheld>"
	OriginsRest    = "<the rest of the message is withheld: it follows a URL>"
)

// Prefix is the text before the first URL candidate, a request echo's
// method included in what follows; ok is false when there is none.
func Prefix(msg string) (string, bool) {
	at := -1
	for _, i := range []int{index(scheme, msg), index(requestEcho, msg), bareAt(msg)} {
		if i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	return msg[:at], true
}

// first is where Origins starts withholding, and the "<scheme>://" match
// when the candidate there may show its origin (nil when nothing of it
// shows).
func first(msg string) (int, []int) {
	at, sch := -1, scheme.FindStringIndex(msg)
	if sch != nil {
		at = sch[0]
	}
	if e := requestEcho.FindStringIndex(msg); e != nil && (at < 0 || e[0] < at) && !(sch != nil && e[1] == sch[0]) {
		at, sch = e[0], nil
	}
	if b := bareAt(msg); b >= 0 && (at < 0 || b < at) {
		at, sch = b, nil
	}
	return at, sch
}

func index(re *regexp.Regexp, s string) int {
	if loc := re.FindStringIndex(s); loc != nil {
		return loc[0]
	}
	return -1
}

// bareAt is the first "http:" or "https:" not followed by "//".
func bareAt(s string) int {
	for _, loc := range bareHTTP.FindAllStringIndex(s, -1) {
		if !strings.HasPrefix(s[loc[1]:], "//") {
			return loc[0]
		}
	}
	return -1
}

var (
	requestEcho = regexp.MustCompile(`\b(?:(?:Get|Head|Post|Put|Patch|Delete|Connect|Options|Trace|parse) "|(?:GET|HEAD|POST|PUT|PATCH|DELETE|CONNECT|OPTIONS|TRACE) )`)
	bareHTTP    = regexp.MustCompile(`(?i)\bhttps?:`)
)

var originHost = regexp.MustCompile(`^(([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*\.?|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)
