package appbridge

import (
	"encoding/json"
	"strings"

	"github.com/moomora/klyx/internal/certmanager"
)

// certExplainSystem is the grounded system prompt. The honesty rules live here:
// reason only from the provided chain, cite the node, abstain on insufficient
// data, distinguish Failed from Issuing, never imply Klyx will apply a change.
const certExplainSystem = `You are a Kubernetes cert-manager diagnostics assistant embedded in Klyx, a read-only desktop client. You are given the ALREADY-RESOLVED issuance chain for one Certificate as structured JSON - this is ground truth Klyx extracted from the live cluster. In 2-4 sentences, explain why issuance is stuck and what the operator should check next.

Rules:
- Reason ONLY from the provided chain. Never invent objects, conditions, namespaces, or fixes not supported by the input.
- Name the specific node (kind + name) your conclusion rests on.
- ready="Failed" is broken; ready="Issuing" is in-flight and NOT yet a failure; ready="Ready" is healthy. If every node is Ready, say issuance is healthy.
- If the data is insufficient to determine a cause, say exactly that.
- Klyx is read-only and desired state is authored in Git. Suggest what to change; never imply Klyx will apply it.
- Use cert-manager vocabulary correctly. No preamble, no markdown headings.`

// chainNodeView is the compact, redaction-safe JSON view of one chain node. The
// cert-manager chain types carry no secret values by construction, so this is
// inherently safe to send; the field set here is the belt to that suspenders.
type chainNodeView struct {
	Kind      string          `json:"kind"`
	Namespace string          `json:"namespace,omitempty"`
	Name      string          `json:"name"`
	Ready     string          `json:"ready"`
	Reason    string          `json:"reason,omitempty"`
	Message   string          `json:"message,omitempty"`
	Children  []chainNodeView `json:"children,omitempty"`
}

type chainView struct {
	Certificate chainNodeView   `json:"certificate"`
	DNSNames    []string        `json:"dnsNames,omitempty"`
	BoundIssuer *chainNodeView  `json:"boundIssuer"`
	Requests    []chainNodeView `json:"requests"`
	Warnings    []string        `json:"warnings,omitempty"`
}

func toChainNodeView(n certmanager.ChainNode) chainNodeView {
	v := chainNodeView{
		Kind:      string(n.Kind),
		Namespace: n.Namespace,
		Name:      n.Name,
		Ready:     string(n.Ready),
		Reason:    n.Reason,
		Message:   n.Message,
	}
	for _, ch := range n.Children {
		v.Children = append(v.Children, toChainNodeView(ch))
	}
	return v
}

// BuildCertExplainPrompt turns a resolved chain into the (system, user) prompt.
// Pure and self-contained; the user message carries Klyx's own deepest-stuck
// verdict so the model anchors on it.
func BuildCertExplainPrompt(chain certmanager.Chain) (system, user string) {
	view := chainView{
		Certificate: chainNodeView{
			Kind:    "Certificate",
			Name:    chain.Certificate.Name,
			Ready:   string(chain.Certificate.Ready),
			Reason:  chain.Certificate.Reason,
			Message: chain.Certificate.Message,
		},
		DNSNames: chain.Certificate.DNSNames,
		Warnings: chain.Warnings,
	}
	view.Certificate.Namespace = chain.Certificate.Namespace
	if chain.Issuer != nil {
		bi := chainNodeView{
			Kind:      string(chain.Issuer.Kind),
			Namespace: chain.Issuer.Namespace,
			Name:      chain.Issuer.Name,
			Ready:     string(chain.Issuer.Ready),
			Reason:    chain.Issuer.Reason,
			Message:   chain.Issuer.Message,
		}
		view.BoundIssuer = &bi
	}
	for _, r := range chain.Requests {
		view.Requests = append(view.Requests, toChainNodeView(r))
	}

	failing := "none - every node is Ready"
	if chain.FailingLink != nil {
		failing = string(chain.FailingLink.Kind) + "/" + chain.FailingLink.Name + " (ready=" + string(chain.FailingLink.Ready) + ")"
	}

	b, _ := json.Marshal(view)
	user = "Klyx's computed deepest-stuck node (failingLink): " + failing +
		"\n\nResolved issuance chain (ground truth):\n" + string(b)
	return certExplainSystem, user
}

// certChainIdentities is the human-readable "grounded on" line: the certificate,
// the bound issuer, and the failing link Klyx flagged.
func certChainIdentities(chain certmanager.Chain) string {
	parts := []string{"Certificate/" + chain.Certificate.Namespace + "/" + chain.Certificate.Name}
	if chain.Issuer != nil {
		parts = append(parts, string(chain.Issuer.Kind)+"/"+chain.Issuer.Name)
	}
	if chain.FailingLink != nil {
		parts = append(parts, string(chain.FailingLink.Kind)+"/"+chain.FailingLink.Name+" (failingLink)")
	}
	return strings.Join(parts, ", ")
}
