package capability

// ControllerRef identifies a GitOps controller workload to watch for health.
type ControllerRef struct {
	Tool      string // "flux" | "argo"
	Kind      string // "Deployment" | "StatefulSet"
	Namespace string
	Name      string
}

// ControllerRefs returns the controller workloads to watch for the GitOps tools
// present in s. Empty if neither Flux nor Argo is present.
func ControllerRefs(s Set) []ControllerRef {
	var refs []ControllerRef
	if s.GitOps.Flux.Present && !s.GitOps.Flux.Synthesized { // an operator-only install has no kustomize-controller yet
		refs = append(refs, ControllerRef{
			Tool: "flux", Kind: "Deployment",
			Namespace: fluxNamespace, Name: fluxKustomizeController,
		})
	}
	if s.GitOps.Argo.Present {
		refs = append(refs, ControllerRef{
			Tool: "argo", Kind: "StatefulSet",
			Namespace: argoNamespace, Name: argoAppController,
		})
	}
	return refs
}

// WithGitOpsHealth returns a copy of s.GitOps with the tier, reason, and per-tool
// health recomputed from fresh controller reads. Presence, version, and
// coexistence are preserved. Reads are consulted only for tools that are
// present in s; an unverified read claims nothing.
func WithGitOpsHealth(s Set, flux, argo ControllerRead) GitOpsCapability {
	out := s.GitOps
	if out.Flux.Present && !out.Flux.Synthesized {
		applyRead(flux, &out.Flux.Healthy, &out.Flux.Verified, &out.Flux.Why)
		if out.Flux.Healthy {
			out.Flux.Controllers = []string{fluxKustomizeController}
		} else {
			out.Flux.Controllers = nil
		}
	}
	if out.Argo.Present {
		applyRead(argo, &out.Argo.Healthy, &out.Argo.Verified, &out.Argo.Why)
	}
	tier, reason := gitOpsTier(out.Flux, out.Argo)
	out.Base.Tier = tier
	out.Base.Reason = reason
	return out
}

// ReadFromWorkload is a ControllerRead from a workload a successful list
// returned (nil when the list found none under the conventional name).
func ReadFromWorkload(kind, ns, name string, ready *bool) ControllerRead {
	switch {
	case ready == nil:
		return ControllerRead{Health: ControllerUnverified, Why: "no " + kind + " " + ns + "/" + name}
	case *ready:
		return ControllerRead{Health: ControllerReady}
	default:
		return ControllerRead{Health: ControllerNotReady}
	}
}

// FluxControllerRef and ArgoControllerRef name the conventional workloads.
func FluxControllerRef() (ns, name string) { return fluxNamespace, fluxKustomizeController }
func ArgoControllerRef() (ns, name string) { return argoNamespace, argoAppController }
