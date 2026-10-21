// Package keda reads KEDA for coverage S9: bounded projections of
// ScaledObjects, ScaledJobs and the authentication kinds, the version and
// pause readings, the episode tracker, and the rules in KEDA's vocabulary.
package keda

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/urlscrub"
)

// The API (S9 §1.1).
const (
	Group                       = "keda.sh"
	APIVersion                  = "v1alpha1"
	ResourceScaledObjects       = "scaledobjects"
	ResourceScaledJobs          = "scaledjobs"
	ResourceTriggerAuths        = "triggerauthentications"
	ResourceClusterTriggerAuths = "clustertriggerauthentications"
	KindScaledObject            = "ScaledObject"
	KindScaledJob               = "ScaledJob"
	KindTriggerAuth             = "TriggerAuthentication"
	KindClusterTriggerAuth      = "ClusterTriggerAuthentication"
)

// The annotations S9 reads (§1.3, §1.4): an allowlist, never the rest.
const (
	AnnPaused         = "autoscaling.keda.sh/paused"
	AnnPausedReplicas = "autoscaling.keda.sh/paused-replicas"
	AnnPausedScaleIn  = "autoscaling.keda.sh/paused-scale-in"
	AnnPausedScaleOut = "autoscaling.keda.sh/paused-scale-out"
	AnnForceActivate  = "autoscaling.keda.sh/force-activation"
	AnnTransfer       = "scaledobject.keda.sh/transfer-hpa-ownership"
)

// The conditions KEDA writes (§1.2).
const (
	CondReady     = "Ready"
	CondActive    = "Active"
	CondFallback  = "Fallback"
	CondPaused    = "Paused"
	CondHPAActive = "HPAActive"
)

// Bounds (S9 §10). Size is the retained bytes - string lengths and a fixed
// allowance per field - so the caps below bound every projection. A
// scaler's MaxObject bounds the product of every cap (about 34 KiB,
// TestMaxObjectBoundsTheCapProduct): no ScaledObject or ScaledJob is left
// out for its shape, only for the budget (the design's 4 KiB could drop
// exactly the faulty ones). An authentication's product (16 references per
// source at full length, over 64 KiB) cannot be bounded within its kinds'
// budgets: MaxAuthObject keeps a large realistic one (16 references in each
// in-cluster source, Vault and a pod identity: about 8.5 KiB), and one past
// it is counted, not retained, so its kinds read truncated (S1a §3) and a
// missing authentication stays unverified, never claimed.
const (
	BudgetScaledObjects = 512 << 10
	BudgetScaledJobs    = 128 << 10
	BudgetTriggerAuths  = 128 << 10
	BudgetClusterAuths  = 64 << 10
	MaxObject           = 40 << 10
	MaxAuthObject       = 16 << 10
	maxConds            = 8
	maxTriggers         = 16
	maxHealth           = 16
	maxLabels           = 16
	maxRefs             = 16
	maxMessage          = 512
	maxShort            = 128
	maxName             = 253
	maxLabelValue       = 63
	maxAPI              = 256
	maxKind             = 63
	maxUID              = 64
	maxDetail           = 256
)

// Meta is an object's identity, with the labels operator selectors read.
type Meta struct {
	Namespace  string            `json:"namespace,omitempty"`
	Name       string            `json:"name"`
	UID        string            `json:"uid"`
	RV         string            `json:"rv,omitempty"`
	Generation int64             `json:"generation"`
	Created    time.Time         `json:"created"`
	Labels     map[string]string `json:"labels,omitempty"`
	LabelsCut  bool              `json:"labelsCut,omitempty"`
}

// Cond is a KEDA condition: KEDA writes no transition time (§1.1).
type Cond struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Trigger is one trigger, never its metadata (§10).
type Trigger struct {
	Type       string `json:"type"`
	Name       string `json:"name,omitempty"`
	MetricType string `json:"metricType,omitempty"`
	AuthName   string `json:"authName,omitempty"`
	AuthKind   string `json:"authKind,omitempty"`
	Cached     bool   `json:"cached,omitempty"`
}

// Fallback is the configured fallback.
type Fallback struct {
	FailureThreshold int32  `json:"failureThreshold"`
	Replicas         int32  `json:"replicas"`
	Behavior         string `json:"behavior,omitempty"`
}

// HealthEntry is one metric's failure count (`status.health`).
type HealthEntry struct {
	Failures int32  `json:"failures"`
	Status   string `json:"status"`
}

// GVKR is `status.scaleTargetGVKR`.
type GVKR struct {
	Group    string `json:"group"`
	Version  string `json:"version"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
}

// Pause holds the pause annotations as written: nil is absent.
type Pause struct {
	Paused         *string `json:"paused,omitempty"`
	PausedReplicas *string `json:"pausedReplicas,omitempty"`
	ScaleIn        *string `json:"scaleIn,omitempty"`
	ScaleOut       *string `json:"scaleOut,omitempty"`
}

// Any reports whether any pause annotation is present.
func (p Pause) Any() bool {
	return p.Paused != nil || p.PausedReplicas != nil || p.ScaleIn != nil || p.ScaleOut != nil
}

// Scaler is what ScaledObjects and ScaledJobs share.
type Scaler struct {
	Meta
	Pause       Pause     `json:"pause"`
	Triggers    []Trigger `json:"triggers,omitempty"`
	TriggerN    int       `json:"triggerN"`
	TriggersCut bool      `json:"triggersCut,omitempty"`
	Conds       []Cond    `json:"conds,omitempty"`
	CondsCut    bool      `json:"condsCut,omitempty"`
	LastActive  time.Time `json:"lastActive,omitempty"`
	// Owner is the GitOps unit its labels or Argo tracking name, read from
	// the full object before any cut: the verbs' ownership line (C4).
	Owner *GitOwner `json:"owner,omitempty"`
	Size  int       `json:"-"`
}

// GitOwner is a scaler's GitOps owner, by its labels or annotation.
type GitOwner struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Cond returns the named condition, nil when absent.
func (s *Scaler) Cond(t string) *Cond {
	for i := range s.Conds {
		if s.Conds[i].Type == t {
			return &s.Conds[i]
		}
	}
	return nil
}

// CondIs reports whether the named condition is present with that status.
func (s *Scaler) CondIs(t, status string) bool {
	c := s.Cond(t)
	return c != nil && c.Status == status
}

// AuthRefs are the scaler's authentication references, deduplicated, as
// "Kind/name" (ClusterTriggerAuthentication or TriggerAuthentication).
func (s *Scaler) AuthRefs() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range s.Triggers {
		if t.AuthName == "" {
			continue
		}
		k := t.AuthKind + "/" + t.AuthName
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ScaledObject is a ScaledObject as S9 reads it (§10).
type ScaledObject struct {
	Scaler
	Transfer         bool                   `json:"transfer,omitempty"`
	ForceActivation  bool                   `json:"forceActivation,omitempty"`
	TargetAPIVersion string                 `json:"targetApiVersion,omitempty"`
	TargetKind       string                 `json:"targetKind"`
	TargetName       string                 `json:"targetName"`
	HPANameSpec      string                 `json:"hpaNameSpec,omitempty"`
	Min              *int32                 `json:"min,omitempty"`
	Max              *int32                 `json:"max,omitempty"`
	Idle             *int32                 `json:"idle,omitempty"`
	Fallback         *Fallback              `json:"fallback,omitempty"`
	ScalingModifiers bool                   `json:"scalingModifiers,omitempty"`
	HPAName          string                 `json:"hpaName,omitempty"`
	GVKR             *GVKR                  `json:"gvkr,omitempty"`
	PausedCount      *int32                 `json:"pausedCount,omitempty"` // status.pausedReplicaCount, presence kept
	Health           map[string]HealthEntry `json:"health,omitempty"`
	HealthCut        bool                   `json:"healthCut,omitempty"`
	Activity         map[string]bool        `json:"activity,omitempty"`
	ActivityCut      bool                   `json:"activityCut,omitempty"`
}

// Target is the scale target's group, kind and name: from
// `status.scaleTargetGVKR` when KEDA resolved it, else from the spec, whose
// defaults are apps/v1 Deployment.
func (s *ScaledObject) Target() (group, kind, name string) {
	if s.GVKR != nil && s.GVKR.Kind != "" {
		return s.GVKR.Group, s.GVKR.Kind, s.TargetName
	}
	kind = s.TargetKind
	if kind == "" {
		kind = "Deployment"
	}
	api := s.TargetAPIVersion
	if api == "" {
		api = "apps/v1"
	}
	gv, err := schema.ParseGroupVersion(api)
	if err != nil {
		return "\x00invalid", kind, s.TargetName
	}
	return gv.Group, kind, s.TargetName
}

// ScaledJob is a ScaledJob as S9 reads it (§10): never its Job template.
type ScaledJob struct {
	Scaler
	SuccessfulLimit *int32 `json:"successfulLimit,omitempty"`
	FailedLimit     *int32 `json:"failedLimit,omitempty"`
}

// Ref is one object an authentication source names: a Secret, a ConfigMap,
// a ServiceAccount or an env var - its name and key, never a value.
type Ref struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	Parameter string `json:"parameter,omitempty"`
}

// Source is one authentication source kind with its references.
type Source struct {
	Kind   string `json:"kind"`
	Refs   []Ref  `json:"refs,omitempty"`
	RefN   int    `json:"refN"`
	Cut    bool   `json:"cut,omitempty"`
	Detail string `json:"detail,omitempty"` // an address, vault URI, region or token URL
	Method string `json:"method,omitempty"` // Vault's authentication method
}

// PodIdentity is `spec.podIdentity`, presence of the role and id kept.
type PodIdentity struct {
	Provider      string `json:"provider"`
	IdentityOwner string `json:"identityOwner,omitempty"`
	RoleARN       string `json:"roleArn,omitempty"`
	IdentityID    string `json:"identityId,omitempty"`
	// IdentityTenantID is azure-workload's requested tenant: part of the
	// credential KEDA builds (NewADWorkloadIdentityCredential); "" is the
	// operator's default tenant.
	IdentityTenantID string `json:"identityTenantId,omitempty"`
}

// Auth is a TriggerAuthentication or ClusterTriggerAuthentication (§10):
// sources and references only. hashiCorpVault.credential.token is never
// read; only the method "token" is kept.
type Auth struct {
	Meta
	Cluster     bool         `json:"cluster,omitempty"`
	Sources     []Source     `json:"sources,omitempty"`
	PodIdentity *PodIdentity `json:"podIdentity,omitempty"`
	Size        int          `json:"-"`
}

// Project is the KindSpec projection for one KEDA resource.
func Project(resource string) func(obj any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("keda: want *Unstructured, got %T", obj)
		}
		switch resource {
		case ResourceScaledObjects:
			return ProjectScaledObject(u), nil
		case ResourceScaledJobs:
			return ProjectScaledJob(u), nil
		case ResourceTriggerAuths, ResourceClusterTriggerAuths:
			return ProjectAuth(u, resource == ResourceClusterTriggerAuths), nil
		}
		return nil, fmt.Errorf("keda: no projection for %s", resource)
	}
}

// SizeOf is the budget's Size function.
func SizeOf(v any) int {
	switch o := v.(type) {
	case *ScaledObject:
		return o.Size
	case *ScaledJob:
		return o.Size
	case *Auth:
		return o.Size
	}
	return MaxObject + 1 // never retained
}

// ProjectScaledObject projects a ScaledObject.
func ProjectScaledObject(u *unstructured.Unstructured) *ScaledObject {
	s := &ScaledObject{Scaler: scalerOf(u)}
	ann := u.GetAnnotations()
	s.Transfer = ann[AnnTransfer] == "true"
	_, s.ForceActivation = ann[AnnForceActivate]
	s.Pause = Pause{Paused: annp(ann, AnnPaused), PausedReplicas: annp(ann, AnnPausedReplicas),
		ScaleIn: annp(ann, AnnPausedScaleIn), ScaleOut: annp(ann, AnnPausedScaleOut)}
	s.TargetAPIVersion = clip(str(u, "spec", "scaleTargetRef", "apiVersion"), maxAPI)
	s.TargetKind = clip(str(u, "spec", "scaleTargetRef", "kind"), maxKind)
	s.TargetName = clip(str(u, "spec", "scaleTargetRef", "name"), maxName)
	s.HPANameSpec = clip(str(u, "spec", "advanced", "horizontalPodAutoscalerConfig", "name"), maxName)
	s.Min, s.Max, s.Idle = i32p(u, "spec", "minReplicaCount"), i32p(u, "spec", "maxReplicaCount"), i32p(u, "spec", "idleReplicaCount")
	if fb, ok, _ := unstructured.NestedMap(u.Object, "spec", "fallback"); ok && fb != nil {
		f := &Fallback{Behavior: clip(str(u, "spec", "fallback", "behavior"), maxShort)}
		if v := i32p(u, "spec", "fallback", "failureThreshold"); v != nil {
			f.FailureThreshold = *v
		}
		if v := i32p(u, "spec", "fallback", "replicas"); v != nil {
			f.Replicas = *v
		}
		s.Fallback = f
	}
	_, s.ScalingModifiers, _ = unstructured.NestedMap(u.Object, "spec", "advanced", "scalingModifiers")
	s.HPAName = clip(str(u, "status", "hpaName"), maxName)
	if g, ok, _ := unstructured.NestedMap(u.Object, "status", "scaleTargetGVKR"); ok && g != nil {
		s.GVKR = &GVKR{Group: clip(str(u, "status", "scaleTargetGVKR", "group"), maxAPI), Version: clip(str(u, "status", "scaleTargetGVKR", "version"), maxShort),
			Kind: clip(str(u, "status", "scaleTargetGVKR", "kind"), maxKind), Resource: clip(str(u, "status", "scaleTargetGVKR", "resource"), maxName)}
	}
	s.PausedCount = i32p(u, "status", "pausedReplicaCount")
	if h, ok, _ := unstructured.NestedMap(u.Object, "status", "health"); ok {
		keys := sortedKeys(h)
		s.Health = map[string]HealthEntry{}
		for _, k := range keys {
			if len(s.Health) == maxHealth {
				s.HealthCut = true
				break
			}
			e := HealthEntry{Status: clip(str(u, "status", "health", k, "status"), maxShort)}
			if v := i32p(u, "status", "health", k, "numberOfFailures"); v != nil {
				e.Failures = *v
			}
			s.Health[clip(k, maxShort)] = e
		}
	}
	if a, ok, _ := unstructured.NestedMap(u.Object, "status", "triggersActivity"); ok {
		keys := sortedKeys(a)
		s.Activity = map[string]bool{}
		for _, k := range keys {
			if len(s.Activity) == maxHealth {
				s.ActivityCut = true
				break
			}
			v, _, _ := unstructured.NestedBool(u.Object, "status", "triggersActivity", k, "isActive")
			s.Activity[clip(k, maxShort)] = v
		}
	}
	s.Size = soSize(s)
	return s
}

// ProjectScaledJob projects a ScaledJob: only `autoscaling.keda.sh/paused`
// pauses it (§1.3).
func ProjectScaledJob(u *unstructured.Unstructured) *ScaledJob {
	j := &ScaledJob{Scaler: scalerOf(u)}
	j.Pause = Pause{Paused: annp(u.GetAnnotations(), AnnPaused)}
	j.SuccessfulLimit, j.FailedLimit = i32p(u, "spec", "successfulJobsHistoryLimit"), i32p(u, "spec", "failedJobsHistoryLimit")
	j.Size = scalerSize(&j.Scaler) + 32
	return j
}

func scalerOf(u *unstructured.Unstructured) Scaler {
	s := Scaler{Meta: metaOf(u)}
	triggers, _, _ := unstructured.NestedSlice(u.Object, "spec", "triggers")
	s.TriggerN = len(triggers)
	for _, t := range triggers {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if len(s.Triggers) == maxTriggers {
			s.TriggersCut = true
			break
		}
		tr := Trigger{Type: clip(mstr(m, "type"), maxShort), Name: clip(mstr(m, "name"), maxShort),
			MetricType: clip(mstr(m, "metricType"), maxShort)}
		tr.Cached, _ = m["useCachedMetrics"].(bool)
		if ar, ok := m["authenticationRef"].(map[string]any); ok {
			tr.AuthName = clip(mstr(ar, "name"), maxName)
			tr.AuthKind = KindTriggerAuth
			if mstr(ar, "kind") == KindClusterTriggerAuth {
				tr.AuthKind = KindClusterTriggerAuth
			}
		}
		s.Triggers = append(s.Triggers, tr)
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if len(s.Conds) == maxConds {
			s.CondsCut = true
			break
		}
		s.Conds = append(s.Conds, Cond{Type: clip(mstr(m, "type"), maxShort), Status: clip(mstr(m, "status"), 16),
			Reason: clip(mstr(m, "reason"), maxShort), Message: Display(mstr(m, "message"), maxMessage)})
	}
	if o := flux.OwnerOf(u.GetLabels(), u.GetAnnotations()); o != nil {
		s.Owner = &GitOwner{Kind: o.Kind, Namespace: clip(o.Namespace, maxName), Name: clip(o.Name, 512)}
	}
	if t := str(u, "status", "lastActiveTime"); t != "" {
		if at, err := time.Parse(time.RFC3339, t); err == nil {
			s.LastActive = at
		}
	}
	return s
}

// ProjectAuth projects an authentication: each source's kind and the
// names it references, capped with a flag and an exact count.
func ProjectAuth(u *unstructured.Unstructured, cluster bool) *Auth {
	a := &Auth{Meta: metaOf(u), Cluster: cluster}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	add := func(src Source) {
		if src.RefN > 0 || src.Detail != "" || src.Method != "" {
			a.Sources = append(a.Sources, src)
		}
	}
	// the in-cluster references
	add(refList(spec, "secretTargetRef", "Secret", "name", "key"))
	add(refList(spec, "configMapTargetRef", "ConfigMap", "name", "key"))
	add(refList(spec, "env", "Env", "name", ""))
	add(refList(spec, "boundServiceAccountToken", "ServiceAccount", "serviceAccountName", ""))
	// the external sources: what they point at, and the Secrets their
	// credentials reference (valueFrom.secretKeyRef)
	if v, ok := spec["hashiCorpVault"].(map[string]any); ok {
		// credential.token is a literal token: never read (S9 §10)
		src := Source{Kind: "hashiCorpVault", Detail: Display(mstr(v, "address"), maxDetail), Method: clip(mstr(v, "authentication"), maxShort)}
		if secrets, ok := v["secrets"].([]any); ok {
			src.RefN = len(secrets)
		}
		add(src)
	}
	for _, ext := range []struct{ field, detail string }{{"azureKeyVault", "vaultUri"}, {"gcpSecretManager", ""}, {"awsSecretManager", "region"}, {"oauth2", "tokenUrl"}, {"azureServicePrincipal", "tenantId"}} {
		v, ok := spec[ext.field].(map[string]any)
		if !ok {
			continue
		}
		src := Source{Kind: ext.field}
		if ext.detail != "" {
			src.Detail = Display(mstr(v, ext.detail), maxDetail)
		}
		for _, ref := range secretKeyRefs(v) {
			src.RefN++
			if len(src.Refs) == maxRefs {
				src.Cut = true
				continue
			}
			src.Refs = append(src.Refs, ref)
		}
		add(src)
	}
	if p, ok := spec["podIdentity"].(map[string]any); ok {
		a.PodIdentity = &PodIdentity{Provider: clip(mstr(p, "provider"), maxShort), IdentityOwner: clip(mstr(p, "identityOwner"), maxShort),
			RoleARN: clip(mstr(p, "roleArn"), maxDetail), IdentityID: clip(mstr(p, "identityId"), maxDetail), IdentityTenantID: clip(mstr(p, "identityTenantId"), maxDetail)}
	}
	a.Size = authSize(a)
	return a
}

// refList reads one list source of in-cluster references.
func refList(spec map[string]any, field, kind, nameKey, keyKey string) Source {
	src := Source{Kind: field}
	list, _ := spec[field].([]any)
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		src.RefN++
		if len(src.Refs) == maxRefs {
			src.Cut = true
			continue
		}
		r := Ref{Kind: kind, Name: clip(mstr(m, nameKey), maxName), Parameter: clip(mstr(m, "parameter"), maxShort)}
		if keyKey != "" {
			r.Key = clip(mstr(m, keyKey), maxName)
		}
		src.Refs = append(src.Refs, r)
	}
	return src
}

// secretKeyRefs finds every valueFrom.secretKeyRef under v, in a stable
// order: the external sources' credentials reference Secrets this way.
func secretKeyRefs(v map[string]any) []Ref {
	var out []Ref
	var walk func(path string, x any)
	walk = func(path string, x any) {
		switch t := x.(type) {
		case map[string]any:
			if vf, ok := t["valueFrom"].(map[string]any); ok {
				if sk, ok := vf["secretKeyRef"].(map[string]any); ok {
					out = append(out, Ref{Kind: "Secret", Name: clip(mstr(sk, "name"), maxName), Key: clip(mstr(sk, "key"), maxName), Parameter: clip(path, maxShort)})
				}
			}
			for _, k := range sortedKeys(t) {
				if k != "valueFrom" {
					walk(strings.TrimPrefix(path+"."+k, "."), t[k])
				}
			}
		case []any:
			for i, e := range t {
				walk(path+"["+strconv.Itoa(i)+"]", e)
			}
		}
	}
	walk("", v)
	return out
}

// ---- size ---------------------------------------------------------------

func metaSize(m Meta) int {
	n := 128 + len(m.Namespace) + len(m.Name) + len(m.UID) + len(m.RV)
	for k, v := range m.Labels {
		n += 16 + len(k) + len(v)
	}
	return n
}

func scalerSize(s *Scaler) int {
	n := metaSize(s.Meta) + 64
	if s.Owner != nil {
		n += 48 + len(s.Owner.Namespace) + len(s.Owner.Name)
	}
	for _, p := range []*string{s.Pause.Paused, s.Pause.PausedReplicas, s.Pause.ScaleIn, s.Pause.ScaleOut} {
		if p != nil {
			n += 16 + len(*p)
		}
	}
	for _, t := range s.Triggers {
		n += 48 + len(t.Type) + len(t.Name) + len(t.MetricType) + len(t.AuthName) + len(t.AuthKind)
	}
	for _, c := range s.Conds {
		n += 48 + len(c.Type) + len(c.Status) + len(c.Reason) + len(c.Message)
	}
	return n
}

func soSize(s *ScaledObject) int {
	n := scalerSize(&s.Scaler) + 128 + len(s.TargetAPIVersion) + len(s.TargetKind) + len(s.TargetName) + len(s.HPANameSpec) + len(s.HPAName)
	if s.Fallback != nil {
		n += 32 + len(s.Fallback.Behavior)
	}
	if s.GVKR != nil {
		n += 48 + len(s.GVKR.Group) + len(s.GVKR.Version) + len(s.GVKR.Kind) + len(s.GVKR.Resource)
	}
	for k, e := range s.Health {
		n += 32 + len(k) + len(e.Status)
	}
	for k := range s.Activity {
		n += 24 + len(k)
	}
	return n
}

func authSize(a *Auth) int {
	n := metaSize(a.Meta) + 32
	for _, s := range a.Sources {
		n += 48 + len(s.Kind) + len(s.Detail) + len(s.Method)
		for _, r := range s.Refs {
			n += 32 + len(r.Kind) + len(r.Name) + len(r.Key) + len(r.Parameter)
		}
	}
	if p := a.PodIdentity; p != nil {
		n += 48 + len(p.Provider) + len(p.IdentityOwner) + len(p.RoleARN) + len(p.IdentityID) + len(p.IdentityTenantID)
	}
	return n
}

// ---- building -----------------------------------------------------------

func metaOf(u *unstructured.Unstructured) Meta {
	m := Meta{Namespace: clip(u.GetNamespace(), maxName), Name: clip(u.GetName(), maxName), UID: clip(string(u.GetUID()), maxUID),
		RV: clip(u.GetResourceVersion(), maxShort), Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().Time}
	if labels := u.GetLabels(); len(labels) > 0 {
		m.Labels = map[string]string{}
		for _, k := range sortedStringKeys(labels) {
			if len(m.Labels) == maxLabels {
				m.LabelsCut = true
				break
			}
			v := labels[k]
			if len(k) > maxName || len(v) > maxLabelValue {
				m.LabelsCut = true // never a clipped label: a selector reads exact values
				continue
			}
			m.Labels[k] = v
		}
	}
	return m
}

func annp(ann map[string]string, key string) *string {
	v, ok := ann[key]
	if !ok {
		return nil
	}
	v = clip(v, maxShort)
	return &v
}

func str(u *unstructured.Unstructured, path ...string) string {
	v, _, _ := unstructured.NestedString(u.Object, path...)
	return v
}

func mstr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func i32p(u *unstructured.Unstructured, path ...string) *int32 {
	v, ok, err := unstructured.NestedFieldNoCopy(u.Object, path...)
	if !ok || err != nil {
		return nil
	}
	var n int64
	switch x := v.(type) {
	case int64:
		n = x
	case float64:
		n = int64(x)
	case int:
		n = int64(x)
	default:
		return nil
	}
	if n > 1<<31-1 || n < -(1<<31) {
		return nil
	}
	r := int32(n)
	return &r
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Display is a free string for display: the URL rule first, then the clip.
func Display(s string, n int) string { return clip(urlscrub.Field(s).Text, n) }

// clip cuts s to n bytes on a rune boundary, marked; the concatenation
// copies, so the cut owns its bytes.
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
