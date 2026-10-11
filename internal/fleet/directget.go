package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Direct detail reads (1c slice 3 design §5b): a detail endpoint reads its
// object with one GET at the kind's served version, never from a lazy
// watch's store.

// detailReadTimeout bounds one direct detail read.
const detailReadTimeout = 15 * time.Second

// getServedObject GETs one object of a CRD-backed kind at its served
// version (group/resource, fallback when discovery names none). A NotFound
// re-probes the version once - the served version may have moved since the
// cached discovery - before it is believed. NotFound (the object is gone)
// stays distinguishable from forbidden and transport failures (unverified).
func (c *ClusterConn) getServedObject(ctx context.Context, group, resource, fallback, ns, name string) (*unstructured.Unstructured, error) {
	if c.dyn == nil {
		return nil, errors.New("no cluster connection")
	}
	get := func(version string) (*unstructured.Unstructured, error) {
		r := c.dyn.Resource(schema.GroupVersionResource{Group: group, Version: version, Resource: resource})
		if ns == "" {
			return r.Get(ctx, name, metav1.GetOptions{})
		}
		return r.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	}
	p, err := c.crdServed(ctx, group, resource, fallback)
	if err != nil {
		return nil, err
	}
	if !p.Served {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: group, Resource: resource}, name)
	}
	u, err := get(p.Version)
	if !apierrors.IsNotFound(err) {
		return u, err
	}
	// NotFound is believed only after discovery confirms the version: a
	// re-probe that cannot be read (forbidden, transport, deadline) leaves
	// the object unverified, never confirmed missing (Codex review #4).
	c.probeDiscovery().invalidate(group)
	p2, perr := c.crdServed(ctx, group, resource, fallback)
	switch {
	case perr != nil:
		return nil, fmt.Errorf("re-checking the served version after a NotFound: %w", perr)
	case !p2.Served:
		return nil, err // confirmed: the kind is not served at all
	case p2.Version != p.Version:
		return get(p2.Version)
	}
	return nil, err // confirmed at the same version
}
