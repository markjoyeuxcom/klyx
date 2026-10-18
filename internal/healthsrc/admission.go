package healthsrc

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/observe"
)

// Coverage S3a: the admission task - webhook configurations judged by their
// backends (the shared network kinds, read through the admission consumer),
// and Validating and Mutating AdmissionPolicies with their bindings.
const (
	TaskAdmission             = "admission"
	KindValidatingWebhooks    = admission.ResourceValidatingWebhooks
	KindMutatingWebhooks      = admission.ResourceMutatingWebhooks
	KindValidatingPolicies    = admission.ResourceValidatingPolicies
	KindMutatingPolicies      = admission.ResourceMutatingPolicies
	KindValidatingBindings    = admission.ResourceValidatingBindings
	KindMutatingBindings      = admission.ResourceMutatingBindings
	FactParamKinds            = "admissionParamKinds"
	admissionScope            = "cluster-scoped"
	admissionWebhooksEntity   = "Webhook configurations"
	admissionPoliciesEntity   = "Admission policies"
	admissionWebhooksIncluded = "with a backend that is unavailable"
	admissionPoliciesIncluded = "denying or misconfigured"
)

// Admission returns the admission kinds and task (read-only; S3a).
func Admission(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	kind := func(resource string) observe.KindSpec {
		k := oneVersionKind(d, resource, admission.Group, admission.APIVersion, 0, admission.Project(resource), 0, nil)
		k.Scope = admissionScope
		return k
	}
	kinds := []observe.KindSpec{kind(KindValidatingWebhooks), kind(KindMutatingWebhooks), kind(KindValidatingPolicies),
		kind(KindMutatingPolicies), kind(KindValidatingBindings), kind(KindMutatingBindings)}
	c := d.Cluster
	domains := []observe.DomainSpec{{ID: TaskAdmission, Parts: []observe.PartSpec{
		{ID: "webhooks", Entity: admissionWebhooksEntity, Scope: admissionScope, Classifier: "native",
			Counted: []string{KindValidatingWebhooks, KindMutatingWebhooks}, Deps: []string{KindServices, KindEndpointSlices},
			Consumer: ConsumerAdmission, Evaluate: evalAdmissionWebhooks(c), Ledger: true},
		{ID: "policies", Entity: admissionPoliciesEntity, Scope: admissionScope, Classifier: "native",
			Counted: []string{KindValidatingPolicies, KindMutatingPolicies}, Deps: []string{KindValidatingBindings, KindMutatingBindings},
			Facts: []string{FactParamKinds}, Evaluate: evalAdmissionPolicies(c), Ledger: true},
	}}}
	return kinds, domains
}

// readOf is how a part read one kind (C3's view applied by the capture).
func readOf(c observe.Capture, kind string) admission.Read {
	_, has := c.Objects[kind]
	switch {
	case c.Complete[kind]:
		return admission.ReadComplete
	case has:
		return admission.ReadStale
	}
	return admission.ReadNone
}

// AdmissionBackends indexes the shared network kinds for the webhook rule.
func AdmissionBackends(c observe.Capture) admission.Backends {
	b := admission.Backends{Services: map[string]*corev1.Service{}, Slices: map[string][]*discoveryv1.EndpointSlice{},
		SvcRead: readOf(c, KindServices), SliceRead: readOf(c, KindEndpointSlices)}
	for _, o := range c.Objects[KindServices] {
		if s, ok := o.(*corev1.Service); ok {
			b.Services[s.Namespace+"/"+s.Name] = s
		}
	}
	for _, o := range c.Objects[KindEndpointSlices] {
		if s, ok := o.(*discoveryv1.EndpointSlice); ok {
			k := s.Namespace + "/" + s.Labels[discoveryv1.LabelServiceName]
			b.Slices[k] = append(b.Slices[k], s)
		}
	}
	return b
}

func admissionRef(m admission.Meta, resource string) refInput {
	return refInput{gvr: admission.Group + "/" + admission.APIVersion + "/" + resource, name: m.Name, uid: m.UID, generation: m.Generation, source: resource}
}

// AdmissionConfigRow is one webhook configuration with each webhook's
// backend, in the configuration's webhook order.
type AdmissionConfigRow struct {
	Config   *admission.Config
	Backends []admission.Backend
}

func evalAdmissionWebhooks(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		b := AdmissionBackends(c)
		var items []nativeItem
		var rows []Row[AdmissionConfigRow]
		for _, kind := range []string{KindValidatingWebhooks, KindMutatingWebhooks} {
			for _, o := range c.Objects[kind] {
				cfg, ok := o.(*admission.Config)
				if !ok {
					continue
				}
				backends := make([]admission.Backend, len(cfg.Webhooks))
				for i := range cfg.Webhooks {
					backends[i] = admission.BackendOf(&cfg.Webhooks[i], b)
				}
				it := nativeItem{admission.ConfigVerdict(cfg, backends), admissionRef(cfg.Meta, kind)}
				items = append(items, it)
				if c.Rows {
					rows = append(rows, nativeRow(it, AdmissionConfigRow{Config: cfg, Backends: backends}))
				}
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskAdmission, admissionWebhooksIncluded, items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// AdmissionPolicyRow is one policy with the bindings that name it, or, as
// a listed fact with no verdict, one binding (S3a §3): FactOnly.
type AdmissionPolicyRow struct {
	Policy   *admission.Policy
	Bindings []*admission.Binding
	// Binding is set on a binding's fact row; PolicyState is its policy's:
	// "found", "not found" (from a complete read) or "unverified".
	Binding     *admission.Binding
	PolicyState string
	ParamKind   *admission.ParamKindState
}

// FactOnly: a binding's row is a fact, never a verdict (S2b's FactRow).
func (r AdmissionPolicyRow) FactOnly() bool { return r.Binding != nil }

func evalAdmissionPolicies(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		pk, _ := c.Facts[FactParamKinds].(admission.ParamKinds)
		type pair struct{ policies, bindings string }
		var items []nativeItem
		var rows []Row[AdmissionPolicyRow]
		for _, kinds := range []pair{{KindValidatingPolicies, KindValidatingBindings}, {KindMutatingPolicies, KindMutatingBindings}} {
			byPolicy := map[string][]*admission.Binding{}
			var bindings []*admission.Binding
			for _, o := range c.Objects[kinds.bindings] {
				if bd, ok := o.(*admission.Binding); ok {
					byPolicy[bd.PolicyName] = append(byPolicy[bd.PolicyName], bd)
					bindings = append(bindings, bd)
				}
			}
			bindingsRead := readOf(c, kinds.bindings) != admission.ReadNone
			policies := map[string]bool{}
			for _, o := range c.Objects[kinds.policies] {
				p, ok := o.(*admission.Policy)
				if !ok {
					continue
				}
				policies[p.Name] = true
				bs := byPolicy[p.Name]
				sort.Slice(bs, func(i, j int) bool { return bs[i].Name < bs[j].Name })
				it := nativeItem{admission.PolicyVerdict(admission.PolicyInput{P: p, Bindings: bs, BindingsRead: bindingsRead, ParamKinds: pk}),
					admissionRef(p.Meta, kinds.policies)}
				items = append(items, it)
				if c.Rows {
					row := AdmissionPolicyRow{Policy: p, Bindings: bs}
					if p.ParamKind != nil && pk != nil {
						if g, v, k, ok := p.ParamKind.GVK(); ok {
							if st, found := pk[admission.KeyOf(g, v, k)]; found {
								row.ParamKind = &st
							}
						}
					}
					rows = append(rows, nativeRow(it, row))
				}
			}
			if !c.Rows {
				continue
			}
			policyRead := readOf(c, kinds.policies)
			for _, bd := range bindings {
				state := "found"
				if !policies[bd.PolicyName] {
					state = "unverified"
					if policyRead == admission.ReadComplete {
						state = "not found"
					}
				}
				rows = append(rows, Row[AdmissionPolicyRow]{Ref: admissionRef(bd.Meta, kinds.bindings).rowRef(),
					Data: AdmissionPolicyRow{Binding: bd, PolicyState: state}})
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskAdmission, admissionPoliciesIncluded, items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}
