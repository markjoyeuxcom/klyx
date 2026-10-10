package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
)

// KindCoverage is one watched kind's evidence state: Served=false is a
// definitive absence; otherwise Synced (initial list done) and Error (a
// CURRENT failure - including one after the initial sync, while the view is
// showing last-known state).
type KindCoverage struct {
	Kind   string
	Served bool
	Synced bool
	Error  string
}

// kindSet is a lazy watch's set of per-kind informers, each with its OWN
// recovery-aware error tracker, so coverage is per kind: one kind syncing never
// vouches for another kind's pending or forbidden watch.
type kindSet struct {
	watched  []kindWatched
	unserved []string
}

type kindWatched struct {
	kind string
	inf  cache.SharedIndexInformer
	errs *watchErrTracker
}

// watch builds one kind's informer (mirrors OpenGitOps' watchKind): at the
// version discovery says THAT resource is served at, or records it unserved
// (absent, not an error) and returns nil. list/watch successes mark the
// kind's tracker recovered (withRecovery). Callers run the informers.
func (k *kindSet) watch(disc discoveryLike, dyn dynamic.Interface, kind, group, resource, fallback string) cache.SharedIndexInformer {
	ver, served := servedVersion(disc, group, resource, fallback)
	if !served {
		k.unserved = append(k.unserved, kind)
		return nil
	}
	gvr := schema.GroupVersionResource{Group: group, Version: ver, Resource: resource}
	ri := dyn.Resource(gvr)
	errs := &watchErrTracker{}
	lw := withRecovery(&cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return ri.List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return ri.Watch(ctx, o)
		},
	}, errs)
	inf := cache.NewSharedIndexInformerWithOptions(
		cache.ToListWatcherWithWatchListSemantics(lw, dyn),
		&unstructured.Unstructured{},
		cache.SharedIndexInformerOptions{
			ResyncPeriod:      defaultResync,
			Indexers:          cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			ObjectDescription: gvr.String(),
		},
	)
	trackWatchErrors(inf, errs)
	k.watched = append(k.watched, kindWatched{kind: kind, inf: inf, errs: errs})
	return inf
}

func (k *kindSet) run(stop <-chan struct{}) {
	for _, w := range k.watched {
		go w.inf.Run(stop)
	}
}

// coverage reports evidence per kind, sorted by kind.
func (k *kindSet) coverage() []KindCoverage {
	out := make([]KindCoverage, 0, len(k.watched)+len(k.unserved))
	for _, w := range k.watched {
		out = append(out, KindCoverage{Kind: w.kind, Served: true, Synced: w.inf.HasSynced(), Error: w.errs.currentError()})
	}
	for _, u := range k.unserved {
		out = append(out, KindCoverage{Kind: u, Served: false, Synced: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// healthOf is the aggregate view of a coverage set: synced = every served kind
// synced; lastErr = every CURRENT failure, named by kind (an after-sync failure
// says the view is showing last-known state). Mirrors GitOpsWatchHealth.
func healthOf(cov []KindCoverage) (synced bool, lastErr string) {
	synced = true
	var errs []string
	for _, k := range cov {
		if !k.Served {
			continue
		}
		if !k.Synced {
			synced = false
		}
		switch {
		case k.Error == "":
		case k.Synced:
			errs = append(errs, fmt.Sprintf("%s watch failing (showing last-known state): %s", k.Kind, k.Error))
		default:
			errs = append(errs, fmt.Sprintf("%s: %s", k.Kind, k.Error))
		}
	}
	return synced, strings.Join(errs, "; ")
}
