package appbridge

import (
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/gitops/fluxop"
	"github.com/moomora/klyx/internal/healthsrc"
)

// The Flux Operator's DTOs (coverage S1a §2): the Operator view's rows from
// the shared Observer, and the inspector's on-demand facts.

// FluxOpOwnerDTO is who applies an object (C4's consequence line).
type FluxOpOwnerDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// FluxOpCommonDTO is what every operator row carries.
type FluxOpCommonDTO struct {
	UID             string          `json:"uid"`
	GVR             string          `json:"gvr"`
	Generation      int64           `json:"generation"`
	Kind            string          `json:"kind"`
	Namespace       string          `json:"namespace"`
	Name            string          `json:"name"`
	Created         *time.Time      `json:"created,omitempty"`
	Suspended       bool            `json:"suspended"`
	Ready           string          `json:"ready"`
	Reason          string          `json:"reason,omitempty"`
	Message         string          `json:"message,omitempty"`
	ReadySince      *time.Time      `json:"readySince,omitempty"`
	ReadyGeneration int64           `json:"readyGeneration,omitempty"`
	Stalled         bool            `json:"stalled,omitempty"`
	Progress        string          `json:"progress,omitempty"`
	Resume          string          `json:"resume,omitempty"` // "", "awaiting", "evaluating"
	Owner           *FluxOpOwnerDTO `json:"owner,omitempty"`
	Clipped         []string        `json:"clipped"`
	Rule            RuleDTO         `json:"rule"`
}

// FluxInstanceDTO is one FluxInstance.
type FluxInstanceDTO struct {
	FluxOpCommonDTO
	Binding       string                  `json:"binding"`
	Version       string                  `json:"version,omitempty"`
	Registry      string                  `json:"registry,omitempty"`
	Variant       string                  `json:"variant,omitempty"`
	Artifact      string                  `json:"artifact,omitempty"`
	Components    []string                `json:"components"`
	Running       []FluxComponentImageDTO `json:"running"`
	ClusterType   string                  `json:"clusterType,omitempty"`
	Multitenant   bool                    `json:"multitenant,omitempty"`
	NetworkPolicy bool                    `json:"networkPolicy,omitempty"`
	ClusterSize   string                  `json:"clusterSize,omitempty"`
	ShardKey      string                  `json:"shardKey,omitempty"`
	Shards        []string                `json:"shards"`
	Sync          *FluxInstanceSyncDTO    `json:"sync,omitempty"`
	SyncName      string                  `json:"syncName,omitempty"`
	Wait          *bool                   `json:"wait,omitempty"`
	PatchCount    int64                   `json:"patchCount,omitempty"`
	LastApplied   string                  `json:"lastApplied,omitempty"`
	LastAttempted string                  `json:"lastAttempted,omitempty"`
	LastArtifact  string                  `json:"lastArtifact,omitempty"`
}

// FluxComponentImageDTO is one running component.
type FluxComponentImageDTO struct {
	Name       string `json:"name"`
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
}

// FluxInstanceSyncDTO is an instance's declared sync.
type FluxInstanceSyncDTO struct {
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
	Interval string `json:"interval,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// FluxSetDTO is one ResourceSet.
type FluxSetDTO struct {
	FluxOpCommonDTO
	InputStrategy       string              `json:"inputStrategy,omitempty"`
	InputsFrom          []FluxInputRefDTO   `json:"inputsFrom"`
	DependsOn           []FluxDependencyDTO `json:"dependsOn"`
	Steps               []string            `json:"steps"`
	StepCount           int64               `json:"stepCount,omitempty"`
	InputCount          int64               `json:"inputCount,omitempty"`
	ResourceCount       int64               `json:"resourceCount,omitempty"`
	HasTemplate         bool                `json:"hasTemplate,omitempty"`
	ServiceAccountName  string              `json:"serviceAccountName,omitempty"`
	Wait                bool                `json:"wait,omitempty"`
	LastAppliedRevision string              `json:"lastAppliedRevision,omitempty"`
	InventoryCount      int64               `json:"inventoryCount,omitempty"`
}

// FluxInputRefDTO is one inputsFrom entry.
type FluxInputRefDTO struct {
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name,omitempty"`
	Selector string `json:"selector,omitempty"`
}

// FluxDependencyDTO is one dependsOn entry.
type FluxDependencyDTO struct {
	APIVersion   string `json:"apiVersion"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace,omitempty"`
	Ready        bool   `json:"ready,omitempty"`
	HasReadyExpr bool   `json:"hasReadyExpr,omitempty"`
}

// FluxProviderDTO is one ResourceSetInputProvider.
type FluxProviderDTO struct {
	FluxOpCommonDTO
	Type                 string               `json:"type"`
	URL                  string               `json:"url,omitempty"`
	ServiceAccountName   string               `json:"serviceAccountName,omitempty"`
	SecretRef            string               `json:"secretRef,omitempty"`
	CertSecretRef        string               `json:"certSecretRef,omitempty"`
	Insecure             bool                 `json:"insecure,omitempty"`
	Schedule             []FluxScheduleDTO    `json:"schedule"`
	NextSchedule         *FluxNextScheduleDTO `json:"nextSchedule,omitempty"`
	LastExportedRevision string               `json:"lastExportedRevision,omitempty"`
	ExportedInputCount   int64                `json:"exportedInputCount"`
}

// FluxScheduleDTO is one provider schedule.
type FluxScheduleDTO struct {
	Cron     string `json:"cron"`
	TimeZone string `json:"timeZone,omitempty"`
	Window   string `json:"window,omitempty"`
}

// FluxNextScheduleDTO is the operator's next scheduled run.
type FluxNextScheduleDTO struct {
	FluxScheduleDTO
	When *time.Time `json:"when,omitempty"`
}

// FluxSetRowDTO is one row of the ResourceSets part: a set or a provider.
// Its identity and the capture's rule are at the top, as every rows DTO's
// are (the parity gate reads them there); the kind says which one is set.
type FluxSetRowDTO struct {
	UID      string           `json:"uid"`
	Kind     string           `json:"kind"`
	Rule     RuleDTO          `json:"rule"`
	Set      *FluxSetDTO      `json:"set,omitempty"`
	Provider *FluxProviderDTO `json:"provider,omitempty"`
}

// FluxReportDTO is a FluxReport, "as reported by the operator".
type FluxReportDTO struct {
	UID                 string                    `json:"uid"`
	Namespace           string                    `json:"namespace"`
	Name                string                    `json:"name"`
	ReportingSucceeded  bool                      `json:"reportingSucceeded"`
	ConditionChanged    *time.Time                `json:"conditionChanged,omitempty"`
	Entitlement         string                    `json:"entitlement,omitempty"`
	DistributionStatus  string                    `json:"distributionStatus,omitempty"`
	DistributionVersion string                    `json:"distributionVersion,omitempty"`
	ManagedBy           string                    `json:"managedBy,omitempty"`
	ServerVersion       string                    `json:"serverVersion,omitempty"`
	Platform            string                    `json:"platform,omitempty"`
	Nodes               int64                     `json:"nodes,omitempty"`
	OperatorVersion     string                    `json:"operatorVersion,omitempty"`
	OperatorPlatform    string                    `json:"operatorPlatform,omitempty"`
	Components          []FluxReportComponentDTO  `json:"components"`
	Reconcilers         []FluxReportReconcilerDTO `json:"reconcilers"`
	Sync                *FluxReportSyncDTO        `json:"sync,omitempty"`
}

// FluxReportComponentDTO is a component as reported.
type FluxReportComponentDTO struct {
	Name   string `json:"name"`
	Ready  bool   `json:"ready"`
	Status string `json:"status,omitempty"`
	Image  string `json:"image,omitempty"`
}

// FluxReportReconcilerDTO is one kind's reported stats.
type FluxReportReconcilerDTO struct {
	Kind      string `json:"kind"`
	Running   int64  `json:"running"`
	Failing   int64  `json:"failing"`
	Suspended int64  `json:"suspended"`
	TotalSize string `json:"totalSize,omitempty"`
}

// FluxReportSyncDTO is the reported sync.
type FluxReportSyncDTO struct {
	ID     string `json:"id,omitempty"`
	Path   string `json:"path,omitempty"`
	Status string `json:"status,omitempty"`
	Source string `json:"source,omitempty"`
	Ready  bool   `json:"ready"`
}

func timePtrOf(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func fluxOpCommon(c fluxop.Common, ref healthsrc.RowRef, resume string, eval RuleDTO) FluxOpCommonDTO {
	d := FluxOpCommonDTO{UID: ref.UID, GVR: ref.GVR, Generation: ref.Generation, Kind: c.Kind, Namespace: c.Namespace, Name: c.Name,
		Created: timePtrOf(c.Created), Suspended: c.Disabled, Ready: string(c.Ready), Reason: c.Reason, Message: c.Message,
		ReadySince: timePtrOf(c.ReadySince), ReadyGeneration: c.ReadyGeneration, Stalled: c.Stalled, Progress: c.Progress,
		Resume: resume, Clipped: nonNil(c.Clipped), Rule: eval}
	if c.Owner != nil {
		d.Owner = &FluxOpOwnerDTO{Kind: c.Owner.Kind, Namespace: c.Owner.Namespace, Name: c.Owner.Name}
	}
	return d
}

func fluxInstanceDTO(r healthsrc.Row[healthsrc.FluxInstanceRow]) FluxInstanceDTO {
	in := r.Data.Instance
	d := FluxInstanceDTO{FluxOpCommonDTO: fluxOpCommon(in.Common, r.Ref, r.Data.Resume, toRuleDTO(r.Eval)), Binding: string(r.Data.Binding),
		Version: in.Version, Registry: in.Registry, Variant: in.Variant, Artifact: in.Artifact, Components: nonNil(in.Components),
		Running: []FluxComponentImageDTO{}, ClusterType: in.ClusterType, Multitenant: in.Multitenant, NetworkPolicy: in.NetworkPolicy,
		ClusterSize: in.ClusterSize, ShardKey: in.ShardKey, Shards: nonNil(in.Shards), SyncName: in.SyncName(), Wait: in.Wait,
		PatchCount: in.PatchCount, LastApplied: in.LastApplied, LastAttempted: in.LastAttempted, LastArtifact: in.LastArtifact}
	for _, c := range in.Running {
		d.Running = append(d.Running, FluxComponentImageDTO{Name: c.Name, Repository: c.Repository, Tag: c.Tag, Digest: c.Digest})
	}
	if in.Sync != nil {
		d.Sync = &FluxInstanceSyncDTO{Kind: in.Sync.Kind, URL: in.Sync.URL, Ref: in.Sync.Ref, Path: in.Sync.Path, Interval: in.Sync.Interval, Provider: in.Sync.Provider}
	}
	return d
}

func fluxSetRowDTO(r healthsrc.Row[healthsrc.FluxSetRow]) FluxSetRowDTO {
	eval := toRuleDTO(r.Eval)
	if st := r.Data.Set; st != nil {
		d := FluxSetDTO{FluxOpCommonDTO: fluxOpCommon(st.Common, r.Ref, r.Data.Resume, eval), InputStrategy: st.InputStrategy,
			InputsFrom: []FluxInputRefDTO{}, DependsOn: []FluxDependencyDTO{}, Steps: nonNil(st.Steps), StepCount: st.StepCount,
			InputCount: st.InputCount, ResourceCount: st.ResourceCount, HasTemplate: st.HasTemplate, ServiceAccountName: st.ServiceAccountName,
			Wait: st.Wait, LastAppliedRevision: st.LastAppliedRevision, InventoryCount: st.InventoryCount}
		for _, in := range st.InputsFrom {
			d.InputsFrom = append(d.InputsFrom, FluxInputRefDTO{Kind: in.Kind, Name: in.Name, Selector: in.Selector})
		}
		for _, dep := range st.DependsOn {
			d.DependsOn = append(d.DependsOn, FluxDependencyDTO{APIVersion: dep.APIVersion, Kind: dep.Kind, Name: dep.Name,
				Namespace: dep.Namespace, Ready: dep.Ready, HasReadyExpr: dep.HasReadyExpr})
		}
		return FluxSetRowDTO{UID: r.Ref.UID, Kind: fluxop.KindSet, Rule: eval, Set: &d}
	}
	p := r.Data.Provider
	d := FluxProviderDTO{FluxOpCommonDTO: fluxOpCommon(p.Common, r.Ref, r.Data.Resume, eval), Type: p.Type, URL: p.URL,
		ServiceAccountName: p.ServiceAccountName, SecretRef: p.SecretRef, CertSecretRef: p.CertSecretRef, Insecure: p.Insecure,
		Schedule: []FluxScheduleDTO{}, LastExportedRevision: p.LastExportedRevision, ExportedInputCount: p.ExportedInputCount}
	for _, sc := range p.Schedule {
		d.Schedule = append(d.Schedule, FluxScheduleDTO{Cron: sc.Cron, TimeZone: sc.TimeZone, Window: sc.Window})
	}
	if n := p.NextSchedule; n != nil {
		d.NextSchedule = &FluxNextScheduleDTO{FluxScheduleDTO: FluxScheduleDTO{Cron: n.Cron, TimeZone: n.TimeZone, Window: n.Window}, When: timePtrOf(n.When)}
	}
	return FluxSetRowDTO{UID: r.Ref.UID, Kind: fluxop.KindProvider, Rule: eval, Provider: &d}
}

func fluxReportDTO(r healthsrc.Row[fluxop.Report]) FluxReportDTO {
	rp := r.Data
	d := FluxReportDTO{UID: r.Ref.UID, Namespace: rp.Namespace, Name: rp.Name, ReportingSucceeded: rp.Ready == fluxop.Ready,
		ConditionChanged: timePtrOf(rp.ReadySince), Entitlement: rp.Entitlement, DistributionStatus: rp.DistributionStatus,
		DistributionVersion: rp.DistributionVersion, ManagedBy: rp.ManagedBy, ServerVersion: rp.ServerVersion, Platform: rp.Platform,
		Nodes: rp.Nodes, OperatorVersion: rp.OperatorVersion, OperatorPlatform: rp.OperatorPlatform,
		Components: []FluxReportComponentDTO{}, Reconcilers: []FluxReportReconcilerDTO{}}
	for _, c := range rp.Components {
		d.Components = append(d.Components, FluxReportComponentDTO{Name: c.Name, Ready: c.Ready, Status: c.Status, Image: c.Image})
	}
	for _, rc := range rp.Reconcilers {
		d.Reconcilers = append(d.Reconcilers, FluxReportReconcilerDTO{Kind: rc.Kind, Running: rc.Running, Failing: rc.Failing,
			Suspended: rc.Suspended, TotalSize: rc.TotalSize})
	}
	if s := rp.Sync; s != nil {
		d.Sync = &FluxReportSyncDTO{ID: s.ID, Path: s.Path, Status: s.Status, Source: s.Source, Ready: s.Ready}
	}
	return d
}

func init() {
	rowsConverters[healthsrc.TaskFlux+"/fluxinstance"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.FluxInstanceRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]FluxInstanceDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, fluxInstanceDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskFlux+"/resourcesets"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.FluxSetRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]FluxSetRowDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, fluxSetRowDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskFlux+"/fluxreport"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[fluxop.Report](rows)
		if err != nil {
			return nil, err
		}
		out := make([]FluxReportDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, fluxReportDTO(r))
		}
		return out, nil
	}
}

// FluxOpDetailDTO is the inspector's on-demand read of one operator object
// (S1a §2): its history and inventory, and a dependency's readyExpr. The
// object is sanitised (S0b) before anything is read from it; history
// digests are never sent (S0b: no digests of protected content).
type FluxOpDetailDTO struct {
	History   []FluxOpHistoryDTO   `json:"history"`
	Inventory []FluxOpInventoryDTO `json:"inventory"`
	// InventoryTotal counts every entry; Inventory holds at most 200.
	InventoryTotal int               `json:"inventoryTotal"`
	ReadyExprs     map[string]string `json:"readyExprs,omitempty"` // dependsOn index -> expression
	Events         []EventRowDTO     `json:"events"`
	EventsError    string            `json:"eventsError,omitempty"`
	Error          string            `json:"error,omitempty"`
}

// FluxOpHistoryDTO is one history snapshot, without its digest.
type FluxOpHistoryDTO struct {
	FirstReconciled      *time.Time        `json:"firstReconciled,omitempty"`
	LastReconciled       *time.Time        `json:"lastReconciled,omitempty"`
	Duration             string            `json:"duration,omitempty"`
	Status               string            `json:"status,omitempty"`
	TotalReconciliations int64             `json:"totalReconciliations"`
	Metadata             map[string]string `json:"metadata,omitempty"`
}

// FluxOpInventoryDTO is one inventory entry, parsed from its id.
type FluxOpInventoryDTO struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Version   string `json:"version,omitempty"`
}

// historyMetadataKeys are the operator's own history metadata (v0.61.0:
// counts and the Flux version); any other key is dropped.
var historyMetadataKeys = map[string]bool{"inputs": true, "resources": true, "steps": true, "flux": true}

const inventoryMax = 200

// fluxOpDetail builds the detail from a read object: sanitised first.
func fluxOpDetail(u *unstructured.Unstructured) FluxOpDetailDTO {
	s := crd.SanitizeEmbedded(fluxop.Group, u.GetKind(), u.Object)
	obj := &unstructured.Unstructured{Object: s.Object}
	d := FluxOpDetailDTO{History: []FluxOpHistoryDTO{}, Inventory: []FluxOpInventoryDTO{}, Events: []EventRowDTO{}}
	hist, _, _ := unstructured.NestedSlice(obj.Object, "status", "history")
	for i, x := range hist {
		if i == 5 {
			break
		}
		h, _ := x.(map[string]any)
		e := FluxOpHistoryDTO{Status: strOf(h, "lastReconciledStatus"), Duration: strOf(h, "lastReconciledDuration")}
		e.TotalReconciliations, _, _ = unstructured.NestedInt64(h, "totalReconciliations")
		for k, f := range map[string]**time.Time{"firstReconciled": &e.FirstReconciled, "lastReconciled": &e.LastReconciled} {
			if t, err := time.Parse(time.RFC3339, strOf(h, k)); err == nil {
				*f = &t
			}
		}
		if md, ok := h["metadata"].(map[string]any); ok {
			for k, v := range md {
				if sv, ok := v.(string); ok && historyMetadataKeys[k] {
					if e.Metadata == nil {
						e.Metadata = map[string]string{}
					}
					e.Metadata[k] = sv
				}
			}
		}
		d.History = append(d.History, e)
	}
	entries, _, _ := unstructured.NestedSlice(obj.Object, "status", "inventory", "entries")
	d.InventoryTotal = len(entries)
	for i, x := range entries {
		if i == inventoryMax {
			break
		}
		e, _ := x.(map[string]any)
		parts := strings.Split(strOf(e, "id"), "_")
		if len(parts) != 4 {
			continue
		}
		d.Inventory = append(d.Inventory, FluxOpInventoryDTO{Namespace: parts[0], Name: parts[1], Group: parts[2], Kind: parts[3], Version: strOf(e, "v")})
	}
	deps, _, _ := unstructured.NestedSlice(obj.Object, "spec", "dependsOn")
	for i, x := range deps {
		if dm, ok := x.(map[string]any); ok {
			if expr := strOf(dm, "readyExpr"); expr != "" {
				if d.ReadyExprs == nil {
					d.ReadyExprs = map[string]string{}
				}
				d.ReadyExprs[strconv.Itoa(i)] = expr
			}
		}
	}
	return d
}

func strOf(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
