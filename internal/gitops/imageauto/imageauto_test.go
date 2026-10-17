package imageauto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/health"
)

var t0 = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

func obj(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("%v: %s", err, doc)
	}
	normalise(m)
	return &unstructured.Unstructured{Object: m}
}

// normalise turns JSON numbers into int64, as the API decoder does.
func normalise(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalise(e)
		}
	case []any:
		for i, e := range x {
			x[i] = normalise(e)
		}
	case float64:
		return int64(x)
	}
	return v
}

func project(t *testing.T, kind string, u *unstructured.Unstructured) *Object {
	t.Helper()
	v, err := Project(kind)(u)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*Object)
}

func ready(status, reason, msg string, gen int) string {
	return fmt.Sprintf(`{"type":"Ready","status":%q,"reason":%q,"message":%q,"lastTransitionTime":"2026-10-17T09:00:00Z","observedGeneration":%d}`, status, reason, msg, gen)
}

func repoDoc(gen, observed int, cond, scan string) string {
	if scan == "" {
		scan = `"lastScanResult":{"revision":"sha256:abc","tagCount":42,"scanTime":"2026-10-17T09:55:00Z","latestTags":["v9","v8"]},`
	}
	return fmt.Sprintf(`{"apiVersion":"image.toolkit.fluxcd.io/v1","kind":"ImageRepository",
		"metadata":{"name":"app","namespace":"flux-system","uid":"u-repo","generation":%d},
		"spec":{"image":"ghcr.io/org/app","interval":"5m","provider":"generic","secretRef":{"name":"regcred"},"exclusionList":["^.*\\.sig$"]},
		"status":{%s"canonicalImageName":"ghcr.io/org/app","observedGeneration":%d,"conditions":[%s]}}`, gen, scan, observed, cond)
}

func policyDoc(apiVersion, labels, status string) string {
	return fmt.Sprintf(`{"apiVersion":%q,"kind":"ImagePolicy",
		"metadata":{"name":"app","namespace":"apps","uid":"u-pol","generation":1,"labels":%s},
		"spec":{"imageRepositoryRef":{"name":"app","namespace":"flux-system"},"policy":{"semver":{"range":">=1.0.0"}}},
		"status":%s}`, apiVersion, labels, status)
}

func autoDoc(spec, status string) string {
	return fmt.Sprintf(`{"apiVersion":"image.toolkit.fluxcd.io/v1","kind":"ImageUpdateAutomation",
		"metadata":{"name":"apps","namespace":"apps","uid":"u-auto","generation":2},
		"spec":%s,"status":%s}`, spec, status)
}

const autoSpec = `{"sourceRef":{"kind":"GitRepository","name":"fleet","namespace":"flux-system"},"interval":"10m",
	"git":{"checkout":{"ref":{"branch":"main"}},"push":{"branch":"auto","options":{"ci.skip":"SECRETneedle"}},
	"commit":{"author":{"name":"bot","email":"bot@example.com"},"messageTemplate":"deploy {{.SECRETneedle}}","messageTemplateValues":{"k":"SECRETneedle"},"signingKey":{"secretRef":{"name":"gpg"}}}},
	"update":{"strategy":"Setters","path":"./clusters"},"policySelector":{"matchLabels":{"team":"apps"}}}`

func TestParseVersions(t *testing.T) {
	// v1 latestRef
	p := ParsePolicy(project(t, KindPolicy, obj(t, policyDoc("image.toolkit.fluxcd.io/v1", `{"team":"apps"}`,
		`{"latestRef":{"name":"ghcr.io/org/app","tag":"1.2.0","digest":"sha256:d"},"observedPreviousRef":{"name":"ghcr.io/org/app","tag":"1.1.0"},"conditions":[`+ready("True", "Succeeded", "ok", 1)+`]}`))))
	if p.Latest == nil || p.Latest.String() != "ghcr.io/org/app:1.2.0@sha256:d" || p.Previous.Tag != "1.1.0" || p.PolicyType != "semver" || p.PolicyValue != ">=1.0.0" || p.RepositoryNamespace != "flux-system" {
		t.Fatalf("v1: %+v", p)
	}
	// v1beta2 with latestRef
	p = ParsePolicy(project(t, KindPolicy, obj(t, policyDoc("image.toolkit.fluxcd.io/v1beta2", `{}`, `{"latestRef":{"name":"a","tag":"2"},"latestImage":"a:1"}`))))
	if p.Latest.String() != "a:2" {
		t.Fatalf("latestRef wins: %+v", p.Latest)
	}
	// v1beta2 with only latestImage: split after the last /
	p = ParsePolicy(project(t, KindPolicy, obj(t, policyDoc("image.toolkit.fluxcd.io/v1beta2", `{}`, `{"latestImage":"registry.example:5000/team/app:1.2"}`))))
	if p.Latest.Name != "registry.example:5000/team/app" || p.Latest.Tag != "1.2" {
		t.Fatalf("latestImage: %+v", p.Latest)
	}
	for in, want := range map[string]ImageRef{
		"registry.example:5000/team/app":      {Name: "registry.example:5000/team/app"},
		"ghcr.io/org/app:1.2@sha256:abc":      {Name: "ghcr.io/org/app", Tag: "1.2", Digest: "sha256:abc"},
		"ghcr.io/org/app@sha256:abc":          {Name: "ghcr.io/org/app", Digest: "sha256:abc"},
		"localhost:5000/app":                  {Name: "localhost:5000/app"},
		"docker.io/library/nginx:1.27-alpine": {Name: "docker.io/library/nginx", Tag: "1.27-alpine"},
	} {
		if got := SplitImage(in); got != want {
			t.Errorf("SplitImage(%q) = %+v", in, got)
		}
	}
}

func TestRepositoryRuleAndClock(t *testing.T) {
	ok := ready("True", "Succeeded", "successful scan: found 42 tags", 3)
	r := ParseRepository(project(t, KindRepository, obj(t, repoDoc(3, 3, ok, ""))))
	if r.TagCount != 42 || len(r.LatestTags) != 2 || r.SecretRef != "regcred" || r.Interval != 5*time.Minute {
		t.Fatalf("parse: %+v", r)
	}
	// scanned 09:55, interval 5m, slack 5m: due 10:05
	e := RepositoryRule(r, t0)
	if e.Role != health.OK || e.Next == nil || !e.Next.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("current: %+v", e)
	}
	e = RepositoryRule(r, t0.Add(5*time.Minute))
	if e.Role != health.Degraded || e.Label != LabelScanDue || !e.Included || e.Boundary == nil || e.Next != nil {
		t.Fatalf("overdue: %+v", e)
	}
	// a skewed scanTime in the future: never overdue early, Next later
	skew := ParseRepository(project(t, KindRepository, obj(t, repoDoc(3, 3, ok, `"lastScanResult":{"tagCount":1,"scanTime":"2026-10-17T10:30:00Z"},`))))
	if e := RepositoryRule(skew, t0); e.Role != health.OK || e.Next == nil || !e.Next.After(t0) {
		t.Fatalf("skewed: %+v", e)
	}
	// failing: the reason, no clock
	bad := ParseRepository(project(t, KindRepository, obj(t, repoDoc(3, 3, ready("False", "AuthenticationFailed", "401", 3), ""))))
	if e := RepositoryRule(bad, t0.Add(time.Hour)); e.Role != health.Fault || !strings.HasPrefix(e.Reason, "AuthenticationFailed") {
		t.Fatalf("failing: %+v", e)
	}
	// S1b §4.1: a failure of the current generation with status behind is a fault
	cur := ParseRepository(project(t, KindRepository, obj(t, repoDoc(4, 3, ready("False", "ImageURLInvalid", "bad", 4), ""))))
	if e := Rule(cur.Common); e.Role != health.Fault {
		t.Fatalf("current failure: %+v", e)
	}
	// a pending generation (Ready for 3, spec at 4): Syncing, never overdue
	pend := ParseRepository(project(t, KindRepository, obj(t, repoDoc(4, 3, ok, ""))))
	if e := RepositoryRule(pend, t0.Add(time.Hour)); e.Label != LabelSyncing {
		t.Fatalf("pending: %+v", e)
	}
	// suspended: paused, no clock
	susp := obj(t, repoDoc(3, 3, ok, ""))
	_ = unstructured.SetNestedField(susp.Object, true, "spec", "suspend")
	if e := RepositoryRule(ParseRepository(project(t, KindRepository, susp)), t0.Add(time.Hour)); e.Role != health.Paused || e.Next != nil {
		t.Fatalf("suspended: %+v", e)
	}
	// Stalled
	st := ParseRepository(project(t, KindRepository, obj(t, repoDoc(3, 3, ready("False", "ImageURLInvalid", "bad", 3)+`,{"type":"Stalled","status":"True","reason":"ImageURLInvalid"}`, ""))))
	if e := Rule(st.Common); e.Label != "Stalled" || e.Role != health.Fault {
		t.Fatalf("stalled: %+v", e)
	}
}

func TestPolicyRuleBlockedBy(t *testing.T) {
	p := ParsePolicy(project(t, KindPolicy, obj(t, policyDoc("image.toolkit.fluxcd.io/v1", `{}`,
		`{"latestRef":{"name":"a","tag":"1"},"conditions":[`+ready("False", "DependencyNotReady", "waiting", 1)+`]}`))))
	if p.BlockedBy() != "flux-system/app" || PolicyRule(p).Role != health.Fault || p.Latest == nil {
		t.Fatalf("blocked, the image retained: %+v %s", p, p.BlockedBy())
	}
	p = ParsePolicy(project(t, KindPolicy, obj(t, policyDoc("image.toolkit.fluxcd.io/v1", `{}`, `{"conditions":[`+ready("False", "AccessDenied", "refused", 1)+`]}`))))
	if p.BlockedBy() != "" || PolicyRule(p).Role != health.Fault {
		t.Fatalf("access denied: %+v", p)
	}
}

func TestAutomationRuleAndClock(t *testing.T) {
	a := ParseAutomation(project(t, KindAutomation, obj(t, autoDoc(autoSpec,
		`{"observedGeneration":2,"lastAutomationRunTime":"2026-10-17T09:50:00Z","lastPushCommit":"abc1234def","lastPushTime":"2026-10-17T09:51:00Z","conditions":[`+ready("True", "Succeeded", "repository up-to-date", 2)+`]}`))))
	// run 09:50, interval 10m: due 10:10
	if e := AutomationRule(a, t0); e.Role != health.OK || e.Next == nil || !e.Next.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("current: %+v", e)
	}
	e := AutomationRule(a, t0.Add(10*time.Minute))
	if e.Role != health.Degraded || e.Label != LabelRunDue || !strings.Contains(e.Reason, "a run may be in progress") {
		t.Fatalf("overdue: %+v", e)
	}
	// resume by generation: Ready from the old generation reads Syncing
	r := ParseAutomation(project(t, KindAutomation, obj(t, strings.Replace(autoDoc(autoSpec, `{"observedGeneration":1,"conditions":[`+ready("True", "Succeeded", "ok", 1)+`]}`), `"generation":2`, `"generation":2`, 1))))
	if e := AutomationRule(r, t0); e.Label != LabelSyncing {
		t.Fatalf("resumed: %+v", e)
	}
}

// S1b §6: the projection never holds the template, its values or push
// option values; messages pass the allowlist.
func TestAutomationProjectionWithholds(t *testing.T) {
	o := project(t, KindAutomation, obj(t, autoDoc(autoSpec,
		`{"conditions":[`+ready("False", "GitOperationFailed", "failed to push: push option ci.skip=SECRETneedle rejected", 2)+`]}`)))
	b, _ := json.Marshal(o.Object)
	if strings.Contains(string(b), "SECRETneedle") {
		t.Fatalf("a needle crossed: %s", b)
	}
	a := ParseAutomation(o)
	if a.Message != crd.AutomationWithheld || a.Reason != "GitOperationFailed" || a.PushOptionCount != 1 || a.SigningKeySecret != "gpg" || a.AuthorEmail != "bot@example.com" {
		t.Fatalf("parse: %+v", a)
	}
}

func policy(ns, name string, labels map[string]string, latest string) Policy {
	p := Policy{Common: Common{Namespace: ns, Name: name}, Labels: labels}
	if latest != "" {
		r := SplitImage(latest)
		p.Latest = &r
	}
	return p
}

func TestAutomationPoliciesJoin(t *testing.T) {
	a := ParseAutomation(project(t, KindAutomation, obj(t, autoDoc(autoSpec, `{"observedPolicies":{"app":{"name":"ghcr.io/org/app","tag":"1.2.0"}}}`))))
	pols := []Policy{
		policy("apps", "app", map[string]string{"team": "apps"}, "ghcr.io/org/app:1.2.0"),
		policy("apps", "api", map[string]string{"team": "apps"}, "ghcr.io/org/api:2.0.0"), // newer than any run: pending
		policy("apps", "other", map[string]string{"team": "web"}, "x:1"),
		policy("apps", "unresolved", map[string]string{"team": "apps"}, ""),
		policy("elsewhere", "app", map[string]string{"team": "apps"}, "y:1"),
	}
	failing := policy("apps", "failing", map[string]string{"team": "apps"}, "ghcr.io/org/f:1")
	failing.Ready = Failed
	pols = append(pols, failing) // a failing policy's last image still drives updates
	uses, state := AutomationPolicies(a, pols, CoverageComplete)
	got := map[string]string{}
	for _, u := range uses {
		got[u.Name] = u.State
	}
	if state != JoinFound || len(uses) != 3 || got["app"] != UseUsed || got["api"] != UsePending || got["failing"] != UsePending {
		t.Fatalf("uses: %+v %s", uses, state)
	}
	if _, st := AutomationPolicies(a, pols, CoveragePartial); st != JoinIncomplete {
		t.Fatal("a capped read is incomplete")
	}
	if _, st := AutomationPolicies(a, pols, CoverageUnreadable); st != JoinUnreadable {
		t.Fatal("unreadable")
	}
	// a clipped observed map: no used or pending claim
	a.ObservedComplete = false
	uses, _ = AutomationPolicies(a, pols, CoverageComplete)
	for _, u := range uses {
		if u.State != UseIncomplete {
			t.Fatalf("clipped map: %+v", u)
		}
	}
	// no selector: every policy in the namespace with an image
	a.HasSelector, a.Selector, a.ObservedComplete = false, nil, true
	if uses, _ := AutomationPolicies(a, pols, CoverageComplete); len(uses) != 4 {
		t.Fatalf("no selector: %+v", uses)
	}
	if got := PolicyAutomations(pols[0], []Automation{a}); len(got) != 1 || got[0].State != UseUsed {
		t.Fatalf("policy side: %+v", got)
	}
	// the repository across namespaces
	p := ParsePolicy(project(t, KindPolicy, obj(t, policyDoc("image.toolkit.fluxcd.io/v1", `{}`, `{}`))))
	repos := []Repository{{Common: Common{Namespace: "flux-system", Name: "app"}}}
	if l := PolicyRepository(p, repos, CoverageComplete); l.State != JoinFound {
		t.Fatalf("repository: %+v", l)
	}
	if l := PolicyRepository(p, nil, CoverageComplete); l.State != JoinNotFound {
		t.Fatalf("missing: %+v", l)
	}
	if l := PolicyRepository(p, nil, CoveragePartial); l.State != JoinIncomplete {
		t.Fatalf("capped: %+v", l)
	}
	if got := RepositoryPolicies(repos[0], []Policy{p}); len(got) != 1 {
		t.Fatal("repository side")
	}
}

// Join inputs are kept whole through the real projection (S1b §3,
// revision 1): a long valid selector, labels that only a full comparison
// tells apart, and an input too large to keep, which claims nothing.
func TestLosslessJoins(t *testing.T) {
	long := strings.Repeat("a", 300)
	spec := strings.Replace(autoSpec, `"policySelector":{"matchLabels":{"team":"apps"}}`, `"policySelector":{"matchLabels":{"team":"`+long[:63]+`"},"matchExpressions":[{"key":"tier","operator":"In","values":["`+strings.Repeat("b", 63)+`","`+strings.Repeat("c", 63)+`","`+strings.Repeat("d", 63)+`"]}]}`, 1)
	a := ParseAutomation(project(t, KindAutomation, obj(t, autoDoc(spec, `{"observedPolicies":{}}`))))
	if a.Selector == nil || a.SelectorIncomplete || a.Selector.MatchLabels["team"] != long[:63] || len(a.Selector.MatchExpressions[0].Values[2]) != 63 {
		t.Fatalf("a selector over 256 bytes, kept whole: %+v", a.Selector)
	}
	near := map[string]string{"team": long[:63], "tier": strings.Repeat("b", 62) + "x"} // differs only at the end
	match := map[string]string{"team": long[:63], "tier": strings.Repeat("c", 63)}
	uses, st := AutomationPolicies(a, []Policy{policy("apps", "near", near, "a:1"), policy("apps", "match", match, "b:1")}, CoverageComplete)
	if st != JoinFound || len(uses) != 1 || uses[0].Name != "match" {
		t.Fatalf("a full comparison: %+v", uses)
	}
	// labels past the object's budget: dropped, and the join says incomplete
	big := map[string]string{}
	for i := 0; i < 80; i++ {
		big[fmt.Sprintf("label-%02d.example.com/key", i)] = strings.Repeat("v", 60)
	}
	lb, _ := json.Marshal(big)
	po := project(t, KindPolicy, obj(t, policyDoc("image.toolkit.fluxcd.io/v1", string(lb), `{"latestRef":{"name":"a","tag":"1"}}`)))
	if po.Size > MaxObject || len(po.Incomplete) != 1 {
		t.Fatalf("too large to keep: size %d incomplete %v", po.Size, po.Incomplete)
	}
	p := ParsePolicy(po)
	p.Namespace = "apps"
	if _, st := AutomationPolicies(a, []Policy{p}, CoverageComplete); !p.LabelsIncomplete || st != JoinIncomplete {
		t.Fatalf("no claim: %v %s", p.LabelsIncomplete, st)
	}
}

func TestPushTarget(t *testing.T) {
	cases := []struct {
		name    string
		a       Automation
		source  *CheckoutRef
		from    string
		branch  string
		failure bool
	}{
		{"the push branch", Automation{PushBranch: "auto", Checkout: &CheckoutRef{Branch: "main"}}, nil, CheckoutAutomation, "auto", false},
		{"an explicit checkout by branch", Automation{Checkout: &CheckoutRef{Branch: "dev"}}, &CheckoutRef{Branch: "main"}, CheckoutAutomation, "dev", false},
		{"an explicit checkout by tag, no push branch: never the source's branch", Automation{Checkout: &CheckoutRef{Tag: "v1"}}, &CheckoutRef{Branch: "main"}, CheckoutAutomation, "", true},
		{"the GitRepository's ref", Automation{}, &CheckoutRef{Branch: "main"}, CheckoutSource, "main", false},
		{"none", Automation{}, nil, CheckoutDefault, "", true},
		{"a deletion refspec", Automation{PushBranch: "a", PushRefspec: ":refs/heads/x"}, nil, CheckoutDefault, "", true},
	}
	for _, c := range cases {
		got := Target(c.a, c.source)
		if got.CheckoutFrom != c.from || got.Branch != c.branch || (got.Error != "") != c.failure {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestBoundBranch(t *testing.T) {
	a := Automation{Common: Common{UID: "u"}, LastPushCommit: "abc1234fff"}
	ev := func(uid, msg string) PushEvent { return PushEvent{UID: uid, Message: msg} }
	push := "pushed commit 'abc1234' to branch 'auto'\ncommit message"
	if b := BoundBranch(a, []PushEvent{ev("u", push)}); b.State != BranchBound || b.Branch != "auto" {
		t.Fatalf("one: %+v", b)
	}
	if b := BoundBranch(a, nil); b.State != BranchExpired {
		t.Fatalf("none: %+v", b)
	}
	if b := BoundBranch(a, []PushEvent{ev("u", push), ev("u", "pushed commit 'abc1234' to branch 'main'")}); b.State != BranchAmbiguous {
		t.Fatalf("disagree: %+v", b)
	}
	if b := BoundBranch(a, []PushEvent{ev("other", push)}); b.State != BranchExpired {
		t.Fatalf("another automation's: %+v", b)
	}
	if b := BoundBranch(a, []PushEvent{ev("u", "pushed commit 'fff0000' to branch 'auto'")}); b.State != BranchExpired {
		t.Fatalf("an older push's: %+v", b)
	}
	if b := BoundBranch(Automation{}, []PushEvent{ev("", push)}); b.State != BranchNoPush {
		t.Fatalf("no push: %+v", b)
	}
}

func TestSourceAndAppliedLines(t *testing.T) {
	sha := "abc1234def5678abc1234def5678abc1234def56"
	a := Automation{LastPushCommit: sha}
	bound := BranchEvidence{State: BranchBound, Branch: "auto"}
	if l := Source(a, SourceInput{State: JoinFound, Revision: "auto@sha1:" + sha, Branch: "auto"}, bound); l.Kind != SourceFetched || l.BranchNote != "" {
		t.Fatalf("fetched: %+v", l)
	}
	l := Source(a, SourceInput{State: JoinFound, Revision: "main@sha1:fff", Branch: "main"}, bound)
	if l.Kind != SourceOther || l.BranchNote != "pushed to auto; this source tracks main" {
		t.Fatalf("another revision and branch: %+v", l)
	}
	a.PushRefspec = "refs/heads/auto:refs/heads/main"
	if l := Source(a, SourceInput{State: JoinFound, Revision: "main@sha1:fff", Branch: "main"}, bound); !strings.Contains(l.BranchNote, "refspec") {
		t.Fatalf("a refspec: %+v", l)
	}
	if l := Source(a, SourceInput{State: JoinFound, Revision: "main@sha1:fff", Branch: "main"}, BranchEvidence{State: BranchExpired}); l.BranchNote == "pushed to auto; this source tracks main" {
		t.Fatalf("no bound branch, no branch claim: %+v", l)
	}
	if l := Source(a, SourceInput{State: JoinUnreadable}, bound); l.Kind != SourceMissing {
		t.Fatalf("unreadable: %+v", l)
	}
	lines := Applied(a, []AppliedInput{{Name: "at", Revision: "main@sha1:" + sha}, {Name: "behind", Revision: "main@sha1:000"}})
	if !lines[0].Applied || lines[1].Applied {
		t.Fatalf("applied: %+v", lines)
	}
	if Applied(Automation{}, []AppliedInput{{Name: "x"}}) != nil {
		t.Fatal("no push, no lines")
	}
}
