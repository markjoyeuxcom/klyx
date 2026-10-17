package capability

import (
	"context"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Flux Operator detection (coverage S1a §2, revision 2). Operators are
// found by their label, never by name: the Helm chart names the Deployment
// after the release. The runtime namespace is read from the manager
// container's RUNTIME_NAMESPACE, never from where the Deployment runs.
const (
	fluxOperatorGroup    = "fluxcd.controlplane.io"
	fluxOperatorSelector = "app.kubernetes.io/name=flux-operator"
	fluxOperatorLimit    = 10
	// fluxOperatorDefaultNS is the operator's RUNTIME_NAMESPACE default.
	fluxOperatorDefaultNS = "flux-system"
)

// FluxOperatorInfo is what detection established about the operator.
type FluxOperatorInfo struct {
	// Present: the operator's API group is served.
	Present bool
	// Verified: the labelled Deployments were listed. Complete: in full -
	// a list that returned a continue token is not, and absence is never
	// declared from it.
	Verified, Complete bool
	Why                string
	Operators          []FluxOperator
}

// FluxOperator is one labelled Deployment.
type FluxOperator struct {
	Namespace, Name string
	// RuntimeNamespace is resolved from the manager container: a
	// metadata.namespace field ref gives the Deployment's namespace, a
	// literal gives itself, an unset variable gives flux-system. Empty
	// when another source makes it unresolvable.
	RuntimeNamespace string
	// WebOnly: run with --web-server-only, which runs no reconcilers.
	WebOnly bool
	Ready   bool
}

// Binding is how a namespace's FluxInstance relates to the operators.
type Binding string

const (
	BindingBound      Binding = "bound"      // exactly one reconciling operator
	BindingOrphan     Binding = "orphan"     // none, from a complete scan
	BindingAmbiguous  Binding = "ambiguous"  // several reconciling operators
	BindingUnverified Binding = "unverified" // the scan could not tell
)

// Bind classifies the FluxInstance namespace ns.
func (i FluxOperatorInfo) Bind(ns string) Binding {
	if !i.Verified {
		return BindingUnverified
	}
	n, unresolved := 0, false
	for _, op := range i.Operators {
		switch {
		case op.WebOnly:
		case op.RuntimeNamespace == "":
			unresolved = true
		case op.RuntimeNamespace == ns:
			n++
		}
	}
	switch {
	case n == 1:
		return BindingBound
	case n > 1:
		return BindingAmbiguous
	case unresolved || !i.Complete:
		return BindingUnverified
	}
	return BindingOrphan
}

// RuntimeNamespaces are the namespaces reconciling operators run for, each
// once, in the order found.
func (i FluxOperatorInfo) RuntimeNamespaces() []string {
	var out []string
	seen := map[string]bool{}
	for _, op := range i.Operators {
		if op.WebOnly || op.RuntimeNamespace == "" || seen[op.RuntimeNamespace] {
			continue
		}
		seen[op.RuntimeNamespace] = true
		out = append(out, op.RuntimeNamespace)
	}
	return out
}

// Issue names the operator's own problem for the Flux task, "" when none.
func (i FluxOperatorInfo) Issue() string {
	switch {
	case !i.Present:
		return ""
	case !i.Verified:
		return "Flux Operator installed but its controller is unverified (" + i.Why + ")"
	}
	ready, reconcilers := false, 0
	for _, ns := range i.RuntimeNamespaces() {
		if i.Bind(ns) == BindingAmbiguous {
			return "several Flux Operators reconcile " + ns
		}
	}
	for _, op := range i.Operators {
		if !op.WebOnly {
			reconcilers++
			ready = ready || op.Ready
		}
	}
	switch {
	case reconcilers == 0 && i.Complete:
		return "Flux Operator CRDs installed but no operator runs its reconcilers"
	case reconcilers > 0 && !ready:
		return "Flux Operator installed but not ready"
	}
	return ""
}

func (d *Detector) detectFluxOperator(ctx context.Context, served map[string]bool) FluxOperatorInfo {
	out := FluxOperatorInfo{Present: served[fluxOperatorGroup]}
	if !out.Present {
		return out
	}
	deps, err := d.cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: fluxOperatorSelector, Limit: fluxOperatorLimit})
	if err != nil {
		out.Why = "could not list Deployments: " + err.Error()
		return out
	}
	out.Verified, out.Complete = true, deps.Continue == ""
	for i := range deps.Items {
		out.Operators = append(out.Operators, readFluxOperator(&deps.Items[i]))
	}
	return out
}

func readFluxOperator(dep *appsv1.Deployment) FluxOperator {
	op := FluxOperator{Namespace: dep.Namespace, Name: dep.Name, Ready: DeploymentReady(dep)}
	c := managerContainer(dep.Spec.Template.Spec.Containers)
	if c == nil {
		return op // unresolvable
	}
	for _, a := range append(append([]string{}, c.Command...), c.Args...) {
		if a == "--web-server-only" || a == "--web-server-only=true" {
			op.WebOnly = true
		}
	}
	op.RuntimeNamespace = fluxOperatorDefaultNS
	for _, e := range c.Env {
		if e.Name != "RUNTIME_NAMESPACE" {
			continue
		}
		switch {
		case e.ValueFrom == nil:
			op.RuntimeNamespace = e.Value
			if e.Value == "" {
				op.RuntimeNamespace = fluxOperatorDefaultNS
			}
		case e.ValueFrom.FieldRef != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.namespace":
			op.RuntimeNamespace = dep.Namespace
		default:
			op.RuntimeNamespace = "" // a ConfigMap, Secret or other source: unverified
		}
	}
	return op
}

// managerContainer is the container named "manager", else the only one.
func managerContainer(cs []corev1.Container) *corev1.Container {
	for i := range cs {
		if strings.EqualFold(cs[i].Name, "manager") {
			return &cs[i]
		}
	}
	if len(cs) == 1 {
		return &cs[0]
	}
	return nil
}
