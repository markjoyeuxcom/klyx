// Package argo parses Argo CD Application objects into a typed model, speaking
// Argo's own vocabulary (design principle 8): sync status is Synced/OutOfSync,
// health is Healthy/Progressing/Degraded/Suspended/Missing - never translated
// into Flux terms.
package argo

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// AppGVR is the Application resource group-version-resource.
var AppGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
var ProjectGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}
var ApplicationSetGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}

// Condition is one status condition (Argo uses type+message, no status field).
type Condition struct {
	Type    string
	Message string
}

// Resource is one resource Argo reports under status.resources. It is enough
// to drive selective sync without fetching the full Application tree.
type Resource struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	Status    string
	Health    string
}

// App is one Argo CD Application.
type App struct {
	Namespace      string
	Name           string
	Project        string
	SyncStatus     string // Synced | OutOfSync | Unknown ("" -> Unknown)
	HealthStatus   string // Healthy | Progressing | Degraded | Suspended | Missing | Unknown
	Revision       string // status.sync.revision (deployed)
	RepoURL        string
	Path           string
	Chart          string // helm-source apps use chart instead of path
	TargetRevision string
	ExtraSources   int // multi-source apps: number of sources beyond the first
	DestNamespace  string
	DestServer     string
	ApplicationSet string
	AutoSync       bool // spec.syncPolicy.automated present
	OpPhase        string
	OpMessage      string
	Conditions     []Condition
	Resources      []Resource
	ReconciledAt   time.Time
	CreatedAt      time.Time
}

type SyncStrategy string

const (
	SyncStrategyHook  SyncStrategy = "hook"
	SyncStrategyApply SyncStrategy = "apply"
)

// SyncOptions maps Klyx's sync modal to Argo's Application operation field.
// Defaults match argocd app sync: hook strategy, prune enabled, no force.
type SyncOptions struct {
	Revision        string
	Strategy        SyncStrategy
	Prune           bool
	DryRun          bool
	Force           bool
	Replace         bool
	ServerSideApply bool
	Resources       []Resource
}

type ResourceRule struct {
	Group string
	Kind  string
}

type Project struct {
	Namespace                  string
	Name                       string
	Description                string
	SourceRepos                []string
	Destinations               []string
	Roles                      int
	RoleNames                  []string
	OrphanWarn                 bool
	ClusterResourceWhitelist   []ResourceRule
	ClusterResourceBlacklist   []ResourceRule
	NamespaceResourceWhitelist []ResourceRule
	NamespaceResourceBlacklist []ResourceRule
}

type ApplicationSetCondition struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

type ApplicationSet struct {
	Namespace      string
	Name           string
	Generators     []string
	TemplateName   string
	Project        string
	RepoURL        string
	Path           string
	TargetRevision string
	DestNamespace  string
	DestServer     string
	GeneratedApps  int
	GeneratedNames []string
	Healthy        bool
	Conditions     []ApplicationSetCondition
}

// Broken reports whether the app needs attention: not synced or unhealthy.
// Progressing is transitional, not broken. Suspended means an operator paused
// the resource on purpose (e.g. a paused Rollout) - Argo's own UI renders it
// as a distinct paused state, not a failure, and the Flux side treats
// Suspended the same way, so it does not count as broken; it stays visible
// via HealthStatus.
func (a App) Broken() bool {
	if a.SyncStatus != "Synced" {
		return true
	}
	switch a.HealthStatus {
	case "Healthy", "Progressing", "Suspended":
		return false
	}
	return true
}

// Parse maps an Application unstructured into the model. Absent status fields
// become "Unknown", never fabricated healthy values.
func Parse(u *unstructured.Unstructured) App {
	a := App{
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		CreatedAt: u.GetCreationTimestamp().Time,
	}
	a.Project, _, _ = unstructured.NestedString(u.Object, "spec", "project")

	// Source: single spec.source, or first of spec.sources for multi-source apps.
	src, found, _ := unstructured.NestedMap(u.Object, "spec", "source")
	if !found {
		if srcs, ok, _ := unstructured.NestedSlice(u.Object, "spec", "sources"); ok && len(srcs) > 0 {
			if first, ok := srcs[0].(map[string]interface{}); ok {
				src = first
				a.ExtraSources = len(srcs) - 1
			}
		}
	}
	if src != nil {
		a.RepoURL, _ = src["repoURL"].(string)
		a.Path, _ = src["path"].(string)
		a.Chart, _ = src["chart"].(string)
		a.TargetRevision, _ = src["targetRevision"].(string)
	}

	a.DestNamespace, _, _ = unstructured.NestedString(u.Object, "spec", "destination", "namespace")
	a.DestServer, _, _ = unstructured.NestedString(u.Object, "spec", "destination", "server")
	for _, owner := range u.GetOwnerReferences() {
		if owner.Kind == "ApplicationSet" && owner.Name != "" {
			a.ApplicationSet = owner.Name
			break
		}
	}
	if _, ok, _ := unstructured.NestedMap(u.Object, "spec", "syncPolicy", "automated"); ok {
		a.AutoSync = true
	}

	a.SyncStatus, _, _ = unstructured.NestedString(u.Object, "status", "sync", "status")
	if a.SyncStatus == "" {
		a.SyncStatus = "Unknown"
	}
	a.Revision, _, _ = unstructured.NestedString(u.Object, "status", "sync", "revision")
	if a.Revision == "" {
		// Multi-source apps leave status.sync.revision empty and report
		// status.sync.revisions[] instead (parallel to spec.sources). The first
		// entry matches the headline source parsed above.
		if revs, ok, _ := unstructured.NestedStringSlice(u.Object, "status", "sync", "revisions"); ok && len(revs) > 0 {
			a.Revision = revs[0]
		}
	}
	a.HealthStatus, _, _ = unstructured.NestedString(u.Object, "status", "health", "status")
	if a.HealthStatus == "" {
		a.HealthStatus = "Unknown"
	}
	a.OpPhase, _, _ = unstructured.NestedString(u.Object, "status", "operationState", "phase")
	a.OpMessage, _, _ = unstructured.NestedString(u.Object, "status", "operationState", "message")

	if conds, ok, _ := unstructured.NestedSlice(u.Object, "status", "conditions"); ok {
		for _, c := range conds {
			if m, ok := c.(map[string]interface{}); ok {
				t, _ := m["type"].(string)
				msg, _ := m["message"].(string)
				a.Conditions = append(a.Conditions, Condition{Type: t, Message: msg})
			}
		}
	}
	if resources, ok, _ := unstructured.NestedSlice(u.Object, "status", "resources"); ok {
		for _, r := range resources {
			if m, ok := r.(map[string]interface{}); ok {
				res := Resource{}
				res.Group, _ = m["group"].(string)
				res.Kind, _ = m["kind"].(string)
				res.Namespace, _ = m["namespace"].(string)
				res.Name, _ = m["name"].(string)
				res.Status, _ = m["status"].(string)
				if h, ok := m["health"].(map[string]interface{}); ok {
					res.Health, _ = h["status"].(string)
				}
				if res.Kind != "" && res.Name != "" {
					a.Resources = append(a.Resources, res)
				}
			}
		}
	}
	if ts, ok, _ := unstructured.NestedString(u.Object, "status", "reconciledAt"); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			a.ReconciledAt = t
		}
	}
	return a
}

func ParseProject(u *unstructured.Unstructured) Project {
	p := Project{Namespace: u.GetNamespace(), Name: u.GetName()}
	p.Description, _, _ = unstructured.NestedString(u.Object, "spec", "description")
	if repos, ok, _ := unstructured.NestedStringSlice(u.Object, "spec", "sourceRepos"); ok {
		p.SourceRepos = repos
	}
	if roles, ok, _ := unstructured.NestedSlice(u.Object, "spec", "roles"); ok {
		p.Roles = len(roles)
		for _, r := range roles {
			if m, ok := r.(map[string]interface{}); ok {
				if name, _ := m["name"].(string); name != "" {
					p.RoleNames = append(p.RoleNames, name)
				}
			}
		}
	}
	if warn, ok, _ := unstructured.NestedBool(u.Object, "spec", "orphanedResources", "warn"); ok {
		p.OrphanWarn = warn
	}
	if dests, ok, _ := unstructured.NestedSlice(u.Object, "spec", "destinations"); ok {
		for _, d := range dests {
			if m, ok := d.(map[string]interface{}); ok {
				server, _ := m["server"].(string)
				name, _ := m["name"].(string)
				ns, _ := m["namespace"].(string)
				target := server
				if name != "" {
					target = name
				}
				if ns != "" {
					target += "/" + ns
				}
				if target != "" {
					p.Destinations = append(p.Destinations, target)
				}
			}
		}
	}
	p.ClusterResourceWhitelist = parseResourceRules(u, "spec", "clusterResourceWhitelist")
	p.ClusterResourceBlacklist = parseResourceRules(u, "spec", "clusterResourceBlacklist")
	p.NamespaceResourceWhitelist = parseResourceRules(u, "spec", "namespaceResourceWhitelist")
	p.NamespaceResourceBlacklist = parseResourceRules(u, "spec", "namespaceResourceBlacklist")
	return p
}

func parseResourceRules(u *unstructured.Unstructured, fields ...string) []ResourceRule {
	items, ok, _ := unstructured.NestedSlice(u.Object, fields...)
	if !ok {
		return nil
	}
	out := make([]ResourceRule, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		r := ResourceRule{}
		r.Group, _ = m["group"].(string)
		r.Kind, _ = m["kind"].(string)
		if r.Group != "" || r.Kind != "" {
			out = append(out, r)
		}
	}
	return out
}

// generatorTypes are the generator keys the ApplicationSet spec defines. A
// spec.generators[] entry can carry sibling keys next to the generator itself
// (e.g. `selector` for post-generation filtering), so only these count when
// naming generators - iterating all keys would list `selector` as a generator
// and overcount.
var generatorTypes = map[string]bool{
	"clusterDecisionResource": true,
	"clusters":                true,
	"git":                     true,
	"list":                    true,
	"matrix":                  true,
	"merge":                   true,
	"plugin":                  true,
	"pullRequest":             true,
	"scmProvider":             true,
}

func ParseApplicationSet(u *unstructured.Unstructured) ApplicationSet {
	a := ApplicationSet{Namespace: u.GetNamespace(), Name: u.GetName(), Healthy: true}
	if gens, ok, _ := unstructured.NestedSlice(u.Object, "spec", "generators"); ok {
		for _, g := range gens {
			if m, ok := g.(map[string]interface{}); ok {
				for k := range m {
					if generatorTypes[k] {
						a.Generators = append(a.Generators, k)
					}
				}
			}
		}
		// Map iteration order is random; sort for stable output run-to-run.
		sort.Strings(a.Generators)
	}
	a.TemplateName, _, _ = unstructured.NestedString(u.Object, "spec", "template", "metadata", "name")
	a.Project, _, _ = unstructured.NestedString(u.Object, "spec", "template", "spec", "project")
	a.RepoURL, _, _ = unstructured.NestedString(u.Object, "spec", "template", "spec", "source", "repoURL")
	a.Path, _, _ = unstructured.NestedString(u.Object, "spec", "template", "spec", "source", "path")
	a.TargetRevision, _, _ = unstructured.NestedString(u.Object, "spec", "template", "spec", "source", "targetRevision")
	a.DestNamespace, _, _ = unstructured.NestedString(u.Object, "spec", "template", "spec", "destination", "namespace")
	a.DestServer, _, _ = unstructured.NestedString(u.Object, "spec", "template", "spec", "destination", "server")
	if resources, ok, _ := unstructured.NestedSlice(u.Object, "status", "resources"); ok {
		a.GeneratedApps = len(resources)
		for _, r := range resources {
			if m, ok := r.(map[string]interface{}); ok {
				if name, _ := m["name"].(string); name != "" {
					a.GeneratedNames = append(a.GeneratedNames, name)
				}
			}
		}
	}
	if conds, ok, _ := unstructured.NestedSlice(u.Object, "status", "conditions"); ok {
		for _, c := range conds {
			if m, ok := c.(map[string]interface{}); ok {
				cond := ApplicationSetCondition{}
				cond.Type, _ = m["type"].(string)
				cond.Status, _ = m["status"].(string)
				cond.Reason, _ = m["reason"].(string)
				cond.Message, _ = m["message"].(string)
				a.Conditions = append(a.Conditions, cond)
				if appSetConditionBroken(cond) {
					a.Healthy = false
				}
			}
		}
	}
	return a
}

// appSetConditionBroken applies per-type condition polarity. The ApplicationSet
// controller emits ErrorOccurred with status False on success (reason
// ApplicationSetUpToDate) - True means an error, the inverse of the other
// types. RolloutProgressing is progressive-sync progress info, not health.
// Unknown types stay neutral: we cannot know their polarity, and the known
// trio is what the controller drives health with.
func appSetConditionBroken(cond ApplicationSetCondition) bool {
	switch cond.Type {
	case "ErrorOccurred":
		return cond.Status == "True"
	case "ParametersGenerated", "ResourcesUpToDate":
		return cond.Status != "True"
	default:
		return false
	}
}

// RefreshPatch is the merge patch that asks the controller to re-compare the
// app against its source - Argo's equivalent of a flux reconcile annotation.
func RefreshPatch() []byte {
	return []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"normal"}}}`)
}

// SyncPatch is the merge patch that starts a sync operation. Setting the
// .operation field is the documented headless trigger (the argocd CLI's API
// server route ends in the same field); the controller picks it up.
func SyncPatch(opts SyncOptions) []byte {
	if opts.Revision == "" {
		opts.Revision = "HEAD"
	}
	if opts.Strategy == "" {
		opts.Strategy = SyncStrategyHook
	}
	sync := map[string]interface{}{
		"revision": opts.Revision,
		"prune":    opts.Prune,
	}
	if opts.DryRun {
		sync["dryRun"] = true
	}
	if opts.Strategy == SyncStrategyApply {
		sync["syncStrategy"] = map[string]interface{}{"apply": map[string]interface{}{}}
	} else {
		sync["syncStrategy"] = map[string]interface{}{"hook": map[string]interface{}{}}
	}
	syncOptions := []string{}
	if opts.Force {
		syncOptions = append(syncOptions, "Force=true")
	}
	if opts.Replace {
		syncOptions = append(syncOptions, "Replace=true")
	}
	if opts.ServerSideApply {
		syncOptions = append(syncOptions, "ServerSideApply=true")
	}
	if len(syncOptions) > 0 {
		sync["syncOptions"] = syncOptions
	}
	if len(opts.Resources) > 0 {
		resources := make([]map[string]string, 0, len(opts.Resources))
		for _, r := range opts.Resources {
			resources = append(resources, map[string]string{
				"group":     r.Group,
				"kind":      r.Kind,
				"namespace": r.Namespace,
				"name":      r.Name,
			})
		}
		sync["resources"] = resources
	}
	patch := map[string]interface{}{
		"operation": map[string]interface{}{
			"initiatedBy": map[string]interface{}{"username": "klyx"},
			"sync":        sync,
		},
	}
	b, err := json.Marshal(patch)
	if err != nil {
		return []byte(fmt.Sprintf(`{"operation":{"initiatedBy":{"username":"klyx"},"sync":{"revision":%q,"prune":%t}}}`, opts.Revision, opts.Prune))
	}
	return b
}
