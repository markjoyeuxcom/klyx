package appbridge

import (
	"context"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
)

const (
	aiTimeout   = 60 * time.Second
	aiMaxTokens = 512
)

// Completer is the LLM surface AIService needs; *ai.Client satisfies it and
// tests inject a fake. Kept minimal so the appbridge never imports the transport
// details.
type Completer interface {
	Complete(ctx context.Context, system, user string, maxTokens int) (string, error)
	Model() string
}

// CertExplainConn is the narrow per-cluster surface AIService borrows - the same
// on-demand chain resolver CertManagerConn already exposes.
type CertExplainConn interface {
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
}

// AIService is the Wails-bound, READ-ONLY AI-assist surface. Stateless and not
// per-cluster: it borrows the data layer's chain resolver on demand, sends only
// the resolved (secret-free) chain to the model, and never mutates cluster state.
// Errors fold into the result DTO; it never throws across the bridge.
type AIService struct {
	lookup      func(string) (CertExplainConn, bool)
	client      Completer // nil when the backend is off
	backend     string
	allowEgress bool
	isProtected func(string) bool
}

func NewAIService(lookup func(string) (CertExplainConn, bool), client Completer, backend string, allowEgress bool, isProtected func(string) bool) *AIService {
	return &AIService{lookup: lookup, client: client, backend: backend, allowEgress: allowEgress, isProtected: isProtected}
}

// Available reports whether the Explain affordance should render at all.
func (s *AIService) Available() AIAvailabilityDTO {
	return AIAvailabilityDTO{Backend: s.backend, Ready: s.client != nil}
}

// EgressAllowed reports whether an Explain call is permitted for this cluster:
// false when a hosted backend would send a protected cluster's topology off the
// machine without an explicit opt-in. A local backend always allows it (nothing
// leaves the machine). Lets the UI disable-with-tooltip rather than fail on click.
func (s *AIService) EgressAllowed(cluster string) bool {
	if s.client == nil {
		return false
	}
	if s.backend == "hosted" && s.protected(cluster) && !s.allowEgress {
		return false
	}
	return true
}

func (s *AIService) protected(cluster string) bool {
	return s.isProtected != nil && s.isProtected(cluster)
}

// ExplainCertificate resolves the certificate's issuance chain fresh (server-
// authoritative), sends the resolved chain - no secrets - to the model, and
// returns a grounded explanation. Read-only.
func (s *AIService) ExplainCertificate(cluster, ns, name string) AIResultDTO {
	if s.client == nil {
		return AIResultDTO{Error: "AI backend not configured"}
	}
	if !s.EgressAllowed(cluster) {
		return AIResultDTO{Error: "AI is disabled for protected cluster \"" + cluster + "\": use a local backend or set ai.allowProtectedEgress"}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return AIResultDTO{Error: "cluster not connected"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), aiTimeout)
	defer cancel()

	chain, err := conn.GetCertificateChain(ctx, ns, name)
	if err != nil {
		return AIResultDTO{Error: "resolve chain: " + err.Error()}
	}
	system, user := BuildCertExplainPrompt(chain)
	text, err := s.client.Complete(ctx, system, user, aiMaxTokens)
	if err != nil {
		return AIResultDTO{Error: "model call failed: " + err.Error()}
	}
	return AIResultDTO{
		OK:          true,
		Explanation: strings.TrimSpace(text),
		Model:       s.client.Model(),
		Backend:     s.backend,
		GroundedOn:  certChainIdentities(chain),
	}
}
