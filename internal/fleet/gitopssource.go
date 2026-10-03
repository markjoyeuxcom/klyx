package fleet

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// sourceGVR maps a Flux source kind to its group, fallback version, and resource.
func sourceGVR(kind string) (group, fallbackVersion, resource string, ok bool) {
	const g = "source.toolkit.fluxcd.io"
	switch kind {
	case "GitRepository":
		return g, "v1", "gitrepositories", true
	case "OCIRepository":
		return g, "v1beta2", "ocirepositories", true
	case "Bucket":
		return g, "v1", "buckets", true
	case "HelmRepository":
		return g, "v1", "helmrepositories", true
	case "HelmChart":
		return g, "v1", "helmcharts", true
	default:
		return "", "", "", false
	}
}

// sourceServedGVR is the GVR a Flux source kind is served at, resolved per
// resource (servedVersion): OCIRepository can lag the group's preferred
// version (review 2026-10-03 #11). ok=false for an unsupported or unserved
// kind.
func (c *ClusterConn) sourceServedGVR(kind string) (schema.GroupVersionResource, bool) {
	group, fallback, resource, ok := sourceGVR(kind)
	if !ok {
		return schema.GroupVersionResource{}, false
	}
	version, served := servedVersion(c.discovery(), group, resource, fallback)
	if !served {
		return schema.GroupVersionResource{}, false
	}
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}, true
}

// SourceURL fetches spec.url from a Flux source object via a one-off dynamic Get
// (the source is not watched). Returns ok=false for an unsupported kind, a Get
// error, or an empty url.
func (c *ClusterConn) SourceURL(ctx context.Context, kind, ns, name string) (string, bool) {
	gvr, ok := c.sourceServedGVR(kind)
	if !ok {
		return "", false
	}
	u, err := c.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", false
	}
	url, _, _ := unstructured.NestedString(u.Object, "spec", "url")
	if url == "" {
		return "", false
	}
	return url, true
}
