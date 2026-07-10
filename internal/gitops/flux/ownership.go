package flux

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// OwnershipSignals are the write-safety signals a Kustomization carries: the
// objects it OWNS (status.inventory - authoritative, present even when the
// managed objects carry no Flux labels), the namespace it deploys into
// (spec.targetNamespace - a heuristic reach signal only; manifests can target
// namespaces without it), and whether it PRUNES (spec.prune: an object created
// out-of-band inside its reach is deleted on the next reconcile).
type OwnershipSignals struct {
	Namespace       string
	Name            string
	Prune           bool
	TargetNamespace string
	Suspended       bool
	Inventory       []InventoryEntry
}

// ParseOwnershipSignals extracts the ownership signals from a watched
// Kustomization unstructured. Lean by design - this runs per-Kustomization on
// every pre-write ownership check.
func ParseOwnershipSignals(u *unstructured.Unstructured) OwnershipSignals {
	s := OwnershipSignals{Namespace: u.GetNamespace(), Name: u.GetName()}
	s.Prune, _, _ = unstructured.NestedBool(u.Object, "spec", "prune")
	s.TargetNamespace, _, _ = unstructured.NestedString(u.Object, "spec", "targetNamespace")
	s.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
	entries, _, _ := unstructured.NestedSlice(u.Object, "status", "inventory", "entries")
	for _, e := range entries {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := em["id"].(string)
		v, _ := em["v"].(string)
		if ie, ok := parseInventoryID(id, v); ok {
			s.Inventory = append(s.Inventory, ie)
		}
	}
	return s
}

// Owns reports whether the Kustomization's inventory contains the object.
// Group/kind/namespace/name identity; version deliberately ignored (the same
// object at a new served version is still the same object).
func (s OwnershipSignals) Owns(group, kind, ns, name string) bool {
	for _, e := range s.Inventory {
		if e.Group == group && e.Kind == kind && e.Namespace == ns && e.Name == name {
			return true
		}
	}
	return false
}

// ReachesNamespace reports whether the Kustomization plausibly deploys into ns:
// its spec.targetNamespace matches, or its inventory already holds an object
// there. Heuristic in the safe direction - false negatives are possible (a
// Kustomization whose manifests set namespaces inline and which currently owns
// nothing in ns), so callers must phrase a negative as "no managing unit
// DETECTED", never as "safe".
func (s OwnershipSignals) ReachesNamespace(ns string) bool {
	if ns == "" {
		return false
	}
	if s.TargetNamespace == ns {
		return true
	}
	for _, e := range s.Inventory {
		if e.Namespace == ns {
			return true
		}
	}
	return false
}
