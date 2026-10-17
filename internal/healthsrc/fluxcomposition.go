package healthsrc

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/gitops/composition"
	"github.com/moomora/klyx/internal/gitops/notify"
	"github.com/moomora/klyx/internal/gitops/resume"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
)

// Coverage S1c §3: source composition and Receivers, in the Flux task.
// Generators and artifacts are a part of their own, "composition", shown in
// the Sources view: their rows are not Flux sources' rows.
const (
	KindArtifactGenerators = "artifactgenerators"
	KindExternalArtifacts  = "externalartifacts"
	KindReceivers          = "receivers"
)

// The budget per cluster (S1c §3): 1 MiB together.
const (
	budgetGenerators = 512 << 10
	budgetArtifacts  = 384 << 10
	budgetReceivers  = 128 << 10
)

// FluxComposition returns the composition and Receiver kinds and parts.
func FluxComposition(d NativeDeps) ([]observe.KindSpec, []observe.PartSpec) {
	kinds := []observe.KindSpec{
		oneVersionKind(d, KindArtifactGenerators, composition.GeneratorGroup, composition.GeneratorVersion, budgetGenerators,
			composition.Project(composition.KindGenerator), composition.MaxObject, composition.SizeOf),
		oneVersionKind(d, KindExternalArtifacts, composition.ArtifactGroup, composition.ArtifactVersion, budgetArtifacts,
			composition.Project(composition.KindArtifact), composition.MaxObject, composition.SizeOf),
		oneVersionKind(d, KindReceivers, notify.Group, notify.ReceiverVersion, budgetReceivers,
			notify.ProjectReceiver, notify.MaxObject, notify.SizeOf),
	}
	const ns = "all namespaces"
	parts := []observe.PartSpec{
		{ID: "composition", Entity: "ArtifactGenerators and ExternalArtifacts", Scope: ns, Classifier: "native",
			Counted: []string{KindArtifactGenerators, KindExternalArtifacts}, Evaluate: evalComposition(d.Cluster, resume.NewTracker()), Ledger: true},
		{ID: "receivers", Entity: "Receivers", Scope: ns, Classifier: "native",
			Counted: []string{KindReceivers}, Evaluate: evalReceivers(d.Cluster), Ledger: true},
	}
	return kinds, parts
}

// oneVersionKind is a budgeted, projected kind served at one version (S1c
// §3): another served version is a named gap, never an empty list.
func oneVersionKind(d NativeDeps, resource, group, version string, budget int, project func(any) (any, error), maxObject int, size func(any) int) observe.KindSpec {
	return observe.KindSpec{Name: resource, GVR: group + "/" + resource, Scope: "all namespaces", ExpectedType: &unstructured.Unstructured{},
		Project: project, Budget: budget, MaxObject: maxObject, Size: size,
		Probe: func(ctx context.Context) (observe.Probe, error) {
			p, err := d.Served(ctx, group, resource, version)
			if err == nil && p.Served && p.Version != version {
				return observe.Probe{}, fmt.Errorf("unsupported version %s for %s: Klyx reads %s", p.Version, resource, version)
			}
			return p, err
		},
		NewListWatch: func(v string) cache.ListerWatcherWithContext {
			ri := d.Dyn.Resource(schema.GroupVersionResource{Group: group, Version: v, Resource: resource})
			return cache.ToListWatcherWithWatchListSemantics(&cache.ListWatch{
				ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
					return ri.List(ctx, o)
				},
				WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
					return ri.Watch(ctx, o)
				},
			}, d.Dyn).(cache.ListerWatcherWithContext)
		}}
}

// CompositionRow is one generator or artifact (exactly one is set), with
// the joins its inspector shows beside its own verdict, from this capture.
type CompositionRow struct {
	Generator *composition.Generator
	Artifact  *composition.Artifact
	Resume    string
	// A generator's generated artifacts, as read now, and that read's state.
	Generated      []CompositionRef
	GeneratedState string
	// An artifact's producer: a separate block, never its verdict.
	Producer *ProducerView
}

// CompositionRef is a joined object with its verdict, when read.
type CompositionRef struct {
	Namespace, Name string
	Found           bool
	Label, Role     string
}

// ProducerView is an artifact's producer as the evidence establishes it
// (S1c §2): Label and Role are given only for a producer whose health may
// be shown, and never fold into the artifact's own verdict.
type ProducerView struct {
	State                 string
	Kind, Namespace, Name string
	Label, Role           string
	// GeneratorFailing: "the producer is failing; this artifact is its
	// last good one".
	GeneratorFailing bool
	// On disagree: the live generator under the recorded UID, if read, or
	// whether a complete read found none, or that lookup's state.
	RecordedNamespace, RecordedName string
	RecordedGone                    bool
	RecordedLookup                  string
}

func compCoverage(c observe.Capture, source string) composition.Coverage {
	_, has := c.Objects[source]
	switch {
	case c.Complete[source]:
		return composition.CoverageComplete
	case has:
		return composition.CoveragePartial
	}
	return composition.CoverageUnreadable
}

func compObjects(c observe.Capture, source string) []*composition.Object {
	var out []*composition.Object
	for _, x := range c.Objects[source] {
		if o, ok := x.(*composition.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

func evalComposition(cluster string, tr *resume.Tracker) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		genObjs, artObjs := compObjects(c, KindArtifactGenerators), compObjects(c, KindExternalArtifacts)
		gens := make([]composition.Generator, 0, len(genObjs))
		for _, o := range genObjs {
			gens = append(gens, composition.ParseGenerator(o))
		}
		arts := make([]composition.Artifact, 0, len(artObjs))
		artAt := map[string]int{}
		for i, o := range artObjs {
			a := composition.ParseArtifact(o)
			arts = append(arts, a)
			artAt[a.Namespace+"/"+a.Name] = i
		}
		genCov, artCov := compCoverage(c, KindArtifactGenerators), compCoverage(c, KindExternalArtifacts)
		var items []nativeItem
		var rows []Row[CompositionRow]
		genEval := map[string]health.NativeEval{}
		for i, g := range gens {
			r := tr.Observe(c.Now, g.Sample(genObjs[i]))
			ev := composition.GeneratorRule(g, r)
			genEval[g.UID] = ev
			it := nativeItem{ev, refOf(genObjs[i].Unstructured, KindArtifactGenerators)}
			items = append(items, it)
			if !c.Rows {
				continue
			}
			row := CompositionRow{Generator: &gens[i], Resume: resumeWord(r), GeneratedState: compState(artCov)}
			if g.InventoryIncomplete {
				row.GeneratedState = compState(composition.CoveragePartial)
			}
			for _, ref := range g.Inventory {
				if ref.Namespace == "" {
					ref.Namespace = g.Namespace
				}
				cr := CompositionRef{Namespace: ref.Namespace, Name: ref.Name}
				if j, ok := artAt[ref.Namespace+"/"+ref.Name]; ok {
					e := composition.ArtifactRule(arts[j])
					cr.Found, cr.Label, cr.Role = true, e.Label, e.Role.String()
				}
				row.Generated = append(row.Generated, cr)
			}
			rows = append(rows, nativeRow(it, row))
		}
		tr.Prune(composition.KindGenerator, uidsOf(genObjs), c.Complete[KindArtifactGenerators])
		for i, a := range arts {
			it := nativeItem{composition.ArtifactRule(a), refOf(artObjs[i].Unstructured, KindExternalArtifacts)}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, CompositionRow{Artifact: &arts[i], Producer: ProducerViewOf(a, gens, genCov, genEval)}))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskFlux, "not ready", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// ProducerViewOf is ProducerOf for display: the producer's health only
// when its identity is established (verified, by identity or named).
func ProducerViewOf(a composition.Artifact, gens []composition.Generator, cov composition.Coverage, genEval map[string]health.NativeEval) *ProducerView {
	p := composition.ProducerOf(a, gens, cov)
	pv := &ProducerView{State: p.State, RecordedGone: p.RecordedGone, RecordedLookup: p.RecordedLookup}
	if p.Declared != nil {
		pv.Kind, pv.Namespace, pv.Name = p.Declared.Kind, p.Declared.Namespace, p.Declared.Name
	}
	if p.Generator != nil {
		e := genEval[p.Generator.UID]
		pv.Kind, pv.Namespace, pv.Name = composition.KindGenerator, p.Generator.Namespace, p.Generator.Name
		pv.Label, pv.Role, pv.GeneratorFailing = e.Label, e.Role.String(), e.Role == health.Fault
	}
	if p.Recorded != nil {
		pv.RecordedNamespace, pv.RecordedName = p.Recorded.Namespace, p.Recorded.Name
	}
	return pv
}

func compState(cov composition.Coverage) string {
	switch cov {
	case composition.CoverageUnreadable:
		return "not readable"
	case composition.CoveragePartial:
		return "incomplete"
	}
	return "found"
}

func uidsOf(objs []*composition.Object) map[string]bool {
	out := map[string]bool{}
	for _, o := range objs {
		out[string(o.GetUID())] = true
	}
	return out
}

// ReceiverRow is one Receiver and what its Ready means.
type ReceiverRow struct {
	notify.Receiver
	Meaning string
}

func evalReceivers(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[ReceiverRow]
		for _, x := range c.Objects[KindReceivers] {
			o, ok := x.(*notify.Object)
			if !ok {
				continue
			}
			r := notify.ParseReceiver(o)
			it := nativeItem{notify.ReceiverRule(r), refOf(o.Unstructured, KindReceivers)}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, ReceiverRow{Receiver: r, Meaning: notify.ReadyMeaning(r)}))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskFlux, "not ready", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}
