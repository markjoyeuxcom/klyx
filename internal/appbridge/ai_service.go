package appbridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/helmcli"
)

const (
	aiTimeout      = 60 * time.Second
	aiMaxTokens    = 512
	aiFixMaxTokens = 900 // a suggested patch needs more room than an explanation
	// aiRelatedCap bounds how many related objects the cross-lens bundle resolves,
	// so an Explain on a highly-connected object stays a handful of Gets, not a
	// cluster crawl.
	aiRelatedCap = 6
	// aiRelatedGetCap bounds the number of GetInstanceDetail calls made while
	// filling the bundle, so a wall of unresolvable refs cannot drive many Gets.
	aiRelatedGetCap = 12
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
	lookup       func(string) (AIConn, bool)
	client       Completer // nil when the backend is off
	backend      string
	onMachine    bool // the endpoint is THIS machine (loopback) - nothing leaves it
	allowEgress  bool
	isProtected  func(string) bool
	capabilities func(string) capability.Set // per-cluster operator health; may be nil
}

// NewAIService wires the read-only AI surface. onMachine must be true ONLY when
// the configured endpoint is this machine (ai.IsLoopbackEndpoint): it is what
// lets a protected cluster's context reach the model without an explicit
// opt-in. The backend label alone ("local" = OpenAI-compatible protocol) says
// nothing about where the server runs.
func NewAIService(lookup func(string) (AIConn, bool), client Completer, backend string, onMachine, allowEgress bool, isProtected func(string) bool, capabilities func(string) capability.Set) *AIService {
	return &AIService{lookup: lookup, client: client, backend: backend, onMachine: onMachine, allowEgress: allowEgress, isProtected: isProtected, capabilities: capabilities}
}

// egressRefusal is the reason a protected cluster's context may not be sent.
func egressRefusal(cluster string) string {
	return "AI is disabled for protected cluster \"" + cluster + "\": the configured AI endpoint is not on this machine - use a local model on localhost, or set ai.allowProtectedEgress to allow it"
}

// operators projects the cluster's controller health into the bundle (nil-safe).
func (s *AIService) operators(cluster string) *operatorHealth {
	if s.capabilities == nil {
		return nil
	}
	return projectOperators(s.capabilities(cluster))
}

// Available reports whether the Explain affordance should render at all.
func (s *AIService) Available() AIAvailabilityDTO {
	return AIAvailabilityDTO{Backend: s.backend, Ready: s.client != nil}
}

// EgressAllowed reports whether an AI call is permitted for this cluster: false
// when a protected cluster's context would leave the machine without the
// explicit ai.allowProtectedEgress opt-in. Only an endpoint on this machine
// (loopback) needs no opt-in - a "local" (OpenAI-compatible) backend pointed at
// a LAN or hosted server is egress like any other (review 2026-10-03 #14).
// Lets the UI disable-with-tooltip rather than fail on click.
func (s *AIService) EgressAllowed(cluster string) bool {
	if s.client == nil {
		return false
	}
	if !s.onMachine && s.protected(cluster) && !s.allowEgress {
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
		return AIResultDTO{Error: egressRefusal(cluster)}
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
		return AIResultDTO{Error: egressRefusal(cluster)}
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
	bundle.Operators = s.operators(cluster)
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
		return AIResultDTO{Error: egressRefusal(cluster)}
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
	bundle.Operators = s.operators(cluster)
	// The manifest leaves the machine (unless the endpoint is loopback): drop the
	// last-applied annotation and mask credential-like values first. Fail
	// closed - a manifest that cannot be redacted is not sent (review
	// 2026-10-03 E).
	manifest, redacted, err := redactManifestForAI(root.YAML)
	if err != nil {
		return AIResultDTO{Error: "could not redact the manifest, so nothing was sent: " + err.Error()}
	}
	system, user := BuildFixPrompt(bundle, manifest)
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
		Redacted:    redacted,
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
	// Cap Gets, not just appended results: an object with many unresolvable
	// related refs (each skipped without filling a slot) must not drive an
	// unbounded number of GetInstanceDetail calls.
	gets := 0
	for _, r := range root.Related {
		if len(b.Related) >= aiRelatedCap || gets >= aiRelatedGetCap {
			break
		}
		k := r.Group + "/" + r.Plural + "/" + r.Namespace + "/" + r.Name
		if seen[k] {
			continue
		}
		seen[k] = true
		gets++
		rd, err := conn.GetInstanceDetail(ctx, r.Group, r.Version, r.Plural, r.Namespace, r.Name)
		if err != nil {
			continue
		}
		b.Related = append(b.Related, toResourceView(rd, r.Relation))
	}
	return b, root, nil
}

// lastAppliedAnnotation is kubectl's client-side-apply snapshot: a JSON copy
// of the whole object inside one string, which a structural masker cannot see
// into - so it is dropped, never masked.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// redactManifestForAI prepares a resource manifest for an AI prompt. Secret
// objects arrive masked already (MaskSecretData); this covers every OTHER kind,
// whose spec can carry inline credentials (env literals, ConfigMap data, inline
// chart values): it drops the last-applied annotation, then masks every value
// under a credential-like key - and the value of env entries whose name looks
// like a secret - keeping secret REFERENCE fields readable
// (helmcli.MaskManifest). Heuristic: it reduces, but cannot rule out, sensitive
// data in the prompt, and the prompt says so. Returns the number of masked
// values; an unparseable manifest is an error (the caller sends nothing).
func redactManifestForAI(manifest string) (string, int, error) {
	if strings.TrimSpace(manifest) == "" {
		return "", 0, nil
	}
	var obj map[string]interface{}
	if err := yaml.Unmarshal([]byte(manifest), &obj); err != nil {
		return "", 0, fmt.Errorf("parse manifest: %w", err)
	}
	if md, ok := obj["metadata"].(map[string]interface{}); ok {
		if anns, ok := md["annotations"].(map[string]interface{}); ok {
			delete(anns, lastAppliedAnnotation)
			if len(anns) == 0 {
				delete(md, "annotations")
			}
		}
	}
	stripped, err := yaml.Marshal(obj)
	if err != nil {
		return "", 0, fmt.Errorf("re-encode manifest: %w", err)
	}
	masked, paths, err := helmcli.MaskManifest(string(stripped))
	if err != nil {
		return "", 0, err
	}
	return masked, len(paths), nil
}
