package gitcli

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// scpLike is git's scp syntax: [user@]host:path.
var scpLike = regexp.MustCompile(`^(?:([^@/]+)@)?([^:/]+):(.+)$`)

// ValidateApprovedURL refuses a URL that could carry a credential or run a
// protocol other than https and ssh (slice 6 note §5.1): Klyx stores no
// tokens, and only https and ssh run (parent §7).
func ValidateApprovedURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("empty URL")
	}
	if strings.ContainsAny(s, " \t\"\\") || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("the URL contains a space, quote, backslash or control character")
	}
	if !strings.Contains(s, "://") {
		m := scpLike.FindStringSubmatch(s)
		if m == nil {
			return fmt.Errorf("not an https or ssh URL")
		}
		if strings.Contains(m[1], ":") {
			return fmt.Errorf("an ssh URL must not carry a password")
		}
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("not a URL: %v", err)
	}
	if u.Host == "" {
		return fmt.Errorf("no host")
	}
	switch u.Scheme {
	case "https":
		if u.User != nil {
			return fmt.Errorf("an https URL must not carry a user or token; use the credential helper")
		}
		if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return fmt.Errorf("an https URL must not carry a query or fragment")
		}
	case "ssh":
		if _, has := u.User.Password(); has {
			return fmt.Errorf("an ssh URL must not carry a password")
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("an ssh URL must not carry a query or fragment")
		}
	default:
		return fmt.Errorf("only https and ssh run, not %q", u.Scheme)
	}
	return nil
}
