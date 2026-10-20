package appbridge

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Coverage S11b §6: one egress policy for the DRA kinds, at every path that
// sends cluster content to a model (Explain, chat get/list and the CLI
// snapshot, Suggest-fix), as S10 does for Karpenter. Never sent: CEL
// selectors, opaque configs (spec and allocation), device data,
// networkData and condition messages, slice attributes and capacities,
// taint keys and values.

// draResources are the five DRA kinds by group/resource.
var draResources = map[string]bool{
	"resource.k8s.io/resourceclaims": true, "resource.k8s.io/resourceclaimtemplates": true, "resource.k8s.io/resourceslices": true,
	"resource.k8s.io/deviceclasses": true, "resource.k8s.io/devicetaintrules": true,
}

// draKinds are the same kinds by name, for evidence that carries a kind but
// no group (related objects, events).
var draKinds = map[string]bool{"ResourceClaim": true, "ResourceClaimTemplate": true, "ResourceSlice": true, "DeviceClass": true, "DeviceTaintRule": true}

// draOmitted is the marker a sanitized manifest carries where a never-sent
// field was, so the model sees that it exists and never its content.
const draOmitted = "<omitted by Klyx>"

// draChatSpec is a DRA object's chat projection: the fields the policy
// allows, by kind.
func draChatSpec(p *projector, resource string, u *unstructured.Unstructured) (spec, status any) {
	switch resource {
	case "resourceclaims", "resourceclaimtemplates":
		var c dra.Claim
		if resource == "resourceclaims" {
			c = dra.ProjectClaim(u)
		} else {
			inner := &unstructured.Unstructured{Object: map[string]any{"metadata": u.Object["metadata"], "spec": nestedMap(u.Object, "spec", "spec")}}
			c = dra.ProjectClaim(inner)
		}
		reqs := []any{}
		for _, r := range c.Requests {
			reqs = append(reqs, map[string]any{"name": p.str(r.Name, chatStringMax), "deviceClassName": p.str(r.Class, chatStringMax),
				"allocationMode": r.Mode, "count": r.Count, "adminAccess": r.AdminAccess, "subrequestClasses": r.Subclasses})
		}
		p.drop(".spec (selectors' CEL, configs and tolerations)")
		if resource == "resourceclaimtemplates" {
			return map[string]any{"requests": reqs}, nil
		}
		results := []any{}
		for _, x := range c.Results {
			results = append(results, map[string]any{"request": x.Request, "driver": x.Driver, "pool": x.Pool, "device": x.Device, "adminAccess": x.AdminAccess})
		}
		devices := []any{}
		for _, d := range c.Devices {
			conds := []any{}
			for _, cd := range d.Conditions {
				conds = append(conds, map[string]any{"type": cd.Type, "status": cd.Status, "reason": tokenOrWithheld(cd.Reason)})
			}
			devices = append(devices, map[string]any{"driver": d.Driver, "pool": d.Pool, "device": d.Device, "conditions": conds})
		}
		p.drop(".status.devices (data, networkData and condition messages)")
		p.drop(".status.allocation.devices.config")
		return map[string]any{"requests": reqs}, map[string]any{"allocated": c.Allocated, "results": results, "reservedForCount": c.ReservedCount, "devices": devices}
	case "resourceslices":
		s := dra.ProjectSlice(u)
		devs := []any{}
		for _, d := range s.Devices {
			devs = append(devs, map[string]any{"name": d.Name, "nodeName": d.NodeName, "allNodes": d.AllNodes, "taintEffects": d.TaintEffects})
		}
		sets := []any{}
		for _, cs := range s.CounterSets {
			sets = append(sets, map[string]any{"name": cs.Name, "counters": cs.Counters})
		}
		p.drop(".spec.devices (attributes, capacities, taint keys and values)")
		return map[string]any{"driver": s.Driver, "pool": map[string]any{"name": s.Pool, "generation": s.Generation, "resourceSliceCount": s.Count},
			"nodeName": s.NodeName, "allNodes": s.AllNodes, "nodeSelector": s.NodeSelector, "perDeviceNodeSelection": s.PerDevice, "devices": devs, "sharedCounters": sets}, nil
	case "deviceclasses":
		c := dra.ProjectClass(u)
		p.drop(".spec (selectors' CEL and opaque configs)")
		return map[string]any{"selectors": c.Selectors, "configDrivers": c.ConfigDrivers, "extendedResourceName": c.ExtendedResourceName}, nil
	case "devicetaintrules":
		p.drop(".spec (the selector's CEL, the taint's key and value)")
		return map[string]any{"effect": nestedString(u.Object, "spec", "taint", "effect")}, nil
	}
	return nil, nil
}

// tokenOrWithheld keeps a reason only when it is a machine token.
func tokenOrWithheld(r string) string {
	if r == "" || len(r) > 64 {
		return ""
	}
	for _, c := range r {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return "reason withheld"
		}
	}
	return r
}

// draManifest sanitizes a DRA manifest for Suggest-fix structurally:
// status removed, and every never-sent field replaced by the marker. It
// reports false for a ResourceSlice, which gets no manifest at all: it is
// the driver's, never Git's.
func draManifest(kind string, obj map[string]any) bool {
	if kind == "ResourceSlice" {
		return false
	}
	delete(obj, "status")
	spec, _ := obj["spec"].(map[string]any)
	if kind == "ResourceClaimTemplate" {
		spec, _ = spec["spec"].(map[string]any)
	}
	if spec == nil {
		return true
	}
	markSelectors := func(list any) {
		items, _ := list.([]any)
		for _, x := range items {
			if m, ok := x.(map[string]any); ok {
				if _, ok := m["cel"]; ok {
					m["cel"] = draOmitted
				}
			}
		}
	}
	markConfig := func(m map[string]any) {
		items, _ := m["config"].([]any)
		for _, x := range items {
			if c, ok := x.(map[string]any); ok {
				if o, ok := c["opaque"].(map[string]any); ok {
					if _, ok := o["parameters"]; ok {
						o["parameters"] = draOmitted
					}
				}
			}
		}
	}
	switch kind {
	case "DeviceClass":
		markSelectors(spec["selectors"])
		markConfig(spec)
	case "ResourceClaim", "ResourceClaimTemplate":
		devices, _ := spec["devices"].(map[string]any)
		if devices != nil {
			reqs, _ := devices["requests"].([]any)
			for _, x := range reqs {
				r, _ := x.(map[string]any)
				if r == nil {
					continue
				}
				if ex, ok := r["exactly"].(map[string]any); ok {
					markSelectors(ex["selectors"])
				}
				subs, _ := r["firstAvailable"].([]any)
				for _, y := range subs {
					if sm, ok := y.(map[string]any); ok {
						markSelectors(sm["selectors"])
					}
				}
			}
			markConfig(devices)
		}
	case "DeviceTaintRule":
		if sel, ok := spec["deviceSelector"].(map[string]any); ok {
			markSelectors(sel["selectors"])
		}
		if t, ok := spec["taint"].(map[string]any); ok {
			for _, k := range []string{"key", "value"} {
				if _, ok := t[k]; ok {
					t[k] = draOmitted
				}
			}
		}
	}
	return true
}

// Explain's lens fields for the DRA parts: states, identities, counts,
// condition types, statuses and reason tokens, and fixed reason keys;
// never a message, a CEL selector or a config.
func init() {
	reading := allow{"kind": nil, "label": nil, "reason": nil, "rule": nil, "note": nil}
	aiEvidenceFields[healthsrc.TaskDRA+"/claims"] = allow{"namespace": nil, "name": nil, "rule": allow{"included": nil, "role": nil, "label": nil}, "reading": reading,
		"deleting": nil, "ownerKind": nil, "podClaimName": nil, "extended": nil,
		"requests":  allow{"name": nil, "class": nil, "mode": nil, "count": nil, "adminAccess": nil, "subclasses": nil},
		"allocated": nil, "results": allow{"request": nil, "driver": nil, "pool": nil, "device": nil, "adminAccess": nil},
		"reservedCount": nil, "devices": allow{"driver": nil, "pool": nil, "device": nil, "conditions": allow{"type": nil, "status": nil, "reason": nil}},
		"consumers": allow{"pod": nil, "row": nil, "kind": nil, "label": nil, "reason": nil, "meaning": nil}, "consumersOmitted": nil}
	aiEvidenceFields[healthsrc.TaskDRA+"/pools"] = allow{"key": nil, "driver": nil, "pool": nil, "generation": nil, "slices": nil,
		"rule": allow{"included": nil, "role": nil, "label": nil}, "reading": reading, "assumed": nil, "unread": nil, "obsolete": nil,
		"views": allow{"node": nil, "agreed": nil, "kind": nil, "variants": allow{"minor": nil, "partitionable": nil, "outcome": nil, "devices": nil, "have": nil, "want": nil}}}
	aiEvidenceFields[healthsrc.TaskDRA+"/classes"] = allow{"name": nil, "selectors": nil, "configDrivers": nil, "extendedResourceName": nil}
}

// draConditions blanks a DRA object's condition messages, keeping type,
// status and the reason token.
func draConditions(conds []conditionView) {
	for i, c := range conds {
		c.Reason, c.Message = tokenOrWithheld(c.Reason), ""
		conds[i] = c
	}
}

func draEvidenceObject(o evidenceObject) evidenceObject {
	if !draKinds[o.Kind] {
		return o
	}
	for i, c := range o.Conditions {
		c.Reason, c.Message = tokenOrWithheld(c.Reason), ""
		o.Conditions[i] = c
	}
	o.Facts = nil
	return o
}

// draEvidence applies the policy to an assembled Explain document: the
// target and every related object of a DRA kind.
func draEvidence(ev explainEvidence) explainEvidence {
	ev.Target = draEvidenceObject(ev.Target)
	for i := range ev.Related {
		ev.Related[i] = draEvidenceObject(ev.Related[i])
	}
	return ev
}

// draBundle applies the policy to Suggest-fix's bundle.
func draBundle(b resourceBundle) resourceBundle {
	view := func(v resourceView) resourceView {
		if !draKinds[v.Kind] {
			return v
		}
		draConditions(v.Conditions)
		v.Facts = nil
		return v
	}
	b.Root = view(b.Root)
	for i := range b.Related {
		b.Related[i] = view(b.Related[i])
	}
	return b
}
