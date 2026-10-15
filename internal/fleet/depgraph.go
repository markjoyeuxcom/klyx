package fleet

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/gitops/depgraph"
)

// DependencyGraph walks the GitOps dependency graph from one Kustomization
// or HelmRelease (The Line slice 8b, parent §5.1): every node one fresh
// read at its served version, bounded by the walk, cancelled with ctx.
func (c *ClusterConn) DependencyGraph(ctx context.Context, kind, ns, name, uid string) depgraph.Graph {
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	group := depgraph.GroupKustomize
	if kind == "HelmRelease" {
		group = depgraph.GroupHelm
	}
	return depgraph.Build(ctx, depReader{c}, depgraph.Ref{Group: group, Kind: kind, Namespace: ns, Name: name}, uid, clk.Now)
}

// depReader reads through the cached discovery: a whole object, or a
// Secret's metadata only.
type depReader struct{ c *ClusterConn }

func (r depReader) Get(ctx context.Context, ref depgraph.Ref) (*unstructured.Unstructured, error) {
	gvr, namespaced, err := r.c.resourceFor(ctx, ObjectRef{Group: ref.Group, Kind: ref.Kind})
	if err != nil {
		return nil, err
	}
	if namespaced {
		return r.c.dyn.Resource(gvr).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	}
	return r.c.dyn.Resource(gvr).Get(ctx, ref.Name, metav1.GetOptions{})
}

func (r depReader) GetMeta(ctx context.Context, ref depgraph.Ref) (*metav1.PartialObjectMetadata, error) {
	gvr, namespaced, err := r.c.resourceFor(ctx, ObjectRef{Group: ref.Group, Kind: ref.Kind})
	if err != nil {
		return nil, err
	}
	if namespaced {
		return r.c.meta.Resource(gvr).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	}
	return r.c.meta.Resource(gvr).Get(ctx, ref.Name, metav1.GetOptions{})
}
