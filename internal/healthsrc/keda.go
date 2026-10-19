package healthsrc

import (
	"sort"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
	"github.com/moomora/klyx/internal/observe"
)

// KEDA's kinds, task and facts (coverage S9 §2). The kinds are named by
// their resources, as every CRD kind is.
const (
	KindScaledObjects       = keda.ResourceScaledObjects
	KindScaledJobs          = keda.ResourceScaledJobs
	KindTriggerAuths        = keda.ResourceTriggerAuths
	KindClusterTriggerAuths = keda.ResourceClusterTriggerAuths

	TaskKEDA = "keda"

	// FactKEDA carries the running version (KEDAFacts); FactKEDAEpisodes the
	// episode tracker's open episodes (S9 §3.1), by scaler UID.
	FactKEDA         = "keda"
	FactKEDAEpisodes = "kedaEpisodes"

	kedaGV = keda.Group + "/" + keda.APIVersion
)

// KEDAFacts is what the rules read of the operators: the version every
// running operator agrees on, unknown otherwise (S9 §1.8).
type KEDAFacts struct {
	Version keda.Version
}

// KEDAFactsOf maps the detector's operators onto the rules' facts.
func KEDAFactsOf(k capability.KEDACapability) KEDAFacts {
	return KEDAFacts{Version: k.Version()}
}

// KEDA returns its kinds and its task.
func KEDA(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	kind := func(resource string, budget, maxObject int) observe.KindSpec {
		return oneVersionKind(d, resource, keda.Group, keda.APIVersion, budget, keda.Project(resource), maxObject, keda.SizeOf)
	}
	ctas := kind(KindClusterTriggerAuths, keda.BudgetClusterAuths, keda.MaxAuthObject)
	ctas.Scope = "cluster-scoped"
	kinds := []observe.KindSpec{
		kind(KindScaledObjects, keda.BudgetScaledObjects, keda.MaxObject),
		kind(KindScaledJobs, keda.BudgetScaledJobs, keda.MaxObject),
		kind(KindTriggerAuths, keda.BudgetTriggerAuths, keda.MaxAuthObject),
		ctas,
	}
	c := d.Cluster
	facts := []string{FactKEDA, FactKEDAEpisodes}
	auths := []string{KindTriggerAuths, KindClusterTriggerAuths}
	domains := []observe.DomainSpec{{ID: TaskKEDA, Parts: []observe.PartSpec{
		{ID: "scaledobjects", Entity: "ScaledObjects", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindScaledObjects}, Deps: append([]string{KindHPAs, KindDeployments, KindStatefulSets}, auths...),
			Facts: facts, Consumer: ConsumerKEDA, Evaluate: evalScaledObjects(c), Ledger: true},
		{ID: "scaledjobs", Entity: "ScaledJobs", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindScaledJobs}, Deps: auths, Facts: facts, Evaluate: evalScaledJobs(c), Ledger: true},
		{ID: "authentications", Entity: "TriggerAuthentications", Scope: "all namespaces + cluster-scoped", Classifier: "evidence",
			Counted: auths, Deps: []string{KindScaledObjects, KindScaledJobs}, Evaluate: evalKEDAAuths},
	}}}
	return kinds, domains
}

func kedaFacts(c observe.Capture) KEDAFacts {
	f, _ := c.Facts[FactKEDA].(KEDAFacts)
	return f
}

func kedaEpisodes(c observe.Capture) map[string]map[keda.EpisodeKind]keda.Episode {
	e, _ := c.Facts[FactKEDAEpisodes].(map[string]map[keda.EpisodeKind]keda.Episode)
	return e
}

func kedaRef(m keda.Meta, resource string) refInput {
	return refInput{gvr: kedaGV + "/" + resource, ns: m.Namespace, name: m.Name, uid: m.UID, generation: m.Generation, source: resource}
}

// ScaledObjectRow is one ScaledObject row: its projection, its verdict
// with every finding, and the joined HPA as read.
type ScaledObjectRow struct {
	ScaledObject *keda.ScaledObject
	Verdict      keda.Verdict
	HPA          *hpa.HPA
	HPACurrent   bool
	HPAComplete  bool
	Others       []string
	Version      keda.Version
	// AuthsRead: the authentication kinds' reads, "found" from complete
	// reads, else "incomplete" or "not readable".
	AuthsRead string
}

// ScaledJobRow is one ScaledJob row.
type ScaledJobRow struct {
	ScaledJob *keda.ScaledJob
	Verdict   keda.Verdict
	Version   keda.Version
}

// AuthRow is one authentication: a fact row (S9 §6.2), never a verdict.
type AuthRow struct {
	Auth *keda.Auth
	// Users are the scalers that reference it ("Kind ns/name"), from their
	// specs; UsersComplete is false when a user's triggers were cut or a
	// scaler kind is incomplete, so it is never shown as unused.
	Users         []string
	UsersComplete bool
}

// FactOnly: an authentication row is always a fact.
func (AuthRow) FactOnly() bool { return true }

// missingAuths are the scaler's references absent from a complete, uncut
// read of their kind (S9 §3.2).
func missingAuths(s *keda.Scaler, c observe.Capture) []string {
	have := map[string]bool{}
	for _, kind := range []string{KindTriggerAuths, KindClusterTriggerAuths} {
		for _, x := range c.Objects[kind] {
			if a, ok := x.(*keda.Auth); ok {
				if a.Cluster {
					have[keda.KindClusterTriggerAuth+"/"+a.Name] = true
				} else {
					have[keda.KindTriggerAuth+"/"+a.Namespace+"/"+a.Name] = true
				}
			}
		}
	}
	var out []string
	for _, ref := range s.AuthRefs() {
		kind, name, _ := cutRef(ref)
		resource := KindTriggerAuths
		key := kind + "/" + s.Namespace + "/" + name
		if kind == keda.KindClusterTriggerAuth {
			resource, key = KindClusterTriggerAuths, kind+"/"+name
		}
		if c.Complete[resource] && c.Current[resource] && !have[key] {
			out = append(out, ref)
		}
	}
	return out
}

func cutRef(ref string) (kind, name string, ok bool) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			return ref[:i], ref[i+1:], true
		}
	}
	return ref, "", false
}

// targetReplicas is a scale target's actual replicas, when it is a
// Deployment or StatefulSet in the models.
func targetReplicas(so *keda.ScaledObject, c observe.Capture) *int32 {
	group, kind, name := so.Target()
	if group != "apps" {
		return nil
	}
	switch kind {
	case "Deployment":
		for _, x := range c.Objects[KindDeployments] {
			if d, ok := x.(*ProjectedDeployment); ok && d.Namespace == so.Namespace && d.Name == name {
				n := d.Status.Replicas
				return &n
			}
		}
	case "StatefulSet":
		for _, x := range c.Objects[KindStatefulSets] {
			if s, ok := x.(*ProjectedStatefulSet); ok && s.Namespace == so.Namespace && s.Name == name {
				n := s.Status.Replicas
				return &n
			}
		}
	}
	return nil
}

func evalScaledObjects(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		facts, eps := kedaFacts(c), kedaEpisodes(c)
		hs := hpasOf(c)
		controlled := map[string]*hpa.HPA{}
		for _, h := range hs {
			if so, ok := h.KEDAOwner(); ok {
				controlled[so.UID] = h
			}
		}
		others := OtherAutoscalers(hs)
		current, complete := c.Current[KindHPAs], c.Complete[KindHPAs] && c.Current[KindHPAs]
		var items []nativeItem
		var rows []Row[ScaledObjectRow]
		for _, x := range c.Objects[KindScaledObjects] {
			so, ok := x.(*keda.ScaledObject)
			if !ok {
				continue
			}
			ev := keda.HPAEvidence{Joined: controlled[so.UID], Current: current, Complete: complete, AsOf: c.AsOf[KindHPAs]}
			if j := ev.Joined; j != nil {
				ev.Others = others[j.UID]
			}
			in := keda.SOInputs{Now: c.Now, Version: facts.Version, HPA: ev, Episodes: eps[so.UID],
				MissingAuths: missingAuths(&so.Scaler, c), TargetReplicas: targetReplicas(so, c)}
			v := keda.ScaledObjectRule(so, in)
			it := nativeItem{v.Eval, kedaRef(so.Meta, KindScaledObjects)}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, ScaledObjectRow{ScaledObject: so, Verdict: v, HPA: ev.Joined, HPACurrent: current,
					HPAComplete: complete, Others: ev.Others, Version: facts.Version, AuthsRead: authsRead(c)}))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskKEDA, "failing or not scaling", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

func authsRead(c observe.Capture) string {
	w := coverageWord(c, KindTriggerAuths)
	if cw := coverageWord(c, KindClusterTriggerAuths); cw != "found" {
		w = cw
	}
	return w
}

func evalScaledJobs(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		facts, eps := kedaFacts(c), kedaEpisodes(c)
		var items []nativeItem
		var rows []Row[ScaledJobRow]
		for _, x := range c.Objects[KindScaledJobs] {
			j, ok := x.(*keda.ScaledJob)
			if !ok {
				continue
			}
			v := keda.ScaledJobRule(j, keda.JobInputs{Now: c.Now, Version: facts.Version, Episodes: eps[j.UID], MissingAuths: missingAuths(&j.Scaler, c)})
			it := nativeItem{v.Eval, kedaRef(j.Meta, KindScaledJobs)}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, ScaledJobRow{ScaledJob: j, Verdict: v, Version: facts.Version}))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskKEDA, "failing", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// evalKEDAAuths lists the authentications: evidence only, no verdict. The
// users come from the scalers' specs, never the authentications' status
// strings (S9 §6.2).
func evalKEDAAuths(c observe.Capture) observe.PartResult {
	users := map[string][]string{}
	complete := c.Complete[KindScaledObjects] && c.Complete[KindScaledJobs]
	for _, kind := range []string{KindScaledObjects, KindScaledJobs} {
		for _, x := range c.Objects[kind] {
			var s *keda.Scaler
			label := ""
			switch o := x.(type) {
			case *keda.ScaledObject:
				s, label = &o.Scaler, keda.KindScaledObject
			case *keda.ScaledJob:
				s, label = &o.Scaler, keda.KindScaledJob
			default:
				continue
			}
			if s.TriggersCut {
				complete = false
			}
			for _, ref := range s.AuthRefs() {
				kind, name, _ := cutRef(ref)
				key := kind + "/" + name
				if kind == keda.KindTriggerAuth {
					key = kind + "/" + s.Namespace + "/" + name
				}
				users[key] = append(users[key], label+" "+s.Namespace+"/"+s.Name)
			}
		}
	}
	var rows []Row[AuthRow]
	for _, kind := range []string{KindTriggerAuths, KindClusterTriggerAuths} {
		for _, x := range c.Objects[kind] {
			a, ok := x.(*keda.Auth)
			if !ok {
				continue
			}
			key := keda.KindClusterTriggerAuth + "/" + a.Name
			if !a.Cluster {
				key = keda.KindTriggerAuth + "/" + a.Namespace + "/" + a.Name
			}
			u := append([]string(nil), users[key]...)
			sort.Strings(u)
			rows = append(rows, Row[AuthRow]{Ref: kedaRef(a.Meta, kind).rowRef(), Data: AuthRow{Auth: a, Users: u, UsersComplete: complete}})
		}
	}
	n := len(rows)
	res := observe.PartResult{Evidence: &n}
	if c.Rows {
		res.Rows = rowSet(rows)
	}
	return res
}
