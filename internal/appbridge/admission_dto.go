package appbridge

import (
	"fmt"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Coverage S3a: the Admission lens's rows (the Observer's admission task).
// Counts and presence only: never an expression, a warning's text, a
// selector value, a caBundle or a URL beyond its origin.

// AdmissionRuleDTO is one admission rule, its lists capped.
type AdmissionRuleDTO struct {
	Operations []string `json:"operations"`
	Groups     []string `json:"groups"`
	Versions   []string `json:"versions"`
	Resources  []string `json:"resources"`
	Scope      string   `json:"scope,omitempty"`
}

// AdmissionBackendDTO is one webhook's backend as Klyx reads it (S3a §5).
type AdmissionBackendDTO struct {
	State           string `json:"state"` // ready | unavailable | terminating | unknown
	Label           string `json:"label"`
	Reason          string `json:"reason,omitempty"`
	EndpointRouting int    `json:"endpointRouting"`
	ClusterRouting  int    `json:"clusterRouting"`
	Terminating     int    `json:"terminating"`
	Selectorless    bool   `json:"selectorless,omitempty"`
}

// AdmissionWebhookDTO is one webhook of a configuration, with its own
// state.
type AdmissionWebhookDTO struct {
	Name               string              `json:"name"`
	FailurePolicy      string              `json:"failurePolicy"` // as configured; "" is the default, Fail
	FailClosed         bool                `json:"failClosed"`
	SideEffects        string              `json:"sideEffects,omitempty"`
	TimeoutSeconds     int32               `json:"timeoutSeconds"` // defaulted to 10
	MatchPolicy        string              `json:"matchPolicy,omitempty"`
	ReinvocationPolicy string              `json:"reinvocationPolicy,omitempty"`
	ServiceNamespace   string              `json:"serviceNamespace,omitempty"`
	ServiceName        string              `json:"serviceName,omitempty"`
	ServicePort        int32               `json:"servicePort,omitempty"`
	PathSet            bool                `json:"pathSet,omitempty"`
	URLOrigin          string              `json:"urlOrigin,omitempty"`
	URL                bool                `json:"url,omitempty"`
	CABundle           bool                `json:"caBundle"`
	Rules              []AdmissionRuleDTO  `json:"rules"`
	RuleCount          int                 `json:"ruleCount"`
	NamespaceSelector  bool                `json:"namespaceSelector,omitempty"`
	NamespaceExprs     int                 `json:"namespaceExprs,omitempty"`
	ObjectSelector     bool                `json:"objectSelector,omitempty"`
	ObjectExprs        int                 `json:"objectExprs,omitempty"`
	MatchConditions    int                 `json:"matchConditions,omitempty"`
	Backend            AdmissionBackendDTO `json:"backend"`
	Role               string              `json:"role"`
	Reason             string              `json:"reason,omitempty"`
}

// AdmissionConfigDTO is a webhook configuration row: the counted entity.
type AdmissionConfigDTO struct {
	UID          string                `json:"uid"`
	GVR          string                `json:"gvr"`
	Name         string                `json:"name"`
	Kind         string                `json:"kind"` // Validating | Mutating
	Generation   int64                 `json:"generation"`
	Created      *time.Time            `json:"created,omitempty"`
	WebhookCount int                   `json:"webhookCount"`
	Cut          bool                  `json:"cut,omitempty"`
	Webhooks     []AdmissionWebhookDTO `json:"webhooks"`
	Unavailable  int                   `json:"unavailable"`
	Unverified   int                   `json:"unverified"`
	Reason       string                `json:"reason,omitempty"`
	Rule         RuleDTO               `json:"rule"`
}

// AdmissionParamRefDTO is a binding's param reference.
type AdmissionParamRefDTO struct {
	Name           string `json:"name,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
	Selector       bool   `json:"selector,omitempty"`
	NotFoundAction string `json:"notFoundAction,omitempty"`
}

// AdmissionBindingDTO is a binding, inside its policy's row or as its own
// fact row.
type AdmissionBindingDTO struct {
	UID         string                `json:"uid"`
	Name        string                `json:"name"`
	PolicyName  string                `json:"policyName"`
	PolicyState string                `json:"policyState,omitempty"` // found | not found | unverified
	ParamRef    *AdmissionParamRefDTO `json:"paramRef,omitempty"`
	Actions     []string              `json:"actions"`
	MatchRules  int                   `json:"matchRules,omitempty"`
}

// AdmissionParamKindDTO is a policy's paramKind with its discovery.
type AdmissionParamKindDTO struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	State      string     `json:"state"` // served | not-served | unknown
	Namespaced bool       `json:"namespaced,omitempty"`
	At         *time.Time `json:"at,omitempty"`
}

// AdmissionPolicyDTO is one row of the policies part: a policy with its
// verdict, or (entity "binding") a binding as a fact with no rule.
type AdmissionPolicyDTO struct {
	Entity             string                 `json:"entity"` // policy | binding
	UID                string                 `json:"uid"`
	GVR                string                 `json:"gvr"`
	Name               string                 `json:"name"`
	Kind               string                 `json:"kind"` // Validating | Mutating
	Generation         int64                  `json:"generation"`
	Created            *time.Time             `json:"created,omitempty"`
	FailurePolicy      string                 `json:"failurePolicy,omitempty"`
	FailClosed         bool                   `json:"failClosed"`
	ParamKind          *AdmissionParamKindDTO `json:"paramKind,omitempty"`
	MatchRules         []AdmissionRuleDTO     `json:"matchRules"`
	MatchRuleCount     int                    `json:"matchRuleCount"`
	Validations        int                    `json:"validations,omitempty"`
	Mutations          int                    `json:"mutations,omitempty"`
	Variables          int                    `json:"variables,omitempty"`
	MatchConditions    int                    `json:"matchConditions,omitempty"`
	AuditAnnotations   int                    `json:"auditAnnotations,omitempty"`
	ReinvocationPolicy string                 `json:"reinvocationPolicy,omitempty"`
	ObservedGeneration int64                  `json:"observedGeneration,omitempty"`
	Behind             bool                   `json:"behind,omitempty"`
	WarningCount       int                    `json:"warningCount,omitempty"`
	WarningFields      []string               `json:"warningFields"`
	Bindings           []AdmissionBindingDTO  `json:"bindings"`
	// BindingsRead is how the bindings were read: complete | stale | unread.
	// Only complete proves that no binding names the policy.
	BindingsRead string `json:"bindingsRead,omitempty"`
	// ParamRevision is what a param check's reading depends on; a check
	// whose reply carries another one is stale (Codex's S3a implementation
	// review, finding 2).
	ParamRevision string               `json:"paramRevision,omitempty"`
	Binding       *AdmissionBindingDTO `json:"binding,omitempty"`
	Reason        string               `json:"reason,omitempty"`
	Rule          *RuleDTO             `json:"rule,omitempty"`
}

func admissionRules(rs []admission.Rule) []AdmissionRuleDTO {
	out := make([]AdmissionRuleDTO, 0, len(rs))
	for _, r := range rs {
		out = append(out, AdmissionRuleDTO{Operations: nonNil(r.Operations), Groups: nonNil(r.Groups), Versions: nonNil(r.Versions), Resources: nonNil(r.Resources), Scope: r.Scope})
	}
	return out
}

func admissionConfigDTO(r healthsrc.Row[healthsrc.AdmissionConfigRow]) AdmissionConfigDTO {
	c := r.Data.Config
	d := AdmissionConfigDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Name: c.Name, Kind: c.Kind, Generation: c.Generation, Created: timePtrOf(c.Created),
		WebhookCount: c.WebhookCount, Cut: c.Cut, Webhooks: []AdmissionWebhookDTO{}, Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval)}
	for i := range c.Webhooks {
		w := &c.Webhooks[i]
		b := r.Data.Backends[i]
		role, _, reason := admission.WebhookVerdict(w, b)
		timeout := w.TimeoutSeconds
		if timeout == 0 {
			timeout = 10
		}
		wd := AdmissionWebhookDTO{Name: w.Name, FailurePolicy: w.FailurePolicy, FailClosed: w.FailClosed(), SideEffects: w.SideEffects, TimeoutSeconds: timeout,
			MatchPolicy: w.MatchPolicy, ReinvocationPolicy: w.ReinvocationPolicy, URLOrigin: w.URLOrigin, URL: w.URL, CABundle: w.CABundle,
			Rules: admissionRules(w.Rules), RuleCount: w.RuleCount, NamespaceSelector: w.NamespaceSelector, NamespaceExprs: w.NamespaceExprs,
			ObjectSelector: w.ObjectSelector, ObjectExprs: w.ObjectExprs, MatchConditions: w.MatchConditions, Role: role.String(), Reason: reason,
			Backend: AdmissionBackendDTO{State: b.State, Label: b.Label, Reason: b.Reason, EndpointRouting: b.EndpointRouting, ClusterRouting: b.ClusterRouting,
				Terminating: b.Terminating, Selectorless: b.Selectorless}}
		if s := w.Service; s != nil {
			wd.ServiceNamespace, wd.ServiceName, wd.ServicePort, wd.PathSet = s.Namespace, s.Name, s.Port, s.PathSet
		} else {
			wd.PathSet = w.URLPathSet
		}
		switch b.State {
		case admission.BackendUnavailable, admission.BackendTerminating:
			d.Unavailable++
		case admission.BackendUnknown:
			d.Unverified++
		}
		d.Webhooks = append(d.Webhooks, wd)
	}
	return d
}

func admissionBindingDTO(uid string, b *admission.Binding, state string) AdmissionBindingDTO {
	d := AdmissionBindingDTO{UID: uid, Name: b.Name, PolicyName: b.PolicyName, PolicyState: state, Actions: nonNil(b.Actions), MatchRules: b.MatchRules}
	if pr := b.ParamRef; pr != nil {
		d.ParamRef = &AdmissionParamRefDTO{Name: pr.Name, Namespace: pr.Namespace, Selector: pr.Selector, NotFoundAction: pr.NotFoundAction}
	}
	return d
}

func admissionPolicyDTO(r healthsrc.Row[healthsrc.AdmissionPolicyRow]) AdmissionPolicyDTO {
	if bd := r.Data.Binding; bd != nil {
		b := admissionBindingDTO(r.Ref.UID, bd, r.Data.PolicyState)
		return AdmissionPolicyDTO{Entity: "binding", UID: r.Ref.UID, GVR: r.Ref.GVR, Name: bd.Name, Kind: bd.Kind, Generation: bd.Generation,
			Created: timePtrOf(bd.Created), MatchRules: []AdmissionRuleDTO{}, WarningFields: []string{}, Bindings: []AdmissionBindingDTO{}, Binding: &b}
	}
	p := r.Data.Policy
	rule := toRuleDTO(r.Eval)
	d := AdmissionPolicyDTO{Entity: "policy", UID: r.Ref.UID, GVR: r.Ref.GVR, Name: p.Name, Kind: p.Kind, Generation: p.Generation, Created: timePtrOf(p.Created),
		FailurePolicy: p.FailurePolicy, FailClosed: p.FailClosed(), MatchRules: admissionRules(p.MatchRules), MatchRuleCount: p.MatchRuleCount,
		Validations: p.Validations, Mutations: p.Mutations, Variables: p.Variables, MatchConditions: p.MatchConditions, AuditAnnotations: p.AuditAnnotations,
		ReinvocationPolicy: p.ReinvocationPolicy, ObservedGeneration: p.ObservedGeneration, Behind: p.Behind(), WarningCount: p.WarningCount,
		WarningFields: nonNil(p.WarningFields), Bindings: []AdmissionBindingDTO{}, Reason: r.Eval.Reason, Rule: &rule}
	if pk := p.ParamKind; pk != nil {
		d.ParamKind = &AdmissionParamKindDTO{APIVersion: pk.APIVersion, Kind: pk.Kind, State: admission.ParamUnknown}
		if st := r.Data.ParamKind; st != nil {
			d.ParamKind.State, d.ParamKind.Namespaced, d.ParamKind.At = st.State, st.Namespaced, timePtrOf(st.At)
		}
	}
	for _, b := range r.Data.Bindings {
		d.Bindings = append(d.Bindings, admissionBindingDTO(b.UID, b, "found"))
	}
	d.BindingsRead = readWord(r.Data.BindingsRead)
	d.ParamRevision = paramRevision(r.Data)
	return d
}

func readWord(r admission.Read) string {
	switch r {
	case admission.ReadComplete:
		return "complete"
	case admission.ReadStale:
		return "stale"
	}
	return "unread"
}

// paramRevision is what a param check depends on: the policy's
// generation (its paramKind and failure policy) and the paramKind's
// discovered state, scope and resource.
func paramRevision(r healthsrc.AdmissionPolicyRow) string {
	if r.Policy == nil {
		return ""
	}
	st := "unknown/false/"
	if pk := r.ParamKind; pk != nil {
		st = fmt.Sprintf("%s/%v/%s", pk.State, pk.Namespaced, pk.Resource)
	}
	return fmt.Sprintf("%d|%s", r.Policy.Generation, st)
}

func init() {
	rowsConverters[healthsrc.TaskAdmission+"/webhooks"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.AdmissionConfigRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]AdmissionConfigDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, admissionConfigDTO(r))
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	}
	rowsConverters[healthsrc.TaskAdmission+"/policies"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.AdmissionPolicyRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]AdmissionPolicyDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, admissionPolicyDTO(r))
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Entity+out[i].Name < out[j].Entity+out[j].Name })
		return out, nil
	}
}
