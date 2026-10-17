package healthsrc

import (
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/observe"
)

// Flux and Argo CD kinds and tasks (1c addendum §1). Flux sources are their
// own part: a failing GitRepository lights the task even when nothing that
// uses it is failing yet.
const (
	KindKustomizations   = "kustomizations"
	KindHelmReleases     = "helmreleases"
	KindGitRepositories  = "gitrepositories"
	KindOCIRepositories  = "ocirepositories"
	KindBuckets          = "buckets"
	KindHelmRepositories = "helmrepositories"
	KindHelmCharts       = "helmcharts"
	KindApplications     = "applications"
	KindApplicationSets  = "applicationsets"

	TaskFlux = "flux"
	TaskArgo = "argo"
)

var fluxSourceKinds = []string{KindGitRepositories, KindOCIRepositories, KindBuckets, KindHelmRepositories, KindHelmCharts}

// GitOps returns the Flux and Argo CD kinds and tasks. Their rules are the
// relocated lens rules (internal/gitops/flux and argo rules.go).
func GitOps(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	const ns = "all namespaces"
	kinds := []observe.KindSpec{
		crdKind(d, KindKustomizations, "kustomize.toolkit.fluxcd.io", "kustomizations", "v1", ns),
		crdKind(d, KindHelmReleases, "helm.toolkit.fluxcd.io", "helmreleases", "v2", ns),
		crdKind(d, KindGitRepositories, "source.toolkit.fluxcd.io", "gitrepositories", "v1", ns),
		crdKind(d, KindOCIRepositories, "source.toolkit.fluxcd.io", "ocirepositories", "v1beta2", ns),
		crdKind(d, KindBuckets, "source.toolkit.fluxcd.io", "buckets", "v1", ns),
		crdKind(d, KindHelmRepositories, "source.toolkit.fluxcd.io", "helmrepositories", "v1", ns),
		crdKind(d, KindHelmCharts, "source.toolkit.fluxcd.io", "helmcharts", "v1", ns),
		crdKind(d, KindApplications, "argoproj.io", "applications", "v1alpha1", ns),
		crdKind(d, KindApplicationSets, "argoproj.io", "applicationsets", "v1alpha1", ns),
	}
	c := d.Cluster
	opKinds, opParts := FluxOperator(d)
	imgKinds, imgParts := FluxImages(d)
	kinds = append(append(kinds, opKinds...), imgKinds...)
	domains := []observe.DomainSpec{
		{ID: TaskFlux, Parts: append([]observe.PartSpec{
			{ID: "reconciliations", Entity: "Kustomizations and HelmReleases", Scope: ns, Classifier: "native",
				Counted: []string{KindKustomizations, KindHelmReleases}, Evaluate: evalFluxReconciliations(c), Ledger: true},
			{ID: "sources", Entity: "Flux sources", Scope: ns, Classifier: "native",
				Counted: fluxSourceKinds, Evaluate: evalFluxSources(c), Ledger: true},
		}, append(opParts, imgParts...)...)},
		{ID: TaskArgo, Parts: []observe.PartSpec{
			{ID: "applications", Entity: "Applications", Scope: ns, Classifier: "native",
				Counted: []string{KindApplications}, Evaluate: evalArgoApps(c), Ledger: true},
			{ID: "applicationsets", Entity: "ApplicationSets", Scope: ns, Classifier: "native",
				Counted: []string{KindApplicationSets}, Evaluate: evalArgoAppSets(c), Ledger: true},
		}},
	}
	return kinds, domains
}

func evalFluxReconciliations(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[flux.Resource]
		for _, u := range unstructuredOf(c.Objects[KindKustomizations]) {
			r := flux.ParseKustomization(u)
			it := nativeItem{flux.ReconciliationRule(r), refOf(u, KindKustomizations)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, r))
		}
		for _, u := range unstructuredOf(c.Objects[KindHelmReleases]) {
			r := flux.ParseHelmRelease(u)
			it := nativeItem{flux.ReconciliationRule(r), refOf(u, KindHelmReleases)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, r))
		}
		// The lens's legacy word: "not ready" still includes progressing.
		res := nativeResult(c.RoleCap, cluster, TaskFlux, "not ready", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

func evalFluxSources(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[flux.Source]
		for _, kind := range fluxSourceKinds {
			for _, u := range unstructuredOf(c.Objects[kind]) {
				src := flux.ParseSource(u)
				it := nativeItem{flux.SourceRule(src), refOf(u, kind)}
				items = append(items, it)
				rows = append(rows, nativeRow(it, src))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskFlux, "not ready", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

func evalArgoApps(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[argo.App]
		for _, u := range unstructuredOf(c.Objects[KindApplications]) {
			a := argo.Parse(u)
			it := nativeItem{argo.AppRule(a), refOf(u, KindApplications)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, a))
		}
		res := nativeResult(c.RoleCap, cluster, TaskArgo, "out of sync or unhealthy", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

func evalArgoAppSets(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[argo.ApplicationSet]
		for _, u := range unstructuredOf(c.Objects[KindApplicationSets]) {
			s := argo.ParseApplicationSet(u)
			it := nativeItem{argo.AppSetRule(s), refOf(u, KindApplicationSets)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, s))
		}
		res := nativeResult(c.RoleCap, cluster, TaskArgo, "need attention", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}
