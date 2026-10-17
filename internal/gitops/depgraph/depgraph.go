// Package depgraph builds the GitOps dependency graph (The Line slice 8b,
// parent §5.1, slice 8 note §3): from a selected Kustomization or
// HelmRelease, its bound source and its dependsOn, followed recursively,
// each node one fresh read. Edges are typed (source, dependsOn) and marked
// a reported blocker only where the downstream controller reports one.
// Evidence that stops is a stub naming what happened. Supply facts and
// change proximity are stated without causality; "root cause" is never
// claimed. Pure: reads go through a Reader.
package depgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/lastwrite"
	"github.com/moomora/klyx/internal/line"
)

// The walk's bounds (parent §5.1).
const (
	MaxDepth = 6
	MaxNodes = 24
	MaxReads = 24
	// ProximityWindow: a main-resource update this long before a failing
	// node's Ready=False condition time is placed beside it.
	ProximityWindow = 10 * time.Minute
)

const (
	GroupKustomize = "kustomize.toolkit.fluxcd.io"
	GroupHelm      = "helm.toolkit.fluxcd.io"
	GroupSource    = "source.toolkit.fluxcd.io"
	GroupESO       = "external-secrets.io"
	GroupGenerator = "source.extensions.fluxcd.io"
)

// Ref names one object by group, kind, namespace and name.
type Ref struct{ Group, Kind, Namespace, Name string }

func (r Ref) ID() string { return r.Kind + "/" + r.Namespace + "/" + r.Name }

// Reader reads one object whole, or (Secrets) its metadata only. A missing
// object is an error apierrors.IsNotFound recognises.
type Reader interface {
	Get(ctx context.Context, ref Ref) (*unstructured.Unstructured, error)
	GetMeta(ctx context.Context, ref Ref) (*metav1.PartialObjectMetadata, error)
}

type EdgeType string

const (
	EdgeSource    EdgeType = "source"
	EdgeDependsOn EdgeType = "dependsOn"
	// EdgeProducer leads from an ExternalArtifact to the ArtifactGenerator
	// its sourceRef names (coverage S1c §2), qualified by Edge.Producer.
	EdgeProducer EdgeType = "producer"
)

// Producer evidence on an EdgeProducer edge (S1c §2): the artifact's
// recorded generator UID is the named generator's (verified), it records
// none (named), or another (disagree: drawn, never attributed). A stub
// target is its stub's word.
const (
	ProducerVerified = "verified"
	ProducerNamed    = "named"
	ProducerDisagree = "disagree"
)

// Edge leads from a node to what it declares it needs. Reported: the
// downstream node's controller reports this edge is what blocks it. Cycle:
// the edge leads back to a node on its own path - cut here, and named.
type Edge struct {
	From, To string
	Type     EdgeType
	Reported bool
	Cycle    bool
	Producer string // an EdgeProducer edge's evidence
}

// Stub kinds: a node whose evidence stops. Every kind but NotFound is
// incomplete evidence and is named in the graph's header. NotFound,
// unreadable and replaced stand for the object itself (its ID, read once).
// Whatever a budget, the depth or a cancellation stops is summed up in one
// summary node for the whole walk, so the graph holds at most MaxNodes
// nodes and that summary. A cycle is an edge, not a node.
type Stub string

const (
	StubDepth      Stub = "depth"
	StubNodes      Stub = "node limit"
	StubReads      Stub = "read limit"
	StubUnreadable Stub = "unreadable"
	StubReplaced   Stub = "replaced"
	StubCancelled  Stub = "cancelled"
	StubNotFound   Stub = "not found" // a read that answered: the object does not exist
)

// Node is one object of the graph, or a stub where its evidence stops.
type Node struct {
	ID     string
	Ref    Ref
	UID    string
	ReadAt time.Time
	Stub   Stub
	Note   string // the stub's words, or the read's error

	// The Ready condition as read, qualified by its own observedGeneration.
	Ready               string // True | False | Unknown | "" (none)
	Reason, Message     string
	ConditionTime       time.Time // lastTransitionTime
	ConditionGeneration int64     // the condition's observedGeneration; 0 when it carries none
	Generation          int64
	ObservedGeneration  int64
	Stale               bool // status.observedGeneration present and behind metadata.generation
	Suspended           bool
	// A source's readiness as helm-controller judges it: no artifact, or
	// Stalled=True, is not ready whatever Ready says.
	HasArtifact bool
	Stalled     bool
	ObservedSet bool // status.observedGeneration is recorded

	Failing         bool // Ready=False and not suspended
	UpstreamFailure bool // failing, and not the selected node
	ChainEnd        bool // the last node of a chain of reported blockers

	Writes    lastwrite.Writes
	Supply    []Supply
	Inventory *Inventory
	Git       *GitFacts

	// An ExternalArtifact's producer evidence (S1c §2): the generator UID
	// it records, and a sourceRef of another kind - shown as declared,
	// never followed or matched to a same-named generator.
	GeneratorUID     string
	DeclaredProducer *Ref
}

// Supply is a credential a node reads, followed to what supplies it.
type Supply struct {
	Field     string // spec.secretRef | spec.decryption.secretRef
	Secret    Ref
	SecretUID string
	ReadAt    time.Time
	Error     string // the Secret could not be read (or the read limit stopped it)
	NotFound  bool
	Writes    lastwrite.Writes
	Supplier  *Supplier // nil: no supplier recorded
	// SupplierNote says why none is named when the Secret's owner reference
	// could not be confirmed.
	SupplierNote string
}

// Supplier is the ExternalSecret a Secret's controller ownerReference names,
// confirmed by reading it with that UID.
type Supplier struct {
	Ref             Ref
	UID             string
	ReadAt          time.Time
	Ready           string
	Reason, Message string
	Writes          lastwrite.Writes
}

// Inventory is a Kustomization's applied inventory: it never says the
// workloads run that revision.
type Inventory struct {
	LastApplied string
	Entries     int
	Workloads   int
}

// GitFacts are what Git's "not here yet" needs, as the cluster states them.
type GitFacts struct {
	URL, RefKind, RefName string
	Applied               string // the full sha
	Path                  string
}

// Pair places a main-resource update beside a failing node's condition time.
type Pair struct {
	Failing       string // the failing node's ID
	ConditionTime time.Time
	Object        string // "Kustomization/ns/name", "Secret/ns/name", "ExternalSecret/ns/name"
	Write         lastwrite.Record
}

// Graph is the walk's result.
type Graph struct {
	Start      string
	Nodes      []Node
	Edges      []Edge
	Incomplete []string
	Proximity  []Pair
	Reads      int
}

type builder struct {
	ctx    context.Context
	r      Reader
	now    func() time.Time
	g      Graph
	idx    map[string]int
	onPath map[string]bool
	reads  int
	stubs  int
	// nodes counts the objects read (MaxNodes bounds it); the one summary
	// stands for everything not followed, by reason
	nodes     int
	summaryID string
	omitted   map[Stub]int
	omitOrder []Stub
	// what Git's facts need, kept from the reads: a GitRepository's URL and
	// ref, a Kustomization's spec.path
	sourceFacts map[string]flux.Source
	paths       map[string]string
	// every read once: a Secret's metadata and an ExternalSecret's read are
	// shared by the nodes that name them
	secrets map[string]Supply
	ess     map[string]esRead
}

type esRead struct {
	u   *unstructured.Unstructured
	err error
	at  time.Time
}

// Build walks from start. startUID pins the selection: a different UID read
// for it is "replaced".
func Build(ctx context.Context, r Reader, start Ref, startUID string, now func() time.Time) Graph {
	b := &builder{ctx: ctx, r: r, now: now, idx: map[string]int{}, onPath: map[string]bool{}, sourceFacts: map[string]flux.Source{}, paths: map[string]string{},
		secrets: map[string]Supply{}, ess: map[string]esRead{}, omitted: map[Stub]int{}}
	if kind, why := b.exhausted(); kind != "" {
		b.g.Start = b.stub(start, kind, why)
	} else {
		b.g.Start = b.visit(start, startUID, 0)
		if b.g.Start == "" { // cancelled during its own read
			b.g.Start = b.stub(start, StubCancelled, "cancelled")
		}
	}
	b.g.Reads = b.reads
	b.summarize()
	b.producers()
	b.label()
	b.proximity()
	return b.g
}

func (b *builder) incomplete(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	for _, x := range b.g.Incomplete {
		if x == s {
			return
		}
	}
	b.g.Incomplete = append(b.g.Incomplete, s)
}

// stub adds a stub node standing for ref and returns its ID.
func (b *builder) stub(ref Ref, kind Stub, note string) string {
	b.stubs++
	id := fmt.Sprintf("stub:%d:%s", b.stubs, ref.ID())
	b.g.Nodes = append(b.g.Nodes, Node{ID: id, Ref: ref, Stub: kind, Note: note})
	b.idx[id] = len(b.g.Nodes) - 1
	return id
}

func (b *builder) cancelled() bool {
	if b.ctx.Err() != nil {
		b.incomplete("cancelled")
		return true
	}
	return false
}

// read spends one read from the budget; false when none is left.
func (b *builder) spend() bool {
	if b.reads >= MaxReads {
		b.incomplete("read limit: %d reads", MaxReads)
		return false
	}
	b.reads++
	return true
}

// object adds a node standing for the object itself (indexed by its ID, so
// a diamond reuses it, failed reads included).
func (b *builder) object(n Node) string {
	b.g.Nodes = append(b.g.Nodes, n)
	b.idx[n.ID] = len(b.g.Nodes) - 1
	b.nodes++
	return n.ID
}

// exhausted names the budget that stops the walk, or "".
func (b *builder) exhausted() (Stub, string) {
	switch {
	case b.ctx.Err() != nil:
		b.incomplete("cancelled")
		return StubCancelled, "cancelled"
	case b.nodes >= MaxNodes:
		b.incomplete("node limit: %d nodes", MaxNodes)
		return StubNodes, fmt.Sprintf("the %d-node limit", MaxNodes)
	case b.reads >= MaxReads:
		b.incomplete("read limit: %d reads", MaxReads)
		return StubReads, fmt.Sprintf("the %d-read limit", MaxReads)
	}
	return "", ""
}

// visit reads ref once (the caller checked the budget) and follows it.
func (b *builder) visit(ref Ref, wantUID string, depth int) string {
	id := ref.ID()
	b.reads++
	u, err := b.r.Get(b.ctx, ref)
	at := b.now()
	switch {
	case err != nil && b.ctx.Err() != nil:
		b.incomplete("cancelled")
		return "" // the caller sums it up
	case apierrors.IsNotFound(err):
		return b.object(Node{ID: id, Ref: ref, ReadAt: at, Stub: StubNotFound, Note: "not found"})
	case err != nil:
		b.incomplete("unreadable: %s: %v", id, err)
		return b.object(Node{ID: id, Ref: ref, ReadAt: at, Stub: StubUnreadable, Note: err.Error()})
	}
	if wantUID != "" && string(u.GetUID()) != wantUID {
		b.incomplete("replaced: %s now has UID %s, not %s", id, u.GetUID(), wantUID)
		return b.object(Node{ID: id, Ref: ref, ReadAt: at, Stub: StubReplaced, Note: fmt.Sprintf("replaced: UID %s, not %s", u.GetUID(), wantUID)})
	}
	n := nodeOf(ref, u, at)
	if n.Stale {
		b.incomplete("stale: %s status describes generation %d of %d", id, n.ObservedGeneration, n.Generation)
	}
	b.object(n)
	switch ref.Kind {
	case string(flux.GitRepositoryKind):
		b.sourceFacts[id] = flux.ParseSource(u)
	case string(flux.KustomizationKind):
		b.paths[id], _, _ = unstructured.NestedString(u.Object, "spec", "path")
	}
	b.supply(id, u)
	b.follow(id, outgoing(ref, u), depth)
	return id
}

// follow walks a node's targets: a node already read (a diamond) is linked
// at no cost, a cycle is an edge back, cut and named, and whatever the
// depth, a budget or a cancellation stops joins the walk's one summary,
// linked once from this node.
func (b *builder) follow(id string, targets []target, depth int) {
	if len(targets) == 0 {
		return
	}
	b.onPath[id] = true
	defer func() { b.onPath[id] = false }()
	var omittedType EdgeType
	for _, t := range targets {
		tid := t.ref.ID()
		if _, seen := b.idx[tid]; seen {
			if b.onPath[tid] {
				b.incomplete("cycle: %s → %s, cut", id, tid)
				// cut, but still the controller's report when it is one: a
				// reported cycle never ends a chain
				b.g.Edges = append(b.g.Edges, Edge{From: id, To: tid, Type: t.typ, Cycle: true, Reported: b.reported(id, t, tid)})
				continue
			}
			b.g.Edges = append(b.g.Edges, Edge{From: id, To: tid, Type: t.typ, Reported: b.reported(id, t, tid)})
			continue
		}
		var why Stub
		if depth >= MaxDepth {
			b.incomplete("depth: not followed past depth %d", MaxDepth)
			why = StubDepth
		} else {
			why, _ = b.exhausted()
		}
		if why == "" {
			if to := b.visit(t.ref, "", depth+1); to != "" {
				b.g.Edges = append(b.g.Edges, Edge{From: id, To: to, Type: t.typ, Reported: b.reported(id, t, to)})
				continue
			}
			why = StubCancelled
		}
		b.omit(why)
		if omittedType == "" {
			omittedType = t.typ
		}
	}
	if omittedType != "" {
		b.g.Edges = append(b.g.Edges, Edge{From: id, To: b.summary(), Type: omittedType})
	}
}

func (b *builder) omit(why Stub) {
	if b.omitted[why] == 0 {
		b.omitOrder = append(b.omitOrder, why)
	}
	b.omitted[why]++
}

// summary is the walk's one node for everything not followed.
func (b *builder) summary() string {
	if b.summaryID == "" {
		b.summaryID = "stub:not-followed"
		b.g.Nodes = append(b.g.Nodes, Node{ID: b.summaryID})
		b.idx[b.summaryID] = len(b.g.Nodes) - 1
	}
	return b.summaryID
}

// summarize words the summary once the walk is done: how many were not
// followed, and why.
func (b *builder) summarize() {
	if b.summaryID == "" {
		return
	}
	var parts []string
	for _, why := range b.omitOrder {
		n := b.omitted[why]
		switch why {
		case StubDepth:
			parts = append(parts, fmt.Sprintf("%d not followed past depth %d", n, MaxDepth))
		case StubNodes:
			parts = append(parts, fmt.Sprintf("%d not read: the %d-node limit", n, MaxNodes))
		case StubReads:
			parts = append(parts, fmt.Sprintf("%d not read: the %d-read limit", n, MaxReads))
		case StubCancelled:
			parts = append(parts, fmt.Sprintf("%d not read: cancelled", n))
		}
	}
	sn := &b.g.Nodes[b.idx[b.summaryID]]
	sn.Stub, sn.Note = b.omitOrder[0], strings.Join(parts, "; ")
}

type target struct {
	ref Ref
	typ EdgeType
}

// outgoing lists what a node declares it needs: its source first, then
// its dependsOn in spec order.
func outgoing(ref Ref, u *unstructured.Unstructured) []target {
	var out []target
	ns := u.GetNamespace()
	switch ref.Kind {
	case string(flux.KustomizationKind):
		if s, ok := flux.BoundSource(u); ok {
			out = append(out, target{Ref{GroupSource, s.Kind, s.Namespace, s.Name}, EdgeSource})
		}
	case string(flux.HelmReleaseKind):
		if s, ok := helmSource(u); ok {
			out = append(out, target{s, EdgeSource})
		}
	case string(flux.HelmChartKind):
		kind, _, _ := unstructured.NestedString(u.Object, "spec", "sourceRef", "kind")
		name, _, _ := unstructured.NestedString(u.Object, "spec", "sourceRef", "name")
		if kind != "" && name != "" {
			out = append(out, target{Ref{GroupSource, kind, ns, name}, EdgeSource})
		}
	case string(flux.ExternalArtifactKind):
		if g, ok := generatorRef(u); ok {
			out = append(out, target{g, EdgeProducer})
		}
	case string(flux.ArtifactGeneratorKind):
		srcs, _, _ := unstructured.NestedSlice(u.Object, "spec", "sources")
		for _, x := range srcs {
			m, _ := x.(map[string]any)
			kind, _ := m["kind"].(string)
			name, _ := m["name"].(string)
			sns, _ := m["namespace"].(string)
			if sns == "" {
				sns = ns
			}
			if kind != "" && name != "" {
				out = append(out, target{Ref{GroupSource, kind, sns, name}, EdgeSource})
			}
		}
	}
	if ref.Kind == string(flux.KustomizationKind) || ref.Kind == string(flux.HelmReleaseKind) {
		for _, d := range dependsOn(u) {
			out = append(out, target{Ref{ref.Group, ref.Kind, d.Namespace, d.Name}, EdgeDependsOn})
		}
	}
	seen := map[string]bool{}
	uniq := out[:0]
	for _, t := range out {
		k := string(t.typ) + " " + t.ref.ID()
		if !seen[k] {
			seen[k] = true
			uniq = append(uniq, t)
		}
	}
	return uniq
}

// generatorRef is an ExternalArtifact's sourceRef when it names an
// ArtifactGenerator: only that kind in source.extensions.fluxcd.io (or with
// no apiVersion) is a generator (S1c §2, revision 1).
func generatorRef(u *unstructured.Unstructured) (Ref, bool) {
	ref, ok, _ := unstructured.NestedMap(u.Object, "spec", "sourceRef")
	if !ok {
		return Ref{}, false
	}
	kind, _ := ref["kind"].(string)
	name, _ := ref["name"].(string)
	api, _ := ref["apiVersion"].(string)
	ns, _ := ref["namespace"].(string)
	if ns == "" {
		ns = u.GetNamespace()
	}
	if kind != string(flux.ArtifactGeneratorKind) || name == "" || (api != "" && groupOf(api) != GroupGenerator) {
		return Ref{}, false
	}
	return Ref{GroupGenerator, kind, ns, name}, true
}

// producers qualifies every producer edge by its evidence.
func (b *builder) producers() {
	for i := range b.g.Edges {
		e := &b.g.Edges[i]
		if e.Type != EdgeProducer {
			continue
		}
		from, to := b.g.Nodes[b.idx[e.From]], b.g.Nodes[b.idx[e.To]]
		switch {
		case to.Stub != "":
			e.Producer = string(to.Stub)
		case from.GeneratorUID == "":
			e.Producer = ProducerNamed
		case from.GeneratorUID == to.UID:
			e.Producer = ProducerVerified
		default:
			e.Producer = ProducerDisagree
		}
	}
}

// attributable is every node reached from the start without crossing a
// disagreeing producer edge: a generator the artifact does not record is
// drawn, and its failure is never the artifact's upstream failure.
func (b *builder) attributable() map[string]bool {
	seen := map[string]bool{b.g.Start: true}
	queue := []string{b.g.Start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, e := range b.g.Edges {
			if e.From != id || seen[e.To] || (e.Type == EdgeProducer && e.Producer == ProducerDisagree) {
				continue
			}
			seen[e.To] = true
			queue = append(queue, e.To)
		}
	}
	return seen
}

// helmSource is what a HelmRelease's controller reads: its chartRef, else
// the HelmChart its status records for the chart template, else the
// template's own sourceRef.
func helmSource(u *unstructured.Unstructured) (Ref, bool) {
	ns := u.GetNamespace()
	if name, _, _ := unstructured.NestedString(u.Object, "spec", "chartRef", "name"); name != "" {
		kind, _, _ := unstructured.NestedString(u.Object, "spec", "chartRef", "kind")
		cns, _, _ := unstructured.NestedString(u.Object, "spec", "chartRef", "namespace")
		if cns == "" {
			cns = ns
		}
		return Ref{GroupSource, kind, cns, name}, true
	}
	if hc, _, _ := unstructured.NestedString(u.Object, "status", "helmChart"); hc != "" {
		for i := 0; i < len(hc); i++ {
			if hc[i] == '/' && i > 0 && i < len(hc)-1 {
				return Ref{GroupSource, string(flux.HelmChartKind), hc[:i], hc[i+1:]}, true
			}
		}
	}
	if s, ok := flux.BoundSource(u); ok {
		return Ref{GroupSource, s.Kind, s.Namespace, s.Name}, true
	}
	return Ref{}, false
}

func dependsOn(u *unstructured.Unstructured) []flux.DependencyRef {
	return flux.ParseDetail(u).DependsOn
}

// reported: the downstream node's controller reports this edge blocks it,
// in a condition that describes its current spec (a condition from an
// older generation is evidence about that generation, never about today's
// edges). The source's readiness is judged by the controller that reported
// it, as that controller judges it:
//   - dependsOn (both): DependencyNotReady, the message naming this dependency.
//   - a Kustomization's source (kustomize-controller): ArtifactFailed with
//     the source not found, or without an artifact. A source that has one is
//     used as it is, whatever its generation, so the same reason then means
//     a fetch or a missing path, not the source.
//   - a HelmRelease's source (helm-controller): ArtifactFailed with the
//     source not found; or SourceNotReady naming this source while its
//     isReady still fails (generation not reconciled, Stalled, no artifact).
func (b *builder) reported(from string, t target, to string) bool {
	n := b.g.Nodes[b.idx[from]]
	if n.Ready != "False" || n.Suspended || !current(n) {
		return false
	}
	s := b.g.Nodes[b.idx[to]]
	switch t.typ {
	case EdgeDependsOn:
		dep, ok := flux.DependencyNamed(n.Message)
		return n.Reason == flux.ReasonDependencyNotReady && ok && dep == t.ref.Namespace+"/"+t.ref.Name
	case EdgeSource:
		switch {
		case n.Ref.Kind == string(flux.KustomizationKind) && n.Reason == flux.ReasonArtifactFailed:
			return s.Stub == StubNotFound || (s.Stub == "" && !s.HasArtifact)
		case n.Ref.Kind == string(flux.HelmReleaseKind) && n.Reason == flux.ReasonArtifactFailed:
			return s.Stub == StubNotFound
		case n.Ref.Kind == string(flux.HelmReleaseKind) && n.Reason == flux.ReasonSourceNotReady:
			kind, ns, name, ok := flux.SourceNamed(n.Message)
			return ok && kind == t.ref.Kind && ns == t.ref.Namespace && name == t.ref.Name && helmNotReady(s)
		}
	}
	return false
}

// current: the Ready condition describes the object's current generation -
// its own observedGeneration when it carries one, else the status's.
func current(n Node) bool {
	if n.ConditionGeneration > 0 {
		return n.ConditionGeneration == n.Generation
	}
	return !n.Stale
}

// helmNotReady is helm-controller's isReady (v1.6.5), inverted: not found,
// no recorded observedGeneration or one other than the generation,
// Stalled, or no artifact. Ready=False alone is not it.
func helmNotReady(s Node) bool {
	switch s.Stub {
	case StubNotFound:
		return true
	case "":
		return !s.ObservedSet || s.Generation != s.ObservedGeneration || s.Stalled || !s.HasArtifact
	}
	return false
}

func nodeOf(ref Ref, u *unstructured.Unstructured, at time.Time) Node {
	n := Node{ID: ref.ID(), Ref: ref, UID: string(u.GetUID()), ReadAt: at, Generation: u.GetGeneration()}
	observed, found, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	n.ObservedGeneration, n.ObservedSet = observed, found
	n.Stale = found && n.Generation > 0 && n.Generation > observed // a recorded 0 is lag; an absent field is not
	_, n.HasArtifact, _ = unstructured.NestedMap(u.Object, "status", "artifact")
	n.Stalled = conditionTrue(u, "Stalled")
	n.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
	switch ref.Kind {
	case string(flux.ArtifactGeneratorKind):
		// suspended by its own annotation (S1c §4)
		n.Suspended = strings.EqualFold(u.GetAnnotations()["source.extensions.fluxcd.io/reconcile"], "disabled")
	case string(flux.ExternalArtifactKind):
		n.GeneratorUID = u.GetLabels()["source.extensions.fluxcd.io/generator"]
		if ref, ok, _ := unstructured.NestedMap(u.Object, "spec", "sourceRef"); ok {
			if _, isGen := generatorRef(u); !isGen {
				kind, _ := ref["kind"].(string)
				name, _ := ref["name"].(string)
				ns, _ := ref["namespace"].(string)
				if ns == "" {
					ns = u.GetNamespace()
				}
				if kind != "" && name != "" {
					n.DeclaredProducer = &Ref{Group: groupOf(fmt.Sprint(ref["apiVersion"])), Kind: kind, Namespace: ns, Name: name}
				}
			}
		}
	}
	readyCondition(u, &n)
	n.Failing = n.Ready == "False" && !n.Suspended
	n.Writes = lastwrite.Read(u.GetManagedFields())
	if ref.Kind == string(flux.KustomizationKind) {
		d := flux.ParseDetail(u)
		inv := &Inventory{LastApplied: d.AppliedRevision, Entries: len(d.Inventory)}
		for _, e := range d.Inventory {
			if workloadKinds[e.Group+"/"+e.Kind] {
				inv.Workloads++
			}
		}
		n.Inventory = inv
	}
	return n
}

var workloadKinds = map[string]bool{
	"apps/Deployment": true, "apps/StatefulSet": true, "apps/DaemonSet": true,
	"batch/Job": true, "batch/CronJob": true,
}

func conditionTrue(u *unstructured.Unstructured, typ string) bool {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		if cm, ok := c.(map[string]any); ok && cm["type"] == typ {
			return cm["status"] == "True"
		}
	}
	return false
}

func readyCondition(u *unstructured.Unstructured, n *Node) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t != "Ready" {
			continue
		}
		n.Ready, _ = cm["status"].(string)
		n.Reason, _ = cm["reason"].(string)
		n.Message, _ = cm["message"].(string)
		// S1c §6: the kind's message rule, as the lens rows pass it
		n.Message, _ = crd.MessageRule(u.GetKind(), n.Reason, n.Message)
		if lt, ok := cm["lastTransitionTime"].(string); ok {
			n.ConditionTime, _ = time.Parse(time.RFC3339, lt)
		}
		switch og := cm["observedGeneration"].(type) {
		case int64:
			n.ConditionGeneration = og
		case float64:
			n.ConditionGeneration = int64(og)
		}
		return
	}
}

// supply follows a node's credential: a source's spec.secretRef, a
// Kustomization's spec.decryption.secretRef. The Secret is read as
// metadata only; its supplier is named only through a controller
// ownerReference to an ExternalSecret whose read returns that UID.
func (b *builder) supply(id string, u *unstructured.Unstructured) {
	var fields [][]string
	switch u.GetKind() {
	case string(flux.KustomizationKind):
		fields = [][]string{{"spec", "decryption", "secretRef"}}
	case string(flux.GitRepositoryKind), string(flux.OCIRepositoryKind), string(flux.BucketKind), string(flux.HelmRepositoryKind):
		fields = [][]string{{"spec", "secretRef"}}
	}
	for _, f := range fields {
		name, _, _ := unstructured.NestedString(u.Object, append(append([]string{}, f...), "name")...)
		if name == "" {
			continue
		}
		s := Supply{Field: joinPath(f), Secret: Ref{"", "Secret", u.GetNamespace(), name}}
		b.readSecret(&s)
		i := b.idx[id]
		b.g.Nodes[i].Supply = append(b.g.Nodes[i].Supply, s)
	}
}

func joinPath(p []string) string {
	out := ""
	for i, x := range p {
		if i > 0 {
			out += "."
		}
		out += x
	}
	return out
}

// readSecret reads a Secret's metadata, and its supplier, once per walk:
// every node that names it shares the same read (and the same facts).
func (b *builder) readSecret(s *Supply) {
	field := s.Field
	if done, ok := b.secrets[s.Secret.ID()]; ok {
		*s = done
		s.Field = field
		return
	}
	defer func() {
		if s.Error != "cancelled" && !strings.HasPrefix(s.Error, "not read") {
			b.secrets[s.Secret.ID()] = *s
		}
	}()
	if b.cancelled() {
		s.Error = "cancelled"
		return
	}
	if !b.spend() {
		s.Error = fmt.Sprintf("not read: the %d-read limit", MaxReads)
		return
	}
	m, err := b.r.GetMeta(b.ctx, s.Secret)
	s.ReadAt = b.now()
	switch {
	case apierrors.IsNotFound(err):
		s.NotFound = true
		return
	case err != nil:
		s.Error = err.Error()
		b.incomplete("unreadable: %s: %v", s.Secret.ID(), err)
		return
	}
	s.SecretUID = string(m.GetUID())
	s.Writes = lastwrite.Read(m.GetManagedFields())
	for _, o := range m.GetOwnerReferences() {
		if o.Controller == nil || !*o.Controller || o.Kind != "ExternalSecret" || groupOf(o.APIVersion) != GroupESO {
			continue
		}
		ref := Ref{GroupESO, "ExternalSecret", s.Secret.Namespace, o.Name}
		es, ok := b.ess[ref.ID()]
		if !ok {
			if b.cancelled() {
				s.SupplierNote = "cancelled"
				return
			}
			if !b.spend() {
				s.SupplierNote = fmt.Sprintf("its owner ExternalSecret %s was not read: the %d-read limit", o.Name, MaxReads)
				return
			}
			u, err := b.r.Get(b.ctx, ref)
			es = esRead{u: u, err: err, at: b.now()}
			b.ess[ref.ID()] = es
		}
		switch {
		case es.err != nil:
			s.SupplierNote = fmt.Sprintf("its owner ExternalSecret %s could not be read: %v", o.Name, es.err)
		case string(es.u.GetUID()) != string(o.UID):
			s.SupplierNote = fmt.Sprintf("its owner reference names ExternalSecret %s with UID %s, but that name now has UID %s", o.Name, o.UID, es.u.GetUID())
		default:
			var n Node
			readyCondition(es.u, &n)
			s.Supplier = &Supplier{Ref: ref, UID: string(es.u.GetUID()), ReadAt: es.at, Ready: n.Ready, Reason: n.Reason, Message: n.Message, Writes: lastwrite.Read(es.u.GetManagedFields())}
		}
		return
	}
}

func groupOf(apiVersion string) string {
	for i := 0; i < len(apiVersion); i++ {
		if apiVersion[i] == '/' {
			return apiVersion[:i]
		}
	}
	return ""
}

// label marks upstream failures, the ends of reported chains, and the Git
// facts of Kustomizations bound to a GitRepository.
func (b *builder) label() {
	reportedIn := map[string]bool{}
	reportedOut := map[string]bool{}
	for _, e := range b.g.Edges {
		if e.Reported {
			reportedIn[e.To] = true
			reportedOut[e.From] = true
		}
	}
	reach := b.attributable()
	for i := range b.g.Nodes {
		n := &b.g.Nodes[i]
		if n.Stub != "" && n.Stub != StubNotFound {
			continue
		}
		n.UpstreamFailure = n.Failing && n.ID != b.g.Start && reach[n.ID]
		n.ChainEnd = reportedIn[n.ID] && !reportedOut[n.ID]
	}
	for _, e := range b.g.Edges {
		if e.Type != EdgeSource {
			continue
		}
		from, src := &b.g.Nodes[b.idx[e.From]], b.g.Nodes[b.idx[e.To]]
		if from.Ref.Kind != string(flux.KustomizationKind) || src.Stub != "" || src.Ref.Kind != string(flux.GitRepositoryKind) || from.Inventory == nil {
			continue
		}
		rev, ok := line.ParseGitRevision(from.Inventory.LastApplied)
		if !ok {
			continue
		}
		g := &GitFacts{Applied: rev.SHA}
		if f, ok := b.sourceFacts[src.ID]; ok {
			g.URL, g.RefKind, g.RefName = f.URL, f.RefKind, f.RefName
		}
		g.Path = b.paths[from.ID]
		if g.URL != "" {
			from.Git = g
		}
	}
}

// proximity pairs each failing node's Ready=False condition time with the
// main-resource updates (never status-only) that fall within the window
// before it: its own, its upstream nodes', and their supply objects'.
func (b *builder) proximity() {
	out := map[string][]string{}
	for _, e := range b.g.Edges {
		out[e.From] = append(out[e.From], e.To)
	}
	for _, f := range b.g.Nodes {
		if !f.Failing || f.ConditionTime.IsZero() {
			continue
		}
		seen := map[string]bool{}
		var walk func(id string)
		walk = func(id string) {
			if seen[id] {
				return
			}
			seen[id] = true
			n := b.g.Nodes[b.idx[id]]
			if n.Stub == "" {
				b.pair(f, n.Ref.ID(), n.Writes)
				for _, s := range n.Supply {
					b.pair(f, s.Secret.ID(), s.Writes)
					if s.Supplier != nil {
						b.pair(f, s.Supplier.Ref.ID(), s.Supplier.Writes)
					}
				}
			}
			for _, to := range out[id] {
				walk(to)
			}
		}
		walk(f.ID)
	}
	sort.SliceStable(b.g.Proximity, func(i, j int) bool { return b.g.Proximity[i].Write.Time.After(b.g.Proximity[j].Write.Time) })
}

func (b *builder) pair(f Node, object string, w lastwrite.Writes) {
	if w.LastSpec == nil {
		return
	}
	t := w.LastSpec.Time
	if t.After(f.ConditionTime) || t.Before(f.ConditionTime.Add(-ProximityWindow)) {
		return
	}
	b.g.Proximity = append(b.g.Proximity, Pair{Failing: f.ID, ConditionTime: f.ConditionTime, Object: object, Write: *w.LastSpec})
}
