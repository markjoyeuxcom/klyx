package fluxop

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Readiness is what Ready says, read as the Flux lens reads it.
type Readiness string

const (
	Ready       Readiness = "Ready"
	Failed      Readiness = "Failed"
	Reconciling Readiness = "Reconciling"
	Unknown     Readiness = "Unknown"
	NoReady     Readiness = "" // no Ready condition
)

// Operator reasons the rules name (v0.61.0 api/v1).
const (
	ReasonDisabled        = "ReconciliationDisabled"
	ReasonSkippedSchedule = "SkippedDueToSchedule"
	ReasonProgressing     = "Progressing"
)

// Owner is who applies an object, from its labels and annotations (C4's
// consequence line): a Flux Kustomization or HelmRelease, or Argo CD.
type Owner struct {
	Kind, Namespace, Name string
}

// Common is what every operator kind carries.
type Common struct {
	Kind, Namespace, Name, UID string
	Generation                 int64
	Created                    time.Time
	// Disabled: the reconcile annotation is "disabled" - suspended.
	Disabled bool
	Ready    Readiness
	// Reason and Message are Ready's (the message clipped and URL-scrubbed);
	// ReadySince is its transition time; ReadyGeneration its own
	// observedGeneration (0 when absent).
	Reason, Message string
	ReadySince      time.Time
	ReadyGeneration int64
	Stalled         bool
	StalledReason   string
	// Progress is the Reconciling condition's message while it is present
	// ("Applying step 2/3 \"apps\"").
	Progress    string
	RequestedAt string
	Owner       *Owner
	Clipped     []string
}

func common(o *Object) Common {
	u := o.Unstructured
	c := Common{Kind: u.GetKind(), Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()),
		Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().Time, Clipped: o.Clipped}
	ann := u.GetAnnotations()
	c.Disabled = ann[AnnReconcile] == "disabled"
	c.RequestedAt = ann[AnnRequestedAt]
	c.Owner = ownerOf(u.GetLabels(), ann)
	c.Ready = NoReady
	reconciling := false
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, x := range conds {
		cm, _ := x.(map[string]any)
		t, _ := cm["type"].(string)
		st, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		msg, _ := cm["message"].(string)
		switch t {
		case "Ready":
			c.Reason, c.Message = reason, msg
			if ts, _ := cm["lastTransitionTime"].(string); ts != "" {
				c.ReadySince, _ = time.Parse(time.RFC3339, ts)
			}
			c.ReadyGeneration, _, _ = unstructured.NestedInt64(cm, "observedGeneration")
			switch st {
			case "True":
				c.Ready = Ready
			case "False":
				c.Ready = Failed
			default:
				c.Ready = Unknown
			}
		case "Reconciling":
			if st == "True" {
				reconciling, c.Progress = true, msg
			}
		case "Stalled":
			if st == "True" {
				c.Stalled, c.StalledReason = true, reason
			}
		}
	}
	if reconciling && c.Ready != Ready && c.Ready != Failed {
		c.Ready = Reconciling
	}
	return c
}

func ownerOf(labels, ann map[string]string) *Owner {
	switch {
	case labels["kustomize.toolkit.fluxcd.io/name"] != "":
		return &Owner{Kind: "Kustomization", Namespace: labels["kustomize.toolkit.fluxcd.io/namespace"], Name: labels["kustomize.toolkit.fluxcd.io/name"]}
	case labels["helm.toolkit.fluxcd.io/name"] != "":
		return &Owner{Kind: "HelmRelease", Namespace: labels["helm.toolkit.fluxcd.io/namespace"], Name: labels["helm.toolkit.fluxcd.io/name"]}
	case ann["argocd.argoproj.io/tracking-id"] != "":
		return &Owner{Kind: "Application", Name: ann["argocd.argoproj.io/tracking-id"]}
	}
	return nil
}

// Instance is a FluxInstance.
type Instance struct {
	Common
	Version, Registry, Variant, Artifact string
	// Components are the declared components; Running what status reports
	// runs, each with its image tag and digest.
	Components    []string
	Running       []ComponentImage
	ClusterType   string
	Multitenant   bool
	NetworkPolicy bool
	ClusterSize   string
	ShardKey      string
	Shards        []string
	Sync          *Sync
	Wait          *bool
	PatchCount    int64
	LastApplied   string
	LastAttempted string
	LastArtifact  string
}

// ComponentImage is one running component.
type ComponentImage struct{ Name, Repository, Tag, Digest string }

// Sync is a FluxInstance's declared sync.
type Sync struct{ Kind, URL, Ref, Path, Name, Interval, Provider string }

// ParseInstance reads a projected FluxInstance.
func ParseInstance(o *Object) Instance {
	u := o.Unstructured
	in := Instance{Common: common(o)}
	in.Version, in.Registry, in.Variant, in.Artifact = str(u, "spec", "distribution", "version"), str(u, "spec", "distribution", "registry"),
		str(u, "spec", "distribution", "variant"), str(u, "spec", "distribution", "artifact")
	in.Components = strList(u, "spec", "components")
	if comps, ok, _ := unstructured.NestedSlice(u.Object, "status", "components"); ok {
		for _, x := range comps {
			cm, _ := x.(map[string]any)
			in.Running = append(in.Running, ComponentImage{Name: s(cm, "name"), Repository: s(cm, "repository"), Tag: s(cm, "tag"), Digest: s(cm, "digest")})
		}
	}
	in.ClusterType, in.ClusterSize = str(u, "spec", "cluster", "type"), str(u, "spec", "cluster", "size")
	in.Multitenant, _, _ = unstructured.NestedBool(u.Object, "spec", "cluster", "multitenant")
	in.NetworkPolicy, _, _ = unstructured.NestedBool(u.Object, "spec", "cluster", "networkPolicy")
	in.ShardKey, in.Shards = str(u, "spec", "sharding", "key"), strList(u, "spec", "sharding", "shards")
	if sy, ok, _ := unstructured.NestedMap(u.Object, "spec", "sync"); ok {
		in.Sync = &Sync{Kind: s(sy, "kind"), URL: s(sy, "url"), Ref: s(sy, "ref"), Path: s(sy, "path"), Name: s(sy, "name"),
			Interval: s(sy, "interval"), Provider: s(sy, "provider")}
	}
	if w, ok, _ := unstructured.NestedBool(u.Object, "spec", "wait"); ok {
		in.Wait = &w
	}
	in.PatchCount, _, _ = unstructured.NestedInt64(u.Object, "spec", "patchCount")
	in.LastApplied, in.LastAttempted, in.LastArtifact = str(u, "status", "lastAppliedRevision"), str(u, "status", "lastAttemptedRevision"),
		str(u, "status", "lastArtifactRevision")
	return in
}

// SyncName is the name of the source and Kustomization a FluxInstance's
// sync creates: spec.sync.name, else the instance's namespace.
func (in Instance) SyncName() string {
	if in.Sync == nil {
		return ""
	}
	if in.Sync.Name != "" {
		return in.Sync.Name
	}
	return in.Namespace
}

// Report is a FluxReport: the operator's own account, never Klyx's counts.
type Report struct {
	Common
	Entitlement, DistributionStatus, DistributionVersion, ManagedBy string
	ServerVersion, Platform                                         string
	Nodes                                                           int64
	OperatorVersion, OperatorPlatform                               string
	Components                                                      []ReportComponent
	Reconcilers                                                     []ReportReconciler
	Sync                                                            *ReportSync
}

// ReportComponent is one component as the operator reports it.
type ReportComponent struct {
	Name, Status, Image string
	Ready               bool
}

// ReportReconciler is one kind's reconciler stats as reported.
type ReportReconciler struct {
	APIVersion, Kind            string
	Running, Failing, Suspended int64
	TotalSize                   string
}

// ReportSync is the reported sync.
type ReportSync struct {
	ID, Path, Status, Source string
	Ready                    bool
}

// ParseReport reads a projected FluxReport.
func ParseReport(o *Object) Report {
	u := o.Unstructured
	r := Report{Common: common(o)}
	r.Entitlement, r.DistributionStatus = str(u, "spec", "distribution", "entitlement"), str(u, "spec", "distribution", "status")
	r.DistributionVersion, r.ManagedBy = str(u, "spec", "distribution", "version"), str(u, "spec", "distribution", "managedBy")
	r.ServerVersion, r.Platform = str(u, "spec", "cluster", "serverVersion"), str(u, "spec", "cluster", "platform")
	r.Nodes, _, _ = unstructured.NestedInt64(u.Object, "spec", "cluster", "nodes")
	r.OperatorVersion, r.OperatorPlatform = str(u, "spec", "operator", "version"), str(u, "spec", "operator", "platform")
	if comps, ok, _ := unstructured.NestedSlice(u.Object, "spec", "components"); ok {
		for _, x := range comps {
			cm, _ := x.(map[string]any)
			ready, _ := cm["ready"].(bool)
			r.Components = append(r.Components, ReportComponent{Name: s(cm, "name"), Status: s(cm, "status"), Image: s(cm, "image"), Ready: ready})
		}
	}
	if recs, ok, _ := unstructured.NestedSlice(u.Object, "spec", "reconcilers"); ok {
		for _, x := range recs {
			rm, _ := x.(map[string]any)
			rr := ReportReconciler{APIVersion: s(rm, "apiVersion"), Kind: s(rm, "kind")}
			if st, ok := rm["stats"].(map[string]any); ok {
				rr.Running, _, _ = unstructured.NestedInt64(st, "running")
				rr.Failing, _, _ = unstructured.NestedInt64(st, "failing")
				rr.Suspended, _, _ = unstructured.NestedInt64(st, "suspended")
				rr.TotalSize = s(st, "totalSize")
			}
			r.Reconcilers = append(r.Reconcilers, rr)
		}
	}
	if sy, ok, _ := unstructured.NestedMap(u.Object, "spec", "sync"); ok {
		ready, _ := sy["ready"].(bool)
		r.Sync = &ReportSync{ID: s(sy, "id"), Path: s(sy, "path"), Status: s(sy, "status"), Source: s(sy, "source"), Ready: ready}
	}
	return r
}

// Set is a ResourceSet.
type Set struct {
	Common
	InputStrategy       string
	InputsFrom          []InputRef
	DependsOn           []Dependency
	Steps               []string
	StepCount           int64
	InputCount          int64
	ResourceCount       int64
	HasTemplate         bool
	ServiceAccountName  string
	Wait                bool
	LastAppliedRevision string
	InventoryCount      int64
}

// InputRef is one inputsFrom entry: a provider by name, or by selector.
type InputRef struct{ APIVersion, Kind, Name, Selector string }

// Dependency is one dependsOn entry.
type Dependency struct {
	APIVersion, Kind, Name, Namespace string
	Ready, HasReadyExpr               bool
}

// ParseSet reads a projected ResourceSet.
func ParseSet(o *Object) Set {
	u := o.Unstructured
	st := Set{Common: common(o)}
	st.InputStrategy = str(u, "spec", "inputStrategy", "name")
	if refs, ok, _ := unstructured.NestedSlice(u.Object, "spec", "inputsFrom"); ok {
		for _, x := range refs {
			rm, _ := x.(map[string]any)
			st.InputsFrom = append(st.InputsFrom, InputRef{APIVersion: s(rm, "apiVersion"), Kind: s(rm, "kind"), Name: s(rm, "name"), Selector: s(rm, "selector")})
		}
	}
	if deps, ok, _ := unstructured.NestedSlice(u.Object, "spec", "dependsOn"); ok {
		for _, x := range deps {
			dm, _ := x.(map[string]any)
			ready, _ := dm["ready"].(bool)
			expr, _ := dm["hasReadyExpr"].(bool)
			st.DependsOn = append(st.DependsOn, Dependency{APIVersion: s(dm, "apiVersion"), Kind: s(dm, "kind"), Name: s(dm, "name"),
				Namespace: s(dm, "namespace"), Ready: ready, HasReadyExpr: expr})
		}
	}
	st.Steps = strList(u, "spec", "steps")
	st.StepCount, _, _ = unstructured.NestedInt64(u.Object, "spec", "stepCount")
	st.InputCount, _, _ = unstructured.NestedInt64(u.Object, "spec", "inputCount")
	st.ResourceCount, _, _ = unstructured.NestedInt64(u.Object, "spec", "resourceCount")
	st.HasTemplate, _, _ = unstructured.NestedBool(u.Object, "spec", "hasTemplate")
	st.ServiceAccountName = str(u, "spec", "serviceAccountName")
	st.Wait, _, _ = unstructured.NestedBool(u.Object, "spec", "wait")
	st.LastAppliedRevision = str(u, "status", "lastAppliedRevision")
	st.InventoryCount, _, _ = unstructured.NestedInt64(u.Object, "status", "inventoryCount")
	return st
}

// Provider is a ResourceSetInputProvider.
type Provider struct {
	Common
	Type, URL, ServiceAccountName, SecretRef, CertSecretRef string
	Insecure                                                bool
	Schedule                                                []Schedule
	NextSchedule                                            *NextSchedule
	LastExportedRevision                                    string
	ExportedInputCount                                      int64
}

// Schedule is one provider schedule.
type Schedule struct{ Cron, TimeZone, Window string }

// NextSchedule is the next scheduled run, as the operator computed it.
type NextSchedule struct {
	Schedule
	When time.Time
}

// Scheduled reports a provider whose runs are gated by a schedule.
func (p Provider) Scheduled() bool { return len(p.Schedule) > 0 }

// ParseProvider reads a projected provider.
func ParseProvider(o *Object) Provider {
	u := o.Unstructured
	p := Provider{Common: common(o)}
	p.Type, p.URL, p.ServiceAccountName = str(u, "spec", "type"), str(u, "spec", "url"), str(u, "spec", "serviceAccountName")
	p.SecretRef, p.CertSecretRef = str(u, "spec", "secretRef", "name"), str(u, "spec", "certSecretRef", "name")
	p.Insecure, _, _ = unstructured.NestedBool(u.Object, "spec", "insecure")
	if sch, ok, _ := unstructured.NestedSlice(u.Object, "spec", "schedule"); ok {
		for _, x := range sch {
			sm, _ := x.(map[string]any)
			p.Schedule = append(p.Schedule, Schedule{Cron: s(sm, "cron"), TimeZone: s(sm, "timeZone"), Window: s(sm, "window")})
		}
	}
	if ns, ok, _ := unstructured.NestedMap(u.Object, "status", "nextSchedule"); ok {
		n := &NextSchedule{Schedule: Schedule{Cron: s(ns, "cron"), TimeZone: s(ns, "timeZone"), Window: s(ns, "window")}}
		n.When, _ = time.Parse(time.RFC3339, s(ns, "when"))
		p.NextSchedule = n
	}
	p.LastExportedRevision = str(u, "status", "lastExportedRevision")
	p.ExportedInputCount, _, _ = unstructured.NestedInt64(u.Object, "status", "exportedInputCount")
	return p
}

func s(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func strList(u *unstructured.Unstructured, path ...string) []string {
	raw, _, _ := unstructured.NestedSlice(u.Object, path...)
	var out []string
	for _, x := range raw {
		if v, ok := x.(string); ok {
			out = append(out, v)
		}
	}
	return out
}
