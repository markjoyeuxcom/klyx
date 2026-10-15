package fleet

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/lastwrite"
)

// ObjectRef names one object for a managed-field read, pinned to its UID,
// which is required: a name alone could read a replacement. An empty
// Version takes the group's served preferred version.
type ObjectRef struct {
	Group, Version, Kind, Namespace, Name, UID string
}

// WriteResult is one reference's last recorded managed-field update.
type WriteResult struct {
	Ref    ObjectRef
	State  string // ok | no record | replaced | gone | error
	Writes lastwrite.Writes
	Error  string
}

// Write read states.
const (
	WritesOK       = "ok"
	WritesNone     = "no record"
	WritesReplaced = "replaced"
	WritesGone     = "gone"
	WritesError    = "error"
)

// The bounds of one batch (slice 7 note §2).
const (
	MaxWriteRefs    = 200
	writesParallel  = 8
	writesBatchTime = 10 * time.Second
)

// ObjectWrites reads each reference's managed fields with one metadata GET
// (PartialObjectMetadata): metadata only, UID-pinned. A failed read is that
// reference's error, never "no record" (The Line parent §8.3, slice 7a).
func (c *ClusterConn) ObjectWrites(ctx context.Context, refs []ObjectRef) ([]WriteResult, error) {
	if len(refs) > MaxWriteRefs {
		return nil, fmt.Errorf("at most %d objects per read, not %d", MaxWriteRefs, len(refs))
	}
	if c.meta == nil {
		return nil, fmt.Errorf("no metadata client for %s", c.name)
	}
	ctx, cancel := context.WithTimeout(ctx, writesBatchTime)
	defer cancel()
	out := make([]WriteResult, len(refs))
	sem := make(chan struct{}, writesParallel)
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = c.objectWrites(ctx, ref)
		}()
	}
	wg.Wait()
	return out, nil
}

func (c *ClusterConn) objectWrites(ctx context.Context, ref ObjectRef) WriteResult {
	r := WriteResult{Ref: ref}
	if ref.UID == "" {
		// Never a name-only read reported as pinned (design review of 74175e6).
		r.State, r.Error = WritesError, "no UID to pin the read to"
		return r
	}
	gvr, namespaced, err := c.resourceFor(ctx, ref)
	if err != nil {
		r.State, r.Error = WritesError, err.Error()
		return r
	}
	res := c.meta.Resource(gvr)
	get := res.Get
	if namespaced {
		get = res.Namespace(ref.Namespace).Get
	}
	m, err := get(ctx, ref.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		r.State = WritesGone
		return r
	case err != nil:
		r.State, r.Error = WritesError, err.Error()
		return r
	case string(m.GetUID()) != ref.UID:
		r.State = WritesReplaced
		return r
	}
	r.Writes = lastwrite.Read(m.GetManagedFields())
	r.State = WritesOK
	if r.Writes.Last == nil {
		r.State = WritesNone
	}
	return r
}

// resourceFor resolves a reference's kind to its served resource through the
// cached, context-aware discovery: the requested version, else the group's
// preferred version, else any other advertised version that serves the kind
// (the Flux source group prefers v1 while OCIRepository is served only at
// v1beta2; servedVersion's rule, per kind). The caller's context ends the
// discovery reads too, so a cancelled walk is never held by a cold cache.
func (c *ClusterConn) resourceFor(ctx context.Context, ref ObjectRef) (schema.GroupVersionResource, bool, error) {
	d := c.probeDiscovery()
	versions := []string{}
	if ref.Version != "" {
		versions = append(versions, ref.Version)
	} else {
		groups, err := d.ServerGroupsCtx(ctx)
		if ctx.Err() != nil {
			return schema.GroupVersionResource{}, false, ctx.Err()
		}
		if err == nil && groups != nil {
			for _, g := range groups.Groups {
				if g.Name != ref.Group {
					continue
				}
				if g.PreferredVersion.Version != "" {
					versions = append(versions, g.PreferredVersion.Version)
				}
				for _, v := range g.Versions {
					if v.Version != g.PreferredVersion.Version {
						versions = append(versions, v.Version)
					}
				}
			}
		}
		if len(versions) == 0 {
			versions = append(versions, "v1") // the group is not advertised (fake discovery in tests)
		}
	}
	var unreadable []string
	for _, v := range versions {
		gv := schema.GroupVersion{Group: ref.Group, Version: v}
		list, err := d.ServerResourcesForGroupVersionCtx(ctx, gv.String())
		if ctx.Err() != nil {
			return schema.GroupVersionResource{}, false, ctx.Err()
		}
		if err != nil || list == nil {
			unreadable = append(unreadable, fmt.Sprintf("%s: %v", gv, err))
			continue
		}
		for _, r := range list.APIResources {
			if r.Kind == ref.Kind && !strings.Contains(r.Name, "/") {
				return gv.WithResource(r.Name), r.Namespaced, nil
			}
		}
	}
	if len(unreadable) > 0 {
		return schema.GroupVersionResource{}, false, fmt.Errorf("%s %s is not served by any readable version (%s)", ref.Group, ref.Kind, strings.Join(unreadable, "; "))
	}
	return schema.GroupVersionResource{}, false, fmt.Errorf("%s %s is not served", ref.Group, ref.Kind)
}
