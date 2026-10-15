package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/moomora/klyx/internal/gitcli"
)

// ApprovedGitRepos is the git.repos entries Klyx may use, and a note for
// each one it ignores (slice 6 note §5.1): a URL that could carry a
// credential or run another protocol is never executed, and its note never
// repeats a credential. A bad web link or match is dropped on its own; the
// entry stays.
func (c *Config) ApprovedGitRepos() ([]GitRepoConfig, []string) {
	if c.Git == nil {
		return nil, nil
	}
	var ok []GitRepoConfig
	var notes []string
	seen := map[string]bool{}
	for i, r := range c.Git.Repos {
		u := strings.TrimSpace(r.URL)
		if err := gitcli.ValidateApprovedURL(u); err != nil {
			notes = append(notes, fmt.Sprintf("git.repos[%d] %s is ignored, never fetched: %v", i, RedactURL(u), err))
			continue
		}
		if seen[u] {
			notes = append(notes, fmt.Sprintf("git.repos[%d] %s is listed twice; the second is ignored", i, u))
			continue
		}
		seen[u] = true
		e := GitRepoConfig{URL: u, RemoteBases: r.RemoteBases}
		if w := strings.TrimSpace(r.Web); w != "" {
			if err := ValidWebLink(w); err != nil {
				notes = append(notes, fmt.Sprintf("git.repos[%d] web link ignored: %v", i, err))
			} else {
				e.Web = strings.TrimRight(w, "/")
			}
		}
		for k, m := range r.Matches {
			m = strings.TrimSpace(m)
			if err := ValidRepoKey(m); err != nil {
				// The rejected value is never repeated: it may hold a token.
				notes = append(notes, fmt.Sprintf("git.repos[%d].matches[%d] ignored: %v", i, k, err))
				continue
			}
			e.Matches = append(e.Matches, m)
		}
		ok = append(ok, e)
	}
	return ok, notes
}

var scpHostPath = regexp.MustCompile(`^(?:[^@/]*@)?([^:/@]+):(.+)$`)

// RedactURL names a refused URL without anything that could be a
// credential: scheme, host and path only, a scp-like URL as host:path,
// anything else not at all.
func RedactURL(s string) string {
	if strings.Contains(s, "://") {
		// A scheme URL is named only when it parses; the scp form never
		// applies to one (a bad escape would read "https" as a host).
		if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host + u.Path
		}
		return "(an unrecognised URL)"
	}
	if m := scpHostPath.FindStringSubmatch(s); m != nil {
		return m[1] + ":" + m[2]
	}
	return "(an unrecognised URL)"
}

// ValidWebLink accepts an https link base with no credential, query or
// fragment. Its errors never repeat the value.
func ValidWebLink(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("not an https link")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("a web link must not carry a user, query or fragment")
	}
	return nil
}

// ValidRepoKey checks a normalised repository key as the line writes it
// (internal/line.NormalizeRepo): host/path, no scheme, a lowercase host, no
// trailing "/" or ".git" - what an explicit match names.
func ValidRepoKey(k string) error {
	host, path, ok := strings.Cut(k, "/")
	switch {
	case !ok || host == "" || path == "":
		return fmt.Errorf("not a host/path key")
	case strings.Contains(k, "://") || strings.ContainsAny(k, " \t@?#\\\"") ||
		strings.ContainsFunc(k, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return fmt.Errorf("not a host/path key")
	case host != strings.ToLower(host):
		return fmt.Errorf("the host must be lowercase")
	case strings.HasSuffix(k, "/") || strings.HasSuffix(k, ".git"):
		return fmt.Errorf("not normalised (a trailing / or .git)")
	}
	return nil
}
