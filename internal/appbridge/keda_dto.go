package appbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
)

// Coverage S9's rows: the standalone HPAs, KEDA's scalers and its
// authentications, converted from the SAME evaluation as the parts' counts.
// A fact row (a KEDA-owned HPA, an authentication) carries no verdict: its
// zero evaluation never reaches the frontend as one (S2b's FactRow).

// HPAConditionDTO is one HPA condition, with its own transition time.
type HPAConditionDTO struct {
	Type    string     `json:"type"`
	Status  string     `json:"status"`
	Reason  string     `json:"reason,omitempty"`
	Message string     `json:"message,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

// HPATargetDTO is an HPA's scale target.
type HPATargetDTO struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// HPARowDTO is one HPA row (S9 §4).
type HPARowDTO struct {
	UID                string            `json:"uid"`
	GVR                string            `json:"gvr"`
	Namespace          string            `json:"namespace"`
	Name               string            `json:"name"`
	Created            *time.Time        `json:"created,omitempty"`
	Target             HPATargetDTO      `json:"target"`
	Min                *int32            `json:"min,omitempty"`
	Max                int32             `json:"max"`
	Current            int32             `json:"current"`
	Desired            int32             `json:"desired"`
	LastScale          *time.Time        `json:"lastScale,omitempty"`
	Metrics            int               `json:"metrics"`
	MetricTypes        []string          `json:"metricTypes"`
	Conditions         []HPAConditionDTO `json:"conditions"`
	ConditionsCut      bool              `json:"conditionsCut,omitempty"`
	Generation         int64             `json:"generation"`
	ObservedGeneration *int64            `json:"observedGeneration,omitempty"`
	GenerationReported bool              `json:"generationReported"`
	// KEDAOwner names the ScaledObject that controls it: a fact row, its
	// health judged under that ScaledObject; Rule is then absent.
	KEDAOwner string   `json:"kedaOwner,omitempty"`
	Others    []string `json:"others"`
	Reason    string   `json:"reason,omitempty"`
	Rule      *RuleDTO `json:"rule,omitempty"`
}

func hpaConditions(cs []hpa.Cond) []HPAConditionDTO {
	out := make([]HPAConditionDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, HPAConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, At: timePtrOf(c.At)})
	}
	return out
}

func hpaRowDTO(r healthsrc.Row[healthsrc.HPARow]) HPARowDTO {
	h := r.Data.HPA
	d := HPARowDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: h.Namespace, Name: h.Name, Created: timePtrOf(h.Created),
		Target: HPATargetDTO{APIVersion: h.Target.APIVersion, Kind: h.Target.Kind, Name: h.Target.Name}, Min: h.Min, Max: h.Max,
		Current: h.Current, Desired: h.Desired, LastScale: timePtrOf(h.LastScale), Metrics: h.Metrics, MetricTypes: nonNil(h.MetricTypes),
		Conditions: hpaConditions(h.Conds), ConditionsCut: h.CondsCut, Generation: h.Generation, ObservedGeneration: h.ObservedGeneration,
		GenerationReported: r.Data.GenerationReported, KEDAOwner: r.Data.KEDAOwner, Others: nonNil(r.Data.Others)}
	if !r.Data.FactOnly() { // only a counted row carries Klyx's verdict
		rule := toRuleDTO(r.Eval)
		d.Reason, d.Rule = r.Eval.Reason, &rule
	}
	return d
}

// KEDAConditionDTO is a KEDA condition: KEDA writes no transition time.
type KEDAConditionDTO struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// KEDATriggerDTO is one trigger: never its metadata (S9 §10).
type KEDATriggerDTO struct {
	Type       string `json:"type"`
	Name       string `json:"name,omitempty"`
	MetricType string `json:"metricType,omitempty"`
	AuthName   string `json:"authName,omitempty"`
	AuthKind   string `json:"authKind,omitempty"`
	Cached     bool   `json:"cached,omitempty"`
}

// KEDAMetricDTO is one metric's failure count and activity.
type KEDAMetricDTO struct {
	Metric   string `json:"metric"`
	Failures *int32 `json:"failures,omitempty"` // absent: not tracked (no fallback)
	Status   string `json:"status,omitempty"`
	Active   *bool  `json:"active,omitempty"`
}

// KEDAPauseDTO is the pause annotations as written, and what they request.
type KEDAPauseDTO struct {
	Paused         *string  `json:"paused,omitempty"`
	PausedReplicas *string  `json:"pausedReplicas,omitempty"`
	ScaleIn        *string  `json:"scaleIn,omitempty"`
	ScaleOut       *string  `json:"scaleOut,omitempty"`
	Kind           string   `json:"kind"` // "full" | "directional" | ""
	Annotation     string   `json:"annotation,omitempty"`
	Requested      *int32   `json:"requested,omitempty"`
	Malformed      bool     `json:"malformed,omitempty"`
	Directions     []string `json:"directions"`
	NotApplied     string   `json:"notApplied,omitempty"`
	Unverified     string   `json:"unverified,omitempty"`
	Applied        *int32   `json:"applied,omitempty"` // status.pausedReplicaCount
	Completed      bool     `json:"completed"`
}

// KEDAFindingDTO is one finding behind a verdict (S9 §3.2).
type KEDAFindingDTO struct {
	Role  string `json:"role"`
	Label string `json:"label"`
	Text  string `json:"text,omitempty"`
	Note  bool   `json:"note,omitempty"`
}

// KEDAFallbackDTO is the configured fallback.
type KEDAFallbackDTO struct {
	FailureThreshold int32  `json:"failureThreshold"`
	Replicas         int32  `json:"replicas"`
	Behavior         string `json:"behavior"`
}

// KEDAJoinedHPADTO is the HPA the ScaledObject controls, as read.
type KEDAJoinedHPADTO struct {
	Name       string            `json:"name"`
	UID        string            `json:"uid"`
	Min        *int32            `json:"min,omitempty"`
	Max        int32             `json:"max"`
	Current    int32             `json:"current"`
	Desired    int32             `json:"desired"`
	Conditions []HPAConditionDTO `json:"conditions"`
}

// ScaledObjectDTO is one ScaledObject row (S9 §9).
type ScaledObjectDTO struct {
	UID              string             `json:"uid"`
	GVR              string             `json:"gvr"`
	Namespace        string             `json:"namespace"`
	Name             string             `json:"name"`
	Created          *time.Time         `json:"created,omitempty"`
	TargetGroup      string             `json:"targetGroup"`
	TargetKind       string             `json:"targetKind"`
	TargetName       string             `json:"targetName"`
	HPANameSpec      string             `json:"hpaNameSpec,omitempty"`
	HPAName          string             `json:"hpaName,omitempty"`
	Min              *int32             `json:"min,omitempty"`
	Max              *int32             `json:"max,omitempty"`
	Idle             *int32             `json:"idle,omitempty"`
	Fallback         *KEDAFallbackDTO   `json:"fallback,omitempty"`
	ScalingModifiers bool               `json:"scalingModifiers,omitempty"`
	Transfer         bool               `json:"transfer,omitempty"`
	Triggers         []KEDATriggerDTO   `json:"triggers"`
	TriggerCount     int                `json:"triggerCount"`
	TriggersCut      bool               `json:"triggersCut,omitempty"`
	Conditions       []KEDAConditionDTO `json:"conditions"`
	ConditionsCut    bool               `json:"conditionsCut,omitempty"`
	Metrics          []KEDAMetricDTO    `json:"metrics"`
	MetricsCut       bool               `json:"metricsCut,omitempty"`
	HealthTracked    bool               `json:"healthTracked"` // status.health present (only with a valid fallback)
	LastActive       *time.Time         `json:"lastActive,omitempty"`
	Pause            KEDAPauseDTO       `json:"pause"`
	HPA              *KEDAJoinedHPADTO  `json:"hpa,omitempty"`
	HPACurrent       bool               `json:"hpaCurrent"`
	HPAComplete      bool               `json:"hpaComplete"`
	Others           []string           `json:"others"`
	Version          string             `json:"version"`
	AuthsRead        string             `json:"authsRead"`
	Findings         []KEDAFindingDTO   `json:"findings"`
	Owner            *FluxOpOwnerDTO    `json:"owner,omitempty"`
	Reason           string             `json:"reason,omitempty"`
	Rule             RuleDTO            `json:"rule"`
	// EvidenceRev changes with every input the on-demand evidence reads
	// from the scaler itself: the inspector re-reads on its change and
	// shows a reply only for its own revision (implementation review 1,
	// finding 7).
	EvidenceRev string `json:"evidenceRev"`
}

// evidenceRev fingerprints the scaler inputs the on-demand evidence reads
// (§6): its generation (every spec change, a ScaledJob's Job template's
// ServiceAccount among them), its namespace and labels (the handling
// operator; a label change writes no generation), its target (the
// workload's ServiceAccount) and its authentication references.
func evidenceRev(s *keda.Scaler, target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n%s\n%s\n", s.Generation, s.Namespace, target)
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + "=" + s.Labels[k] + "\n")
	}
	fmt.Fprintf(&b, "%v|%d|%v\n", s.LabelsCut, s.TriggerN, s.TriggersCut)
	for _, t := range s.Triggers {
		b.WriteString(t.AuthKind + "/" + t.AuthName + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func kedaOwner(o *keda.GitOwner) *FluxOpOwnerDTO {
	if o == nil {
		return nil
	}
	return &FluxOpOwnerDTO{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
}

func kedaConditions(cs []keda.Cond) []KEDAConditionDTO {
	out := make([]KEDAConditionDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, KEDAConditionDTO(c))
	}
	return out
}

func kedaTriggers(ts []keda.Trigger) []KEDATriggerDTO {
	out := make([]KEDATriggerDTO, 0, len(ts))
	for _, t := range ts {
		out = append(out, KEDATriggerDTO(t))
	}
	return out
}

func kedaFindings(fs []keda.Finding) []KEDAFindingDTO {
	out := make([]KEDAFindingDTO, 0, len(fs))
	for _, f := range fs {
		out = append(out, KEDAFindingDTO{Role: f.Role.String(), Label: f.Label, Text: f.Text, Note: f.Note})
	}
	return out
}

func kedaPause(p keda.Pause, in keda.Intent, applied *int32, completed bool) KEDAPauseDTO {
	kind := ""
	switch in.Kind {
	case keda.PauseFull:
		kind = "full"
	case keda.PauseDirectional:
		kind = "directional"
	}
	return KEDAPauseDTO{Paused: p.Paused, PausedReplicas: p.PausedReplicas, ScaleIn: p.ScaleIn, ScaleOut: p.ScaleOut, Kind: kind,
		Annotation: in.Annotation, Requested: in.Requested, Malformed: in.Malformed, Directions: nonNil(in.Directions),
		NotApplied: in.NotApplied, Unverified: in.Unverified, Applied: applied, Completed: completed}
}

// kedaMetrics joins status.health and status.triggersActivity by metric.
func kedaMetrics(so *keda.ScaledObject) []KEDAMetricDTO {
	keys := map[string]bool{}
	for k := range so.Health {
		keys[k] = true
	}
	for k := range so.Activity {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]KEDAMetricDTO, 0, len(names))
	for _, k := range names {
		m := KEDAMetricDTO{Metric: k}
		if e, ok := so.Health[k]; ok {
			f := e.Failures
			m.Failures, m.Status = &f, e.Status
		}
		if a, ok := so.Activity[k]; ok {
			m.Active = &a
		}
		out = append(out, m)
	}
	return out
}

func scaledObjectDTO(r healthsrc.Row[healthsrc.ScaledObjectRow]) ScaledObjectDTO {
	so, v := r.Data.ScaledObject, r.Data.Verdict
	group, kind, name := so.Target()
	d := ScaledObjectDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: so.Namespace, Name: so.Name, Created: timePtrOf(so.Created),
		TargetGroup: group, TargetKind: kind, TargetName: name, HPANameSpec: so.HPANameSpec, HPAName: so.HPAName,
		Min: so.Min, Max: so.Max, Idle: so.Idle, ScalingModifiers: so.ScalingModifiers, Transfer: so.Transfer,
		Triggers: kedaTriggers(so.Triggers), TriggerCount: so.TriggerN, TriggersCut: so.TriggersCut,
		Conditions: kedaConditions(so.Conds), ConditionsCut: so.CondsCut, Metrics: kedaMetrics(so), MetricsCut: so.HealthCut || so.ActivityCut,
		HealthTracked: so.Health != nil, LastActive: timePtrOf(so.LastActive),
		Pause:      kedaPause(so.Pause, v.Intent, so.PausedCount, v.Completed),
		HPACurrent: r.Data.HPACurrent, HPAComplete: r.Data.HPAComplete, Others: nonNil(r.Data.Others),
		Version: r.Data.Version.String(), AuthsRead: r.Data.AuthsRead, Findings: kedaFindings(v.Findings), Owner: kedaOwner(so.Owner),
		Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval), EvidenceRev: evidenceRev(&so.Scaler, group+"/"+kind+"/"+name)}
	if f := so.Fallback; f != nil {
		d.Fallback = &KEDAFallbackDTO{FailureThreshold: f.FailureThreshold, Replicas: f.Replicas, Behavior: f.Behavior}
		if d.Fallback.Behavior == "" {
			d.Fallback.Behavior = "static"
		}
	}
	if h := r.Data.HPA; h != nil {
		d.HPA = &KEDAJoinedHPADTO{Name: h.Name, UID: h.UID, Min: h.Min, Max: h.Max, Current: h.Current, Desired: h.Desired, Conditions: hpaConditions(h.Conds)}
	}
	return d
}

// ScaledJobDTO is one ScaledJob row (S9 §9).
type ScaledJobDTO struct {
	UID             string             `json:"uid"`
	GVR             string             `json:"gvr"`
	Namespace       string             `json:"namespace"`
	Name            string             `json:"name"`
	Created         *time.Time         `json:"created,omitempty"`
	Triggers        []KEDATriggerDTO   `json:"triggers"`
	TriggerCount    int                `json:"triggerCount"`
	TriggersCut     bool               `json:"triggersCut,omitempty"`
	Conditions      []KEDAConditionDTO `json:"conditions"`
	ConditionsCut   bool               `json:"conditionsCut,omitempty"`
	LastActive      *time.Time         `json:"lastActive,omitempty"`
	SuccessfulLimit *int32             `json:"successfulLimit,omitempty"`
	FailedLimit     *int32             `json:"failedLimit,omitempty"`
	Pause           KEDAPauseDTO       `json:"pause"`
	Version         string             `json:"version"`
	Findings        []KEDAFindingDTO   `json:"findings"`
	Owner           *FluxOpOwnerDTO    `json:"owner,omitempty"`
	Reason          string             `json:"reason,omitempty"`
	Rule            RuleDTO            `json:"rule"`
	EvidenceRev     string             `json:"evidenceRev"`
}

func scaledJobDTO(r healthsrc.Row[healthsrc.ScaledJobRow]) ScaledJobDTO {
	j, v := r.Data.ScaledJob, r.Data.Verdict
	return ScaledJobDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: j.Namespace, Name: j.Name, Created: timePtrOf(j.Created),
		Triggers: kedaTriggers(j.Triggers), TriggerCount: j.TriggerN, TriggersCut: j.TriggersCut,
		Conditions: kedaConditions(j.Conds), ConditionsCut: j.CondsCut, LastActive: timePtrOf(j.LastActive),
		SuccessfulLimit: j.SuccessfulLimit, FailedLimit: j.FailedLimit, Pause: kedaPause(j.Pause, v.Intent, nil, v.Completed),
		Version: r.Data.Version.String(), Findings: kedaFindings(v.Findings), Owner: kedaOwner(j.Owner), Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval),
		EvidenceRev: evidenceRev(&j.Scaler, "")}
}

// KEDARefDTO is one object an authentication source names: never a value.
type KEDARefDTO struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	Parameter string `json:"parameter,omitempty"`
}

// KEDASourceDTO is one authentication source kind.
type KEDASourceDTO struct {
	Kind     string       `json:"kind"`
	Refs     []KEDARefDTO `json:"refs"`
	RefCount int          `json:"refCount"`
	Cut      bool         `json:"cut,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	Method   string       `json:"method,omitempty"`
}

// KEDAPodIdentityDTO is podIdentity.
type KEDAPodIdentityDTO struct {
	Provider         string `json:"provider"`
	IdentityOwner    string `json:"identityOwner,omitempty"`
	RoleARN          string `json:"roleArn,omitempty"`
	IdentityID       string `json:"identityId,omitempty"`
	IdentityTenantID string `json:"identityTenantId,omitempty"`
}

// KEDAAuthDTO is one authentication: a fact row (S9 §6.2).
type KEDAAuthDTO struct {
	UID           string              `json:"uid"`
	GVR           string              `json:"gvr"`
	Namespace     string              `json:"namespace,omitempty"`
	Name          string              `json:"name"`
	Cluster       bool                `json:"cluster"`
	Created       *time.Time          `json:"created,omitempty"`
	Sources       []KEDASourceDTO     `json:"sources"`
	PodIdentity   *KEDAPodIdentityDTO `json:"podIdentity,omitempty"`
	Users         []string            `json:"users"`
	UsersComplete bool                `json:"usersComplete"`
}

func kedaAuthDTO(r healthsrc.Row[healthsrc.AuthRow]) KEDAAuthDTO {
	a := r.Data.Auth
	d := KEDAAuthDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: a.Namespace, Name: a.Name, Cluster: a.Cluster, Created: timePtrOf(a.Created),
		Sources: []KEDASourceDTO{}, Users: nonNil(r.Data.Users), UsersComplete: r.Data.UsersComplete}
	for _, s := range a.Sources {
		src := KEDASourceDTO{Kind: s.Kind, Refs: []KEDARefDTO{}, RefCount: s.RefN, Cut: s.Cut, Detail: s.Detail, Method: s.Method}
		for _, ref := range s.Refs {
			src.Refs = append(src.Refs, KEDARefDTO(ref))
		}
		d.Sources = append(d.Sources, src)
	}
	if p := a.PodIdentity; p != nil {
		d.PodIdentity = &KEDAPodIdentityDTO{Provider: p.Provider, IdentityOwner: p.IdentityOwner, RoleARN: p.RoleARN, IdentityID: p.IdentityID, IdentityTenantID: p.IdentityTenantID}
	}
	return d
}

func init() {
	rowsConverters[healthsrc.TaskAutoscaling+"/hpas"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.HPARow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]HPARowDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, hpaRowDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskKEDA+"/scaledobjects"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.ScaledObjectRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ScaledObjectDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, scaledObjectDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskKEDA+"/scaledjobs"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.ScaledJobRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ScaledJobDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, scaledJobDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskKEDA+"/authentications"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.AuthRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]KEDAAuthDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, kedaAuthDTO(r))
		}
		return out, nil
	}
}
