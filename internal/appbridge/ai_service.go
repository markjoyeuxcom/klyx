package appbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/observe"
)

const (
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

// AIConn is the narrow per-cluster surface AIService borrows - the on-demand
// resolvers the cert lens and the generic inspector already expose. The generic
// GetInstanceDetail is what powers cross-lens synthesis: its Related refs are the
// edges (cert->secret, ESO->secret, workload->reloads, ...) the bundle walks.
type AIConn interface {
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
	GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error)
	// HealthRowsNow is Explain's anchor: one fresh Observer capture of a part.
	HealthRowsNow(ctx context.Context, task, part string) (observe.Rows, bool)
	HelmReleases(ctx context.Context) ([]helmcli.Release, error)
	HelmHistory(ctx context.Context, namespace, release string) ([]helmcli.HistoryEntry, error)
}

// AIService is the Wails-bound, READ-ONLY AI-assist surface. It borrows the
// data layer's resolvers on demand, sends only resolved, redacted evidence to
// the model, and never mutates cluster state. Errors fold into the result DTO;
// it never throws across the bridge.
//
// The provider and its egress policy are one immutable AIState, swapped as a
// whole (SetAIState) and loaded once per call (spec 2026-10-13 §1.1).
type AIService struct {
	lookup       func(string) (AIConn, bool)
	state        atomic.Pointer[AIState]
	rev          atomic.Uint64
	isProtected  func(string) bool
	capabilities func(string) capability.Set // per-cluster operator health; may be nil
}

// NewAIService wires the read-only AI surface with its first snapshot.
func NewAIService(lookup func(string) (AIConn, bool), initial *AIState, isProtected func(string) bool, capabilities func(string) capability.Set) *AIService {
	s := &AIService{lookup: lookup, isProtected: isProtected, capabilities: capabilities}
	SetAIState(s, initial)
	return s
}

// SetAIState publishes a new snapshot. A free function, not a method: every
// exported method of a bound service is callable from the webview, and only
// Go (the settings writer) may swap the provider. In-flight calls finish on
// the snapshot they loaded.
func SetAIState(s *AIService, st *AIState) {
	if st == nil {
		st = &AIState{ProviderID: ai.ProviderOff}
	}
	cp := *st
	cp.Rev = s.rev.Add(1)
	s.state.Store(&cp)
}

func (s *AIService) load() *AIState { return s.state.Load() }

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

// Available reports whether the Explain affordance should render at all, and
// where prompts go.
func (s *AIService) Available() AIAvailabilityDTO {
	st := s.load()
	d := AIAvailabilityDTO{Backend: string(st.ProviderID), Ready: st.Provider != nil, Model: st.Model, Origin: st.Origin, Problem: st.Problem}
	if problem := notReady(st); problem != "" {
		d.Ready, d.Problem = false, problem
	}
	return d
}

// notReady is why a built provider would refuse a call before it starts -
// a CLI build that Test has not checked on this machine, or whose admission
// was revoked - so the UI disables Explain with that reason instead of
// failing on click. It runs nothing.
func notReady(st *AIState) string {
	if r, ok := st.Provider.(ai.ReadinessReporter); ok {
		if err := r.Ready(); err != nil {
			return err.Error()
		}
	}
	return ""
}

// EgressAllowed reports whether an AI call is permitted for this cluster: false
// when a protected cluster's context would leave the machine without the
// explicit ai.allowProtectedEgress opt-in. Only an endpoint on this machine
// (loopback) needs no opt-in (review 2026-10-03 #14). Lets the UI
// disable-with-tooltip rather than fail on click.
func (s *AIService) EgressAllowed(cluster string) bool {
	return s.egressAllowed(s.load(), cluster)
}

func (s *AIService) egressAllowed(st *AIState, cluster string) bool {
	if st.Provider == nil {
		return false
	}
	if !st.OnMachine && s.protected(cluster) && !st.AllowEgress {
		return false
	}
	return true
}

func (s *AIService) protected(cluster string) bool {
	return s.isProtected != nil && s.isProtected(cluster)
}

// precheck is the shared gate: a provider exists, the cluster's context may
// go to it, and the cluster is connected. It reads only the given snapshot.
func (s *AIService) precheck(st *AIState, cluster string) (AIConn, string) {
	if st.Provider == nil {
		if st.Problem != "" {
			return nil, "AI is unavailable: " + st.Problem
		}
		return nil, "AI backend not configured"
	}
	if problem := notReady(st); problem != "" {
		return nil, "AI is unavailable: " + problem
	}
	if !s.egressAllowed(st, cluster) {
		return nil, egressRefusal(cluster)
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return nil, "cluster not connected"
	}
	return conn, ""
}

// complete runs one completion on the snapshot's provider and folds the
// outcome into a DTO: a refusal, a truncation or tool output is an error,
// never an explanation (spec §1.2).
func complete(ctx context.Context, st *AIState, req ai.Request, grounded string, redacted int) AIResultDTO {
	res, err := st.Provider.Complete(ctx, req)
	if err != nil {
		return AIResultDTO{Error: modelError(err), Backend: string(st.ProviderID), Origin: st.Origin}
	}
	return AIResultDTO{
		OK:          true,
		Explanation: res.Text,
		Model:       res.ModelUsed,
		Backend:     string(st.ProviderID),
		Origin:      st.Origin,
		GroundedOn:  grounded,
		Redacted:    redacted,
	}
}

// modelError is the user-facing text for a failed completion.
func modelError(err error) string {
	var refused *ai.RefusedError
	var cli *ai.CLIRefusedError
	var discarded *ai.CLIDiscardedError
	switch {
	case errors.As(err, &refused):
		return "The model declined to answer" + refusalDetail(refused) + "."
	case errors.As(err, &discarded):
		// Sent, then dropped: never "did not send" (Codex review 2026-10-13).
		if discarded.Partial {
			return "Part of this may have reached Claude Code before the hand-off failed (" + discarded.Reason + "); no answer is shown."
		}
		return "Claude Code received this, but Klyx discarded its answer: " + discarded.Reason + "."
	case errors.As(err, &cli):
		return "Klyx did not send this to Claude Code: " + cli.Reason + "."
	case errors.Is(err, ai.ErrTruncated):
		return "The model's answer was cut off before it finished; nothing partial is shown. Try again, or choose a model with a larger output limit."
	case errors.Is(err, ai.ErrUnexpectedOutput), errors.Is(err, ai.ErrEmpty), errors.Is(err, ai.ErrUnconfirmed):
		return err.Error() + "."
	case errors.Is(err, context.DeadlineExceeded):
		return "The model did not answer in time."
	default:
		return "model call failed: " + ai.Scrub(err.Error())
	}
}

func refusalDetail(r *ai.RefusedError) string {
	if r.Category == "" {
		return ""
	}
	return " (" + r.Category + ")"
}

// SuggestFix drafts the MINIMAL change to desired state that would fix the
// diagnosed problem, as a copyable patch the operator commits to Git. Klyx never
// applies it - Git stays the source of truth. The same posture as Explain: the
// object is fenced by the UID the operator selected, its cluster text is
// scrubbed and capped, and the evidence is framed as untrusted data. It
// additionally sends the resource's current manifest (Secret data masked
// upstream, credential-like values redacted, capped).
func (s *AIService) SuggestFix(cluster, group, version, plural, ns, name, uid string) AIResultDTO {
	st := s.load()
	conn, refusal := s.precheck(st, cluster)
	if refusal != "" {
		return AIResultDTO{Error: refusal}
	}
	if uid == "" {
		return AIResultDTO{Error: "the selected object has no identity yet; nothing was sent"}
	}
	if karpenterClassResources[group+"/"+plural] {
		// S10 §8: a class's spec holds its role, instance profile and
		// selector IDs; no manifest of one is sent
		return AIResultDTO{Error: "Suggest fix is not available for a Karpenter NodeClass: its spec holds the role, the instance profile and cloud IDs, which Klyx never sends to AI; nothing was sent"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ai.Deadline(ai.PurposeFix))
	defer cancel()

	bundle, root, err := buildResourceBundle(ctx, conn, group, version, plural, ns, name)
	if err != nil {
		return AIResultDTO{Error: "resolve resource: " + ai.Scrub(err.Error())}
	}
	if root.UID != uid {
		return AIResultDTO{Error: errReplaced.Error()}
	}
	sc := &scrubber{}
	bundle = sc.bundle(karpenterBundle(bundle)) // S10 §8: no Karpenter message
	bundle.Operators = sc.operators(s.operators(cluster))
	// The manifest leaves the machine (unless the endpoint is loopback): drop the
	// last-applied annotation and mask credential-like values first. Fail
	// closed - a manifest that cannot be redacted is not sent (review
	// 2026-10-03 E).
	manifest, redacted, err := redactManifestForAI(root.YAML, karpenterResources[group+"/"+plural])
	if err != nil {
		return AIResultDTO{Error: "could not redact the manifest, so nothing was sent: " + err.Error()}
	}
	system, user := BuildFixPrompt(bundle, manifest)
	res := complete(ctx, st, ai.Request{System: system, User: user, Purpose: ai.PurposeFix}, resourceBundleIdentities(bundle), redacted)
	res.Scrubbed = sc.n
	return res
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
// karpenter (coverage S10 §8): a NodePool's or NodeClaim's status holds
// Karpenter's condition text, so it is dropped, and the identifier
// redactor runs over the rest.
func redactManifestForAI(manifest string, karp bool) (string, int, error) {
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
	if karp {
		karpenterManifest(obj)
	}
	stripped, err := yaml.Marshal(obj)
	if err != nil {
		return "", 0, fmt.Errorf("re-encode manifest: %w", err)
	}
	masked, paths, err := helmcli.MaskManifest(string(stripped))
	if err != nil {
		return "", 0, err
	}
	n := len(paths)
	if karp {
		var m int
		masked, m = karpenterText(masked)
		n += m
	}
	return masked, n, nil
}
