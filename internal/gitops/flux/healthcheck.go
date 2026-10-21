package flux

import (
	"regexp"
	"strings"
)

// The objects a failed Flux health check names (AI A1 §3.1). kustomize-
// controller wraps the ssa package's wait error (fluxcd/pkg/ssa,
// manager_wait.go):
//
//	<prefix>: [<entry>, <entry>, ...]
//	prefix: "timeout waiting for" or "failed early due to stalled resources"
//	entry:  <Kind>/[<namespace>/]<name> status: '<Status>'[: <error>]
//	        can't determine status for <Kind>/[<namespace>/]<name>
//
// Flux does not escape an entry's error: it can hold a comma, a closing
// bracket or a whole entry-shaped substring. So the parse is strict and stops
// at ambiguity: entries are read one at a time from the opening bracket, each
// followed by exactly ", " or "]". An entry followed by ": " carries an error;
// that entry is kept, since its own text precedes the error, but its error
// and everything after it are unparsed. Anything else that does not match at
// an entry boundary is unparsed too. Nothing unparsed names an object.

// HealthCheckEntry is one object a failed health check names, by name only.
type HealthCheckEntry struct {
	Kind, Namespace, Name string
	// Status is kstatus's word (InProgress, Failed, Current, Unknown, ...);
	// empty when Flux could not determine it.
	Status       string
	Undetermined bool
	// HasError: the entry carried an error, which is not parsed.
	HasError bool
}

// HealthCheck is a parsed health-check failure.
type HealthCheck struct {
	// Stalled: Flux failed early on stalled resources; otherwise it timed out.
	Stalled bool
	Entries []HealthCheckEntry
	// Unparsed: some of the list was not parsed (an entry's error, or text
	// that did not match at an entry boundary). It names no object.
	Unparsed bool
}

var (
	hcPrefixes = []struct {
		text    string
		stalled bool
	}{{"timeout waiting for: [", false}, {"failed early due to stalled resources: [", true}}
	// <Kind>/<a>[/<b>]: two segments are a cluster-scoped name, three a
	// namespace and a name
	hcRef    = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)/([a-z0-9](?:[-a-z0-9.]*[a-z0-9])?)(?:/([a-z0-9](?:[-a-z0-9.]*[a-z0-9])?))?`)
	hcStatus = regexp.MustCompile(`^ status: '([A-Za-z]+)'`)
)

const hcUndetermined = "can't determine status for "

// ParseHealthCheck reads a Ready condition's message. ok is false when the
// message is not a health-check failure.
func ParseHealthCheck(msg string) (HealthCheck, bool) {
	var hc HealthCheck
	at := -1
	for _, p := range hcPrefixes {
		if i := strings.Index(msg, p.text); i >= 0 && (at < 0 || i < at) {
			at, hc.Stalled = i+len(p.text), p.stalled
		}
	}
	if at < 0 {
		return hc, false
	}
	rest := msg[at:]
	for {
		var e HealthCheckEntry
		undetermined := strings.HasPrefix(rest, hcUndetermined)
		if undetermined {
			rest = rest[len(hcUndetermined):]
		}
		m := hcRef.FindStringSubmatch(rest)
		if m == nil {
			hc.Unparsed = rest != ""
			return hc, true
		}
		e.Kind = m[1]
		if m[3] != "" {
			e.Namespace, e.Name = m[2], m[3]
		} else {
			e.Name = m[2]
		}
		rest = rest[len(m[0]):]
		if undetermined {
			e.Undetermined = true
		} else {
			s := hcStatus.FindStringSubmatch(rest)
			if s == nil {
				hc.Unparsed = true
				return hc, true
			}
			e.Status = s[1]
			rest = rest[len(s[0]):]
		}
		switch {
		case strings.HasPrefix(rest, ", "):
			hc.Entries = append(hc.Entries, e)
			rest = rest[2:]
		case strings.HasPrefix(rest, "]"):
			hc.Entries = append(hc.Entries, e)
			return hc, true
		case strings.HasPrefix(rest, ": "):
			// the entry is its own; its error, and everything after, are not
			e.HasError = true
			hc.Entries = append(hc.Entries, e)
			hc.Unparsed = true
			return hc, true
		default:
			hc.Unparsed = true
			return hc, true
		}
	}
}
