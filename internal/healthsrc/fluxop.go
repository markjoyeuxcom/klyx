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

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/gitops/fluxop"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
)

// The Flux Operator's kinds and parts (coverage S1a §3), in the Flux task.
const (
	KindFluxInstances  = "fluxinstances"
	KindFluxReports    = "fluxreports"
	KindResourceSets   = "resourcesets"
	KindInputProviders = "resourcesetinputproviders"

	// FactFluxOperator is the operator detection (capability.FluxOperatorInfo)
	// a FluxInstance's binding is read from.
	FactFluxOperator = "fluxOperator"
)

// The budget per cluster (S1a §3, as built): 1 MiB encoded together, split
// by kind so each kind admits deterministically on its own key order. The
// design's 4 MiB was cut after the whole Observer was measured at about 5.4
// heap bytes per encoded byte with the lens open (TestFluxOperatorObserverHeap;
// Codex's implementation review preferred a default near 1 MiB). It still
// holds several hundred typical ResourceSets; past it the kind reads capped.
const (
	budgetInstances = 64 << 10
	budgetReports   = 64 << 10
	budgetSets      = 512 << 10
	budgetProviders = (1 << 20) - budgetInstances - budgetReports - budgetSets
)

// FluxOperator returns the operator's kinds and its parts of the Flux task.
// The trackers hold each object's resume baseline for this cluster.
func FluxOperator(d NativeDeps) ([]observe.KindSpec, []observe.PartSpec) {
	const ns = "all namespaces"
	kinds := []observe.KindSpec{
		fluxopKind(d, KindFluxInstances, "fluxinstances", fluxop.KindInstance, budgetInstances),
		fluxopKind(d, KindFluxReports, "fluxreports", fluxop.KindReport, budgetReports),
		fluxopKind(d, KindResourceSets, "resourcesets", fluxop.KindSet, budgetSets),
		fluxopKind(d, KindInputProviders, "resourcesetinputproviders", fluxop.KindProvider, budgetProviders),
	}
	c := d.Cluster
	parts := []observe.PartSpec{
		{ID: "fluxinstance", Entity: "FluxInstances", Scope: ns, Classifier: "native", Counted: []string{KindFluxInstances},
			Facts: []string{FactFluxOperator}, Evaluate: evalFluxInstances(c, fluxop.NewTracker()), Ledger: true},
		{ID: "resourcesets", Entity: "ResourceSets and input providers", Scope: ns, Classifier: "native",
			Counted: []string{KindResourceSets, KindInputProviders}, Evaluate: evalResourceSets(c, fluxop.NewTracker()), Ledger: true},
		// The report is the operator's account, never Klyx's counts: evidence
		// that lights nothing (S1a §2).
		{ID: "fluxreport", Entity: "FluxReports", Scope: ns, Classifier: "evidence", Counted: []string{KindFluxReports},
			Evaluate: evalFluxReports},
	}
	return kinds, parts
}

// fluxopKind is a budgeted, field-by-field projected operator kind at v1.
func fluxopKind(d NativeDeps, name, resource, kind string, budget int) observe.KindSpec {
	return observe.KindSpec{Name: name, GVR: fluxop.Group + "/" + resource, Scope: "all namespaces", ExpectedType: &unstructured.Unstructured{},
		Project: fluxop.Project(kind), Budget: budget, MaxObject: fluxop.MaxObject, Size: fluxop.SizeOf,
		// v1 only (S1a §3): another served version is unsupported - a named
		// discovery gap, never an empty or misread list
		Probe: func(ctx context.Context) (observe.Probe, error) {
			p, err := d.Served(ctx, fluxop.Group, resource, fluxop.Version)
			if err == nil && p.Served && p.Version != fluxop.Version {
				return observe.Probe{}, fmt.Errorf("unsupported Flux Operator API version %s for %s: Klyx reads %s", p.Version, resource, fluxop.Version)
			}
			return p, err
		},
		NewListWatch: func(version string) cache.ListerWatcherWithContext {
			ri := d.Dyn.Resource(schema.GroupVersionResource{Group: fluxop.Group, Version: version, Resource: resource})
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

// Resume states as rows carry them.
const (
	ResumeAwaiting   = "awaiting"
	ResumeEvaluating = "evaluating"
	ResumeUnproven   = "unproven"
)

func resumeWord(r fluxop.Resume) string {
	switch r {
	case fluxop.Awaiting:
		return ResumeAwaiting
	case fluxop.Evaluating:
		return ResumeEvaluating
	case fluxop.Unproven:
		return ResumeUnproven
	}
	return ""
}

// prune hands the tracker what this capture proves about one kind's
// membership (S1a §4; Codex's implementation review, finding 7).
func prune(tr *fluxop.Tracker, c observe.Capture, source, kind string) {
	present := map[string]bool{}
	for _, x := range c.Objects[source] {
		if o, ok := x.(*fluxop.Object); ok {
			present[string(o.GetUID())] = true
		}
	}
	tr.Prune(kind, present, c.Complete[source])
}

// FluxInstanceRow is one FluxInstance: its parse, resume state and binding
// to an operator.
type FluxInstanceRow struct {
	fluxop.Instance
	Resume  string
	Binding capability.Binding
}

// FluxSetRow is one ResourceSet or provider (exactly one is set).
type FluxSetRow struct {
	Set      *fluxop.Set
	Provider *fluxop.Provider
	Resume   string
}

// Binding labels: an instance no single operator reconciles is not counted
// (S1a §3). It stays in the ledger's membership as unevaluated, and it
// lights nothing of its own: an operator's issue lights the task only when
// that operator has one (FluxOperatorInfo.Issue).
const (
	LabelOrphan    = "No operator reconciles this namespace"
	LabelAmbiguous = "Several operators reconcile this namespace"
)

func evalFluxInstances(cluster string, tr *fluxop.Tracker) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		info, _ := c.Facts[FactFluxOperator].(capability.FluxOperatorInfo)
		var items []nativeItem
		var rows []Row[FluxInstanceRow]
		for _, x := range c.Objects[KindFluxInstances] {
			o, ok := x.(*fluxop.Object)
			if !ok {
				continue
			}
			in := fluxop.ParseInstance(o)
			r := tr.Observe(c.Now, o, in.Common, "")
			ev := fluxop.Rule(in.Common, r)
			b := info.Bind(in.Namespace)
			it := nativeItem{ev, refOf(o.Unstructured, KindFluxInstances)}
			switch b {
			case capability.BindingOrphan:
				it.eval = health.NativeEval{Role: health.Unknown, Label: LabelOrphan}
			case capability.BindingAmbiguous:
				it.eval = health.NativeEval{Role: health.Unknown, Label: LabelAmbiguous}
			default:
				items = append(items, it) // counted only where an operator reconciles it
			}
			rows = append(rows, nativeRow(it, FluxInstanceRow{Instance: in, Resume: resumeWord(r), Binding: b}))
		}
		prune(tr, c, KindFluxInstances, fluxop.KindInstance)
		res := nativeResult(c.RoleCap, cluster, TaskFlux, "not ready", items)
		if c.Rows {
			res.Rows = RowSet[FluxInstanceRow]{Rows: rows, Facts: c.Facts}
		}
		return res
	}
}

func evalResourceSets(cluster string, tr *fluxop.Tracker) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[FluxSetRow]
		for _, x := range c.Objects[KindResourceSets] {
			o, ok := x.(*fluxop.Object)
			if !ok {
				continue
			}
			st := fluxop.ParseSet(o)
			r := tr.Observe(c.Now, o, st.Common, "")
			it := nativeItem{fluxop.Rule(st.Common, r), refOf(o.Unstructured, KindResourceSets)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, FluxSetRow{Set: &st, Resume: resumeWord(r)}))
		}
		for _, x := range c.Objects[KindInputProviders] {
			o, ok := x.(*fluxop.Object)
			if !ok {
				continue
			}
			p := fluxop.ParseProvider(o)
			r := tr.Observe(c.Now, o, p.Common, p.LastExportedRevision)
			it := nativeItem{fluxop.ProviderRule(p, r), refOf(o.Unstructured, KindInputProviders)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, FluxSetRow{Provider: &p, Resume: resumeWord(r)}))
		}
		prune(tr, c, KindResourceSets, fluxop.KindSet)
		prune(tr, c, KindInputProviders, fluxop.KindProvider)
		res := nativeResult(c.RoleCap, cluster, TaskFlux, "not ready", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// evalFluxReports counts the reports: evidence only, no verdict.
func evalFluxReports(c observe.Capture) observe.PartResult {
	var rows []Row[fluxop.Report]
	for _, x := range c.Objects[KindFluxReports] {
		if o, ok := x.(*fluxop.Object); ok {
			rows = append(rows, Row[fluxop.Report]{Ref: refOf(o.Unstructured, KindFluxReports).rowRef(), Data: fluxop.ParseReport(o)})
		}
	}
	n := len(rows)
	res := observe.PartResult{Evidence: &n}
	if c.Rows {
		res.Rows = rowSet(rows)
	}
	return res
}
