package appbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/certmanager"
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
	chain certmanager.Chain
	err   error
}

func (f *fakeExplainConn) GetCertificateChain(context.Context, string, string) (certmanager.Chain, error) {
	return f.chain, f.err
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

func newAI(client Completer, backend string, allowEgress bool, protected bool, conn CertExplainConn) *AIService {
	return NewAIService(
		func(string) (CertExplainConn, bool) { return conn, conn != nil },
		client, backend, allowEgress,
		func(string) bool { return protected },
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
