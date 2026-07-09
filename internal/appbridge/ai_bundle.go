package appbridge

import (
	"encoding/json"
	"strings"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/crd"
)

// resourceView is the compact, redaction-safe projection of one object's resolved
// health: identity + conditions + facts ONLY. YAML, secret keys, and secret data
// are deliberately excluded so nothing sensitive reaches the prompt.
type resourceView struct {
	Kind       string          `json:"kind"`
	Namespace  string          `json:"namespace,omitempty"`
	Name       string          `json:"name"`
	Relation   string          `json:"relation,omitempty"` // how it relates to the root (empty for the root)
	Conditions []conditionView `json:"conditions,omitempty"`
	Facts      []factView      `json:"facts,omitempty"`
}

type conditionView struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

type factView struct {
	Group string `json:"group,omitempty"`
	Label string `json:"label"`
	Value string `json:"value"`
	Tone  string `json:"tone,omitempty"`
}

type resourceBundle struct {
	Root      resourceView    `json:"root"`
	Related   []resourceView  `json:"related,omitempty"`
	Operators *operatorHealth `json:"operators,omitempty"`
}

// operatorView is the health of one cluster controller. A resource can be stuck
// because its own operator is down, so this often carries the real root cause.
type operatorView struct {
	Healthy bool   `json:"healthy"`
	Tier    string `json:"tier"`
	Reason  string `json:"reason,omitempty"`
}

// operatorHealth reports the health of the controllers that are actually present
// on the cluster (absent operators are omitted to keep the context focused).
type operatorHealth struct {
	Flux            *operatorView `json:"flux,omitempty"`
	Argo            *operatorView `json:"argo,omitempty"`
	CertManager     *operatorView `json:"certManager,omitempty"`
	ExternalSecrets *operatorView `json:"externalSecrets,omitempty"`
	Reloader        *operatorView `json:"reloader,omitempty"`
	Gateway         *operatorView `json:"gateway,omitempty"`
}

// projectOperators maps the capability Set to the present controllers' health.
// Returns nil when nothing is installed (the section is then omitted).
func projectOperators(set capability.Set) *operatorHealth {
	present := false
	view := func(isPresent, healthy bool, tier capability.Tier, reason string) *operatorView {
		if !isPresent {
			return nil
		}
		present = true
		v := &operatorView{Healthy: healthy, Tier: tier.String()}
		if !healthy {
			v.Reason = reason
		}
		return v
	}
	// GitOps carries ONE combined Base.Tier/Reason for Flux+Argo, so per-tool tiers
	// are derived from each tool's own Present/Healthy (else a healthy Flux reads as
	// Degraded whenever Argo is degraded, and vice versa). The combined reason is
	// only attached when THAT tool is unhealthy - it names the failing tool.
	oh := &operatorHealth{
		Flux:            view(set.GitOps.Flux.Present, set.GitOps.Flux.Healthy, capability.Classify(set.GitOps.Flux.Present, set.GitOps.Flux.Healthy), set.GitOps.Reason),
		Argo:            view(set.GitOps.Argo.Present, set.GitOps.Argo.Healthy, capability.Classify(set.GitOps.Argo.Present, set.GitOps.Argo.Healthy), set.GitOps.Reason),
		CertManager:     view(set.CertManager.Present, set.CertManager.Healthy, set.CertManager.Tier, set.CertManager.Reason),
		ExternalSecrets: view(set.ExternalSecrets.Present, set.ExternalSecrets.Healthy, set.ExternalSecrets.Tier, set.ExternalSecrets.Reason),
		Reloader:        view(set.Reloader.Present, set.Reloader.Healthy, set.Reloader.Tier, set.Reloader.Reason),
		Gateway: view(set.Network.GatewayAPIVersion != "" || set.Network.CiliumPresent,
			set.Network.Tier == capability.Healthy, set.Network.Tier, set.Network.Reason),
	}
	if !present {
		return nil
	}
	return oh
}

func toResourceView(d crd.InstanceDetail, relation string) resourceView {
	v := resourceView{Kind: d.Kind, Namespace: d.Namespace, Name: d.Name, Relation: relation}
	for _, c := range d.Conditions {
		v.Conditions = append(v.Conditions, conditionView{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, Scope: c.Scope})
	}
	for _, f := range d.Facts {
		v.Facts = append(v.Facts, factView{Group: f.Group, Label: f.Label, Value: f.Value, Tone: f.Tone})
	}
	return v
}

// resourceExplainSystem is the cross-lens root-cause system prompt.
const resourceExplainSystem = `You are a Kubernetes diagnostics assistant embedded in Klyx, a read-only desktop client. You are given one resource and the resolved health (conditions + facts) of the objects it relates to - Klyx already extracted all of this from the live cluster. In 3-5 sentences, explain the ROOT CAUSE of any problem, tracing the causal chain across the related objects (for example: a route is not accepted because its gateway's listener is down because a certificate is stuck because a challenge failed).

An "operators" section reports the health of the cluster's controllers (Flux, Argo, cert-manager, External Secrets, Reloader, Gateway). If the operator responsible for a resource is not Healthy, that is often the real root cause - say so explicitly.

Rules:
- Reason ONLY from the provided data. Never invent objects, conditions, or fixes not present in the input.
- Cite each node (kind + name) in the causal chain.
- A condition with status="True" on a positive type (Ready, Accepted, Available, Healthy, Synced) is healthy; status="False", or a reason/message indicating Failed/Degraded/Error, is a problem.
- If every object is healthy, say so plainly. If the data is insufficient to determine a cause, say exactly that.
- Klyx is read-only and desired state is authored in Git. Suggest what to change; never imply Klyx will apply it.
- Use correct Kubernetes vocabulary. No preamble, no markdown headings.`

// BuildResourceExplainPrompt turns a resolved bundle into the (system, user)
// prompt. Pure; the user message is the compact JSON bundle.
func BuildResourceExplainPrompt(bundle resourceBundle) (system, user string) {
	b, _ := json.Marshal(bundle)
	return resourceExplainSystem, "Resolved resource + related-object health (ground truth):\n" + string(b)
}

// fixSystem is the remediation system prompt. The load-bearing boundary: Klyx
// will NOT apply the suggestion - it is authored into Git and reconciled.
const fixSystem = `You are a Kubernetes remediation assistant embedded in Klyx, a READ-ONLY desktop client. You are given a broken resource, the resolved health of it and its related objects, and its current manifest - all extracted from the live cluster by Klyx. Propose the MINIMAL change to the DESIRED STATE that would fix the diagnosed problem.

Klyx will NOT apply your suggestion. The operator commits it to Git (a Kustomize patch, a Helm values change, or a manifest edit) and Flux/Argo reconciles it. So author a change to desired state, never an imperative command.

An "operators" section reports the health of the cluster's controllers. If the operator responsible for this resource is not Healthy, no manifest change will help until the operator recovers - say that instead of proposing a spec edit.

Output exactly three parts, in order:
1. A one-line summary of the change.
2. The change as a YAML snippet inside a fenced ` + "```yaml" + ` block - a strategic-merge patch or the corrected fragment, minimal, only the changed fields with just enough surrounding context (apiVersion/kind/metadata.name) to locate them.
3. One or two sentences on why it fixes the diagnosis.

Rules:
- Reason ONLY from the provided data. If you cannot determine a safe, specific fix, say exactly that and name what to investigate instead - do not guess.
- Never fabricate secret values, credentials, or image digests. Secret values are masked (<masked>); if the fix needs a secret, say where it should come from (e.g. the ExternalSecret / SealedSecret), do not invent one.
- Do not suggest kubectl or other imperative commands. Desired state is authored in Git.`

// BuildFixPrompt turns a resolved bundle + the resource's current manifest into
// the (system, user) fix prompt. Pure. The manifest is capped; secret values are
// already masked upstream (MaskSecretData) before the YAML is built.
func BuildFixPrompt(bundle resourceBundle, manifest string) (system, user string) {
	b, _ := json.Marshal(bundle)
	m := manifest
	if len(m) > aiManifestCap {
		m = m[:aiManifestCap] + "\n# ...(truncated)"
	}
	user = "Resolved resource + related-object health (ground truth):\n" + string(b) +
		"\n\nCurrent manifest of the resource (desired state; secret values are masked):\n```yaml\n" + m + "\n```"
	return fixSystem, user
}

// resourceBundleIdentities is the human-readable "grounded on" line: the root
// plus each related object that carried into the bundle.
func resourceBundleIdentities(bundle resourceBundle) string {
	parts := []string{identity(bundle.Root)}
	for _, r := range bundle.Related {
		parts = append(parts, identity(r))
	}
	return strings.Join(parts, ", ")
}

func identity(v resourceView) string {
	id := v.Kind + "/"
	if v.Namespace != "" {
		id += v.Namespace + "/"
	}
	id += v.Name
	return id
}
