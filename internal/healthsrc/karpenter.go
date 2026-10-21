package healthsrc

import (
	"sort"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/observe"
)

// Karpenter's kinds, parts and facts (coverage S10 §2). The kinds are named
// by their resources.
const (
	KindEC2NodeClasses  = "ec2nodeclasses"
	KindAKSNodeClasses  = "aksnodeclasses"
	KindAutoNodeClasses = "nodeclasses"

	// FactKarpenter carries §5's readings per class kind (KarpenterFacts).
	FactKarpenter = "karpenter"
)

// karpenterClassKinds are the class kinds, in ClassKinds order.
var karpenterClassKinds = []string{KindEC2NodeClasses, KindAKSNodeClasses, KindAutoNodeClasses}

// KarpenterKindFact is one class kind's reading from the detector.
type KarpenterKindFact struct {
	Handler      karpenter.Handler
	Reading      string
	OtherVersion bool
}

// KarpenterFacts is what the NodePool rule reads of the controllers (§5).
// Read is false until the detector has run: every handler is then unproven.
type KarpenterFacts struct {
	Read              bool
	InventoryComplete bool
	Kinds             map[karpenter.Adapter]KarpenterKindFact
}

// KarpenterFactsOf maps the detector's capability onto the rules' facts.
func KarpenterFactsOf(k capability.KarpenterCapability) KarpenterFacts {
	if !k.Present {
		return KarpenterFacts{}
	}
	f := KarpenterFacts{Read: true, InventoryComplete: k.Inventory == capability.InventoryComplete, Kinds: map[karpenter.Adapter]KarpenterKindFact{}}
	for _, ck := range karpenter.ClassKinds {
		h, text := k.Reading(ck)
		kf := KarpenterKindFact{Handler: h, Reading: text}
		for _, kk := range k.Kinds {
			if kk.Kind.Adapter == ck.Adapter {
				kf.OtherVersion = kk.OtherVersion
			}
		}
		f.Kinds[ck.Adapter] = kf
	}
	return f
}

func karpenterFacts(c observe.Capture) KarpenterFacts {
	f, _ := c.Facts[FactKarpenter].(KarpenterFacts)
	return f
}

// karpenterSpecs are Karpenter's kinds and task: the core kinds and the
// three class kinds, each pinned to one version and projected (§7).
func karpenterSpecs(d NativeDeps) ([]observe.KindSpec, observe.DomainSpec) {
	kind := func(resource, group, version string, budget, maxObject int) observe.KindSpec {
		k := oneVersionKind(d, resource, group, version, budget, karpenter.Project(resource), maxObject, karpenter.SizeOf)
		k.Scope = "cluster-scoped"
		return k
	}
	kinds := []observe.KindSpec{
		kind(KindNodePools, karpenter.Group, karpenter.Version, karpenter.BudgetPools, karpenter.MaxPoolObject),
		kind(KindNodeClaims, karpenter.Group, karpenter.Version, karpenter.BudgetClaims, karpenter.MaxClaimObject),
	}
	for i, ck := range karpenter.ClassKinds {
		kinds = append(kinds, kind(karpenterClassKinds[i], ck.Group, ck.Version, karpenter.BudgetClasses, karpenter.MaxClassObject))
	}
	c := d.Cluster
	poolDeps := append([]string{KindNodeClaims}, karpenterClassKinds...)
	return kinds, observe.DomainSpec{ID: TaskKarpenter, Parts: []observe.PartSpec{
		{ID: "nodeclaims", Entity: "NodeClaims", Scope: "cluster-scoped", Classifier: "native",
			Counted: []string{KindNodeClaims}, Evaluate: evalNodeClaims(c), Ledger: true},
		{ID: "nodepools", Entity: "NodePools", Scope: "cluster-scoped", Classifier: "native",
			Counted: []string{KindNodePools}, Deps: poolDeps, Facts: []string{FactKarpenter}, Evaluate: evalNodePools(c), Ledger: true},
		{ID: "nodeclasses", Entity: "NodeClasses", Scope: "cluster-scoped", Classifier: "evidence",
			Counted: karpenterClassKinds, Deps: []string{KindNodePools}, Evaluate: evalNodeClasses},
	}}
}

func karpenterRef(m karpenter.Meta, group, version, resource string) refInput {
	return refInput{gvr: group + "/" + version + "/" + resource, name: m.Name, uid: m.UID, generation: m.Generation, source: resource}
}

func evalNodeClaims(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[karpenter.NodeClaim]
		for _, x := range c.Objects[KindNodeClaims] {
			p, ok := x.(*karpenter.NodeClaim)
			if !ok {
				continue
			}
			cl := p.At(c.Now)
			it := nativeItem{karpenter.ClaimRule(cl), karpenterRef(cl.Meta, karpenter.Group, karpenter.Version, KindNodeClaims)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, cl))
		}
		r := nativeResult(c.RoleCap, cluster, TaskKarpenter, "not ready or appear stuck", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

// KarpenterPoolRow is a NodePool with its findings and its class link, from
// this capture (§3, §8).
type KarpenterPoolRow struct {
	Pool     karpenter.NodePool
	Findings []karpenter.Finding
	Link     karpenter.Link
	// Class is the linked class's reading: current when the link is found,
	// the last good one when it is stale; nil otherwise.
	Class *karpenter.ClassReading
	// Handler is §5's reading for the class kind ("" for no supported kind).
	Handler string
}

// classEvidence is each class kind's model, for the links (§3). A kind
// confirmed absent is not served; one whose read failed while the detector
// saw it served only at another version is an unsupported version.
func classEvidence(c observe.Capture, f KarpenterFacts) map[karpenter.Adapter]karpenter.KindEvidence {
	out := map[karpenter.Adapter]karpenter.KindEvidence{}
	for i, ck := range karpenter.ClassKinds {
		kind := karpenterClassKinds[i]
		ev := karpenter.KindEvidence{Current: c.Current[kind], Complete: c.Complete[kind] && c.Current[kind], AsOf: c.AsOf[kind]}
		switch {
		case !ev.Current && f.Kinds[ck.Adapter].OtherVersion:
			ev.OtherVersion = true
		case c.Complete[kind] && !c.Current[kind]:
		default:
			ev.Served = true
		}
		ev.Classes = map[string]*karpenter.NodeClass{}
		for _, x := range c.Objects[kind] {
			if cl, ok := x.(*karpenter.NodeClass); ok {
				ev.Classes[cl.Meta.Name] = cl
			}
		}
		out[ck.Adapter] = ev
	}
	return out
}

func evalNodePools(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		perPool := map[string]int{}
		for _, x := range c.Objects[KindNodeClaims] {
			if cl, ok := x.(*karpenter.NodeClaim); ok {
				perPool[cl.Pool]++
			}
		}
		facts := karpenterFacts(c)
		kinds := classEvidence(c, facts)
		var items []nativeItem
		var rows []Row[KarpenterPoolRow]
		for _, x := range c.Objects[KindNodePools] {
			p, ok := x.(*karpenter.NodePool)
			if !ok {
				continue
			}
			pool := *p
			pool.Nodes = perPool[pool.Meta.Name]
			in := karpenter.PoolInputs{Link: karpenter.Resolve(pool.Ref, kinds)}
			row := KarpenterPoolRow{Pool: pool, Link: in.Link}
			switch kf, known := facts.Kinds[in.Link.Kind.Adapter]; {
			case in.Link.Kind.Kind != "" && known:
				in.Handler, row.Handler = kf.Handler, kf.Reading
			case in.Link.State == karpenter.LinkUnsupported:
				in.Handler.NoneSupports = facts.Read && facts.InventoryComplete
			}
			v := karpenter.PoolRule(pool, in)
			row.Findings = v.Findings
			if cl := in.Link.Class; cl != nil {
				r := cl.Read()
				row.Class = &r
			}
			it := nativeItem{v.Eval, karpenterRef(pool.Meta, karpenter.Group, karpenter.Version, KindNodePools)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, row))
		}
		r := nativeResult(c.RoleCap, cluster, TaskKarpenter, "not ready or failing to register", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

// KarpenterClassRow is one class: inventory, never counted (§2, §4). The
// pools carry the verdict; the row carries the class's own reading and its
// users.
type KarpenterClassRow struct {
	Class   *karpenter.NodeClass
	Kind    karpenter.ClassKind
	Reading karpenter.ClassReading
	// Users are the NodePools whose reference resolves to it; complete only
	// when the pool kind is live, current and uncut.
	Users         []string
	UsersComplete bool
}

// FactOnly: a class row is always a fact: no NativeEval, never a verdict.
func (KarpenterClassRow) FactOnly() bool { return true }

func evalNodeClasses(c observe.Capture) observe.PartResult {
	users := map[string][]string{}
	for _, x := range c.Objects[KindNodePools] {
		if p, ok := x.(*karpenter.NodePool); ok && p.Ref != nil {
			key := p.Ref.Group + "/" + p.Ref.Kind + "/" + p.Ref.Name
			users[key] = append(users[key], p.Meta.Name)
		}
	}
	complete := c.Complete[KindNodePools] && c.Current[KindNodePools]
	var rows []Row[KarpenterClassRow]
	for i, ck := range karpenter.ClassKinds {
		kind := karpenterClassKinds[i]
		for _, x := range c.Objects[kind] {
			cl, ok := x.(*karpenter.NodeClass)
			if !ok {
				continue
			}
			u := append([]string(nil), users[ck.Group+"/"+ck.Kind+"/"+cl.Meta.Name]...)
			sort.Strings(u)
			rows = append(rows, Row[KarpenterClassRow]{Ref: karpenterRef(cl.Meta, ck.Group, ck.Version, kind).rowRef(),
				Data: KarpenterClassRow{Class: cl, Kind: ck, Reading: cl.Read(), Users: u, UsersComplete: complete}})
		}
	}
	n := len(rows)
	res := observe.PartResult{Evidence: &n}
	if c.Rows {
		res.Rows = rowSet(rows)
	}
	return res
}
