package fleet

import (
	"context"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// paramKindsTimeout bounds one discovery of a param kind's group/version.
const paramKindsTimeout = 10 * time.Second

// startParamKinds keeps FactParamKinds (coverage S3a §6): the policies'
// paramKinds, each from a completed discovery of its exact group and
// version. It follows both policy kinds through the Observer and runs on
// their changes and on every health sync (the capability re-probe). A cold
// or failed discovery is unknown, never "not served".
func (c *ClusterConn) startParamKinds(ctx context.Context, h *observe.Observer) {
	kick := make(chan struct{}, 1)
	signal := func(observe.KindState) {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	h.WatchKind(healthsrc.KindValidatingPolicies, signal)
	h.WatchKind(healthsrc.KindMutatingPolicies, signal)
	c.mu.Lock()
	c.paramKindsKick = func() { signal(observe.KindState{}) }
	c.mu.Unlock()
	c.goTracked(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-kick:
			}
			// every resolution is set, so its read time stays current
			if pk := c.resolveParamKinds(ctx, h); pk != nil {
				h.SetFact(healthsrc.FactParamKinds, pk)
			}
		}
	})
}

// referencedParamKinds is every exact group/version/kind the policies name,
// or nil when neither policy kind has a complete read yet.
func referencedParamKinds(h *observe.Observer) map[string][3]string {
	out := map[string][3]string{}
	read := false
	for _, kind := range []string{healthsrc.KindValidatingPolicies, healthsrc.KindMutatingPolicies} {
		st, _, ok := h.VisitKind(kind, func(obj any) bool {
			if p, ok := obj.(*admission.Policy); ok && p.ParamKind != nil {
				if g, v, k, ok := p.ParamKind.GVK(); ok {
					out[admission.KeyOf(g, v, k)] = [3]string{g, v, k}
				}
			}
			return true
		})
		if ok && (st.CountKnown || st.Coverage == observe.Absent) {
			read = true
		}
	}
	if !read {
		return nil
	}
	return out
}

func (c *ClusterConn) resolveParamKinds(ctx context.Context, h *observe.Observer) admission.ParamKinds {
	refs := referencedParamKinds(h)
	if refs == nil {
		return nil
	}
	return resolveParamKindsWith(ctx, refs, c.probeDiscovery(), c.clk.Now)
}

// resolveParamKindsWith discovers each referenced kind's exact group and
// version (S3a §6): served with its scope, not served (the group/version is
// NotFound, or discovered without the kind), or unknown when discovery
// fails - never "not served" from a failure.
func resolveParamKindsWith(ctx context.Context, refs map[string][3]string, disc *cachedDiscovery, now func() time.Time) admission.ParamKinds {
	out := admission.ParamKinds{}
	byGV := map[string][]string{}
	for key, r := range refs {
		gv := r[1]
		if r[0] != "" {
			gv = r[0] + "/" + r[1]
		}
		byGV[gv] = append(byGV[gv], key)
	}
	gvs := make([]string, 0, len(byGV))
	for gv := range byGV {
		gvs = append(gvs, gv)
	}
	sort.Strings(gvs)
	for _, gv := range gvs {
		rctx, cancel := context.WithTimeout(ctx, paramKindsTimeout)
		rl, err := disc.ServerResourcesForGroupVersionCtx(rctx, gv)
		cancel()
		at := now()
		for _, key := range byGV[gv] {
			kind := refs[key][2]
			switch {
			case apierrors.IsNotFound(err):
				out[key] = admission.ParamKindState{State: admission.ParamNotServed, At: at}
			case err != nil:
				out[key] = admission.ParamKindState{State: admission.ParamUnknown, At: at, Error: "discovery of " + gv + " failed"}
			default:
				st := admission.ParamKindState{State: admission.ParamNotServed, At: at}
				for _, r := range rl.APIResources {
					if r.Kind == kind && !containsSlash(r.Name) {
						st = admission.ParamKindState{State: admission.ParamServed, Namespaced: r.Namespaced, Resource: r.Name, At: at}
						break
					}
				}
				out[key] = st
			}
		}
	}
	return out
}

func containsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}
