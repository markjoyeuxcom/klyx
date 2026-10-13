package appbridge

import (
	"github.com/moomora/klyx/internal/certmanager"
)

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
