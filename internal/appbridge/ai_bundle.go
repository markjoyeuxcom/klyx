package appbridge

import (
	"encoding/json"
	"strings"

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
	Root    resourceView   `json:"root"`
	Related []resourceView `json:"related,omitempty"`
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
