package appbridge

import (
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/scrub"
)

// Coverage S10 §8, the identifier boundary. Karpenter's condition messages
// never cross to AI or chat, for any Karpenter kind: providers write
// identifiers into them (an Azure subnet's ARM ID, a bare instance-profile
// name) and core copies a class's text into every pool that uses it. What
// crosses is a condition's type, status and reason token, and Klyx's fixed
// sentence for that reason. The same holds for an event about a Karpenter
// object (the controller's launch errors carry the same text), and the
// generic detail facts are not sent for these kinds: the lens fields carry
// Klyx's own. The local inspector keeps the native text.

// karpenterResources are the Karpenter kinds by "group/resource".
var karpenterResources = map[string]bool{
	"karpenter.sh/nodepools": true, "karpenter.sh/nodeclaims": true,
	"karpenter.k8s.aws/ec2nodeclasses": true, "karpenter.azure.com/aksnodeclasses": true, "eks.amazonaws.com/nodeclasses": true,
}

// karpenterClassResources are the class kinds: their spec holds the role,
// the instance profile and selector IDs, so no manifest of one is sent.
var karpenterClassResources = map[string]bool{
	"karpenter.k8s.aws/ec2nodeclasses": true, "karpenter.azure.com/aksnodeclasses": true, "eks.amazonaws.com/nodeclasses": true,
}

// karpenterKinds are the Karpenter kinds by name, for evidence that carries
// a kind but no group (related objects, events). Another CRD of the same
// kind name is treated the same way: withholding more is safe.
var karpenterKinds = map[string]bool{"NodePool": true, "NodeClaim": true, "EC2NodeClass": true, "AKSNodeClass": true, "NodeClass": true}

const karpenterWithheld = "reason withheld"

// karpenterReason is a reason token, or the withheld marker.
func karpenterReason(r string) string {
	if t, ok := karpenter.ReasonToken(r); ok {
		return t
	}
	return karpenterWithheld
}

// karpenterSentence is Klyx's sentence for a reason, in place of a message.
func karpenterSentence(reason string) string {
	if reason == karpenterWithheld {
		return karpenter.Unworded
	}
	return karpenter.Sentence(reason)
}

func karpenterEvidenceObject(o evidenceObject) evidenceObject {
	if !karpenterKinds[o.Kind] {
		return o
	}
	for i, c := range o.Conditions {
		c.Reason = karpenterReason(c.Reason)
		c.Message = karpenterSentence(c.Reason)
		o.Conditions[i] = c
	}
	o.Facts = nil
	return o
}

// karpenterEvidence applies the boundary to an assembled Explain document:
// the target and every related object of a Karpenter kind, and the target's
// events when it is one.
func karpenterEvidence(ev explainEvidence) explainEvidence {
	if karpenterKinds[ev.Target.Kind] {
		ev.Target = karpenterEvidenceObject(ev.Target)
		for i := range ev.WarningEvents {
			ev.WarningEvents[i].Reason = karpenterReason(ev.WarningEvents[i].Reason)
			ev.WarningEvents[i].Message = ""
		}
	}
	for i := range ev.Related {
		ev.Related[i] = karpenterEvidenceObject(ev.Related[i])
	}
	return ev
}

// karpenterBundle applies the boundary to Suggest-fix's bundle.
func karpenterBundle(b resourceBundle) resourceBundle {
	view := func(v resourceView) resourceView {
		if !karpenterKinds[v.Kind] {
			return v
		}
		for i, c := range v.Conditions {
			c.Reason = karpenterReason(c.Reason)
			c.Message = karpenterSentence(c.Reason)
			v.Conditions[i] = c
		}
		v.Facts = nil
		return v
	}
	b.Root = view(b.Root)
	for i := range b.Related {
		b.Related[i] = view(b.Related[i])
	}
	return b
}

// karpenterManifest is a pool's or claim's manifest for Suggest-fix: no
// status (it holds the copied condition text), and identifiers redacted
// from what remains.
func karpenterManifest(obj map[string]any) {
	delete(obj, "status")
}

// karpenterText redacts identifiers from a manifest's text.
func karpenterText(s string) (string, int) { return scrub.Identifiers(s) }

// Explain's lens fields for the Karpenter parts (§8): names, the
// reference, the link, conditions as type, status and reason, Klyx's
// findings and readings, and counts; never a message, a native text, the
// role or the instance profile.
func init() {
	cond := allow{"type": nil, "status": nil, "reason": nil, "reasonWithheld": nil, "observedGeneration": nil}
	ref := allow{"group": nil, "kind": nil, "name": nil}
	aiEvidenceFields[healthsrc.TaskKarpenter+"/nodepools"] = allow{"ready": nil, "reason": nil, "weight": nil, "cpuLimit": nil, "cpuUsed": nil,
		"memLimit": nil, "memUsed": nil, "nodes": nil, "nodeClassRef": ref, "nodeClassObservedGeneration": nil,
		"link":       allow{"state": nil, "adapter": nil, "group": nil, "kind": nil, "version": nil, "asOf": nil},
		"class":      allow{"state": nil, "text": nil},
		"handler":    nil,
		"findings":   allow{"role": nil, "label": nil, "text": nil, "note": nil, "unversioned": nil},
		"conditions": cond, "conditionsTotal": nil, "conditionsCut": nil}
	aiEvidenceFields[healthsrc.TaskKarpenter+"/nodeclaims"] = allow{"pool": nil, "nodeName": nil, "ready": nil, "failingType": nil,
		"failingStatus": nil, "failingReason": nil, "failingReasonWithheld": nil, "drifted": nil, "consolidatable": nil, "capacityType": nil,
		"instanceType": nil, "ageSeconds": nil, "nodeClassRef": ref}
	aiEvidenceFields[healthsrc.TaskKarpenter+"/nodeclasses"] = allow{"group": nil, "kind": nil, "adapter": nil, "deleting": nil, "syncing": nil,
		"reading": allow{"state": nil, "text": nil}, "conditions": cond, "conditionsTotal": nil, "conditionsCut": nil, "unknown": nil, "unknownTotal": nil,
		"facts": allow{"subnets": nil, "securityGroups": nil, "amis": nil, "deprecatedAmi": nil, "podSubnets": nil, "kubernetesVersion": nil,
			"images": nil, "imageFamily": nil, "subnetSet": nil},
		"users": nil, "usersComplete": nil}
}
