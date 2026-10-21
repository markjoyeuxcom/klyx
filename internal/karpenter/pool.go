package karpenter

import (
	"time"

	"github.com/moomora/klyx/internal/health"
)

// Finding is one independent finding of the NodePool rule (coverage S10
// §3): the verdict is the worst.
type Finding struct {
	Role  health.Kind `json:"role"`
	Label string      `json:"label"`
	// Text is Klyx's fixed wording: names, condition types, statuses, reason
	// tokens and Klyx's sentences, never a provider's message. Label and
	// Text are all of a finding AI and chat may read (§8).
	Text string `json:"text,omitempty"`
	// Native is the provider's own condition text, for the local inspector
	// only.
	Native string `json:"native,omitempty"`
	// Note: informational (ok), never the headline.
	Note bool `json:"note,omitempty"`
	// Unversioned: the condition reports no observedGeneration, so it is
	// read as reported, never as a proven lag; the inspector says so.
	Unversioned bool `json:"unversioned,omitempty"`
	cond        *health.CondSince
}

// HandlerState is §5's reading of the controllers for one class kind.
type HandlerState string

const (
	// HandlerUnproven: no proven handler, a gap.
	HandlerUnproven HandlerState = ""
	// HandlerReady and HandlerNotReady: an observed controller attributed to
	// the kind by its image.
	HandlerReady    HandlerState = "ready"
	HandlerNotReady HandlerState = "not ready"
	// HandlerManaged: platform evidence (EKS Auto Mode, AKS NAP activity).
	// It is never a ready controller: its health is not observed.
	HandlerManaged HandlerState = "managed"
)

// Handler is what §5 establishes about the controllers for one class kind.
type Handler struct {
	State HandlerState
	// NoneSupports: the controller inventory is complete, every candidate is
	// attributed to a provider, and none supports the kind (§3 rule 1).
	NoneSupports bool
}

// PoolInputs is what the NodePool rule reads beside the pool.
type PoolInputs struct {
	Link    Link
	Handler Handler
}

// PoolVerdict is a pool's verdict and every finding behind it.
type PoolVerdict struct {
	Eval     health.NativeEval
	Findings []Finding
}

// PoolRule judges a NodePool (§3): the worst of independent findings, each
// with its own freshness. A class failure is the pool's headline in Klyx's
// wording of the class's condition, once per pool; the copy Karpenter writes
// into the pool's NodeClassReady, and the Ready it derives from it, are the
// same finding, never a second one.
func PoolRule(p NodePool, in PoolInputs) PoolVerdict {
	var f []Finding
	if p.Conds.Total == 0 {
		f = append(f, notReported(p, in.Handler))
	}
	// owned: the class side has a current reading (found, or a proven
	// absence), which replaces the pool's NodeClassReady copy.
	owned := false
	l := in.Link
	if l.State == LinkFound && l.Class == nil {
		l.State = LinkUnresolved // a found link always carries its class
	}
	switch l.State {
	case LinkMissingRef:
		f = append(f, Finding{Role: health.Fault, Label: "no nodeClassRef", Text: "the NodePool names no NodeClass"})
	case LinkFound:
		owned = true
		f = append(f, classFinding(p, l.Class, in.Handler, l.Kind)...)
	case LinkNotFound:
		owned = true
		f = append(f, Finding{Role: health.Fault, Label: "NodeClass " + l.Kind.Kind + "/" + refName(p.Ref) + " not found",
			Text: "the NodeClass does not exist on the cluster"})
	default:
		f = append(f, linkNote(p.Ref, l))
	}
	gen := p.Meta.Generation
	if c := p.CondNodeClassReady; !owned && c != nil && c.Status == "False" {
		f = append(f, own(c, gen, "NodeClassReady False, as reported", health.Fault))
	}
	if c := p.CondValidation; c != nil && c.Status == "False" {
		f = append(f, own(c, gen, "validation failed", health.Fault))
	}
	f = append(f, registration(p, l)...)
	f = append(f, readyFinding(p)...)
	return PoolVerdict{Eval: poolEval(p, f), Findings: f}
}

func refName(r *ClassRef) string {
	if r == nil {
		return ""
	}
	return r.Name
}

func refText(r *ClassRef) string {
	if r == nil {
		return ""
	}
	return r.Group + "/" + r.Kind
}

// notReported is rule 1: no conditions at all.
func notReported(p NodePool, h Handler) Finding {
	f := Finding{Role: health.Unknown, Label: "not reported", Text: "Karpenter has not reported on this NodePool"}
	switch {
	case p.Ref == nil:
	case h.NoneSupports:
		f.Text = "no running Karpenter supports " + refText(p.Ref) + ", so nothing reconciles this pool"
	default:
		f.Text += "; its class is " + refText(p.Ref)
	}
	return f
}

// classFinding is rule 2: the class, current and found.
func classFinding(p NodePool, c *NodeClass, h Handler, k ClassKind) []Finding {
	name := "NodeClass " + c.Meta.Name
	r := c.Read()
	switch r.State {
	case ClassTerminating:
		return []Finding{{Role: health.Fault, Label: name + " is terminating", Text: "the NodeClass is being deleted, so the NodePool cannot launch nodes"}}
	case ClassSyncing:
		return []Finding{{Role: health.Progressing, Label: name + " syncing", Text: r.Text}}
	case ClassNotReady:
		return []Finding{{Role: health.Fault, Label: name + " not ready", Text: r.Text, Native: r.Native, cond: condOf(r.cond)}}
	case ClassReady:
		if nc := p.CondNodeClassReady; nc != nil && nc.Status == "False" {
			return []Finding{{Role: health.Progressing, Label: name + " is ready; the NodePool's status has not caught up",
				Text: "the NodePool still reports " + Phrase(nc)}}
		}
		return nil
	}
	// Ready Unknown or not reported: progress only with an observed, ready
	// controller attributed to the kind. Platform management is never one.
	switch h.State {
	case HandlerReady:
		return []Finding{{Role: health.Progressing, Label: name + " not yet reconciled", Text: "its " + k.Kind + " controller is ready"}}
	case HandlerManaged:
		return []Finding{{Role: health.Unknown, Label: name + " not reconciled",
			Text: k.Kind + " is managed by the platform, whose controller health is not observed"}}
	case HandlerNotReady:
		return []Finding{{Role: health.Unknown, Label: name + " not reconciled", Text: "the controller attributed to " + k.Kind + " is not ready"}}
	}
	return []Finding{{Role: health.Unknown, Label: name + " not reconciled", Text: "no controller attributed to " + k.Kind + " is established"}}
}

// linkNote is rule 4 and the unsupported provider: the link is not
// established, said beside the pool's own conditions, never a class verdict
// from a gap.
func linkNote(ref *ClassRef, l Link) Finding {
	f := Finding{Role: health.OK, Note: true, Label: "class link not established"}
	k := l.Kind.Kind
	switch l.State {
	case LinkUnresolved:
		f.Label, f.Text = "class link not read", "the NodePool's class link was not read"
	case LinkKindNotServed:
		f.Text = k + " (" + l.Kind.Group + ") is not served on this cluster"
	case LinkVersionUnsupported:
		f.Text = k + " is served only at a version Klyx does not read (it reads " + l.Kind.Version + ")"
	case LinkUnreadable:
		f.Text = k + " could not be read"
	case LinkStale:
		at := l.AsOf.UTC().Format(time.RFC3339)
		f.Label, f.Text = "class link as of "+at, k+" was last read at "+at+"; nothing is claimed current"
	case LinkCut:
		f.Text = "the " + k + " read was capped and " + refName(ref) + " was not retained"
	case LinkUnsupported:
		f.Label, f.Text = "provider not supported", "Klyx reads no "+refText(ref)+" class; the NodePool is judged on its own conditions"
	}
	return f
}

// own is one of the pool's own conditions, counted only for the generation
// it reports (§3 freshness).
func own(c *Cond, gen int64, label string, role health.Kind) Finding {
	if behind(c, gen) {
		return Finding{Role: health.Progressing, Label: c.Type + " syncing",
			Text: c.Type + " was reported for generation " + itoa(c.ObservedGeneration) + "; the NodePool is at generation " + itoa(gen)}
	}
	return Finding{Role: role, Label: label, Text: Phrase(c), Native: Native(c), cond: condOf(c), Unversioned: c.ObservedGeneration == 0}
}

// registration is NodeRegistrationHealthy False with its two fences (§3):
// core resets it when the pool's generation or the class's moves.
func registration(p NodePool, l Link) []Finding {
	c := p.CondRegistration
	if c == nil || c.Status != "False" {
		return nil
	}
	poolKnown := c.ObservedGeneration > 0
	classKnown := p.NodeClassObservedGeneration > 0 && l.State == LinkFound
	switch {
	case poolKnown && c.ObservedGeneration != p.Meta.Generation,
		classKnown && p.NodeClassObservedGeneration != l.Class.Meta.Generation:
		return []Finding{{Role: health.OK, Note: true, Label: "registration failure is historical",
			Text: Phrase(c) + "; last reported before the pool or its NodeClass changed", Native: Native(c)}}
	case poolKnown && classKnown:
		return []Finding{{Role: health.Degraded, Label: "nodes fail to register", Text: Phrase(c), Native: Native(c), cond: condOf(c)}}
	}
	why := "the NodeClass's generation is not established"
	switch {
	case !poolKnown:
		why = "its observedGeneration is not reported"
	case l.State == LinkFound:
		why = "nodeClassObservedGeneration is not reported"
	}
	return []Finding{{Role: health.Unknown, Label: "registration health not established", Text: Phrase(c) + "; " + why, Native: Native(c)}}
}

// readyFinding is the pool's Ready. A Ready False whose False dependents
// already have their findings is the same incident, never a second one.
func readyFinding(p NodePool) []Finding {
	r := p.CondReady
	if r == nil {
		if p.Conds.Total > 0 {
			return []Finding{{Role: health.Unknown, Label: "unknown", Text: "Ready not reported"}}
		}
		return nil
	}
	gen := p.Meta.Generation
	switch r.Status {
	case "True":
		if behind(r, gen) {
			return []Finding{own(r, gen, "", health.OK)}
		}
		return nil
	case "False":
		for _, d := range []*Cond{p.CondValidation, p.CondNodeClassReady} {
			if d != nil && d.Status == "False" {
				return nil
			}
		}
		return []Finding{own(r, gen, "not ready", health.Fault)}
	}
	return []Finding{own(r, gen, "unknown", health.Unknown)}
}

func condOf(c *Cond) *health.CondSince {
	if c == nil {
		return nil
	}
	return health.CondAt(c.Type, c.Status, c.At)
}

func poolEval(p NodePool, f []Finding) health.NativeEval {
	w := -1
	for i := range f {
		if !f[i].Note && (w < 0 || f[i].Role < f[w].Role) {
			w = i
		}
	}
	if w < 0 {
		return health.NativeEval{Role: health.OK, Label: "ready", Cond: condOf(p.CondReady)}
	}
	ev := health.NativeEval{Role: f[w].Role, Label: f[w].Label, Included: f[w].Role.Attention(), Cond: f[w].cond}
	text := f[w].Text
	if text == "" {
		text = f[w].Label
	}
	ev.Reason = health.CapReason(f[w].Label, text)
	return ev
}
