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

	"github.com/moomora/klyx/internal/gitops/imageauto"
	"github.com/moomora/klyx/internal/observe"
)

// Flux image automation's kinds and part (coverage S1b §3), in the Flux
// task.
const (
	KindImageRepositories = "imagerepositories"
	KindImagePolicies     = "imagepolicies"
	KindImageAutomations  = "imageupdateautomations"
)

// The budget per cluster: 1 MiB encoded together, split by kind. The
// design's 2 MiB was cut with S1a's (the whole Observer holds about 5.5
// heap bytes per encoded byte with the lens open); a typical projection is
// about 1 KiB, so this holds several hundred objects of each kind.
const (
	budgetImageRepositories = 256 << 10
	budgetImagePolicies     = 512 << 10
	budgetImageAutomations  = (1 << 20) - budgetImageRepositories - budgetImagePolicies
)

// FluxImages returns the image kinds and their part of the Flux task.
func FluxImages(d NativeDeps) ([]observe.KindSpec, []observe.PartSpec) {
	kinds := []observe.KindSpec{
		imageKind(d, KindImageRepositories, imageauto.KindRepository, budgetImageRepositories),
		imageKind(d, KindImagePolicies, imageauto.KindPolicy, budgetImagePolicies),
		imageKind(d, KindImageAutomations, imageauto.KindAutomation, budgetImageAutomations),
	}
	parts := []observe.PartSpec{
		{ID: "images", Entity: "Image repositories, policies and automations", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindImageRepositories, KindImagePolicies, KindImageAutomations}, Evaluate: evalImages(d.Cluster), Ledger: true},
	}
	return kinds, parts
}

// imageKind is a budgeted, field-by-field projected image kind, read at v1
// or else v1beta2 (S1b §1): another served version is a named gap, never an
// empty or misread list.
func imageKind(d NativeDeps, resource, kind string, budget int) observe.KindSpec {
	return observe.KindSpec{Name: resource, GVR: imageauto.Group + "/" + resource, Scope: "all namespaces", ExpectedType: &unstructured.Unstructured{},
		Project: imageauto.Project(kind), Budget: budget, MaxObject: imageauto.MaxObject, Size: imageauto.SizeOf,
		Probe: func(ctx context.Context) (observe.Probe, error) {
			p, err := d.Served(ctx, imageauto.Group, resource, imageauto.Version)
			if err == nil && p.Served && p.Version != imageauto.Version && p.Version != imageauto.VersionOld {
				return observe.Probe{}, fmt.Errorf("unsupported image automation version %s for %s: Klyx reads %s or %s", p.Version, resource, imageauto.Version, imageauto.VersionOld)
			}
			return p, err
		},
		NewListWatch: func(version string) cache.ListerWatcherWithContext {
			ri := d.Dyn.Resource(schema.GroupVersionResource{Group: imageauto.Group, Version: version, Resource: resource})
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

// ImageRow is one image object (exactly one of the three is set) with the
// joins its inspector shows beside its own verdict, from this capture.
type ImageRow struct {
	Repository *imageauto.Repository
	Policy     *imageauto.Policy
	Automation *imageauto.Automation
	// A repository's policies, across namespaces.
	Policies []ImagePolicyRef
	// A policy's repository (a separate block) and the automations using it.
	RepositoryLink *ImageRepositoryLink
	BlockedBy      string
	Automations    []imageauto.AutomationUse
	// An automation's policies now, with what its last successful run used.
	Uses      []imageauto.PolicyUse
	UsesState string
}

// ImagePolicyRef names a policy and its verdict label.
type ImagePolicyRef struct {
	Namespace, Name, Label, Role string
}

// ImageRepositoryLink is a policy's repository as its own block: never the
// policy's verdict.
type ImageRepositoryLink struct {
	State           string
	Namespace, Name string
	Label, Role     string
	ScanTime        string
}

// coverageOf is how much of a source this capture holds.
func coverageOf(c observe.Capture, source string) imageauto.Coverage {
	_, has := c.Objects[source]
	switch {
	case c.Complete[source]:
		return imageauto.CoverageComplete
	case has:
		return imageauto.CoveragePartial
	}
	return imageauto.CoverageUnreadable
}

func imageObjects(c observe.Capture, source string) []*imageauto.Object {
	var out []*imageauto.Object
	for _, x := range c.Objects[source] {
		if o, ok := x.(*imageauto.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

func evalImages(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var repos []imageauto.Repository
		var repoObjs, polObjs, autoObjs []*imageauto.Object
		repoObjs, polObjs, autoObjs = imageObjects(c, KindImageRepositories), imageObjects(c, KindImagePolicies), imageObjects(c, KindImageAutomations)
		for _, o := range repoObjs {
			repos = append(repos, imageauto.ParseRepository(o))
		}
		var pols []imageauto.Policy
		for _, o := range polObjs {
			pols = append(pols, imageauto.ParsePolicy(o))
		}
		var autos []imageauto.Automation
		for _, o := range autoObjs {
			autos = append(autos, imageauto.ParseAutomation(o))
		}
		repoCov, polCov := coverageOf(c, KindImageRepositories), coverageOf(c, KindImagePolicies)

		var items []nativeItem
		var rows []Row[ImageRow]
		for i, r := range repos {
			it := nativeItem{imageauto.RepositoryRule(r, c.Now), refOf(repoObjs[i].Unstructured, KindImageRepositories)}
			items = append(items, it)
			if c.Rows {
				row := ImageRow{Repository: &repos[i]}
				for _, p := range imageauto.RepositoryPolicies(r, pols) {
					e := imageauto.PolicyRule(p)
					row.Policies = append(row.Policies, ImagePolicyRef{Namespace: p.Namespace, Name: p.Name, Label: e.Label, Role: e.Role.String()})
				}
				rows = append(rows, nativeRow(it, row))
			}
		}
		for i, p := range pols {
			it := nativeItem{imageauto.PolicyRule(p), refOf(polObjs[i].Unstructured, KindImagePolicies)}
			items = append(items, it)
			if c.Rows {
				row := ImageRow{Policy: &pols[i], BlockedBy: p.BlockedBy(), Automations: imageauto.PolicyAutomations(p, autos)}
				l := imageauto.PolicyRepository(p, repos, repoCov)
				link := ImageRepositoryLink{State: l.State, Namespace: p.RepositoryNamespace, Name: p.RepositoryName}
				if l.Repository != nil {
					e := imageauto.RepositoryRule(*l.Repository, c.Now)
					link.Label, link.Role = e.Label, e.Role.String()
					if !l.Repository.ScanTime.IsZero() {
						link.ScanTime = l.Repository.ScanTime.UTC().Format("2006-01-02T15:04:05Z")
					}
				}
				row.RepositoryLink = &link
				rows = append(rows, nativeRow(it, row))
			}
		}
		for i, a := range autos {
			it := nativeItem{imageauto.AutomationRule(a, c.Now), refOf(autoObjs[i].Unstructured, KindImageAutomations)}
			items = append(items, it)
			if c.Rows {
				row := ImageRow{Automation: &autos[i]}
				row.Uses, row.UsesState = imageauto.AutomationPolicies(a, pols, polCov)
				rows = append(rows, nativeRow(it, row))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskFlux, "not ready", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}
