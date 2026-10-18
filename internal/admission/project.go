// Package admission reads the cluster's admission configuration (coverage
// S3a): webhook configurations with their backends, and Validating and
// Mutating AdmissionPolicies with their bindings. Pure: projections from
// unstructured objects, and the rules, with no client and no clock but the
// one passed in. Expressions, selector values and caBundles never cross:
// counts and presence only.
package admission

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/urlscrub"
)

// The API group and version S3a reads (S3a §1).
const (
	Group      = "admissionregistration.k8s.io"
	APIVersion = "v1"
)

// Resources.
const (
	ResourceValidatingWebhooks = "validatingwebhookconfigurations"
	ResourceMutatingWebhooks   = "mutatingwebhookconfigurations"
	ResourceValidatingPolicies = "validatingadmissionpolicies"
	ResourceMutatingPolicies   = "mutatingadmissionpolicies"
	ResourceValidatingBindings = "validatingadmissionpolicybindings"
	ResourceMutatingBindings   = "mutatingadmissionpolicybindings"
)

// Kinds of a configuration or policy.
const (
	Validating = "Validating"
	Mutating   = "Mutating"
)

// Projection limits (S3a §7).
const (
	MaxConfig      = 16 << 10
	maxWebhooks    = 64
	maxRules       = 8
	maxRuleItems   = 8
	maxWarnings    = 8
	maxShort       = 64
	maxName        = 253
	defaultPort    = 443
	defaultTimeout = 10
)

// Meta is a projected object's identity.
type Meta struct {
	Name       string    `json:"n"`
	UID        string    `json:"u"`
	Generation int64     `json:"g,omitempty"`
	Created    time.Time `json:"c"`
}

// Rule is one admission rule, its lists capped.
type Rule struct {
	Operations []string `json:"op,omitempty"`
	Groups     []string `json:"gr,omitempty"`
	Versions   []string `json:"ve,omitempty"`
	Resources  []string `json:"re,omitempty"`
	Scope      string   `json:"sc,omitempty"`
}

// ServiceRef is a webhook's Service backend.
type ServiceRef struct {
	Namespace string `json:"ns"`
	Name      string `json:"n"`
	Port      int32  `json:"p"` // defaulted to 443
	PathSet   bool   `json:"path,omitempty"`
}

// Webhook is one webhook of a configuration.
type Webhook struct {
	Name               string      `json:"n"`
	FailurePolicy      string      `json:"fp,omitempty"` // "" is the v1 default, Fail
	SideEffects        string      `json:"se,omitempty"`
	TimeoutSeconds     int32       `json:"to,omitempty"` // 0 is the default, 10
	MatchPolicy        string      `json:"mp,omitempty"`
	ReinvocationPolicy string      `json:"rp,omitempty"`
	ReviewVersions     []string    `json:"rv,omitempty"`
	Service            *ServiceRef `json:"svc,omitempty"`
	URLOrigin          string      `json:"url,omitempty"`
	URLPathSet         bool        `json:"urlPath,omitempty"`
	URL                bool        `json:"isURL,omitempty"`
	CABundle           bool        `json:"ca,omitempty"`
	Rules              []Rule      `json:"rules,omitempty"`
	RuleCount          int         `json:"rulesN,omitempty"`
	NamespaceSelector  bool        `json:"nss,omitempty"`
	NamespaceExprs     int         `json:"nssN,omitempty"`
	ObjectSelector     bool        `json:"os,omitempty"`
	ObjectExprs        int         `json:"osN,omitempty"`
	MatchConditions    int         `json:"mc,omitempty"`
}

// FailClosed reports Fail, or unset (the v1 default).
func (w *Webhook) FailClosed() bool { return w.FailurePolicy != "Ignore" }

// Config is one projected webhook configuration.
type Config struct {
	Meta
	Kind         string    `json:"k"`
	Webhooks     []Webhook `json:"wh,omitempty"`
	WebhookCount int       `json:"whN"`
	// Cut: webhooks were left out to fit MaxConfig; they are unread (§4).
	Cut  bool `json:"cut,omitempty"`
	Size int  `json:"-"`
}

// ParamKind is a policy's paramKind, kept whole: it classifies by exact
// discovery, so it is never clipped for display (Codex's S3a
// implementation review, finding 4). A value past maxParamKind is not
// kept and reads unknown.
type ParamKind struct {
	APIVersion string `json:"av"`
	Kind       string `json:"k"`
	Oversized  bool   `json:"over,omitempty"`
}

// maxParamKind bounds a paramKind's apiVersion and kind: a group (253) and
// a version (63) with their slash, and a kind, fit with room.
const maxParamKind = 400

// GVK is the exact group, version and kind a ParamKind names; ok is false
// when its apiVersion does not parse or it was not kept.
func (p ParamKind) GVK() (group, version, kind string, ok bool) {
	if p.Oversized {
		return "", "", "", false
	}
	g, v, found := cutLast(p.APIVersion)
	if !found {
		g, v = "", p.APIVersion
	}
	if v == "" || p.Kind == "" {
		return "", "", "", false
	}
	return g, v, p.Kind, true
}

func cutLast(s string) (string, string, bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[:i], s[i+1:], true
		}
	}
	return "", s, false
}

// Policy is one projected Validating or Mutating AdmissionPolicy.
type Policy struct {
	Meta
	Kind               string     `json:"k"`
	FailurePolicy      string     `json:"fp,omitempty"`
	ParamKind          *ParamKind `json:"pk,omitempty"`
	MatchRules         []Rule     `json:"mr,omitempty"`
	MatchRuleCount     int        `json:"mrN,omitempty"`
	Validations        int        `json:"va,omitempty"`
	Mutations          int        `json:"mu,omitempty"`
	Variables          int        `json:"vr,omitempty"`
	MatchConditions    int        `json:"mc,omitempty"`
	AuditAnnotations   int        `json:"aa,omitempty"`
	ReinvocationPolicy string     `json:"rp,omitempty"`
	// HasStatus: a Validating policy (Mutating ones have no status).
	HasStatus          bool  `json:"st,omitempty"`
	ObservedGeneration int64 `json:"og,omitempty"`
	// WarningFields are the type-checking warnings' fields, validated;
	// the warning text never crosses (S3a §8).
	WarningFields []string `json:"wf,omitempty"`
	WarningCount  int      `json:"wN,omitempty"`
	Size          int      `json:"-"`
}

// FailClosed reports Fail, or unset (the v1 default).
func (p *Policy) FailClosed() bool { return p.FailurePolicy != "Ignore" }

// Behind: the status describes an earlier generation.
func (p *Policy) Behind() bool { return p.HasStatus && p.ObservedGeneration < p.Generation }

// ParamRef is a binding's param reference.
type ParamRef struct {
	Name           string `json:"n,omitempty"`
	Namespace      string `json:"ns,omitempty"`
	Selector       bool   `json:"sel,omitempty"`
	NotFoundAction string `json:"nf,omitempty"` // Allow | Deny
}

// Binding is one projected policy binding.
type Binding struct {
	Meta
	Kind       string    `json:"k"`
	PolicyName string    `json:"p"`
	ParamRef   *ParamRef `json:"pr,omitempty"`
	Actions    []string  `json:"ac,omitempty"` // validating: Deny | Warn | Audit
	MatchRules int       `json:"mr,omitempty"`
	Size       int       `json:"-"`
}

// ---- building ------------------------------------------------------------

func str(u map[string]any, path ...string) string {
	v, _, _ := unstructured.NestedString(u, path...)
	return v
}

func i64(u map[string]any, path ...string) int64 {
	v, ok, err := unstructured.NestedFieldNoCopy(u, path...)
	if !ok || err != nil {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

func slice(u map[string]any, path ...string) []any {
	v, _, _ := unstructured.NestedSlice(u, path...)
	return v
}

func strs(u map[string]any, path ...string) []string {
	v, _, _ := unstructured.NestedStringSlice(u, path...)
	return v
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// disp is a display string: the URL rule first, then the clip.
func disp(s string, n int) string { return clip(urlscrub.Field(s).Text, n) }

func capped(in []string, n int) []string {
	var out []string
	for _, s := range in {
		if len(out) == n {
			break
		}
		out = append(out, disp(s, maxShort))
	}
	return out
}

func metaOf(u *unstructured.Unstructured) Meta {
	return Meta{Name: u.GetName(), UID: string(u.GetUID()), Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().UTC()}
}

func rulesOf(items []any) ([]Rule, int) {
	var out []Rule
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if len(out) == maxRules {
			break
		}
		out = append(out, Rule{Operations: capped(strs(m, "operations"), maxRuleItems), Groups: capped(strs(m, "apiGroups"), maxRuleItems),
			Versions: capped(strs(m, "apiVersions"), maxRuleItems), Resources: capped(strs(m, "resources"), maxRuleItems), Scope: disp(str(m, "scope"), maxShort)})
	}
	return out, len(items)
}

func selector(m map[string]any, key string) (set bool, exprs int) {
	sel, ok := m[key].(map[string]any)
	if !ok {
		return false, 0
	}
	labels, _ := sel["matchLabels"].(map[string]any)
	expr, _ := sel["matchExpressions"].([]any)
	return len(labels) > 0 || len(expr) > 0, len(labels) + len(expr)
}

func webhookOf(m map[string]any) Webhook {
	w := Webhook{Name: disp(str(m, "name"), maxName), FailurePolicy: disp(str(m, "failurePolicy"), maxShort), SideEffects: disp(str(m, "sideEffects"), maxShort),
		TimeoutSeconds: int32(i64(m, "timeoutSeconds")), MatchPolicy: disp(str(m, "matchPolicy"), maxShort),
		ReinvocationPolicy: disp(str(m, "reinvocationPolicy"), maxShort), ReviewVersions: capped(strs(m, "admissionReviewVersions"), 4),
		CABundle: str(m, "clientConfig", "caBundle") != "", MatchConditions: len(slice(m, "matchConditions"))}
	if svc, ok, _ := unstructured.NestedMap(m, "clientConfig", "service"); ok {
		ref := &ServiceRef{Namespace: clip(str(svc, "namespace"), maxName), Name: clip(str(svc, "name"), maxName), Port: defaultPort, PathSet: str(svc, "path") != ""}
		if p := i64(svc, "port"); p != 0 {
			ref.Port = int32(p)
		}
		w.Service = ref
	} else if raw := str(m, "clientConfig", "url"); raw != "" {
		w.URL, w.URLOrigin = true, clip(urlscrub.OriginOf(raw), maxName)
		if pu, err := url.Parse(raw); err == nil {
			w.URLPathSet = pu.Path != "" && pu.Path != "/"
		}
	}
	w.Rules, w.RuleCount = rulesOf(slice(m, "rules"))
	w.NamespaceSelector, w.NamespaceExprs = selector(m, "namespaceSelector")
	w.ObjectSelector, w.ObjectExprs = selector(m, "objectSelector")
	return w
}

func size(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return MaxConfig + 1
	}
	return len(b)
}

// ProjectConfig builds a webhook configuration's projection: at most
// maxWebhooks webhooks, then cut from the tail until it measures within
// MaxConfig. The count stays whole and Cut says webhooks are unread.
func ProjectConfig(u *unstructured.Unstructured, kind string) *Config {
	c := &Config{Meta: metaOf(u), Kind: kind}
	items := slice(u.Object, "webhooks")
	c.WebhookCount = len(items)
	for _, it := range items {
		if len(c.Webhooks) == maxWebhooks {
			c.Cut = true
			break
		}
		if m, ok := it.(map[string]any); ok {
			c.Webhooks = append(c.Webhooks, webhookOf(m))
		}
	}
	c.Size = size(c)
	for c.Size > MaxConfig && len(c.Webhooks) > 0 {
		c.Webhooks, c.Cut = c.Webhooks[:len(c.Webhooks)-1], true
		c.Size = size(c)
	}
	return c
}

// warningField is the type checker's own fieldRef form (S3a §8).
var warningField = regexp.MustCompile(`^spec\.(validations|auditAnnotations|matchConditions|variables|mutations)\[[0-9]{1,4}\]\.(expression|messageExpression|valueExpression|applyConfiguration\.expression|jsonPatch\.expression)$`)

// WarningField is a type-checking warning's fieldRef when it has the type
// checker's own form, else "(unrecognised field)": the warning's text is
// never shown (S3a §8).
func WarningField(f string) string {
	if warningField.MatchString(f) {
		return f
	}
	return "(unrecognised field)"
}

// ProjectPolicy builds a policy's projection: counts and presence, never an
// expression or a warning's text.
func ProjectPolicy(u *unstructured.Unstructured, kind string) *Policy {
	p := &Policy{Meta: metaOf(u), Kind: kind, FailurePolicy: disp(str(u.Object, "spec", "failurePolicy"), maxShort),
		Validations: len(slice(u.Object, "spec", "validations")), Mutations: len(slice(u.Object, "spec", "mutations")),
		Variables: len(slice(u.Object, "spec", "variables")), MatchConditions: len(slice(u.Object, "spec", "matchConditions")),
		AuditAnnotations: len(slice(u.Object, "spec", "auditAnnotations")), ReinvocationPolicy: disp(str(u.Object, "spec", "reinvocationPolicy"), maxShort)}
	if kind == Validating {
		p.HasStatus = true
		p.ObservedGeneration = i64(u.Object, "status", "observedGeneration")
		warnings := slice(u.Object, "status", "typeChecking", "expressionWarnings")
		p.WarningCount = len(warnings)
		for _, w := range warnings {
			if len(p.WarningFields) == maxWarnings {
				break
			}
			m, _ := w.(map[string]any)
			p.WarningFields = append(p.WarningFields, WarningField(str(m, "fieldRef")))
		}
	}
	if pk, ok, _ := unstructured.NestedMap(u.Object, "spec", "paramKind"); ok {
		av, kind := str(pk, "apiVersion"), str(pk, "kind")
		if len(av) > maxParamKind || len(kind) > maxParamKind {
			p.ParamKind = &ParamKind{Oversized: true}
		} else {
			p.ParamKind = &ParamKind{APIVersion: av, Kind: kind}
		}
	}
	p.MatchRules, p.MatchRuleCount = rulesOf(slice(u.Object, "spec", "matchConstraints", "resourceRules"))
	p.Size = size(p)
	return p
}

// ProjectBinding builds a binding's projection: its policy, its param
// reference (a selector by presence), and its actions.
func ProjectBinding(u *unstructured.Unstructured, kind string) *Binding {
	b := &Binding{Meta: metaOf(u), Kind: kind, PolicyName: clip(str(u.Object, "spec", "policyName"), maxName),
		Actions: capped(strs(u.Object, "spec", "validationActions"), 3), MatchRules: len(slice(u.Object, "spec", "matchResources", "resourceRules"))}
	if pr, ok, _ := unstructured.NestedMap(u.Object, "spec", "paramRef"); ok {
		r := &ParamRef{Name: clip(str(pr, "name"), maxName), Namespace: clip(str(pr, "namespace"), maxName),
			NotFoundAction: disp(str(pr, "parameterNotFoundAction"), maxShort)}
		_, r.Selector = pr["selector"].(map[string]any)
		b.ParamRef = r
	}
	b.Size = size(b)
	return b
}

// KindOf is a resource's Validating or Mutating: a list's items need not
// carry their kind, so the projection takes it from the resource.
func KindOf(resource string) string {
	switch resource {
	case ResourceMutatingWebhooks, ResourceMutatingPolicies, ResourceMutatingBindings:
		return Mutating
	}
	return Validating
}

// Project is the KindSpec projection for one admission resource.
func Project(resource string) func(obj any) (any, error) {
	kind := KindOf(resource)
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("admission: want *Unstructured, got %T", obj)
		}
		switch resource {
		case ResourceValidatingWebhooks, ResourceMutatingWebhooks:
			return ProjectConfig(u, kind), nil
		case ResourceValidatingPolicies, ResourceMutatingPolicies:
			return ProjectPolicy(u, kind), nil
		case ResourceValidatingBindings, ResourceMutatingBindings:
			return ProjectBinding(u, kind), nil
		}
		return nil, fmt.Errorf("admission: no projection for %s", resource)
	}
}
