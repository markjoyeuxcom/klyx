package promqlproof

import (
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/metrics"
)

// hostile are the scopes the proof renders every template with: several
// labels, and values with quotes, backslashes, braces, a newline, PromQL of
// their own and non-ASCII text.
var hostile = []map[string]string{
	{"cluster": "aks-we-dev"},
	{"cluster": `a"b`, "team": `c\d`, "k8s_cluster_name": `}{"x"}`},
	{"cluster": "dev\nprd", "region": `"} or vector(1) or up{a="`, "__name_hint": "été ⎈"},
}

// Every registered template, over every case it declares, carries every
// scope matcher in every selector, for each hostile scope.
func TestEveryTemplateCarriesTheScopeInEverySelector(t *testing.T) {
	names := 0
	for _, scope := range hostile {
		for name, cases := range metrics.Rendered(scope) {
			names++
			if len(cases) == 0 {
				t.Errorf("%s declares no case", name)
			}
			for i, q := range cases {
				if err := Check(q, scope); err != nil {
					t.Errorf("%s case %d under %v: %v\n%s", name, i, scope, err, q)
				}
			}
		}
	}
	if names == 0 {
		t.Fatal("no template registered")
	}
}

// With an empty scope every template still parses and reads a series.
func TestEveryTemplateParsesUnscoped(t *testing.T) {
	for name, cases := range metrics.Rendered(nil) {
		for i, q := range cases {
			if err := Check(q, nil); err != nil {
				t.Errorf("%s case %d: %v\n%s", name, i, err, q)
			}
		}
	}
}

// The proof refuses what it must: each fixture below would pass a check
// that looked for the scope's text rather than parsing.
func TestTheProofRefusesAnUnscopedQuery(t *testing.T) {
	scope := map[string]string{"cluster": `a"b`, "team": "x"}
	for _, tc := range []struct{ name, q string }{
		{"an unscoped operand", `sum(rate(a{cluster="a\"b",team="x"}[5m])) or b`},
		{"an unscoped operand of a range selector", `rate(a{cluster="a\"b",team="x"}[5m]) / rate(b[5m])`},
		{"one scope matcher missing", `a{cluster="a\"b"}`},
		{"a bare selector", `up`},
		{"the canary only inside a quoted string", `a{x=~".*cluster=\"a\\\"b\".*team=\"x\".*"}`},
		{"the canary in a regex matcher", `a{cluster=~"a\"b",team="x"}`},
		{"a negated matcher", `a{cluster!="a\"b",team="x"}`},
		{"another value", `a{cluster="a\"b2",team="x"}`},
		{"a series from nothing", `a{cluster="a\"b",team="x"} or vector(0)`},
		{"no selector at all", `1 + 1`},
		{"a subquery's operand", `max_over_time(rate(b[1m])[5m:1m]) and a{cluster="a\"b",team="x"}`},
		{"does not parse", `a{cluster="a\"b",team="x"`},
	} {
		err := Check(tc.q, scope)
		if err == nil {
			t.Errorf("%s: passed: %s", tc.name, tc.q)
			continue
		}
		// refused for its reason, not for a fixture that does not parse
		if tc.name != "does not parse" && strings.HasPrefix(err.Error(), "does not parse") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// the control: conjoined with a template's own matcher on the same label
	if err := Check(`sum(rate(a{cluster="x",cluster="a\"b",team="x"}[5m])) or b{team="x",cluster="a\"b"}`, scope); err != nil {
		t.Fatal(err)
	}
}
