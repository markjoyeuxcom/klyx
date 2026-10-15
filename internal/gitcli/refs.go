package gitcli

import (
	"fmt"
	"strings"
)

// Ref is one tracked ref (slice 6 note §3.6): a full branch or tag ref, or
// an object id. Only the line's own key and the line.source override give
// one, and only these constructors build one.
type Ref struct {
	full string // refs/heads/… or refs/tags/…; "" for an object id
	sha  string
}

func (r Ref) String() string {
	if r.sha != "" {
		return r.sha
	}
	return r.full
}

// refspec is the one non-pattern refspec that fetches it into staging.
func (r Ref) refspec() string {
	if r.sha != "" {
		return "+" + r.sha + ":refs/klyx/staging/commit/" + r.sha
	}
	return "+" + r.full + ":refs/klyx/staging/" + r.full
}

// Staging is where a fetch puts it; Published is where publication moves it.
func (r Ref) Staging() string {
	if r.sha != "" {
		return "refs/klyx/staging/commit/" + r.sha
	}
	return "refs/klyx/staging/" + r.full
}

func (r Ref) Published() string {
	if r.sha != "" {
		return "refs/klyx/tracked/commit/" + r.sha
	}
	return "refs/klyx/tracked/" + r.full
}

// RefFromKey maps a line key's ref (internal/line's kinds) onto what is
// fetched: a branch, a tag or a commit - an Argo ref arrives as one of these
// only when it was fully qualified or a sha. Anything else (a semver range,
// the default branch, an unqualified Argo ref, a Flux "name" ref outside
// refs/heads and refs/tags) has no target and is refused with its reason.
func RefFromKey(refKind, refName string) (Ref, error) {
	switch refKind {
	case "branch":
		return fullRef("refs/heads/" + refName)
	case "tag":
		return fullRef("refs/tags/" + refName)
	case "commit":
		return shaRef(refName)
	case "semver":
		return Ref{}, fmt.Errorf("a semver range has no single target")
	case "default", "":
		return Ref{}, fmt.Errorf("the default branch is not named, so it has no target")
	case "unqualified":
		return Ref{}, fmt.Errorf("an unqualified Argo ref may resolve to a branch or a tag, so it has no target")
	case "name":
		return Ref{}, fmt.Errorf("a ref outside refs/heads and refs/tags has no target")
	}
	return Ref{}, fmt.Errorf("ref kind %q has no target", refKind)
}

func shaRef(s string) (Ref, error) {
	if !validSHA(s) {
		return Ref{}, fmt.Errorf("%q is not a full object id", truncate(s, 80))
	}
	return Ref{sha: s}, nil
}

// fullRef accepts a ref exactly in refs/heads/ or refs/tags/ whose name
// passes git's check-ref-format rules.
func fullRef(full string) (Ref, error) {
	var name string
	switch {
	case strings.HasPrefix(full, "refs/heads/"):
		name = strings.TrimPrefix(full, "refs/heads/")
	case strings.HasPrefix(full, "refs/tags/"):
		name = strings.TrimPrefix(full, "refs/tags/")
	default:
		return Ref{}, fmt.Errorf("%q is not a branch or tag ref", truncate(full, 80))
	}
	if err := checkRefName(name); err != nil {
		return Ref{}, fmt.Errorf("ref %q refused: %v", truncate(full, 80), err)
	}
	return Ref{full: full}, nil
}

// checkRefName applies git's check-ref-format rules to a branch or tag
// name, and refuses everything a refspec could read as a pattern.
func checkRefName(name string) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	if name == "@" {
		return fmt.Errorf(`"@" alone is not a ref`)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("a leading '-'")
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("a trailing '/' or '.'")
	}
	for _, bad := range []string{"..", "@{", "//"} {
		if strings.Contains(name, bad) {
			return fmt.Errorf("contains %q", bad)
		}
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == ' ' || strings.ContainsRune("*?[\\^~:", r) {
			return fmt.Errorf("contains %q", r)
		}
	}
	for _, comp := range strings.Split(name, "/") {
		if strings.HasPrefix(comp, ".") {
			return fmt.Errorf("a component starting with '.'")
		}
		if strings.HasSuffix(comp, ".lock") {
			return fmt.Errorf("a component ending in .lock")
		}
	}
	return nil
}

// IsSHA reports whether s is a full object id: 40 or 64 lowercase hex.
func IsSHA(s string) bool { return validSHA(s) }

func validSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
