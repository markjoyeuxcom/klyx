package fleet

import (
	"context"
	"fmt"

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
	case "ExternalArtifact":
		return g, "v1", "externalartifacts", true
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

// fluxGVR maps a Flux kind Klyx reads to its group, fallback version and
// resource: the reconciliation kinds, then the source kinds.
func fluxGVR(kind string) (group, fallbackVersion, resource string, ok bool) {
	switch kind {
	case "Kustomization":
		return "kustomize.toolkit.fluxcd.io", "v1", "kustomizations", true
	case "HelmRelease":
		return "helm.toolkit.fluxcd.io", "v2", "helmreleases", true
	case "ArtifactGenerator":
		return "source.extensions.fluxcd.io", "v1beta1", "artifactgenerators", true
	case "ImageRepository", "ImagePolicy", "ImageUpdateAutomation":
		return "image.toolkit.fluxcd.io", "v1", map[string]string{"ImageRepository": "imagerepositories", "ImagePolicy": "imagepolicies", "ImageUpdateAutomation": "imageupdateautomations"}[kind], true
	}
	return sourceGVR(kind)
}

// FluxObject reads one Flux object directly at its served version (1c slice
// 3 design §5b), under detailReadTimeout and the caller's cancellation: the
// detail and Git-link reads never depend on a lazy watch's store. NotFound
// is believed only after one re-probe; forbidden and transport failures stay
// errors of their own (the object is unverified, not gone).
func (c *ClusterConn) FluxObject(ctx context.Context, kind, ns, name string) (*unstructured.Unstructured, error) {
	group, fallback, resource, ok := fluxGVR(kind)
	if !ok {
		return nil, fmt.Errorf("%s is not a Flux kind Klyx reads", kind)
	}
	ctx, cancel := context.WithTimeout(ctx, detailReadTimeout)
	defer cancel()
	return c.getServedObject(ctx, group, resource, fallback, ns, name)
}
