package dra

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Class is a projected DeviceClass (S11b §3.3): the number of its
// selectors (never their CEL), its config's driver names, and its
// extended resource name.
type Class struct {
	Name, UID            string
	Selectors            int
	ConfigDrivers        []string
	ExtendedResourceName string
}

// ProjectClass keeps a class's facts; CEL and opaque parameters are never
// read.
func ProjectClass(u *unstructured.Unstructured) Class {
	o := u.Object
	c := Class{Name: u.GetName(), UID: string(u.GetUID()), ExtendedResourceName: str(o, "spec", "extendedResourceName")}
	sels, _, _ := unstructured.NestedSlice(o, "spec", "selectors")
	c.Selectors = len(sels)
	cfg, _, _ := unstructured.NestedSlice(o, "spec", "config")
	seen := map[string]bool{}
	for _, x := range cfg {
		m, _ := x.(map[string]any)
		if d := str(m, "opaque", "driver"); d != "" && !seen[d] {
			seen[d] = true
			c.ConfigDrivers = append(c.ConfigDrivers, d)
		}
	}
	return c
}
