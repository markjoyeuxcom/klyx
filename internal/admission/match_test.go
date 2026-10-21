package admission

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The matcher's oracle (S3b §8): testdata/rules/match.json holds the
// upstream rules.Matcher's answers at k8s.io/apiserver v0.34.12 to v0.37.1
// (identical; MODULES records each module's hash; the generator lives in
// the session scratchpad, s3b-golden/MATCH-GENERATOR.go) for a table of
// rules and requests, and the dispatchers' Equivalent pass over an
// upstream equivalent-resource registry. The rule table is rebuilt here in
// the generator's order.

type oracleRule struct {
	Operations []string `json:"op"`
	Groups     []string `json:"gr"`
	Versions   []string `json:"ve"`
	Resources  []string `json:"re"`
	Scope      string   `json:"sc,omitempty"`
}

type oracleReq struct {
	Operation   string `json:"op"`
	Group       string `json:"g"`
	Version     string `json:"v"`
	Resource    string `json:"r"`
	Subresource string `json:"s,omitempty"`
	Namespace   string `json:"ns,omitempty"`
}

func (o oracleReq) request() Request {
	return Request(o)
}

var longGroup = strings.Repeat("l", 60) + ".lab.klyx.dev"

func oracleRules() []oracleRule {
	var rs []oracleRule
	for _, ops := range [][]string{{"CREATE"}, {"UPDATE"}, {"DELETE"}, {"CONNECT"}, {"*"}, {"CREATE", "UPDATE"}} {
		for _, gr := range [][]string{{""}, {"apps"}, {"*"}, {longGroup}, {"lab.klyx.dev"}} {
			for _, ve := range [][]string{{"v1"}, {"*"}, {"v1beta1"}} {
				for _, re := range [][]string{{"pods"}, {"pods/*"}, {"*"}, {"*/*"}, {"*/scale"}, {"deployments"}, {"deployments/scale"}, {"widgets"}, {"namespaces"}, {"pods/eviction"}, {"nodes", "pods/status"}} {
					for _, sc := range []string{"", "*", "Namespaced", "Cluster"} {
						rs = append(rs, oracleRule{Operations: ops, Groups: gr, Versions: ve, Resources: re, Scope: sc})
					}
				}
			}
		}
	}
	return rs
}

func anyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func ruleObject(r oracleRule) map[string]any {
	m := map[string]any{"operations": anyList(r.Operations), "apiGroups": anyList(r.Groups), "apiVersions": anyList(r.Versions), "resources": anyList(r.Resources)}
	if r.Scope != "" {
		m["scope"] = r.Scope
	}
	return m
}

// configsOf projects the rules as webhooks, 16 to a configuration, named
// w-<index>.
func configsOf(t *testing.T, rs []oracleRule, matchPolicy string) []*Config {
	t.Helper()
	var out []*Config
	for start := 0; start < len(rs); start += 16 {
		var hooks []any
		for i := start; i < start+16 && i < len(rs); i++ {
			hooks = append(hooks, map[string]any{"name": fmt.Sprintf("w-%d", i), "matchPolicy": matchPolicy, "rules": []any{ruleObject(rs[i])},
				"clientConfig": map[string]any{"service": map[string]any{"namespace": "lab", "name": "hook"}}})
		}
		u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": fmt.Sprintf("c-%d", start/16), "uid": fmt.Sprintf("u-%d", start)}, "webhooks": hooks}}
		c := ProjectConfig(u, Validating)
		if c.Cut {
			t.Fatalf("a fixture configuration was cut")
		}
		out = append(out, c)
	}
	return out
}

func TestTheExactPassAgreesWithTheMatcher(t *testing.T) {
	var fx struct {
		Requests []oracleReq `json:"requests"`
		Exact    []string    `json:"exact"`
		Rules    int         `json:"rules"`
	}
	b, err := os.ReadFile("testdata/rules/match.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	rs := oracleRules()
	if len(rs) != fx.Rules {
		t.Fatalf("the rule table has %d rules, the oracle %d", len(rs), fx.Rules)
	}
	configs := configsOf(t, rs, "Exact")
	compared := 0
	for qi, oq := range fx.Requests {
		got := map[string]bool{}
		c := Match(oq.request(), configs, nil)
		for _, cand := range c.List {
			if cand.Why != "" {
				t.Errorf("%+v: %s may match (%s) under Exact", oq, cand.Name, cand.Why)
			}
			got[cand.Name] = true
		}
		if c.Aliases != 0 {
			t.Errorf("%+v: aliases counted under Exact", oq)
		}
		for i := range rs {
			want := fx.Exact[qi][i] == '1'
			if got[fmt.Sprintf("w-%d", i)] != want {
				t.Errorf("%+v, rule %+v: Klyx %v, the matcher %v", oq, rs[i], !want, want)
			}
			compared++
		}
	}
	if compared != 3960*12 {
		t.Fatalf("%d compared", compared)
	}
}

func TestTheEquivalentPass(t *testing.T) {
	var fx struct {
		Equivalent []struct {
			Rule  oracleRule `json:"rule"`
			Req   oracleReq  `json:"req"`
			Exact bool       `json:"exact"`
			Via   string     `json:"via"`
		} `json:"equivalent"`
	}
	b, _ := os.ReadFile("testdata/rules/match.json")
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	listed, counted := 0, 0
	for _, c := range fx.Equivalent {
		for _, policy := range []string{"Equivalent", "Exact"} {
			cs := Match(c.Req.request(), configsOf(t, []oracleRule{c.Rule}, policy), nil)
			where := fmt.Sprintf("%s %+v on %+v", policy, c.Rule, c.Req)
			sameName := c.Via != "" && strings.SplitN(c.Via, ", Resource=", 2)[1] == c.Req.Resource
			switch {
			case c.Exact:
				if len(cs.List) != 1 || cs.List[0].Why != "" {
					t.Errorf("%s: an exact match is listed: %+v", where, cs)
				}
			case policy == "Exact":
				if len(cs.List) != 0 || cs.Aliases != 0 {
					t.Errorf("%s: Exact lists or counts nothing else: %+v", where, cs)
				}
			case c.Via != "" && sameName:
				// upstream matched through a same-named equivalent: listed
				if len(cs.List) != 1 || cs.List[0].Why != MayEquivalent {
					t.Errorf("%s: a same-named equivalent (%s) is listed: %+v", where, c.Via, cs)
				}
				listed++
			case c.Via != "":
				// through a differently named alias: counted, never listed
				if len(cs.List) != 0 || cs.Aliases != 1 {
					t.Errorf("%s: an alias (%s) is counted, not listed: %+v", where, c.Via, cs)
				}
				counted++
			}
		}
	}
	if listed == 0 || counted == 0 {
		t.Fatalf("the probe exercises both: %d listed, %d counted", listed, counted)
	}
}

func TestCandidatesSayWhatTheyCannotProve(t *testing.T) {
	q := Request{Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "lab"}
	long := strings.Repeat("g", maxExactID+1)
	hook := func(name string, rules ...map[string]any) map[string]any {
		var rs []any
		for _, r := range rules {
			rs = append(rs, r)
		}
		return map[string]any{"name": name, "rules": rs}
	}
	rule := func(group, resource string) map[string]any {
		return map[string]any{"operations": []any{"UPDATE"}, "apiGroups": []any{group}, "apiVersions": []any{"v1"}, "resources": []any{resource}}
	}
	var nine []map[string]any
	for i := 0; i < 9; i++ {
		nine = append(nine, rule("other", "things"))
	}
	var many []any
	for i := 0; i < maxExactItems+1; i++ {
		many = append(many, fmt.Sprintf("r%d", i))
	}
	cut := rule("apps", "x")
	cut["resources"] = many
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "c", "uid": "u"}, "webhooks": []any{
		hook("inexact", rule(long, "deployments")),
		hook("unread", nine...),
		hook("cut", cut),
		hook("elsewhere", rule("apps", "statefulsets")),
	}}}
	c := ProjectConfig(u, Validating)
	got := Match(q, []*Config{c}, nil)
	why := map[string]string{}
	for _, cand := range got.List {
		why[cand.Name] = cand.Why
	}
	if why["inexact"] != MayInexact || why["unread"] != MayUnread || why["cut"] != MayInexact || len(got.List) != 3 {
		t.Fatalf("each unproven candidate says why: %+v", got)
	}
	if got.Aliases != 1 {
		t.Fatalf("an Equivalent rule on another resource is counted: %+v", got)
	}
	// the exact copy keeps what display clips
	lg := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "c", "uid": "u"}, "webhooks": []any{hook("long", rule(longGroup, "widgets"))}}}
	lc := ProjectConfig(lg, Validating)
	if lc.Webhooks[0].Rules[0].X == nil || Match(Request{Operation: "UPDATE", Group: longGroup, Version: "v1", Resource: "widgets", Namespace: "lab"}, []*Config{lc}, nil).List[0].Why != "" {
		t.Fatalf("a group past the display clip matches exactly: %+v", lc.Webhooks[0].Rules[0])
	}
	// an admission configuration kind is exempt
	if e := Match(Request{Operation: "UPDATE", Group: Group, Version: "v1", Resource: "validatingwebhookconfigurations"}, []*Config{c}, nil); !e.Exempt || len(e.List) != 0 {
		t.Fatalf("exempt: %+v", e)
	}
	// a cut configuration says some webhooks are unread; a policy is matched too
	c.Cut = true
	p := ProjectPolicy(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "p", "uid": "pu"},
		"spec": map[string]any{"matchConstraints": map[string]any{"resourceRules": []any{rule("apps", "deployments")}}}}}, Validating)
	got = Match(q, []*Config{c}, []*Policy{p})
	if !got.Unread || got.List[len(got.List)-1] != (Candidate{Kind: KindValidatingPolicy, Name: "p"}) {
		t.Fatalf("%+v", got)
	}
}
