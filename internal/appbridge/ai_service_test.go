package appbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/crd"
)

// fakeCompleter is a fake ai.Provider. It returns reply trimmed, as a real
// provider does, or err.
type fakeCompleter struct {
	mu        sync.Mutex
	reply     string
	err       error
	gotSystem string
	gotUser   string
	gotReq    ai.Request
	calls     int
}

func (f *fakeCompleter) Complete(_ context.Context, req ai.Request) (ai.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotSystem, f.gotUser, f.gotReq = req.System, req.User, req
	if f.err != nil {
		return ai.Result{}, f.err
	}
	return ai.Result{Text: strings.TrimSpace(f.reply), ModelUsed: "test-model"}, nil
}

// aiTestState builds a snapshot for the tests: "local" is a model on THIS
// machine (loopback), "hosted" is off-machine, "off" (or a nil client) is no
// provider.
func aiTestState(client *fakeCompleter, backend string, allowEgress bool) *AIState {
	st := &AIState{ProviderID: ai.ProviderOff, AllowEgress: allowEgress}
	if client == nil || backend == "off" {
		return st
	}
	st.Provider = client
	switch backend {
	case "local":
		st.ProviderID, st.OnMachine, st.Origin = ai.ProviderOpenAICompatible, true, "http://localhost:11434"
	case "localOffMachine":
		st.ProviderID, st.Origin = ai.ProviderOpenAICompatible, "http://192.168.1.10:11434"
	default:
		st.ProviderID, st.Origin = ai.ProviderAnthropic, ai.DefaultAnthropicOrigin
	}
	return st
}

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

// newAI builds the service for the egress tests: "local" here means a model on
// THIS machine (loopback endpoint); hosted is off-machine.
func newAI(client *fakeCompleter, backend string, allowEgress bool, protected bool, conn AIConn) *AIService {
	return NewAIService(
		func(string) (AIConn, bool) { return conn, conn != nil },
		aiTestState(client, backend, allowEgress),
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
	if r.Model != "test-model" || r.Backend != "anthropic" || r.Origin != ai.DefaultAnthropicOrigin {
		t.Fatalf("model/backend/origin: %+v", r)
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
		aiTestState(cl, "local", false), func(string) bool { return false }, caps,
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
	empty := NewAIService(func(string) (AIConn, bool) { return conn, true }, aiTestState(cl, "local", false), func(string) bool { return false },
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

// Review 2026-10-03 #14: "local" names the OpenAI-compatible wire protocol, not
// where the model runs. A local-protocol backend pointed off this machine is
// egress: a protected cluster needs ai.allowProtectedEgress, like hosted.
func TestAI_LocalProtocolOffMachineIsEgress(t *testing.T) {
	offMachine := NewAIService(
		func(string) (AIConn, bool) { return nil, false },
		aiTestState(&fakeCompleter{}, "localOffMachine", false),
		func(string) bool { return true }, nil,
	)
	if offMachine.EgressAllowed("prd") {
		t.Fatal("a local-protocol backend on a remote host must not receive a protected cluster's context without the opt-in")
	}
	if r := offMachine.ExplainResource("prd", "", "v1", "pods", "ns", "p"); !strings.Contains(r.Error, "not on this machine") {
		t.Fatalf("want the egress refusal, got %+v", r)
	}
	optedIn := NewAIService(
		func(string) (AIConn, bool) { return nil, false },
		aiTestState(&fakeCompleter{}, "localOffMachine", true),
		func(string) bool { return true }, nil,
	)
	if !optedIn.EgressAllowed("prd") {
		t.Fatal("ai.allowProtectedEgress must allow it")
	}
}

// Review 2026-10-03 E: Suggest-fix sent full non-Secret manifests - inline env
// credentials, ConfigMap data and the last-applied JSON copy - to the model,
// while the prompt claimed values were masked. The manifest is now redacted
// before it leaves, with secret REFERENCES kept readable.
func TestAI_SuggestFix_RedactsNonSecretManifest(t *testing.T) {
	yamlText := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"spec":{"env":[{"name":"DB_PASSWORD","value":"hunter2"}]}}'
    team: payments
spec:
  template:
    spec:
      containers:
      - name: api
        env:
        - name: DB_PASSWORD
          value: hunter2
        - name: LOG_LEVEL
          value: debug
        - name: API_TOKEN
          valueFrom:
            secretKeyRef:
              name: api-creds
              key: token
`
	root := crd.InstanceDetail{Kind: "Deployment", Namespace: "app", Name: "api", YAML: yamlText}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"apps/deployments/app/api": root}}
	cl := &fakeCompleter{reply: "ok"}
	s := newAI(cl, "local", false, false, conn)

	r := s.SuggestFix("c", "apps", "v1", "deployments", "app", "api")
	if !r.OK {
		t.Fatalf("want OK, got %+v", r)
	}
	if strings.Contains(cl.gotUser, "hunter2") {
		t.Fatalf("an inline credential reached the model:\n%s", cl.gotUser)
	}
	if strings.Contains(cl.gotUser, "last-applied-configuration") {
		t.Fatal("the last-applied JSON copy must be dropped, not sent")
	}
	for _, keep := range []string{"LOG_LEVEL", "debug", "api-creds", "team: payments"} {
		if !strings.Contains(cl.gotUser, keep) {
			t.Errorf("non-secret context %q must survive redaction:\n%s", keep, cl.gotUser)
		}
	}
	if r.Redacted < 1 {
		t.Fatalf("the result must report what was redacted, got %d", r.Redacted)
	}
	if strings.Contains(cl.gotUser, "secret values are masked)") {
		t.Fatal("the prompt must not overclaim that all values are masked")
	}
}

// Fail closed: a manifest that cannot be parsed for redaction is not sent.
func TestAI_SuggestFix_UnredactableManifestIsNotSent(t *testing.T) {
	root := crd.InstanceDetail{Kind: "Deployment", Namespace: "app", Name: "api", YAML: "kind: [unterminated"}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"apps/deployments/app/api": root}}
	cl := &fakeCompleter{reply: "ok"}
	s := newAI(cl, "local", false, false, conn)
	r := s.SuggestFix("c", "apps", "v1", "deployments", "app", "api")
	if r.OK || !strings.Contains(r.Error, "nothing was sent") || cl.calls != 0 {
		t.Fatalf("want a fail-closed refusal with no model call, got %+v (calls=%d)", r, cl.calls)
	}
}

// swapConn swaps the service's snapshot while the call is between its egress
// check and its send (inside the chain resolve).
type swapConn struct {
	fakeExplainConn
	onResolve func()
}

func (c *swapConn) GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error) {
	if c.onResolve != nil {
		c.onResolve()
	}
	return c.fakeExplainConn.GetCertificateChain(ctx, ns, name)
}

// Spec 2026-10-13 §1.1 (Codex review 3): the egress check, the send and the
// returned provenance read ONE snapshot. A swap mid-call - here from an
// on-machine provider to an off-machine one without the protected opt-in -
// must not pair the local check with a cloud send.
func TestAI_SnapshotCheckAndSendAgree(t *testing.T) {
	local := &fakeCompleter{reply: "local answer"}
	cloud := &fakeCompleter{reply: "cloud answer"}
	conn := &swapConn{fakeExplainConn: fakeExplainConn{chain: stuckChain()}}
	s := NewAIService(func(string) (AIConn, bool) { return conn, true }, aiTestState(local, "local", false), func(string) bool { return true }, nil)
	conn.onResolve = func() { SetAIState(s, aiTestState(cloud, "hosted", false)) }

	r := s.ExplainCertificate("prd", "ns", "n")
	if !r.OK || r.Explanation != "local answer" || r.Backend != string(ai.ProviderOpenAICompatible) {
		t.Fatalf("the call must finish on the snapshot it checked: %+v", r)
	}
	if cloud.calls != 0 {
		t.Fatal("a protected cluster's context reached the provider swapped in after the egress check")
	}
	// The next call uses the new snapshot, and its own check refuses.
	if r := s.ExplainCertificate("prd", "ns", "n"); r.OK || !strings.Contains(r.Error, "protected") {
		t.Fatalf("the new off-machine snapshot must be refused for a protected cluster: %+v", r)
	}
}

// Concurrent calls during swaps (run with -race): every result's provenance
// matches the provider that produced its text.
func TestAI_ConcurrentCallsDuringSwaps(t *testing.T) {
	a := &fakeCompleter{reply: "from-a"}
	b := &fakeCompleter{reply: "from-b"}
	stA, stB := aiTestState(a, "hosted", true), aiTestState(b, "local", true)
	s := NewAIService(func(string) (AIConn, bool) { return &fakeExplainConn{chain: stuckChain()}, true }, stA, func(string) bool { return false }, nil)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				SetAIState(s, stB)
			} else {
				SetAIState(s, stA)
			}
		}
	}()
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := s.ExplainCertificate("c", "ns", "n")
			want := map[string]string{"from-a": string(ai.ProviderAnthropic), "from-b": string(ai.ProviderOpenAICompatible)}[r.Explanation]
			if !r.OK || r.Backend != want {
				t.Errorf("provenance does not match the answering provider: %+v", r)
			}
		}()
	}
	wg.Wait()
	close(stop)
}

// A refusal, a truncation or tool output is an error in the DTO - never a
// partial explanation (spec §0 defect 3).
func TestAI_ModelOutcomesFoldIntoErrors(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"refused":    {&ai.RefusedError{Category: "cyber"}, "declined to answer (cyber)"},
		"truncated":  {ai.ErrTruncated, "cut off"},
		"tool":       {ai.ErrUnexpectedOutput, "other than text"},
		"keyed http": {errors.New("HTTP 401: invalid x-api-key: sk-ant-secret-123456789"), "model call failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newAI(&fakeCompleter{err: tc.err}, "hosted", true, false, &fakeExplainConn{chain: stuckChain()})
			r := s.ExplainCertificate("c", "ns", "n")
			if r.OK || r.Explanation != "" || !strings.Contains(r.Error, tc.want) {
				t.Fatalf("got %+v, want an error containing %q", r, tc.want)
			}
			if strings.Contains(r.Error, "sk-ant-secret-123456789") {
				t.Fatalf("the error leaks a key: %s", r.Error)
			}
		})
	}
}

// keysFake is an in-memory ai.KeyStore for BuildAIState.
type keysFake struct {
	key string
	err error
}

func (k keysFake) Get(ai.ProviderID) (string, error) {
	if k.err != nil {
		return "", k.err
	}
	if k.key == "" {
		return "", ai.ErrNoKey
	}
	return k.key, nil
}
func (keysFake) Set(ai.ProviderID, string) error { return nil }
func (keysFake) Delete(ai.ProviderID) error      { return nil }

// A configured provider that cannot be built is unavailable with a reason,
// never silently off and never falling back to another key.
func TestBuildAIState_Problems(t *testing.T) {
	cfg := &config.AIConfig{Provider: "anthropic", KeySource: "keychain", APIKey: ""}
	st := BuildAIState(cfg, keysFake{}, func(string) string { return "sk-from-env" })
	if st.Provider != nil || !strings.Contains(st.Problem, "No API key") || st.ProviderID != ai.ProviderAnthropic {
		t.Fatalf("missing keychain key: %+v", st)
	}
	st = BuildAIState(cfg, keysFake{err: &ai.KeychainError{Err: errors.New("user interaction is not allowed")}}, nil)
	if st.Provider != nil || !strings.Contains(st.Problem, "keychain unavailable") {
		t.Fatalf("locked keychain: %+v", st)
	}
	st = BuildAIState(cfg, keysFake{key: "sk-ant-k"}, nil)
	if st.Provider == nil || st.Origin != ai.DefaultAnthropicOrigin || st.Model != ai.DefaultAnthropicModel {
		t.Fatalf("keychain key: %+v", st)
	}
	s := NewAIService(func(string) (AIConn, bool) { return nil, false }, BuildAIState(cfg, keysFake{}, nil), nil, nil)
	if av := s.Available(); av.Ready || av.Backend != "anthropic" || av.Problem == "" {
		t.Fatalf("Available must report the problem: %+v", av)
	}
	if r := s.ExplainCertificate("c", "ns", "n"); !strings.Contains(r.Error, "No API key") {
		t.Fatalf("a call must surface the problem: %+v", r)
	}
	if off := BuildAIState(nil, nil, nil); off.Provider != nil || off.ProviderID != ai.ProviderOff || off.Problem != "" {
		t.Fatalf("nil config is off: %+v", off)
	}
}
