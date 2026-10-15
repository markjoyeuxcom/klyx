package line

import (
	"net/url"
	"regexp"
	"strings"
)

// GitRev is a parsed Git revision: the ref it was fetched from (when the
// form names one) and the commit.
type GitRev struct {
	Ref string // "main" from "main@sha1:<hex>"; empty when the form names none
	SHA string // lowercase hex: 40 (sha1) or 64 (sha256)
}

var (
	sha1Hex   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParseGitRevision reads the revision forms Flux and Argo report (§3.2):
// "<ref>@sha1:<hex>", "<ref>@sha256:<hex>", "sha1:<hex>", "sha256:<hex>",
// and Argo's bare hex sha. Anything else is unparsable - its unit is
// excluded and counted, never guessed.
func ParseGitRevision(s string) (GitRev, bool) {
	s = strings.TrimSpace(s)
	ref := ""
	if i := strings.LastIndex(s, "@"); i >= 0 {
		ref, s = s[:i], s[i+1:]
		if ref == "" {
			return GitRev{}, false
		}
	}
	h := strings.ToLower(s)
	switch {
	case strings.HasPrefix(h, "sha1:") && sha1Hex.MatchString(h[5:]):
		return GitRev{Ref: ref, SHA: h[5:]}, true
	case strings.HasPrefix(h, "sha256:") && sha256Hex.MatchString(h[7:]):
		return GitRev{Ref: ref, SHA: h[7:]}, true
	case ref == "" && (sha1Hex.MatchString(h) || sha256Hex.MatchString(h)):
		return GitRev{SHA: h}, true
	}
	return GitRev{}, false
}

// IsSHA reports a full hex commit (sha1 or sha256).
func IsSHA(s string) bool {
	h := strings.ToLower(strings.TrimSpace(s))
	return sha1Hex.MatchString(h) || sha256Hex.MatchString(h)
}

var scpLike = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`) // git@github.com:acme/fleet.git

// NormalizeRepo is a repository URL for matching only (§3.2): the scheme,
// user info, ".git" suffix, trailing slashes and the host's case removed.
// "git@github.com:acme/fleet.git", "ssh://git@github.com/acme/fleet" and
// "https://GitHub.com/acme/fleet.git/" all read "github.com/acme/fleet".
func NormalizeRepo(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	var host, path string
	u, err := url.Parse(s)
	switch {
	case err == nil && u.Scheme != "" && u.Host != "":
		host, path = u.Hostname(), u.Path
		if p := u.Port(); p != "" && !defaultPort(u.Scheme, p) {
			host += ":" + p
		}
	case err != nil && strings.Contains(s, "://"):
		// A scheme URL url.Parse refused (a bad escape): host and path read
		// by hand, never its user info, query or fragment - a key is shown.
		host, path = schemeHostPath(s)
	default:
		if m := scpLike.FindStringSubmatch(s); m != nil {
			host, path = m[1], m[2]
			break
		}
		if at := strings.IndexByte(s, '@'); at >= 0 && !strings.ContainsAny(s[:at], "/:") {
			s = s[at+1:] // a user@ prefix is never part of a key
		}
		return strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	return strings.ToLower(host) + "/" + path
}

// schemeHostPath reads scheme://[userinfo@]host/path[?query][#fragment]
// without parsing escapes, dropping the userinfo, query and fragment.
func schemeHostPath(s string) (string, string) {
	rest := s[strings.Index(s, "://")+3:]
	if j := strings.IndexAny(rest, "?#"); j >= 0 {
		rest = rest[:j]
	}
	auth, path, _ := strings.Cut(rest, "/")
	if at := strings.LastIndexByte(auth, '@'); at >= 0 {
		auth = auth[at+1:]
	}
	return auth, path
}

func defaultPort(scheme, port string) bool {
	switch scheme {
	case "https":
		return port == "443"
	case "http":
		return port == "80"
	case "ssh":
		return port == "22"
	}
	return false
}
