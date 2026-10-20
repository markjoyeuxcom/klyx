package karpenter

import "time"

// LinkState is a NodePool's class link (coverage S10 §3), each kept
// distinct.
type LinkState string

const (
	// LinkUnresolved: no class evidence was supplied, so the link was not
	// read. The pool is judged on its own conditions.
	LinkUnresolved LinkState = ""
	// LinkFound: the class is retained in a live, current model.
	LinkFound LinkState = "found"
	// LinkNotFound: absent from a live, current, uncut read of its kind.
	LinkNotFound LinkState = "not found"
	// LinkKindNotServed: a supported reference whose API is not served.
	LinkKindNotServed LinkState = "kind not served"
	// LinkVersionUnsupported: the kind is served, but not at the pinned
	// version (§2): a stated limit, never a decode of an unread schema.
	LinkVersionUnsupported LinkState = "unsupported version"
	// LinkUnreadable: forbidden or failing, with no last good read.
	LinkUnreadable LinkState = "unreadable"
	// LinkStale: the kind's watch is not current; its last good read is
	// shown with its time, and nothing is claimed current.
	LinkStale LinkState = "stale"
	// LinkCut: the kind's model is capped and the class is not retained, so
	// "not found" is never claimed.
	LinkCut LinkState = "cut"
	// LinkUnsupported: a group/kind no adapter reads (provider not
	// supported).
	LinkUnsupported LinkState = "unsupported"
	// LinkMissingRef: the pool names no class.
	LinkMissingRef LinkState = "missing ref"
)

// KindEvidence is what one class kind's model says, for every link into it.
type KindEvidence struct {
	// Served: the pinned version is served. OtherVersion: the kind is
	// served, at another version only.
	Served, OtherVersion bool
	// Current: the watch is live, so a retained class is the latest the API
	// delivered. Complete: current, uncut and not failed, so absence is
	// provable (S9's HPA evidence, §3).
	Current, Complete bool
	// AsOf is when the kind was last current; zero when it never was.
	AsOf time.Time
	// Classes are the retained classes, by name.
	Classes map[string]*NodeClass
}

// Link is a pool's resolved class link.
type Link struct {
	State LinkState `json:"state"`
	Kind  ClassKind `json:"kind"`
	// Class is the found class, or a stale link's last good one (display
	// only, never a current reading).
	Class *NodeClass `json:"-"`
	AsOf  time.Time  `json:"asOf,omitempty"`
}

// Resolve resolves a nodeClassRef against the class kinds' evidence. A nil
// evidence map is LinkUnresolved for a supported reference.
func Resolve(ref *ClassRef, kinds map[Adapter]KindEvidence) Link {
	if ref == nil || ref.Name == "" {
		return Link{State: LinkMissingRef}
	}
	k, ok := KindOf(ref.Group, ref.Kind)
	if !ok {
		return Link{State: LinkUnsupported}
	}
	l := Link{Kind: k}
	if kinds == nil {
		return l
	}
	ev := kinds[k.Adapter]
	class := ev.Classes[ref.Name]
	switch {
	case !ev.Served && ev.OtherVersion:
		l.State = LinkVersionUnsupported
	case !ev.Served:
		l.State = LinkKindNotServed
	case ev.Current && class != nil:
		l.State, l.Class = LinkFound, class
	case ev.Current && ev.Complete:
		l.State = LinkNotFound
	case ev.Current:
		l.State = LinkCut
	case !ev.AsOf.IsZero():
		l.State, l.Class, l.AsOf = LinkStale, class, ev.AsOf
	default:
		l.State = LinkUnreadable
	}
	return l
}
