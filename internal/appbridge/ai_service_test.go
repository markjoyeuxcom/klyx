package appbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/crd"
)

// The real transport must satisfy the Completer interface main.go passes in.
var _ Completer = (*ai.Client)(nil)

type fakeCompleter struct {
	reply     string
	err       error
	gotSystem string
	gotUser   string
	calls     int
}

func (f *fakeCompleter) Complete(_ context.Context, system, user string, _ int) (string, error) {
	f.calls++
	f.gotSystem, f.gotUser = system, user
	return f.reply, f.err
}
func (f *fakeCompleter) Model() string { return "test-model" }

type fakeExplainConn struct {
	chain   certmanager.Chain
	err     error
	details map[string]crd.InstanceDetail // keyed "group/plural/ns/name"
	detErr  error
	getCall int
}

func (f *fakeExplainConn) GetCertificateChain(context.Context, string, string) (certmanager.Chain, error) {
	return f.chain, f.err
}

func (f *fakeExplainConn) GetInstanceDetail(_ context.Context, group, _, plural, ns, name string) (crd.InstanceDetail, error) {
	f.getCall++
	if f.detErr != nil {
		return crd.InstanceDetail{}, f.detErr
	}
	if d, ok := f.details[group+"/"+plural+"/"+ns+"/"+name]; ok {
		return d, nil
	}
	return crd.InstanceDetail{}, errors.New("not found")
}

func stuckChain() certmanager.Chain {
	return certmanager.Chain{
		Certificate: certmanager.Certificate{Namespace: "prod", Name: "ollama-tls", Ready: certmanager.Issuing, Reason: "Pending", DNSNames: []string{"ollama.example.com"}},
		Issuer:      &certmanager.Issuer{Kind: certmanager.ClusterIssuerKind, Name: "le-prod", Ready: certmanager.Ready},
		Requests: []certmanager.ChainNode{{
			Kind: certmanager.CertificateRequestKind, Namespace: "prod", Name: "ollama-tls-1", Ready: certmanager.Issuing,
			Children: []certmanager.ChainNode{{
				Kind: certmanager.OrderKind, Namespace: "prod", Name: "ollama-tls-1-abc", Ready: certmanager.Issuing,
				Children: []certmanager.ChainNode{{
					Kind: certmanager.ChallengeKind, Namespace: "prod", Name: "ollama-tls-1-abc-0", Ready: certmanager.Failed, Reason: "presentError", Message: "DNS record not propagated",
				}},
			}},
		}},
		FailingLink: &certmanager.ChainNode{Kind: certmanager.ChallengeKind, Namespace: "prod", Name: "ollama-tls-1-abc-0", Ready: certmanager.Failed, Reason: "presentError"},
	}
}

func newAI(client Completer, backend string, allowEgress bool, protected bool, conn AIConn) *AIService {
	return NewAIService(
		func(string) (AIConn, bool) { return conn, conn != nil },
		client, backend, allowEgress,
		func(string) bool { return protected },
		nil, // capabilities: exercised separately
	)
}

func TestAI_Available_OffWhenNoClient(t *testing.T) {
	s := newAI(nil, "off", false, false, &fakeExplainConn{})
	if s.Available().Ready {
		t.Fatal("no client -> not ready")
	}
	if r := s.ExplainCertificate("c", "ns", "n"); r.OK || r.Error == "" {
		t.Fatalf("off backend must refuse with an error, got %+v", r)
	}
}

func TestAI_EgressGate_ProtectedHosted(t *testing.T) {
	cl := &fakeCompleter{reply: "x"}
	// hosted + protected + egress NOT allowed -> refused, and the model is never called.
	s := newAI(cl, "hosted", false, true, &fakeExplainConn{chain: stuckChain()})
	if s.EgressAllowed("prd") {
		t.Fatal("hosted + protected + no allow must not permit egress")
	}
	r := s.ExplainCertificate("prd", "ns", "n")
	if r.OK || !strings.Contains(r.Error, "protected") {
		t.Fatalf("want a protected-egress refusal, got %+v", r)
	}
	if cl.calls != 0 {
		t.Fatal("the model must NOT be called when egress is refused")
	}
}

func TestAI_EgressGate_AllowedForLocalAndOptIn(t *testing.T) {
	cl := &fakeCompleter{reply: "ok"}
	// local backend on a protected cluster is always allowed (nothing leaves the box).
	local := newAI(cl, "local", false, true, &fakeExplainConn{chain: stuckChain()})
	if !local.EgressAllowed("prd") {
		t.Fatal("local backend must allow egress on a protected cluster")
	}
	// hosted + protected + explicit opt-in is allowed.
	optIn := newAI(cl, "hosted", true, true, &fakeExplainConn{chain: stuckChain()})
	if !optIn.EgressAllowed("prd") {
		t.Fatal("hosted + allowProtectedEgress must permit egress")
	}
}

func TestAI_ExplainHappyPath(t *testing.T) {
	cl := &fakeCompleter{reply: "  The Challenge ollama-tls-1-abc-0 failed on presentError - the DNS-01 record has not propagated.  "}
	s := newAI(cl, "hosted", true, false, &fakeExplainConn{chain: stuckChain()})

	r := s.ExplainCertificate("homelab", "prod", "ollama-tls")
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
	if r.Explanation != "The Challenge ollama-tls-1-abc-0 failed on presentError - the DNS-01 record has not propagated." {
		t.Fatalf("explanation not trimmed/returned: %q", r.Explanation)
	}
	if r.Model != "test-model" || r.Backend != "hosted" {
		t.Fatalf("model/backend: %+v", r)
	}
	if !strings.Contains(r.GroundedOn, "Certificate/prod/ollama-tls") || !strings.Contains(r.GroundedOn, "failingLink") {
		t.Fatalf("groundedOn must name the cert + failing link: %q", r.GroundedOn)
	}
	// The prompt must anchor on the failing link and carry the resolved chain.
	if !strings.Contains(cl.gotUser, "failingLink") || !strings.Contains(cl.gotUser, "ollama-tls-1-abc-0") {
		t.Fatalf("prompt missing the failing-link anchor: %q", cl.gotUser)
	}
}

func TestAI_RedactionGuard_NoSecretsInPrompt(t *testing.T) {
	// The chain carries no secret values by construction; assert the serialized
	// prompt never contains secret-bearing keys, so a future field that leaked
	// would fail this test.
	_, user := BuildCertExplainPrompt(stuckChain())
	for _, bad := range []string{"\"data\"", "\"stringData\"", "<masked>", "BEGIN PRIVATE KEY", "apiKey", "token"} {
		if strings.Contains(user, bad) {
			t.Fatalf("prompt leaked a sensitive marker %q: %s", bad, user)
		}
	}
}

func TestAI_ExplainResource_CrossLens(t *testing.T) {
	root := crd.InstanceDetail{
		Kind: "HTTPRoute", Namespace: "prod", Name: "ollama",
		Conditions: []crd.Condition{{Type: "Accepted", Status: "False", Reason: "NoMatchingListener", Scope: "parent eg/eg"}},
		Related:    []crd.RelatedRef{{Kind: "Gateway", Group: "gateway.networking.k8s.io", Version: "v1", Plural: "gateways", Namespace: "eg", Name: "eg", Scope: "Namespaced", Relation: "gateway"}},
	}
	gw := crd.InstanceDetail{
		Kind: "Gateway", Namespace: "eg", Name: "eg",
		Conditions: []crd.Condition{{Type: "Programmed", Status: "False", Reason: "Invalid", Scope: "listener https"}},
	}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{
		"gateway.networking.k8s.io/httproutes/prod/ollama": root,
		"gateway.networking.k8s.io/gateways/eg/eg":         gw,
	}}
	cl := &fakeCompleter{reply: "The HTTPRoute is not Accepted because Gateway eg's https listener is Invalid."}
	s := newAI(cl, "local", false, false, conn)

	r := s.ExplainResource("c", "gateway.networking.k8s.io", "v1", "httproutes", "prod", "ollama")
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
	// The prompt carries BOTH the root's and the related gateway's resolved health.
	for _, want := range []string{"HTTPRoute", "Gateway", "NoMatchingListener", "Invalid"} {
		if !strings.Contains(cl.gotUser, want) {
			t.Fatalf("prompt missing cross-lens context %q: %s", want, cl.gotUser)
		}
	}
	if !strings.Contains(r.GroundedOn, "HTTPRoute/prod/ollama") || !strings.Contains(r.GroundedOn, "Gateway/eg/eg") {
		t.Fatalf("groundedOn must name root + related: %q", r.GroundedOn)
	}
}

func TestAI_ExplainResource_CapsRelated(t *testing.T) {
	var related []crd.RelatedRef
	details := map[string]crd.InstanceDetail{}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("cm%d", i)
		related = append(related, crd.RelatedRef{Kind: "ConfigMap", Group: "", Version: "v1", Plural: "configmaps", Namespace: "ns", Name: name, Scope: "Namespaced", Relation: "uses"})
		details["/configmaps/ns/"+name] = crd.InstanceDetail{Kind: "ConfigMap", Namespace: "ns", Name: name}
	}
	details["/pods/ns/root"] = crd.InstanceDetail{Kind: "Pod", Namespace: "ns", Name: "root", Related: related}
	conn := &fakeExplainConn{details: details}
	s := newAI(&fakeCompleter{reply: "ok"}, "local", false, false, conn)

	s.ExplainResource("c", "", "v1", "pods", "ns", "root")
	// 1 root + aiRelatedCap related = the bundle stops walking at the cap.
	if conn.getCall != aiRelatedCap+1 {
		t.Fatalf("want %d Get calls (root + cap), got %d", aiRelatedCap+1, conn.getCall)
	}
}

func TestAI_ExplainResource_SkipsUnresolvableRelated(t *testing.T) {
	root := crd.InstanceDetail{Kind: "Pod", Namespace: "ns", Name: "p", Related: []crd.RelatedRef{
		{Kind: "Secret", Group: "", Version: "v1", Plural: "secrets", Namespace: "ns", Name: "gone", Relation: "uses"},
	}}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"/pods/ns/p": root}}
	s := newAI(&fakeCompleter{reply: "ok"}, "local", false, false, conn)

	r := s.ExplainResource("c", "", "v1", "pods", "ns", "p")
	if !r.OK {
		t.Fatalf("root must still explain when a related ref is unresolvable: %+v", r)
	}
	if strings.Contains(r.GroundedOn, "gone") {
		t.Fatalf("an unresolvable related ref must not appear in groundedOn: %q", r.GroundedOn)
	}
}

func TestAI_ExplainResource_RedactionGuard(t *testing.T) {
	// A Secret-related object contributes only conditions/facts to the bundle;
	// its (masked) YAML must never enter the prompt.
	root := crd.InstanceDetail{Kind: "ExternalSecret", Namespace: "ns", Name: "es",
		Conditions: []crd.Condition{{Type: "Ready", Status: "False", Reason: "SecretSyncedError"}},
		Related:    []crd.RelatedRef{{Kind: "Secret", Group: "", Version: "v1", Plural: "secrets", Namespace: "ns", Name: "target", Relation: "target"}}}
	sec := crd.InstanceDetail{Kind: "Secret", Namespace: "ns", Name: "target",
		Facts: []crd.Fact{{Label: "keys", Value: "3", Kind: "well"}},
		YAML:  "data:\n  password: <masked>\n"}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{
		"external-secrets.io/externalsecrets/ns/es": root,
		"/secrets/ns/target":                        sec,
	}}
	cl := &fakeCompleter{reply: "ok"}
	s := newAI(cl, "local", false, false, conn)

	s.ExplainResource("c", "external-secrets.io", "v1beta1", "externalsecrets", "ns", "es")
	for _, bad := range []string{"<masked>", "\"data\"", "password", "BEGIN"} {
		if strings.Contains(cl.gotUser, bad) {
			t.Fatalf("bundle leaked %q: %s", bad, cl.gotUser)
		}
	}
	// But the Secret's benign fact + the ExternalSecret's failure condition ARE present.
	if !strings.Contains(cl.gotUser, "SecretSyncedError") {
		t.Fatalf("bundle should carry the ExternalSecret failure condition: %s", cl.gotUser)
	}
}

func TestAI_ExplainResource_EgressGate(t *testing.T) {
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"/pods/ns/p": {Kind: "Pod", Namespace: "ns", Name: "p"}}}
	s := newAI(cl, "hosted", false, true, conn) // hosted + protected + no opt-in
	r := s.ExplainResource("prd", "", "v1", "pods", "ns", "p")
	if r.OK || !strings.Contains(r.Error, "protected") {
		t.Fatalf("want protected-egress refusal, got %+v", r)
	}
	if cl.calls != 0 || conn.getCall != 0 {
		t.Fatal("egress-refused must call neither the resolver nor the model")
	}
}

func TestAI_ExplainResource_IncludesOperatorHealth(t *testing.T) {
	root := crd.InstanceDetail{
		Kind: "Certificate", Namespace: "prod", Name: "tls",
		Conditions: []crd.Condition{{Type: "Ready", Status: "False", Reason: "IssuerNotReady"}},
	}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"cert-manager.io/certificates/prod/tls": root}}
	cl := &fakeCompleter{reply: "cert-manager itself is Degraded - fix the controller first."}
	// cert-manager present but unhealthy: the real root cause is the operator.
	caps := func(string) capability.Set {
		return capability.Set{CertManager: capability.CertManagerCapability{
			Base: capability.Base{Tier: capability.Degraded, Reason: "cert-manager controller not ready"}, Present: true, Healthy: false,
		}}
	}
	s := NewAIService(
		func(string) (AIConn, bool) { return conn, true },
		cl, "local", false, func(string) bool { return false }, caps,
	)

	r := s.ExplainResource("c", "cert-manager.io", "v1", "certificates", "prod", "tls")
	if !r.OK {
		t.Fatalf("want OK, got %+v", r)
	}
	for _, want := range []string{"operators", "certManager", "cert-manager controller not ready"} {
		if !strings.Contains(cl.gotUser, want) {
			t.Fatalf("prompt missing operator health %q: %s", want, cl.gotUser)
		}
	}
	// A cluster with no operators installed carries no operators section.
	empty := NewAIService(func(string) (AIConn, bool) { return conn, true }, cl, "local", false, func(string) bool { return false },
		func(string) capability.Set { return capability.Set{} })
	empty.ExplainResource("c", "cert-manager.io", "v1", "certificates", "prod", "tls")
	if strings.Contains(cl.gotUser, "\"operators\"") {
		t.Fatalf("no installed operators should mean no operators section: %s", cl.gotUser)
	}
}

func TestProjectOperators_PerToolGitOpsTier(t *testing.T) {
	// Flux healthy while Argo degraded (a supported coexistence). Flux must NOT
	// inherit the combined Degraded tier.
	set := capability.Set{GitOps: capability.GitOpsCapability{
		Base: capability.Base{Tier: capability.Degraded, Reason: "Argo installed but argocd-application-controller is not ready"},
		Flux: capability.FluxInfo{Present: true, Healthy: true},
		Argo: capability.ArgoInfo{Present: true, Healthy: false},
	}}
	oh := projectOperators(set)
	if oh == nil || oh.Flux == nil || oh.Argo == nil {
		t.Fatalf("want flux+argo present: %+v", oh)
	}
	if !oh.Flux.Healthy || oh.Flux.Tier != "Healthy" || oh.Flux.Reason != "" {
		t.Fatalf("healthy Flux must read Healthy with no reason, got %+v", oh.Flux)
	}
	if oh.Argo.Healthy || oh.Argo.Tier != "Degraded" || oh.Argo.Reason == "" {
		t.Fatalf("degraded Argo must read Degraded with a reason, got %+v", oh.Argo)
	}
}

func TestAI_ExplainResource_CapsGetsNotJustResults(t *testing.T) {
	var related []crd.RelatedRef
	for i := 0; i < 20; i++ {
		related = append(related, crd.RelatedRef{Kind: "X", Group: "g", Version: "v1", Plural: "xs", Namespace: "ns", Name: fmt.Sprintf("x%d", i), Relation: "r"})
	}
	// Root resolves; every related ref is unresolvable (absent from details).
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{
		"/pods/ns/root": {Kind: "Pod", Namespace: "ns", Name: "root", Related: related},
	}}
	s := newAI(&fakeCompleter{reply: "ok"}, "local", false, false, conn)

	s.ExplainResource("c", "", "v1", "pods", "ns", "root")
	// 1 root + at most aiRelatedGetCap related Gets, even though 20 refs are unresolvable.
	if conn.getCall != 1+aiRelatedGetCap {
		t.Fatalf("Gets must be bounded to root + %d, got %d", aiRelatedGetCap, conn.getCall)
	}
}

func TestAI_SuggestFix_IncludesManifestAndBundle(t *testing.T) {
	root := crd.InstanceDetail{
		Kind: "Deployment", Namespace: "app", Name: "api",
		Conditions: []crd.Condition{{Type: "Available", Status: "False", Reason: "MinimumReplicasUnavailable"}},
		YAML:       "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\nspec:\n  replicas: 0\n",
	}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"apps/deployments/app/api": root}}
	cl := &fakeCompleter{reply: "Scale to 1 replica.\n```yaml\nspec:\n  replicas: 1\n```\nThe Deployment is scaled to 0."}
	s := newAI(cl, "local", false, false, conn)

	r := s.SuggestFix("c", "apps", "v1", "deployments", "app", "api")
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
	// The fix prompt carries the current manifest AND the resolved health.
	if !strings.Contains(cl.gotUser, "replicas: 0") || !strings.Contains(cl.gotUser, "MinimumReplicasUnavailable") {
		t.Fatalf("fix prompt missing manifest or health: %s", cl.gotUser)
	}
	// The system prompt states the read-only / Git boundary.
	if !strings.Contains(cl.gotSystem, "NOT apply") {
		t.Fatalf("fix system prompt must state Klyx will not apply: %s", cl.gotSystem)
	}
	if r.Explanation == "" {
		t.Fatal("want a suggestion")
	}
}

func TestAI_SuggestFix_SecretManifestStaysMasked(t *testing.T) {
	// The manifest is included for a fix, but secret values are already masked
	// upstream - the model sees <masked>, never a real value.
	root := crd.InstanceDetail{
		Kind: "Secret", Namespace: "app", Name: "creds",
		YAML: "apiVersion: v1\nkind: Secret\ndata:\n  password: <masked>\n",
	}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"/secrets/app/creds": root}}
	cl := &fakeCompleter{reply: "ok"}
	s := newAI(cl, "local", false, false, conn)

	s.SuggestFix("c", "", "v1", "secrets", "app", "creds")
	if !strings.Contains(cl.gotUser, "<masked>") {
		t.Fatalf("secret manifest should reach the model already masked: %s", cl.gotUser)
	}
}

func TestAI_SuggestFix_EgressGate(t *testing.T) {
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"/pods/ns/p": {Kind: "Pod", Namespace: "ns", Name: "p"}}}
	s := newAI(cl, "hosted", false, true, conn) // hosted + protected + no opt-in
	r := s.SuggestFix("prd", "", "v1", "pods", "ns", "p")
	if r.OK || !strings.Contains(r.Error, "protected") {
		t.Fatalf("want protected-egress refusal, got %+v", r)
	}
	if cl.calls != 0 || conn.getCall != 0 {
		t.Fatal("egress-refused must call neither the resolver nor the model")
	}
}

func TestAI_Errors_FoldIntoDTO(t *testing.T) {
	// resolver error.
	s1 := newAI(&fakeCompleter{}, "local", false, false, &fakeExplainConn{err: errors.New("forbidden")})
	if r := s1.ExplainCertificate("c", "ns", "n"); r.OK || !strings.Contains(r.Error, "forbidden") {
		t.Fatalf("resolver error must fold into DTO, got %+v", r)
	}
	// model error.
	s2 := newAI(&fakeCompleter{err: errors.New("429 rate limited")}, "local", false, false, &fakeExplainConn{chain: stuckChain()})
	if r := s2.ExplainCertificate("c", "ns", "n"); r.OK || !strings.Contains(r.Error, "rate limited") {
		t.Fatalf("model error must fold into DTO, got %+v", r)
	}
	// cluster miss.
	s3 := newAI(&fakeCompleter{}, "local", false, false, nil)
	if r := s3.ExplainCertificate("c", "ns", "n"); r.OK || !strings.Contains(r.Error, "not connected") {
		t.Fatalf("cluster miss must fold into DTO, got %+v", r)
	}
}
