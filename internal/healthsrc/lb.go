package healthsrc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/lb"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// Coverage S4: the load-balancing task - LoadBalancer Services through the
// shared Services kind (C3's loadbalancing consumer), MetalLB and Cilium
// pools with MetalLB's advertisements, and MetalLB's ConfigurationStates.
const (
	TaskLB           = "lb"
	KindMetalLBPools = lb.ResourceMetalLBPools
	KindL2Ads        = lb.ResourceL2Ads
	KindBGPAds       = lb.ResourceBGPAds
	KindConfigStates = lb.ResourceConfigStates
	KindCiliumPools  = lb.ResourceCiliumPools
	// FactLBEpisodes is the pending-episode tracker's output (S4 §3), a
	// map[string]lb.Episode keyed by Service UID; FactLB is LBFact.
	FactLBEpisodes = "lbEpisodes"
	FactLB         = "lbControllers"

	lbServicesEntity   = "LoadBalancer Services"
	lbPoolsEntity      = "Address pools"
	lbStatesEntity     = "MetalLB configuration states"
	lbServicesIncluded = "without an address or degraded"
	lbPoolsIncluded    = "in conflict, exhausted or not advertised"
	lbStatesIncluded   = "invalid"
)

// LBFact is what capability detection says of the load-balancer
// controllers (S4 §2): MetalLB's CRDs, its controller workloads'
// namespaces (an instance namespace's fallback evidence, §5) and whether
// that list was read, and the AWS Load Balancer Controller's CRDs.
type LBFact struct {
	MetalLB         bool
	ControllersRead bool
	Namespaces      []string
	AWSLBC          bool
}

// LoadBalancingFact derives LBFact from the capability.
func LoadBalancingFact(c capability.LoadBalancingCapability) LBFact {
	f := LBFact{MetalLB: c.MetalLB, ControllersRead: c.ControllersRead, AWSLBC: c.AWSLBC}
	seen := map[string]bool{}
	for _, ctl := range c.Controllers {
		if !seen[ctl.Namespace] {
			seen[ctl.Namespace] = true
			f.Namespaces = append(f.Namespaces, ctl.Namespace)
		}
	}
	sort.Strings(f.Namespaces)
	return f
}

// LoadBalancing returns the S4 kinds and task (read-only).
func LoadBalancing(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	metal := func(resource string) observe.KindSpec {
		return oneVersionKind(d, resource, lb.GroupMetalLB, lb.VersionMetalLB, 0, lb.Project(resource), 0, nil)
	}
	cilium := oneVersionKind(d, KindCiliumPools, lb.GroupCilium, lb.VersionCilium, 0, lb.Project(KindCiliumPools), 0, nil)
	cilium.Scope = "cluster-scoped"
	cilium.Probe = func(ctx context.Context) (observe.Probe, error) {
		p, err := d.Served(ctx, lb.GroupCilium, KindCiliumPools, lb.VersionCilium)
		if err == nil && p.Served && p.Version != lb.VersionCilium && p.Version != "v2alpha1" {
			return observe.Probe{}, fmt.Errorf("unsupported version %s for %s: Klyx reads v2 and v2alpha1", p.Version, KindCiliumPools)
		}
		return p, err
	}
	kinds := []observe.KindSpec{metal(KindMetalLBPools), metal(KindL2Ads), metal(KindBGPAds), metal(KindConfigStates), cilium}
	c := d.Cluster
	domains := []observe.DomainSpec{{ID: TaskLB, Parts: []observe.PartSpec{
		{ID: "services", Entity: lbServicesEntity, Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindServices},
			Deps:    []string{KindDaemonSets, KindPods, KindNodes, SnapEvents, KindMetalLBPools, KindCiliumPools, KindConfigStates},
			Facts:   []string{FactLBEpisodes, FactLB}, Consumer: ConsumerLoadBalancing, Evaluate: evalLBServices(c), Ledger: true},
		{ID: "pools", Entity: lbPoolsEntity, Scope: "all namespaces + cluster-scoped", Classifier: "native",
			Counted: []string{KindMetalLBPools, KindCiliumPools}, Deps: []string{KindL2Ads, KindBGPAds, KindConfigStates},
			Facts: []string{FactLB}, Evaluate: evalLBPools(c), Ledger: true},
		{ID: "metallb", Entity: lbStatesEntity, Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindConfigStates}, Evaluate: evalLBStates(c), Ledger: true},
	}}}
	return kinds, domains
}

// readLB is how a part read one kind.
func readLB(c observe.Capture, kind string) lb.Read {
	_, has := c.Objects[kind]
	switch {
	case c.Complete[kind]:
		return lb.ReadComplete
	case has:
		return lb.ReadStale
	}
	return lb.ReadNone
}

// servedIn reports a kind the capture holds objects for (live or stale):
// served, as opposed to absent or unread.
func servedIn(c observe.Capture, kind string) bool {
	_, has := c.Objects[kind]
	return has
}

func lbConfigStates(c observe.Capture) []*lb.ConfigState {
	var out []*lb.ConfigState
	for _, o := range c.Objects[KindConfigStates] {
		if s, ok := o.(*lb.ConfigState); ok {
			out = append(out, s)
		}
	}
	return out
}

func lbRef(m lb.Meta, group, version, resource string) refInput {
	v := m.APIVersion
	if v == "" {
		v = group + "/" + version
	}
	return refInput{gvr: v + "/" + resource, ns: m.Namespace, name: m.Name, uid: m.UID, generation: m.Generation, source: resource}
}

// LBServiceRow is one LoadBalancer Service with its allocation, its
// pending episode and diagnosis (when it has no address), and the
// controllers its class points at.
type LBServiceRow struct {
	Service    *corev1.Service
	Allocation lb.Allocation
	Expected   lb.Expected
	// Episode is set while pending: Observed false means its Start is a
	// lower bound, the first observation.
	Episode       *lb.Episode
	Diagnosis     *lb.Diagnosis
	MissingFamily string
	Cilium        *lb.ServiceCond
}

// lbEnv reads what the capture shows of the controllers (S4 §4).
func lbEnv(c observe.Capture, f LBFact) lb.Env {
	env := lb.Env{MetalLB: f.MetalLB || servedIn(c, KindMetalLBPools), CiliumLB: servedIn(c, KindCiliumPools),
		AWSLBC: f.AWSLBC, NodesKnown: c.Complete[KindNodes]}
	var nodes []*corev1.Node
	for _, o := range c.Objects[KindNodes] {
		if n, ok := o.(*corev1.Node); ok {
			nodes = append(nodes, n)
		}
	}
	env.K3s, env.Cloud = lb.K3sOf(nodes), lb.CloudOf(nodes)
	for _, o := range c.Objects[KindPods] {
		if p, ok := o.(*corev1.Pod); ok && kubeVIPPod(p) {
			env.KubeVIP = true
			break
		}
	}
	return env
}

// kubeVIPPod: a pod running the kube-vip image (its DaemonSet's or a
// static pod's).
func kubeVIPPod(p *corev1.Pod) bool {
	for _, ct := range p.Spec.Containers {
		if strings.Contains(ct.Image, "kube-vip/kube-vip") || strings.Contains(ct.Image, "/kube-vip:") {
			return true
		}
	}
	return false
}

// lbJoins indexes one capture's evidence for the Service diagnosis.
type lbJoins struct {
	events     map[string][]lb.Event
	eventsRead string
	svclbDS    map[string]string // kube-system DaemonSet name -> UID
	svclbPods  []*corev1.Pod
	svclbRead  bool
	pools      map[string]bool // ns/name of MetalLB pools read
	poolsRead  lb.Read
	instances  lb.Instances
	noPools    []string
}

func lbJoinsOf(c observe.Capture, f LBFact, env lb.Env) lbJoins {
	j := lbJoins{events: map[string][]lb.Event{}, svclbDS: map[string]string{}, pools: map[string]bool{}}
	switch _, has := c.Objects[SnapEvents]; {
	case c.Complete[SnapEvents]:
		j.eventsRead = "complete"
	case has:
		j.eventsRead = "partial" // capped or stale: said, never silence
	default:
		j.eventsRead = "unverified"
	}
	for _, o := range c.Objects[SnapEvents] {
		e, ok := o.(workloads.EventSummary)
		if !ok || e.Kind != "Service" || e.UID == "" {
			continue // a name alone can be a replacement's (S4 §4)
		}
		j.events[e.UID] = append(j.events[e.UID], lb.Event{Reason: clipText(e.Reason, 128), Message: clipText(e.Message, 300), Count: e.Count, LastSeen: e.LastSeenUnix})
	}
	for _, evs := range j.events {
		sort.SliceStable(evs, func(a, b int) bool { return evs[a].LastSeen > evs[b].LastSeen })
	}
	for _, o := range c.Objects[KindDaemonSets] {
		if ds, ok := o.(*ProjectedDaemonSet); ok && ds.Namespace == lb.SvcLBNamespace && strings.HasPrefix(ds.Name, "svclb-") {
			j.svclbDS[ds.Name] = string(ds.UID)
		}
	}
	for _, o := range c.Objects[KindPods] {
		if p, ok := o.(*corev1.Pod); ok && p.Namespace == lb.SvcLBNamespace && strings.HasPrefix(p.Name, "svclb-") {
			j.svclbPods = append(j.svclbPods, p)
		}
	}
	j.svclbRead = c.Complete[KindDaemonSets] && c.Complete[KindPods]
	for _, o := range c.Objects[KindMetalLBPools] {
		if p, ok := o.(*lb.MetalLBPool); ok {
			j.pools[p.Namespace+"/"+p.Name] = true
		}
	}
	j.poolsRead = readLB(c, KindMetalLBPools)
	j.instances = lb.InstancesOf(lbConfigStates(c), readLB(c, KindConfigStates), f.Namespaces, f.ControllersRead)
	if env.MetalLB && j.poolsRead == lb.ReadComplete && len(c.Objects[KindMetalLBPools]) == 0 && servedIn(c, KindMetalLBPools) {
		j.noPools = append(j.noPools, lb.CtlMetalLB)
	}
	if env.CiliumLB && readLB(c, KindCiliumPools) == lb.ReadComplete && len(c.Objects[KindCiliumPools]) == 0 {
		j.noPools = append(j.noPools, lb.CtlCiliumLB)
	}
	return j
}

// requestedPool joins the pool a Service names inside MetalLB's one
// instance namespace; with none or several known it stays unverified.
func (j lbJoins) requestedPool(s *corev1.Service) *lb.RequestedPool {
	name := lb.PoolAnnotation(s)
	if name == "" {
		return nil
	}
	rp := &lb.RequestedPool{Name: name, State: "unverified"}
	if len(j.instances.Namespaces) != 1 {
		return rp
	}
	for ns := range j.instances.Namespaces {
		rp.Namespace = ns
	}
	switch {
	case j.pools[rp.Namespace+"/"+name]:
		rp.State = "found"
	case j.poolsRead == lb.ReadComplete:
		rp.State = "missing"
	}
	return rp
}

func evalLBServices(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		eps, _ := c.Facts[FactLBEpisodes].(map[string]lb.Episode)
		f, _ := c.Facts[FactLB].(LBFact)
		env := lbEnv(c, f)
		var j *lbJoins
		var items []nativeItem
		var rows []Row[LBServiceRow]
		for _, o := range c.Objects[KindServices] {
			s, ok := o.(*corev1.Service)
			if !ok || !lb.IsLoadBalancer(s) {
				continue
			}
			a := lb.AllocationOf(s)
			row := LBServiceRow{Service: s, Allocation: a, Expected: lb.ExpectedFor(lb.Class(s), env), MissingFamily: lb.MissingFamily(s, a), Cilium: lb.CiliumSatisfied(s)}
			var ep lb.Episode
			reason := ""
			if !a.Allocated {
				if e, ok := eps[string(s.UID)]; ok {
					ep = e
				} else {
					ep = lb.Episode{Start: c.Now} // the tracker has not seen it yet: a lower bound
				}
				if j == nil {
					jj := lbJoinsOf(c, f, env)
					j = &jj
				}
				var svclb *lb.SvcLB
				if env.K3s || len(j.svclbDS) > 0 {
					name := lb.SvcLBName(s)
					svclb = lb.SvcLBOf(s, name, j.svclbDS[name], j.svclbPods)
				}
				d := lb.Diagnose(lb.DiagnosisInput{Svc: s, Env: env, Events: j.events[string(s.UID)], EventsRead: j.eventsRead,
					SvcLB: svclb, SvcLBRead: j.svclbRead, Pool: j.requestedPool(s), NoPools: append([]string(nil), j.noPools...)})
				row.Episode, row.Diagnosis, reason = &ep, &d, d.Summary
			}
			it := nativeItem{lb.ServiceRule(s, a, ep, c.Now, reason),
				refInput{gvr: "v1/services", ns: s.Namespace, name: s.Name, uid: string(s.UID), generation: s.Generation, source: KindServices}}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, row))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskLB, lbServicesIncluded, items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// LBPoolRow is one MetalLB or Cilium pool (exactly one is set) with its
// evidence from the same capture.
type LBPoolRow struct {
	Cilium *lb.CiliumPool
	Counts *lb.CiliumCounts
	// Conflict is the PoolConflict condition and whether it is current.
	Conflict        *lb.Cond
	ConflictCurrent bool

	MetalLB   *lb.MetalLBPool
	Selection *lb.Selection
	// AdsRead and StatesRead: "complete", "stale" or "unread".
	AdsRead    string
	StatesRead string
	// Instance is how its namespace is known to be MetalLB's: "state" (a
	// controller ConfigurationState), "workload" (a controller Deployment,
	// which a flag can move) or "" (not known).
	Instance string
	Invalid  []string // the namespace's Invalid ConfigurationStates
}

func readWordLB(r lb.Read) string {
	switch r {
	case lb.ReadComplete:
		return "complete"
	case lb.ReadStale:
		return "stale"
	}
	return "unread"
}

func evalLBPools(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		f, _ := c.Facts[FactLB].(LBFact)
		states := lbConfigStates(c)
		statesRead := readLB(c, KindConfigStates)
		instances := lb.InstancesOf(states, statesRead, f.Namespaces, f.ControllersRead)
		var ads []*lb.Advertisement
		adsRead := lb.ReadComplete
		for _, kind := range []string{KindL2Ads, KindBGPAds} {
			if r := readLB(c, kind); r < adsRead {
				adsRead = r
			}
			for _, o := range c.Objects[kind] {
				if a, ok := o.(*lb.Advertisement); ok {
					ads = append(ads, a)
				}
			}
		}
		var items []nativeItem
		var rows []Row[LBPoolRow]
		for _, o := range c.Objects[KindCiliumPools] {
			p, ok := o.(*lb.CiliumPool)
			if !ok {
				continue
			}
			it := nativeItem{lb.CiliumPoolRule(p), lbRef(p.Meta, lb.GroupCilium, lb.VersionCilium, KindCiliumPools)}
			items = append(items, it)
			if c.Rows {
				n := lb.CountsOf(p)
				cond, cur := p.Conflict()
				rows = append(rows, nativeRow(it, LBPoolRow{Cilium: p, Counts: &n, Conflict: cond, ConflictCurrent: cur}))
			}
		}
		for _, o := range c.Objects[KindMetalLBPools] {
			p, ok := o.(*lb.MetalLBPool)
			if !ok {
				continue
			}
			in := lb.MetalLBPoolInput{Pool: p, Instances: instances, Ads: ads, AdsRead: adsRead, States: states, StatesRead: statesRead}
			it := nativeItem{lb.MetalLBPoolRule(in), lbRef(p.Meta, lb.GroupMetalLB, lb.VersionMetalLB, KindMetalLBPools)}
			items = append(items, it)
			if c.Rows {
				sel := lb.SelectionOf(ads, p)
				row := LBPoolRow{MetalLB: p, Selection: &sel, AdsRead: readWordLB(adsRead), StatesRead: readWordLB(statesRead)}
				switch {
				case instances.Has(p.Namespace) && !instances.FromWorkload[p.Namespace]:
					row.Instance = "state"
				case instances.Has(p.Namespace):
					row.Instance = "workload"
				}
				for _, s := range lb.InvalidStates(states, p.Namespace) {
					row.Invalid = append(row.Invalid, s.Name)
				}
				rows = append(rows, nativeRow(it, row))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskLB, lbPoolsIncluded, items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// LBStateRow is one MetalLB ConfigurationState.
type LBStateRow struct{ State *lb.ConfigState }

func evalLBStates(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[LBStateRow]
		for _, s := range lbConfigStates(c) {
			it := nativeItem{lb.ConfigStateRule(s), lbRef(s.Meta, lb.GroupMetalLB, lb.VersionMetalLB, KindConfigStates)}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, LBStateRow{State: s}))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskLB, lbStatesIncluded, items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}
