package metrics

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// PromQL string literals unquote with Go rules; mirror that to prove the
// matcher survives the PromQL lexer and the regex it carries matches only the
// listed values (cross-review B: a dotted name made the whole query invalid).
func TestMatchAnyOf_DottedNamesSurvivePromQLQuoting(t *testing.T) {
	m := MatchAnyOf("envoy_cluster_name", "httproute/", []string{"default/web", "team.a/api.example.com"}, "/rule/[0-9]+")
	lit := strings.TrimPrefix(m, "envoy_cluster_name=~")
	if lit == m {
		t.Fatalf("matcher shape: %s", m)
	}
	pattern, err := strconv.Unquote(lit)
	if err != nil {
		t.Fatalf("not a valid PromQL string literal %s: %v", lit, err)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("regex %q: %v", pattern, err)
	}
	for in, want := range map[string]bool{
		"httproute/team.a/api.example.com/rule/0": true,
		"httproute/default/web/rule/12":           true,
		"httproute/teamXa/apiXexample.com/rule/0": false, // dots are literal
		"httproute/default/web/rule/x":            false,
		"xhttproute/default/web/rule/0":           false, // anchored
	} {
		if re.MatchString(in) != want {
			t.Errorf("match(%q) = %v, want %v", in, !want, want)
		}
	}
}

func TestEquals_QuotesValue(t *testing.T) {
	if got := Equals("namespace", "team"); got != `namespace="team"` {
		t.Fatalf("got %s", got)
	}
}
