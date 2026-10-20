package dra

import (
	"regexp"
	"strings"
)

// The scheduler DRA plugin's fixed reasons (S11b §1.5), read from
// pkg/scheduler/framework/plugins/dynamicresources/dynamicresources.go at
// v1.34.0 (sha256 90fe8241…), v1.35.0 (52611e4c…), v1.36.0 (90662bb0…) and
// v1.37.0 (9b339905…). A claim is formatted klog.KObj: "<ns>/<name>". A
// message that matches none of these never makes a claim degraded.

// Scope says what a reason is about.
type Scope int

const (
	// SetWide: the pod's claims together (the allocator places a pod's
	// claims all or nothing); the culprit claim is not named.
	SetWide Scope = iota
	// NamesClaim: the reason names one claim.
	NamesClaim
	// NamesClass: the reason names one device class.
	NamesClass
	// InProgress: a claim is being allocated (a transient).
	InProgress
)

// Reason is one matched fixed reason.
type Reason struct {
	Key   string
	Scope Scope
	// Claim ("<ns>/<name>") or Class, when the reason names one.
	Claim, Class string
}

type pattern struct {
	key   string
	scope Scope
	re    *regexp.Regexp
}

// The patterns, claim- and class-naming first. Each names its versions.
var patterns = []pattern{
	// v1.34-v1.37
	{"in-progress", InProgress, regexp.MustCompile(`resource claim ([^ ,]+/[^ ,]+) is in the process of being allocated`)},
	// v1.37
	{"needs-optional-node-operations", NamesClaim, regexp.MustCompile(`resource claim ([^ ,]+/[^ ,]+) allocation requires DRAOptionalNodeOperations feature on the node`)},
	// v1.34-v1.37
	{"subrequests-disabled", NamesClaim, regexp.MustCompile(`resource claim ([^ ,]+/[^ ,]+), request [^:]+: has subrequests, but the DRAPrioritizedList feature is disabled`)},
	{"unknown-request-type", NamesClaim, regexp.MustCompile(`resource claim ([^ ,]+/[^ ,]+), request [^:]+: unknown request type`)},
	{"class-missing", NamesClass, regexp.MustCompile(`request [^:]+: device class ([^ ]+) does not exist`)},
	// set-wide, v1.34-v1.37 unless named
	{"cannot-allocate", SetWide, regexp.MustCompile(`cannot allocate all claims`)},
	{"not-available-on-node", SetWide, regexp.MustCompile(`resourceclaim not available on the node`)},
	{"in-use", SetWide, regexp.MustCompile(`resourceclaim in use`)},
	{"timed-out", SetWide, regexp.MustCompile(`timed out trying to allocate devices`)}, // "node <n>: " from v1.36
	{"unsupported-request-type", SetWide, regexp.MustCompile(`request [^:]+: unsupported request type`)},
	// v1.34 (DRA-backed extended resources)
	{"extended-claim", SetWide, regexp.MustCompile(`cannot schedule extended resource claim`)},
	{"extended-fit", SetWide, regexp.MustCompile(`cannot fit resource`)},
}

// Reasons are every fixed reason a scheduling message carries, in pattern
// order. The scheduler joins its per-node reasons into one message.
func Reasons(msg string) []Reason {
	var out []Reason
	for _, p := range patterns {
		for _, m := range p.re.FindAllStringSubmatch(msg, -1) {
			r := Reason{Key: p.key, Scope: p.scope}
			switch p.scope {
			case InProgress, NamesClaim:
				r.Claim = strings.TrimRight(m[1], ".")
			case NamesClass:
				r.Class = strings.TrimRight(m[1], ".")
			}
			out = append(out, r)
		}
	}
	return out
}

// Meaning is a reason's documented meaning, said beside the quote.
func Meaning(key string) string {
	switch key {
	case "cannot-allocate":
		return "the scheduler could not allocate the pod's claims on any node it tried: no matching device, every matching device allocated, tainted devices, an incomplete pool, or no slices at all"
	case "not-available-on-node":
		return "an allocated claim's devices are not available on the nodes the scheduler tried"
	case "in-use":
		return "the claim is reserved for other consumers"
	case "in-progress":
		return "another scheduling attempt is allocating the claim"
	case "class-missing":
		return "a request names a device class that does not exist"
	case "timed-out":
		return "the allocator gave up after its time limit"
	case "needs-optional-node-operations":
		return "the allocated device needs a node feature the node lacks"
	case "subrequests-disabled", "unknown-request-type", "unsupported-request-type":
		return "the scheduler cannot handle the request's form"
	case "extended-claim", "extended-fit":
		return "the scheduler could not serve the pod's DRA-backed extended resources"
	}
	return ""
}
