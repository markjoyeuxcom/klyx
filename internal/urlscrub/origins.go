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
// It works on a whole message, never on a line.
func Origins(msg string) (string, bool) {
	loc := scheme.FindStringIndex(msg)
	if loc == nil {
		return msg, false
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

var originHost = regexp.MustCompile(`^(([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*\.?|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)
