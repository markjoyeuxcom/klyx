package dra

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The scheduler's reasons pinned to their sources (S11b §9): testdata/
// reasons/quotes.json quotes each line that writes a fixed reason, with
// its file's sha256 and line, at v1.34.0 to v1.37.0 (the plugin) and
// dynamic-resource-allocation v0.35.9 to v0.37.1 (the allocator's one
// unschedulable error). The generator lives in the session scratchpad
// (s11b-reasons). Each quoted literal, formatted, must match its key, and
// every pattern must be quoted at least once.

type reasonQuote struct {
	Version string `json:"version"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
	Line    int    `json:"line"`
	Key     string `json:"key"`
	Code    string `json:"code"`
}

var goLiteral = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

func TestReasonsArePinnedToTheSources(t *testing.T) {
	var quotes []reasonQuote
	readJSON(t, "testdata/reasons/quotes.json", &quotes)
	quoted := map[string][]string{}
	for _, q := range quotes {
		lit := goLiteral.FindString(q.Code)
		format, err := strconv.Unquote(lit)
		if err != nil || len(q.SHA256) != 64 || q.Line <= 0 {
			t.Fatalf("%s %s:%d: a malformed quote %q", q.Version, q.File, q.Line, q.Code)
		}
		// klog.KObj formats a claim "<ns>/<name>"; %w wraps the allocator's
		// empty sentinel
		msg := strings.NewReplacer("%s", "ns/c", "%v", "ns/c", "%w", "").Replace(format)
		rs := Reasons("0/1 nodes are available: 1 " + msg + ".")
		if len(rs) == 0 || rs[0].Key != q.Key {
			t.Errorf("%s %s:%d: %q reads %+v, want %s", q.Version, q.File, q.Line, msg, rs, q.Key)
		}
		quoted[q.Key] = append(quoted[q.Key], q.Version)
	}
	for _, p := range patterns {
		if len(quoted[p.key]) == 0 {
			t.Errorf("%s: no source line quoted", p.key)
		}
	}
	// the version ranges the patterns' comments name
	for key, want := range map[string]string{
		"cannot-allocate":                "v1.34.0 v1.35.0 v1.36.0 v1.37.0",
		"in-progress":                    "v1.34.0 v1.35.0 v1.36.0 v1.37.0",
		"needs-optional-node-operations": "v1.37.0",
		"extended-claim":                 "v1.34.0",
		"extended-fit":                   "v1.34.0",
		"invalid-pools":                  "v1.35.0 v1.36.0 v1.37.0",
	} {
		got := quoted[key]
		sort.Strings(got)
		if g := strings.Join(dedupe(got), " "); g != want {
			t.Errorf("%s quoted at %s, want %s", key, g, want)
		}
	}
}

func dedupe(s []string) []string {
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
