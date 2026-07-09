package appbridge

import (
	"context"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/crd"
)

const (
	aiTimeout      = 60 * time.Second
	aiMaxTokens    = 512
	aiFixMaxTokens = 900 // a suggested patch needs more room than an explanation
	// aiRelatedCap bounds how many related objects the cross-lens bundle resolves,
	// so an Explain on a highly-connected object stays a handful of Gets, not a
	// cluster crawl.
	aiRelatedCap = 6
	// aiManifestCap bounds the current-manifest text sent for a fix suggestion.
	aiManifestCap = 8000
)

// Completer is the LLM surface AIService needs; *ai.Client satisfies it and
// tests inject a fake. Kept minimal so the appbridge never imports the transport
// details.
type Completer interface {
	Complete(ctx context.Context, system, user string, maxTokens int) (string, error)
	Model() string
}

// AIConn is the narrow per-cluster surface AIService borrows - the on-demand
// resolvers the cert lens and the generic inspector already expose. The generic
// GetInstanceDetail is what powers cross-lens synthesis: its Related refs are the
// edges (cert->secret, ESO->secret, workload->reloads, ...) the bundle walks.
type AIConn interface {
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
	GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error)
}

// AIService is the Wails-bound, READ-ONLY AI-assist surface. Stateless and not
// per-cluster: it borrows the data layer's chain resolver on demand, sends only
// the resolved (secret-free) chain to the model, and never mutates cluster state.
// Errors fold into the result DTO; it never throws across the bridge.
type AIService struct {
	lookup      func(string) (AIConn, bool)
	client      Completer // nil when the backend is off
	backend     string
	allowEgress bool
	isProtected func(string) bool
}

func NewAIService(lookup func(string) (AIConn, bool), client Completer, backend string, allowEgress bool, isProtected func(string) bool) *AIService {
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

// ExplainResource is the cross-lens flagship: it assembles the resolved health of
// a resource AND the objects it relates to (its Related refs, which span lenses),
// then asks the model for one causal root-cause narrative. Read-only; only
// conditions + facts cross into the prompt (never YAML or secret data).
func (s *AIService) ExplainResource(cluster, group, version, plural, ns, name string) AIResultDTO {
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

	bundle, _, err := buildResourceBundle(ctx, conn, group, version, plural, ns, name)
	if err != nil {
		return AIResultDTO{Error: "resolve resource: " + err.Error()}
	}
	system, user := BuildResourceExplainPrompt(bundle)
	text, err := s.client.Complete(ctx, system, user, aiMaxTokens)
	if err != nil {
		return AIResultDTO{Error: "model call failed: " + err.Error()}
	}
	return AIResultDTO{
		OK:          true,
		Explanation: strings.TrimSpace(text),
		Model:       s.client.Model(),
		Backend:     s.backend,
		GroundedOn:  resourceBundleIdentities(bundle),
	}
}

// SuggestFix drafts the MINIMAL change to desired state that would fix the
// diagnosed problem, as a copyable patch the operator commits to Git. Klyx never
// applies it - Git stays the source of truth. Same read-only + egress posture as
// Explain; it additionally sends the resource's current manifest (secret values
// already masked upstream, and capped) so the model can target a specific edit.
func (s *AIService) SuggestFix(cluster, group, version, plural, ns, name string) AIResultDTO {
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

	bundle, root, err := buildResourceBundle(ctx, conn, group, version, plural, ns, name)
	if err != nil {
		return AIResultDTO{Error: "resolve resource: " + err.Error()}
	}
	system, user := BuildFixPrompt(bundle, root.YAML)
	text, err := s.client.Complete(ctx, system, user, aiFixMaxTokens)
	if err != nil {
		return AIResultDTO{Error: "model call failed: " + err.Error()}
	}
	return AIResultDTO{
		OK:          true,
		Explanation: strings.TrimSpace(text),
		Model:       s.client.Model(),
		Backend:     s.backend,
		GroundedOn:  resourceBundleIdentities(bundle),
	}
}

// buildResourceBundle resolves the root object plus (capped) its related objects'
// health via the generic GetInstanceDetail. It also returns the raw root detail
// so a fix suggestion can reach the current manifest. Related resolution is
// best-effort: an unresolvable ref (RBAC, gone) is skipped, not fatal. Sequential
// and bounded so the whole assembly fits one on-demand call.
func buildResourceBundle(ctx context.Context, conn AIConn, group, version, plural, ns, name string) (resourceBundle, crd.InstanceDetail, error) {
	root, err := conn.GetInstanceDetail(ctx, group, version, plural, ns, name)
	if err != nil {
		return resourceBundle{}, crd.InstanceDetail{}, err
	}
	b := resourceBundle{Root: toResourceView(root, "")}
	seen := map[string]bool{group + "/" + plural + "/" + ns + "/" + name: true}
	for _, r := range root.Related {
		if len(b.Related) >= aiRelatedCap {
			break
		}
		k := r.Group + "/" + r.Plural + "/" + r.Namespace + "/" + r.Name
		if seen[k] {
			continue
		}
		seen[k] = true
		rd, err := conn.GetInstanceDetail(ctx, r.Group, r.Version, r.Plural, r.Namespace, r.Name)
		if err != nil {
			continue
		}
		b.Related = append(b.Related, toResourceView(rd, r.Relation))
	}
	return b, root, nil
}
