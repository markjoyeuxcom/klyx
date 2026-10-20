package healthsrc

import (
	"context"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
)

// DRA's kinds, task and facts (coverage S11b §2). The kinds are read at
// resource.k8s.io/v1 only; a cluster serving DRA only at a beta reads as a
// named version gap.
const (
	KindResourceSlices = "resourceslices"
	KindResourceClaims = "resourceclaims"
	KindDeviceClasses  = "deviceclasses"

	TaskDRA = "dra"

	// FactServerMinor carries the API server's minor version (0 unknown);
	// the pool rules allow for it and one older (S11b §3.2).
	FactServerMinor = "serverMinor"
	// FactDRAEpisodes carries each incomplete pool's first observation,
	// keyed by dra.EpisodeKey.
	FactDRAEpisodes = "draEpisodes"

	draGroup = "resource.k8s.io"
)

// The budget per cluster (S11b §2.0).
const (
	budgetResourceSlices = 2 << 20
	budgetResourceClaims = 2 << 20
	budgetDeviceClasses  = 256 << 10
)

// DRA returns its kinds and its task (read-only; S11b).
func DRA(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	kind := func(resource, scope string, budget int) observe.KindSpec {
		ks := crdKind(d, resource, draGroup, resource, "v1", scope)
		ks.Probe = func(ctx context.Context) (observe.Probe, error) {
			return d.servedAmong(ctx, draGroup, resource, []string{"v1"})
		}
		ks.Project, ks.Budget, ks.MaxObject, ks.Size = dra.Project(resource), budget, dra.MaxObject(resource), dra.SizeOf
		return ks
	}
	kinds := []observe.KindSpec{
		kind(KindResourceSlices, "cluster-scoped", budgetResourceSlices),
		kind(KindResourceClaims, "all namespaces", budgetResourceClaims),
		kind(KindDeviceClasses, "cluster-scoped", budgetDeviceClasses),
	}
	c := d.Cluster
	domains := []observe.DomainSpec{{ID: TaskDRA, Parts: []observe.PartSpec{
		{ID: "claims", Entity: "ResourceClaims", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindResourceClaims}, Deps: []string{KindPods}, Evaluate: evalDRAClaims(c), Ledger: true},
		{ID: "pools", Entity: "device pools", Scope: "cluster-scoped", Classifier: "native",
			Counted: []string{KindResourceSlices}, Facts: []string{FactServerMinor, FactDRAEpisodes}, Evaluate: evalDRAPools(c), Ledger: true},
		{ID: "classes", Entity: "DeviceClasses", Scope: "cluster-scoped", Classifier: "evidence",
			Counted: []string{KindDeviceClasses}, Evaluate: evalDRAClasses},
	}}}
	return kinds, domains
}

// nativeOf turns a reading into the native evaluation the ledger keeps.
func nativeOf(v health.Verdict) health.NativeEval {
	e := health.NativeEval{Role: v.Kind, Label: v.Label, Next: v.ReclassifyAt}
	e.Included = v.Kind == health.Fault || v.Kind == health.Degraded
	if v.Kind != health.OK && v.Kind != health.Unknown {
		e.Reason = health.CapReason(v.Reason, v.Message)
	}
	return e
}

func objectsOf[T any](objs []any) []T {
	out := make([]T, 0, len(objs))
	for _, o := range objs {
		if s, ok := o.(*dra.Sized[T]); ok {
			out = append(out, s.V)
		}
	}
	return out
}

// ---- claims ---------------------------------------------------------------

// DRAClaimRow is one claim with its reading and, for a pending claim, each
// consumer's row of the decision table (at most 8 kept).
type DRAClaimRow struct {
	Claim            dra.Claim
	Verdict          health.Verdict
	Consumers        []dra.ConsumerReading
	ConsumersOmitted int
}

// claimConsumers indexes the non-terminal pods by the claims they
// reference (dra.PodClaims), with each pod's own claims and their classes.
// A template's claim counts only when the pod is its controller.
func claimConsumers(pods []*corev1.Pod, claims map[string]dra.Claim, claimsKnown bool) map[string][]dra.ConsumerPod {
	out := map[string][]dra.ConsumerPod{}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		refs := dra.PodClaims(p)
		if len(refs) == 0 {
			continue
		}
		cp := dra.ConsumerPod{Pod: p, Classes: map[string][]string{}, Complete: claimsKnown}
		for _, r := range refs {
			switch r.State {
			case dra.RefNoneNeeded:
				continue
			case dra.RefNotCreated:
				cp.Complete = false
				continue
			}
			key := p.Namespace + "/" + r.Claim
			cl, ok := claims[key]
			if !ok || (r.Source == "template" && !cl.OwnedBy(p)) {
				cp.Complete = false
				continue
			}
			cp.Claims = append(cp.Claims, key)
			cp.Classes[key] = cl.Classes()
		}
		for _, key := range cp.Claims {
			out[key] = append(out[key], cp)
		}
	}
	return out
}

func evalDRAClaims(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		claims := objectsOf[dra.Claim](c.Objects[KindResourceClaims])
		byKey := make(map[string]dra.Claim, len(claims))
		for _, cl := range claims {
			byKey[cl.Key()] = cl
		}
		var pods []*corev1.Pod
		for _, x := range c.Objects[KindPods] {
			if p, ok := x.(*corev1.Pod); ok {
				pods = append(pods, p)
			}
		}
		podsKnown := c.Complete[KindPods] && c.Current[KindPods]
		claimsKnown := c.Complete[KindResourceClaims] && c.Current[KindResourceClaims]
		consumers := claimConsumers(pods, byKey, claimsKnown)
		var items []nativeItem
		var rows []Row[DRAClaimRow]
		for _, cl := range claims {
			v, readings := dra.ClaimVerdict(cl, consumers[cl.Key()], podsKnown, c.Now)
			it := nativeItem{nativeOf(v), refInput{gvr: draGroup + "/v1/" + KindResourceClaims, ns: cl.Namespace, name: cl.Name, uid: cl.UID, source: KindResourceClaims}}
			items = append(items, it)
			row := DRAClaimRow{Claim: cl, Verdict: v, Consumers: readings}
			if len(row.Consumers) > dra.MaxReservedKept {
				row.ConsumersOmitted = len(row.Consumers) - dra.MaxReservedKept
				row.Consumers = append([]dra.ConsumerReading(nil), row.Consumers[:dra.MaxReservedKept]...)
			}
			rows = append(rows, nativeRow(it, row))
		}
		res := nativeResult(c.RoleCap, cluster, TaskDRA, "cannot be allocated or appear stuck", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// ---- pools ----------------------------------------------------------------

// DRAPoolRow is one pool's reading: its views under every allowed
// scheduler, and its verdict.
type DRAPoolRow struct {
	Pool    dra.PoolRead
	Verdict health.Verdict
	// Unread: the slices read was capped, so no pool is established.
	Unread bool
}

// slicePools groups slices by driver and pool, in the capture's order.
func slicePools(slices []dra.Slice) ([]string, map[string][]dra.Slice) {
	var order []string
	by := map[string][]dra.Slice{}
	for _, s := range slices {
		k := s.Driver + "/" + s.Pool
		if _, ok := by[k]; !ok {
			order = append(order, k)
		}
		by[k] = append(by[k], s)
	}
	sort.Strings(order)
	return order, by
}

func evalDRAPools(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		minor, _ := c.Facts[FactServerMinor].(int)
		episodes, _ := c.Facts[FactDRAEpisodes].(map[string]time.Time)
		// a capped read cannot show a pool whole, fresh or stale: a missing
		// slice may be Klyx's cut or a newer generation (S11b §2.1); a
		// stale whole read stays a dated reading
		_, stale := c.AsOf[KindResourceSlices]
		unread := c.Capped[KindResourceSlices] || (!c.Complete[KindResourceSlices] && !stale)
		order, by := slicePools(objectsOf[dra.Slice](c.Objects[KindResourceSlices]))
		var items []nativeItem
		var rows []Row[DRAPoolRow]
		for _, key := range order {
			p := dra.ReadPool(by[key], minor)
			var v health.Verdict
			if unread {
				v = health.Verdict{Kind: health.Unknown, Label: "Not established", Reason: "SlicesCapped", Rule: "pool.unread",
					Message: "not every slice was read (the slices budget): a missing slice may be Klyx's cut or a newer generation"}
			} else {
				v = p.Verdict(episodes[dra.EpisodeKey(p.Driver, p.Pool, p.Generation)], c.Now)
			}
			it := nativeItem{nativeOf(v), refInput{name: key, source: KindResourceSlices}}
			items = append(items, it)
			// read from every view, kept bounded (S11b §8)
			rows = append(rows, nativeRow(it, DRAPoolRow{Pool: p.Kept(dra.MaxViewsKept), Verdict: v, Unread: unread}))
		}
		res := nativeResult(c.RoleCap, cluster, TaskDRA, "are incomplete or invalid", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// IncompletePools are the pools any allowed scheduler finds being updated
// on any view, by episode key, each with its slices' fingerprint (every
// slice's UID and resourceVersion): the episode tracker's input. An equal
// fingerprint proves the pool did not change between two reads.
func IncompletePools(slices []dra.Slice, minor int) map[string]string {
	order, by := slicePools(slices)
	out := map[string]string{}
	for _, key := range order {
		p := dra.ReadPool(by[key], minor)
		if p.AnyUpdating() {
			ids := make([]string, 0, len(by[key]))
			for _, s := range by[key] {
				ids = append(ids, s.UID+"@"+s.RV)
			}
			sort.Strings(ids)
			out[dra.EpisodeKey(p.Driver, p.Pool, p.Generation)] = strings.Join(ids, ",")
		}
	}
	return out
}

// SlicesOf turns a visit's objects into slices.
func SlicesOf(objs []any) []dra.Slice { return objectsOf[dra.Slice](objs) }

// ---- classes --------------------------------------------------------------

// DRAClassRow is one class's facts.
type DRAClassRow struct{ Class dra.Class }

// FactOnly: a class row is a fact, never a verdict.
func (DRAClassRow) FactOnly() bool { return true }

func evalDRAClasses(c observe.Capture) observe.PartResult {
	classes := objectsOf[dra.Class](c.Objects[KindDeviceClasses])
	n := len(classes)
	out := observe.PartResult{Evidence: &n}
	if c.Rows {
		rows := make([]Row[DRAClassRow], 0, len(classes))
		for _, cl := range classes {
			rows = append(rows, Row[DRAClassRow]{Ref: RowRef{UID: cl.UID, GVR: draGroup + "/v1/" + KindDeviceClasses, Name: cl.Name}, Data: DRAClassRow{cl}})
		}
		out.Rows = rowSet(rows)
	}
	return out
}

// MinorOf reads a GitVersion's minor ("v1.35.2-eks-..." is 35); 0 when it
// cannot.
func MinorOf(gitVersion string) int {
	v := strings.TrimPrefix(gitVersion, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 || parts[0] != "1" {
		return 0
	}
	n := 0
	for _, r := range parts[1] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
