package appbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/urlscrub"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/depgraph"
	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/routemetrics"
	"github.com/moomora/klyx/internal/workloads"
)

// The cluster chat's read-only tools (spec 2026-10-14 §3). Every executor
// runs on the one ChatConn it was built with - the conversation's pinned
// cluster - and no tool takes a cluster argument. Arguments are validated
// strictly before any read; every result is one bounded envelope.

// ChatConn is the narrow read surface chat needs from a cluster.
type ChatConn interface {
	ChatResolveKind(ctx context.Context, kind, group string) ([]fleet.ChatKind, []string, error)
	ChatGet(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error)
	ChatList(ctx context.Context, gvr schema.GroupVersionResource, ns, selector string, limit int64) (*unstructured.UnstructuredList, error)
	ChatSummary() (observe.Summary, bool)
	HealthRowsNow(ctx context.Context, task, part string) (observe.Rows, bool)
	ListEvents(ctx context.Context, namespace string) ([]workloads.EventSummary, bool, error)
	PodLogSnapshot(ctx context.Context, ns, pod, container string, previous bool, tail int64) (fleet.LogSnapshot, error)
	// FluxObject reads one Flux object live at its served version (AI A1 §3.1).
	FluxObject(ctx context.Context, kind, ns, name string) (*unstructured.Unstructured, error)
	// ChatMetricsRange runs a range query Klyx built from a template (§3.4).
	ChatMetricsRange(ctx context.Context, promql string, start, end time.Time, step time.Duration) ([]metrics.LabeledSeries, error)
	// RuleMetrics reads one Gateway's per-rule Envoy metrics (The Line 7c).
	RuleMetrics(ctx context.Context, namespace, name string) (routemetrics.GatewayRules, error)
	// The drawings get_dependencies reads (A1 §3.3).
	DependencyGraph(ctx context.Context, kind, ns, name, uid string) depgraph.Graph
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
	GetGatewayTopology(ctx context.Context, namespace, name string) (gwapi.Topology, error)
	// WorkloadCompare reads one workload's fleet-compare facts (§3.7).
	WorkloadCompare(ctx context.Context, kind, ns, name string) (fleet.WorkloadCompareInfo, error)
}

// The cluster connection is chat's read surface (main.go asserts it at run
// time; this fails the build instead).
var _ ChatConn = (*fleet.ClusterConn)(nil)

const (
	toolOverview  = "cluster_overview"
	toolAttention = "list_attention"
	toolList      = "list_resources"
	toolGet       = "get_resource"
	toolEvents    = "get_events"
	toolLogs      = "get_pod_logs"
	toolGitOps    = "gitops_status"

	// chatResultMax caps one serialized tool result (spec §5.2).
	chatResultMax = 16 * 1024
	// chatLogLineMax caps one log line after scrubbing (spec §4).
	chatLogLineMax = 2000
	// a read's gaps and by-name subjects in its ledger entry (AI A1)
	chatReadGapsMax  = 3
	chatReadGapMax   = 200
	chatReadAboutMax = 20
	// chatCheckStatementMax caps one check's statement (bytes)
	chatCheckStatementMax = 1024
)

// ChatToolDef is one tool as offered to a provider: a name, what it does, and
// its JSON schema.
type ChatToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string, min, max int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "minimum": min, "maximum": max}
}

// ChatToolDefs are the tools every tool-mode conversation offers.
func ChatToolDefs() []ChatToolDef {
	return []ChatToolDef{
		{toolOverview, "The cluster's health summary from Klyx: each area's verdict counts, coverage and controller state, plus capabilities and namespace count.", obj(map[string]any{})},
		{toolAttention, "Objects Klyx counts as needing attention, worst first, with Klyx's verdict. Counts per area are complete; the listed objects are the worst few per area.", obj(map[string]any{
			"area": strProp("Optional area id from cluster_overview, e.g. workloads, pods, nodes, flux, argo, gateway, certs, eso, karpenter, helm, events."),
		})},
		{toolList, "One page of objects of a kind: name, namespace, age, status summary and Klyx's verdict where it has one.", obj(map[string]any{
			"kind":           strProp("Kind, plural or short name, e.g. Deployment, pods, svc."),
			"group":          strProp("Optional API group to disambiguate, e.g. apps."),
			"namespace":      strProp("Optional namespace; all namespaces when omitted."),
			"label_selector": strProp("Optional label selector, e.g. app=web."),
			"limit":          intProp("Rows to return, 1-100 (default 50).", 1, 100),
		}, "kind")},
		{toolGet, "One object's safe projection: conditions, facts, spec and status fields Klyx allows, and its warning events. Secret values and Helm values are never included.", obj(map[string]any{
			"kind":      strProp("Kind, plural or short name."),
			"group":     strProp("Optional API group."),
			"namespace": strProp("Namespace, for namespaced kinds."),
			"name":      strProp("Exact object name."),
		}, "kind", "name")},
		{toolEvents, "Kubernetes events, newest first.", obj(map[string]any{
			"namespace":     strProp("Optional namespace; all namespaces when omitted."),
			"kind":          strProp("Optional involved object kind (with name)."),
			"name":          strProp("Optional involved object name (with kind)."),
			"warnings_only": map[string]any{"type": "boolean", "description": "Only Warning events (default true)."},
			"limit":         intProp("Events to return, 1-50 (default 20).", 1, 50),
		})},
		{toolLogs, "The last lines of one pod container's log, scrubbed. Name the container when the pod has more than one.", obj(map[string]any{
			"namespace": strProp("Pod namespace."),
			"pod":       strProp("Exact pod name."),
			"container": strProp("Container name; required when the pod has several."),
			"previous":  map[string]any{"type": "boolean", "description": "The previous (crashed) instance's log."},
			"tail":      intProp("Lines, 1-200 (default 100).", 1, 200),
		}, "namespace", "pod")},
		{toolGitOps, "Flux and Argo CD units with Klyx's ready, sync and health verdicts.", obj(map[string]any{
			"namespace": strProp("Optional namespace filter."),
			"name":      strProp("Optional exact unit name."),
		})},
		{toolUnit, "One Flux Kustomization or HelmRelease read live: its Ready condition, applied and attempted revisions, source, dependencies, inventory and Klyx's verdict. A failed health check is parsed into the objects it names, by name, each joined to the unit's inventory, with the object of that name now as a separate fact.", obj(map[string]any{
			"kind":      map[string]any{"type": "string", "enum": []string{"Kustomization", "HelmRelease"}, "description": "Kustomization or HelmRelease."},
			"namespace": strProp("The unit's namespace."),
			"name":      strProp("The unit's exact name."),
		}, "kind", "namespace", "name")},
		{toolChanges, "What changed in Git between a Flux Kustomization's last applied revision and its last attempted one: the commits and files under its path, and both revisions rendered and compared object by object. A render is a source render under the current spec (the spec in force at the applied revision is unknown); hidden values are named by path only. Needs the source's repository approved in Klyx's Git settings.", obj(map[string]any{
			"kind":      map[string]any{"type": "string", "enum": []string{"Kustomization"}, "description": "Kustomization."},
			"namespace": strProp("The Kustomization's namespace."),
			"name":      strProp("The Kustomization's exact name."),
		}, "kind", "namespace", "name")},
		{toolAlerts, "Alertmanager alerts Klyx read for one object: those whose labels name it, one of its current pods, or its namespace, with state, severity, start time, allowlisted labels and the summary. Matched by label, never by UID.", obj(map[string]any{
			"kind":      strProp("Kind, plural or short name, e.g. Deployment, pod, node."),
			"namespace": strProp("Namespace, for namespaced kinds."),
			"name":      strProp("Exact object name."),
		}, "kind", "name")},
		{toolLogHistory, "A pod's log history from Loki, newest first, scrubbed: for a pod that is gone, or history beyond its previous container. By the pod's UID when this conversation's reads hold it and the log shipper records it, otherwise by namespace and name (every pod of that name in the window); the result says which.", obj(map[string]any{
			"namespace": strProp("Pod namespace."),
			"pod":       strProp("Exact pod name."),
			"uid":       strProp("Optional: one of the pod UIDs this conversation's reads returned; needed only when they hold several pods of that name."),
			"container": strProp("Optional container name."),
			"window":    map[string]any{"type": "string", "enum": []string{"15m", "1h", "6h"}, "description": "How far back (default 1h)."},
			"tail":      intProp("Lines, 1-200 (default 100).", 1, 200),
		}, "namespace", "pod")},
		{toolMetrics, "Prometheus series from one of Klyx's templates, summarised (min, max, last, gaps, counter resets, a threshold crossed): pod_cpu and pod_memory per container of a pod, against its limit; workload_cpu and workload_memory summed over a workload's current pods; restarts per pod (kube-state-metrics); route_requests and route_5xx_ratio per rule of an HTTPRoute, through each Gateway it names (Envoy Gateway, its last 30 minutes). Series are by pod or Envoy cluster name.", obj(map[string]any{
			"template":  map[string]any{"type": "string", "enum": []string{"pod_cpu", "pod_memory", "workload_cpu", "workload_memory", "restarts", "route_requests", "route_5xx_ratio"}, "description": "The template."},
			"kind":      map[string]any{"type": "string", "enum": []string{"Pod", "Deployment", "StatefulSet", "DaemonSet", "HTTPRoute"}, "description": "Pod for pod templates (default); a workload kind for workload templates, and for restarts of a workload's pods; HTTPRoute for route templates."},
			"namespace": strProp("Namespace."),
			"name":      strProp("Exact pod or workload name."),
			"window":    map[string]any{"type": "string", "enum": []string{"15m", "1h", "6h", "24h"}, "description": "How far back (default 1h)."},
		}, "template", "namespace", "name")},
		{toolRollout, "One Deployment's, StatefulSet's or DaemonSet's retained revisions, newest first: each revision's images, pods and Klyx's verdicts for them, the controller's revision markers as reported, the revision whose pod template equals the workload's (the only one called current) and the nearest lower retained revision, and what their pod templates differ in (hidden values by path only). Creation times are metadata: a rollback reuses an old revision object, so they date neither a rollout nor a change.", obj(map[string]any{
			"kind":      map[string]any{"type": "string", "enum": []string{"Deployment", "StatefulSet", "DaemonSet"}, "description": "Deployment, StatefulSet or DaemonSet."},
			"namespace": strProp("The workload's namespace."),
			"name":      strProp("The workload's exact name."),
		}, "kind", "namespace", "name")},
		{toolDeps, "The dependencies Klyx draws for one object, as typed nodes and edges with Klyx's verdict by UID: a Flux unit's dependency graph, a workload's owner chain and pods, a Certificate's issuance chain, a Gateway's topology or an HTTPRoute's parents and backends, a ScaledObject's or HPA's target and owner. A node without a UID is by name.", obj(map[string]any{
			"kind":      strProp("Kind, e.g. Kustomization, Deployment, Certificate, Gateway, HTTPRoute, ScaledObject."),
			"namespace": strProp("Namespace, for namespaced kinds."),
			"name":      strProp("Exact object name."),
		}, "kind", "name")},
	}
}

// ChatRef is a validated object reference from a read: the only source for
// the drawer's internal resource chips (spec §8).
type ChatRef struct {
	GVR       string `json:"gvr"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

// ChatRead is one ledger entry (spec §5.4): names only, never content. ID is
// the read's citation ID within its conversation (AI A1 §2); Checks are the
// observations Klyx evaluated on it.
type ChatRead struct {
	ID       string      `json:"id,omitempty"`
	Tool     string      `json:"tool"`
	Target   string      `json:"target,omitempty"`
	Outcome  string      `json:"outcome"` // completed | failed | truncated | cancelled
	Detail   string      `json:"detail,omitempty"`
	Redacted int         `json:"redacted,omitempty"`
	Refs     []ChatRef   `json:"refs,omitempty"`
	Checks   []ChatCheck `json:"checks,omitempty"`
	// Gaps are the parts of a read its result says it could not read: its
	// first errors, short
	Gaps []string `json:"gaps,omitempty"`
	// About is what a read is about that Refs and Checks do not carry, with
	// how Klyx holds each: a by-name read (Loki by name, metrics selected by
	// pod name, alerts matched by label) names its subjects here by name,
	// never as a reference by UID
	About []ChatCheckSubject `json:"about,omitempty"`
	// ObservedFrom and ObservedTo are when a historical read's evidence
	// describes the world: Loki's lines, a metric window. ObservedUnknown
	// says its evidence carries no time Klyx can read, as a pod's log has
	// none. A read without them describes the cluster as read (AI A1
	// re-review, finding 4).
	ObservedFrom    string `json:"observedFrom,omitempty"`
	ObservedTo      string `json:"observedTo,omitempty"`
	ObservedUnknown bool   `json:"observedUnknown,omitempty"`
}

// observedSpan is the earliest and latest of times, RFC 3339 at their full
// precision (a Loki line's nanoseconds decide its order against an onset);
// ok is false when none parses.
func observedSpan(times []string) (from, to string, ok bool) {
	var lo, hi time.Time
	for _, s := range times {
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			continue
		}
		if !ok || tm.Before(lo) {
			lo = tm
		}
		if !ok || tm.After(hi) {
			hi = tm
		}
		ok = true
	}
	if !ok {
		return "", "", false
	}
	return lo.UTC().Format(time.RFC3339Nano), hi.UTC().Format(time.RFC3339Nano), true
}

// byName is a read's subject held by name only.
func byName(kind, ns, name string) ChatCheckSubject {
	return ChatCheckSubject{Kind: kind, Namespace: ns, Name: name, Identity: identityName}
}

// ChatToolResult is one executed call: the envelope the model sees and the
// ledger entry the operator sees.
type ChatToolResult struct {
	JSON []byte
	Read ChatRead
	// facts are what Klyx read off the objects of this call, for the
	// action proposals' eligibility (AI A3 §2.2): never sent
	facts []actionFact
}

type chatIdentity struct {
	GVR        string `json:"gvr"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	UID        string `json:"uid,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

type chatCoverage struct {
	State string `json:"state"` // live | stale | checking | unreadable
	AsOf  string `json:"asOf,omitempty"`
}

type chatTotal struct {
	Count int  `json:"count"`
	Exact bool `json:"exact"`
}

// chatEnvelope is every tool result's shape (spec §3.3).
type chatEnvelope struct {
	Tool      string        `json:"tool"`
	ReadID    string        `json:"readId,omitempty"`
	Source    string        `json:"source"`
	ReadAt    string        `json:"readAt"`
	Identity  *chatIdentity `json:"identity,omitempty"`
	Coverage  *chatCoverage `json:"coverage,omitempty"`
	Complete  bool          `json:"complete"`
	Total     *chatTotal    `json:"total,omitempty"`
	Truncated bool          `json:"truncated"`
	Errors    []string      `json:"errors,omitempty"`
	Note      string        `json:"note,omitempty"`
	Checks    []ChatCheck   `json:"checks,omitempty"`
	Data      any           `json:"data,omitempty"`
}

// chatTools executes tools on one conversation's conn.
type chatTools struct {
	conn ChatConn
	now  func() time.Time
	// readID is the citation ID of the one read this copy runs (AI A1 §2)
	readID string
	// git reads Git history for git_changes; nil when it is not available
	git chatGit
	// history reads Loki for get_log_history; nil when not available
	history chatHistory
	// ledgerUIDs are the pod UIDs the conversation's own reads hold
	ledgerUIDs func(ns, name string) []string
	// cluster is the conversation's; members its comparison set (A-C2)
	cluster string
	members []chatMember
	// anchor is an investigation's pinned object, nil in a chat: a tool
	// that reads it by name says when the UID differs (AI A1.1 §5)
	anchor *ChatSeedDTO
}

// as is a copy of t that runs one read under its citation ID.
func (t *chatTools) as(id string) *chatTools {
	c := *t
	c.readID = id
	return &c
}

func newChatTools(conn ChatConn, now func() time.Time) *chatTools {
	if now == nil {
		now = time.Now
	}
	return &chatTools{conn: conn, now: now}
}

// Run validates and executes one call. It never returns an error: a refused
// or failed call is an error envelope the model reads.
func (t *chatTools) Run(ctx context.Context, name string, args json.RawMessage) ChatToolResult {
	switch name {
	case toolOverview:
		var a struct{}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		return t.overview()
	case toolAttention:
		var a struct {
			Area string `json:"area"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if a.Area != "" && !idRe.MatchString(a.Area) {
			return t.refuse(name, a.Area, errors.New("area must be an area id from cluster_overview"))
		}
		return t.attention(a.Area)
	case toolList:
		var a struct {
			Kind     string `json:"kind"`
			Group    string `json:"group"`
			NS       string `json:"namespace"`
			Selector string `json:"label_selector"`
			Limit    *int   `json:"limit"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		limit := 50
		if a.Limit != nil {
			limit = *a.Limit
		}
		if err := firstErr(checkKind(a.Kind), checkGroup(a.Group), checkName("namespace", a.NS, false), checkRange("limit", limit, 1, 100), checkSelector(a.Selector)); err != nil {
			return t.refuse(name, a.Kind, err)
		}
		return t.list(ctx, a.Kind, a.Group, a.NS, a.Selector, limit)
	case toolGet:
		var a struct {
			Kind  string `json:"kind"`
			Group string `json:"group"`
			NS    string `json:"namespace"`
			Name  string `json:"name"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if err := firstErr(checkKind(a.Kind), checkGroup(a.Group), checkName("namespace", a.NS, false), checkName("name", a.Name, true)); err != nil {
			return t.refuse(name, a.Kind+" "+a.Name, err)
		}
		return t.get(ctx, a.Kind, a.Group, a.NS, a.Name)
	case toolEvents:
		var a struct {
			NS       string `json:"namespace"`
			Kind     string `json:"kind"`
			Name     string `json:"name"`
			Warnings *bool  `json:"warnings_only"`
			Limit    *int   `json:"limit"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		limit := 20
		if a.Limit != nil {
			limit = *a.Limit
		}
		warnings := a.Warnings == nil || *a.Warnings
		if (a.Kind == "") != (a.Name == "") {
			return t.refuse(name, "", errors.New("kind and name go together"))
		}
		if err := firstErr(checkName("namespace", a.NS, false), checkName("name", a.Name, false), checkRange("limit", limit, 1, 50)); err != nil {
			return t.refuse(name, "", err)
		}
		if a.Kind != "" {
			if err := checkKind(a.Kind); err != nil {
				return t.refuse(name, "", err)
			}
		}
		return t.events(ctx, a.NS, a.Kind, a.Name, warnings, limit)
	case toolLogs:
		var a struct {
			NS        string `json:"namespace"`
			Pod       string `json:"pod"`
			Container string `json:"container"`
			Previous  bool   `json:"previous"`
			Tail      *int   `json:"tail"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		tail := 100
		if a.Tail != nil {
			tail = *a.Tail
		}
		if err := firstErr(checkName("namespace", a.NS, true), checkName("pod", a.Pod, true), checkName("container", a.Container, false), checkRange("tail", tail, 1, 200)); err != nil {
			return t.refuse(name, a.NS+"/"+a.Pod, err)
		}
		return t.logs(ctx, a.NS, a.Pod, a.Container, a.Previous, tail)
	case toolGitOps:
		var a struct {
			NS   string `json:"namespace"`
			Name string `json:"name"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if err := firstErr(checkName("namespace", a.NS, false), checkName("name", a.Name, false)); err != nil {
			return t.refuse(name, "", err)
		}
		return t.gitops(ctx, a.NS, a.Name)
	case toolUnit:
		var a struct {
			Kind string `json:"kind"`
			NS   string `json:"namespace"`
			Name string `json:"name"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if _, ok := unitResources[a.Kind]; !ok {
			return t.refuse(name, label(a.Kind), errUnitKind)
		}
		if err := firstErr(checkName("namespace", a.NS, true), checkName("name", a.Name, true)); err != nil {
			return t.refuse(name, a.Kind+" "+a.NS+"/"+a.Name, err)
		}
		return t.unit(ctx, a.Kind, a.NS, a.Name)
	case toolChanges:
		var a struct {
			Kind string `json:"kind"`
			NS   string `json:"namespace"`
			Name string `json:"name"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if a.Kind != "Kustomization" {
			return t.refuse(name, label(a.Kind), errors.New("kind must be Kustomization"))
		}
		if err := firstErr(checkName("namespace", a.NS, true), checkName("name", a.Name, true)); err != nil {
			return t.refuse(name, a.Kind+" "+a.NS+"/"+a.Name, err)
		}
		return t.changes(ctx, a.NS, a.Name)
	case toolAlerts:
		var a struct {
			Kind string `json:"kind"`
			NS   string `json:"namespace"`
			Name string `json:"name"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if err := firstErr(checkKind(a.Kind), checkName("namespace", a.NS, false), checkName("name", a.Name, true)); err != nil {
			return t.refuse(name, a.Kind+" "+a.Name, err)
		}
		return t.alerts(ctx, a.Kind, a.NS, a.Name)
	case toolLogHistory:
		var a struct {
			NS        string `json:"namespace"`
			Pod       string `json:"pod"`
			UID       string `json:"uid"`
			Container string `json:"container"`
			Window    string `json:"window"`
			Tail      *int   `json:"tail"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		tail := 100
		if a.Tail != nil {
			tail = *a.Tail
		}
		if a.Window == "" {
			a.Window = "1h"
		}
		if _, ok := chatHistoryWindows[a.Window]; !ok {
			return t.refuse(name, a.NS+"/"+a.Pod, errors.New("window must be 15m, 1h or 6h"))
		}
		if err := firstErr(checkName("namespace", a.NS, true), checkName("pod", a.Pod, true), checkName("uid", a.UID, false),
			checkName("container", a.Container, false), checkRange("tail", tail, 1, chatHistoryLinesMax)); err != nil {
			return t.refuse(name, a.NS+"/"+a.Pod, err)
		}
		return t.logHistory(ctx, a.NS, a.Pod, a.UID, a.Container, a.Window, tail)
	case toolMetrics:
		var a struct {
			Template string `json:"template"`
			Kind     string `json:"kind"`
			NS       string `json:"namespace"`
			Name     string `json:"name"`
			Window   string `json:"window"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if a.Window == "" {
			a.Window = "1h"
		}
		if _, ok := chatMetricTemplates[a.Template]; !ok {
			return t.refuse(name, label(a.Template), errors.New("template must be one of pod_cpu, pod_memory, workload_cpu, workload_memory, restarts, route_requests or route_5xx_ratio; Klyx runs no other query"))
		}
		if _, ok := chatMetricWindows[a.Window]; !ok {
			return t.refuse(name, a.Template, errors.New("window must be 15m, 1h, 6h or 24h"))
		}
		if a.Kind != "" && a.Kind != "Pod" && a.Kind != "HTTPRoute" && !chatMetricWorkloads[a.Kind] {
			return t.refuse(name, a.Template, errors.New("kind must be Pod, Deployment, StatefulSet, DaemonSet or HTTPRoute"))
		}
		if err := firstErr(checkName("namespace", a.NS, true), checkName("name", a.Name, true)); err != nil {
			return t.refuse(name, a.Template, err)
		}
		return t.metricSeries(ctx, a.Template, a.Kind, a.NS, a.Name, a.Window)
	case toolRollout:
		var a struct {
			Kind string `json:"kind"`
			NS   string `json:"namespace"`
			Name string `json:"name"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if _, ok := rolloutResources[a.Kind]; !ok {
			return t.refuse(name, label(a.Kind), errors.New("kind must be Deployment, StatefulSet or DaemonSet"))
		}
		if err := firstErr(checkName("namespace", a.NS, true), checkName("name", a.Name, true)); err != nil {
			return t.refuse(name, a.Kind+" "+a.NS+"/"+a.Name, err)
		}
		return t.rollout(ctx, a.Kind, a.NS, a.Name)
	case toolDeps:
		var a struct {
			Kind string `json:"kind"`
			NS   string `json:"namespace"`
			Name string `json:"name"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if err := firstErr(checkKind(a.Kind), checkName("namespace", a.NS, false), checkName("name", a.Name, true)); err != nil {
			return t.refuse(name, a.Kind+" "+a.Name, err)
		}
		return t.deps(ctx, a.Kind, a.NS, a.Name)
	case toolFleetDiff:
		var a struct {
			Kind     string   `json:"kind"`
			NS       string   `json:"namespace"`
			Name     string   `json:"name"`
			Clusters []string `json:"clusters"`
		}
		if err := strictArgs(args, &a); err != nil {
			return t.refuse(name, "", err)
		}
		if !chatMetricWorkloads[a.Kind] {
			return t.refuse(name, label(a.Kind), errors.New("kind must be Deployment, StatefulSet or DaemonSet"))
		}
		if err := firstErr(checkName("namespace", a.NS, true), checkName("name", a.Name, true)); err != nil {
			return t.refuse(name, a.Kind+" "+a.NS+"/"+a.Name, err)
		}
		return t.fleetDiff(ctx, a.Kind, a.NS, a.Name, a.Clusters)
	}
	return t.refuse(name, "", fmt.Errorf("Klyx offers no tool named %q", label(name)))
}

// ---- validation -------------------------------------------------------------

var (
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9._:]*[A-Za-z0-9])?$`)
	kindRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.-]{0,62}$`)
	groupRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	idRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)
)

// strictArgs decodes a tool's JSON arguments: exactly one object, no null
// anywhere, no duplicate member at any depth, no unknown member, no wrong
// type and no trailing data (Codex review of aea77e7: a null or a repeated
// member must never silently widen a read). Empty arguments are {}.
func strictArgs(raw json.RawMessage, into any) error {
	return strictArgsWithin(raw, into, 4096)
}

// strictArgsWithin is strictArgs within max bytes: a call whose documented
// fields are larger than a read's arguments (report_findings,
// propose_actions) has its own aggregate bound, named when it is exceeded.
func strictArgsWithin(raw json.RawMessage, into any, max int) error {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 {
		b = []byte("{}")
	}
	if len(b) > max {
		return fmt.Errorf("arguments are too large: at most %d bytes in all", max)
	}
	if err := strictShape(b); err != nil {
		return fmt.Errorf("invalid arguments: %s", err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("invalid arguments: %s", chatScrub(err.Error(), nil))
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("invalid arguments: trailing data")
	}
	return nil
}

// strictShape walks the JSON once: the root is an object, and no value is
// null or a duplicate member.
func strictShape(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return errors.New("not JSON")
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("arguments must be one JSON object")
	}
	return walkObject(dec, 0)
}

func walkObject(dec *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("arguments nest too deeply")
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errors.New("not JSON")
		}
		k, _ := tok.(string)
		if seen[k] {
			return fmt.Errorf("member %q appears twice", label(k))
		}
		seen[k] = true
		if err := walkValue(dec, depth, k); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	return err
}

func walkValue(dec *json.Decoder, depth int, member string) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("not JSON")
	}
	switch v := tok.(type) {
	case nil:
		return fmt.Errorf("member %q is null; omit it instead", label(member))
	case json.Delim:
		switch v {
		case '{':
			return walkObject(dec, depth+1)
		case '[':
			for dec.More() {
				if err := walkValue(dec, depth+1, member); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		}
	}
	return nil
}

// chatScrub is the one scrubber for every free-text field chat sends or
// records - cluster text, read errors, connection reasons - adding its
// redactions to n when n is not nil (Codex review of aea77e7).
func chatScrub(s string, n *int) string {
	return chatScrubWithin(s, chatStringMax, n)
}

// chatScrubWithin is chatScrub with its own cap, for a field documented
// longer than chatStringMax (a remedy part).
func chatScrubWithin(s string, max int, n *int) string {
	out, c := ai.ScrubEvidence(s, max)
	if n != nil {
		*n += c
	}
	return out
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func checkName(field, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if len(v) > 253 || !nameRe.MatchString(v) {
		return fmt.Errorf("%s is not a valid Kubernetes name", field)
	}
	return nil
}

func checkKind(v string) error {
	if !kindRe.MatchString(v) {
		return errors.New("kind must be a kind, plural or short name")
	}
	return nil
}

func checkGroup(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > 253 || !groupRe.MatchString(v) {
		return errors.New("group is not a valid API group")
	}
	return nil
}

func checkRange(field string, v, min, max int) error {
	if v < min || v > max {
		return fmt.Errorf("%s must be between %d and %d", field, min, max)
	}
	return nil
}

func checkSelector(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > 512 {
		return errors.New("label_selector is too long")
	}
	if _, err := labels.Parse(v); err != nil {
		return errors.New("label_selector is not a valid label selector")
	}
	return nil
}

// label bounds a model-supplied identifier before it is echoed.
func label(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 40 {
			break
		}
		if r == '_' || r == '-' || r == '.' || r == '/' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

// ---- envelopes --------------------------------------------------------------

func (t *chatTools) stamp() string { return t.now().UTC().Format(time.RFC3339) }

func (t *chatTools) refuse(tool, target string, err error) ChatToolResult {
	n := 0
	msg := chatScrub(err.Error(), &n)
	target = chatScrub(target, &n)
	return t.finish(chatEnvelope{Tool: tool, Source: "klyx", ReadAt: t.stamp(), Errors: []string{msg}},
		ChatRead{Tool: tool, Target: target, Outcome: "failed", Detail: msg, Redacted: n})
}

func (t *chatTools) failed(tool, source, target string, err error) ChatToolResult {
	n := 0
	msg := chatScrub(readError(err), &n)
	target = chatScrub(target, &n)
	return t.finish(chatEnvelope{Tool: tool, Source: source, ReadAt: t.stamp(), Errors: []string{msg}},
		ChatRead{Tool: tool, Target: target, Outcome: outcomeOf(err), Detail: msg, Redacted: n})
}

func outcomeOf(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	return "failed"
}

// readError is the model-facing text of a failed read: honest about what
// failed, scrubbed, and never "does not exist" for a scoped miss.
func readError(err error) string {
	switch {
	case apierrors.IsNotFound(err):
		return "not found (in the namespace and kind asked for)"
	case apierrors.IsForbidden(err):
		return "forbidden: Klyx's credentials may not read this"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	}
	return err.Error() // the caller scrubs it with chatScrub
}

// finish serializes an envelope within chatResultMax, shrinking its data
// (and marking it truncated) when it does not fit.
func (t *chatTools) finish(env chatEnvelope, read ChatRead) ChatToolResult {
	return t.finishWithin(env, read, chatResultMax)
}

// finishWithin is finish under another cap (git_changes' 32 KiB).
func (t *chatTools) finishWithin(env chatEnvelope, read ChatRead, max int) ChatToolResult {
	// the read's citation ID, and its checks' (AI A1 §2): the envelope the
	// model reads and the ledger entry carry the same IDs
	env.ReadID, read.ID = t.readID, t.readID
	for i := range env.Checks {
		env.Checks[i].ID = fmt.Sprintf("%s.c%d", t.readID, i+1)
		if s := env.Checks[i].Statement; len(s) > chatCheckStatementMax {
			env.Checks[i].Statement = clipUTF8(s, chatCheckStatementMax) + "…"
		}
	}
	read.Gaps = gapsOf(env.Errors)
	for i := 0; i < 12; i++ {
		b, err := json.Marshal(env)
		if err != nil {
			env.Data, env.Errors = nil, append(env.Errors, "the result could not be encoded")
			continue
		}
		if len(b) <= max {
			if env.Truncated && read.Outcome == "completed" {
				read.Outcome = "truncated"
			}
			read.Checks = append([]ChatCheck(nil), env.Checks...)
			return ChatToolResult{JSON: b, Read: read}
		}
		env.Truncated = true
		env.Data = shrink(env.Data)
	}
	// The data does not fit: the envelope alone, with as many of its
	// checks as fit, failures first (capChecks' order). The ledger holds
	// the checks the model was sent (A1 implementation review, finding 10).
	fb := chatEnvelope{Tool: env.Tool, ReadID: env.ReadID, Source: env.Source, ReadAt: env.ReadAt, Truncated: true, Checks: env.Checks,
		Errors: []string{"the result was too large to send"}}
	b, _ := json.Marshal(fb)
	for len(b) > max && len(fb.Checks) > 0 {
		fb.Checks = fb.Checks[:len(fb.Checks)-1]
		fb.Errors = []string{fmt.Sprintf("the result was too large to send; %d of its %d checks were left out", len(env.Checks)-len(fb.Checks), len(env.Checks))}
		b, _ = json.Marshal(fb)
	}
	read.Outcome = "truncated"
	read.Checks = append([]ChatCheck(nil), fb.Checks...)
	return ChatToolResult{JSON: b, Read: read}
}

// gapsOf keeps a read's first errors, short, for its ledger entry.
func gapsOf(errs []string) []string {
	var out []string
	for i, e := range errs {
		if i == chatReadGapsMax {
			out = append(out, fmt.Sprintf("+%d more", len(errs)-i))
			break
		}
		if r := []rune(e); len(r) > chatReadGapMax {
			e = string(r[:chatReadGapMax]) + "…"
		}
		out = append(out, e)
	}
	return out
}

// shrink halves list data and sheds the bulkiest parts of an object.
func shrink(d any) any {
	switch v := d.(type) {
	case []chatRow:
		return v[:len(v)/2]
	case []chatEvent:
		return v[:len(v)/2]
	case []chatAttention:
		return v[:len(v)/2]
	case chatAttentionData:
		if len(v.Rows) > 0 {
			v.Rows = v.Rows[:len(v.Rows)/2]
		} else {
			v.Parts = v.Parts[:len(v.Parts)/2]
		}
		return v
	case []map[string]any:
		return v[:len(v)/2]
	case ChatObject:
		switch {
		case v.Status != nil:
			v.Status = nil
			v.FieldsDropped = append(v.FieldsDropped, "status (size)")
		case v.Spec != nil:
			v.Spec = nil
			v.FieldsDropped = append(v.FieldsDropped, "spec (size)")
		case len(v.Facts) > 0:
			v.Facts = nil
			v.FieldsDropped = append(v.FieldsDropped, "facts (size)")
		default:
			v.Conditions = v.Conditions[:len(v.Conditions)/2]
		}
		return v
	case chatGetData:
		if len(v.Events) > 0 {
			v.Events = v.Events[:len(v.Events)/2]
			return v
		}
		v.Object = shrink(v.Object).(ChatObject)
		return v
	case chatUnitData:
		// the inventory first, then the parts' kinds, then the entries
		switch {
		case len(v.Inventory) > 0:
			v.Inventory = v.Inventory[:len(v.Inventory)/2]
		case v.Verdict != nil && len(v.Verdict.Kinds) > 0:
			vv := *v.Verdict
			vv.Kinds = nil
			v.Verdict = &vv
		case v.HealthCheck != nil && len(v.HealthCheck.Entries) > 0:
			hc := *v.HealthCheck
			hc.Entries = hc.Entries[:len(hc.Entries)/2]
			v.HealthCheck = &hc
		case v.Ready != nil && v.Ready.Message != "":
			r := *v.Ready
			r.Message = ""
			v.Ready = &r
		default:
			return nil
		}
		return v
	case chatFleetDiffData:
		for i := range v.Clusters {
			v.Clusters[i].Containers = nil
		}
		v.Note += " Containers left out to fit."
		return v
	case chatDepsData:
		// half the nodes, and only the edges between those kept
		v.Nodes = v.Nodes[:len(v.Nodes)/2]
		kept := map[string]bool{}
		for _, n := range v.Nodes {
			kept[n.ID] = true
		}
		var es []chatDepEdge
		for _, e := range v.Edges {
			if kept[e.From] && kept[e.To] {
				es = append(es, e)
			}
		}
		v.Edges = es
		return v
	case chatMetricsData:
		if len(v.Pods) > 0 {
			v.Pods = v.Pods[:len(v.Pods)/2]
		} else {
			v.Series = v.Series[:len(v.Series)/2]
		}
		return v
	case chatLogHistoryData:
		// the newest half stays (the lines are newest first)
		lines := strings.Split(v.Text, "\n")
		v.Text = strings.Join(lines[:len(lines)/2], "\n")
		v.Lines = len(lines) / 2
		return v
	case chatAlertsData:
		v.Alerts = v.Alerts[:len(v.Alerts)/2]
		return v
	case chatChangesData:
		// the file list first, then the commits, then the objects
		switch {
		case len(v.Files) > 0:
			v.Files = v.Files[:len(v.Files)/2]
		case len(v.Commits) > 0:
			v.Commits = v.Commits[:len(v.Commits)/2]
		case len(v.Objects) > 0:
			v.Objects = v.Objects[:len(v.Objects)/2]
		default:
			return nil
		}
		return v
	case chatLogData:
		lines := strings.Split(v.Text, "\n")
		v.Text = strings.Join(lines[len(lines)/2:], "\n")
		v.Lines = len(lines) - len(lines)/2
		return v
	}
	return nil
}

// ---- tools ------------------------------------------------------------------

// chatKindCov is one watched kind or snapshot as a part saw it, with the
// same provenance Explain's anchor carries (spec §3.3; Codex review of
// 4456049): live is also the state of a successful periodic snapshot, so
// its read time, cadence, truncation and object count travel with it.
type chatKindCov struct {
	Kind       string `json:"kind"`
	Role       string `json:"role"`
	Coverage   string `json:"coverage"`
	Class      string `json:"class,omitempty"`
	Error      string `json:"error,omitempty"`
	Objects    int    `json:"objects"`
	Truncated  bool   `json:"truncated,omitempty"`
	AsOf       string `json:"asOf,omitempty"`
	LastGoodAt string `json:"lastGoodAt,omitempty"`
	CadenceSec int64  `json:"cadenceSec,omitempty"`
	Discovery  string `json:"discovery,omitempty"`
}

func rfc(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func chatKinds(ks []observe.KindCoverage, n *int) []chatKindCov {
	out := make([]chatKindCov, 0, len(ks))
	for _, k := range ks {
		out = append(out, chatKindCov{Kind: k.Kind, Role: k.Role, Coverage: string(k.Coverage), Class: k.Class, Error: chatScrub(k.Error, n),
			Objects: k.Objects, Truncated: k.Truncated, AsOf: rfc(k.AsOf), LastGoodAt: rfc(k.LastGoodAt), CadenceSec: k.CadenceSec, Discovery: chatScrub(k.Discovery, n)})
	}
	return out
}

// chatPart is one counted part with its provenance: verdict counts, a
// native part's inclusion count and wording kept apart from its roles, or
// an evidence count, plus its version and kinds.
type chatPart struct {
	Area          string               `json:"area,omitempty"`
	ID            string               `json:"id"`
	Entity        string               `json:"entity"`
	Classifier    string               `json:"classifier"`
	Coverage      string               `json:"coverage"`
	Ver           uint64               `json:"ver"`
	AsOf          string               `json:"asOf,omitempty"`
	Counts        *observe.Counts      `json:"counts,omitempty"`
	NativeRoles   *observe.Counts      `json:"nativeRoles,omitempty"`
	NeedAttention *int                 `json:"needAttention,omitempty"`
	Word          string               `json:"word,omitempty"`
	Evidence      *int                 `json:"evidence,omitempty"`
	Alerts        *observe.AlertCounts `json:"alerts,omitempty"` // coverage S8a: never added to object counts
	Kinds         []chatKindCov        `json:"kinds"`
}

func toChatPart(area string, part observe.Part, n *int) chatPart {
	cp := chatPart{Area: area, ID: part.ID, Entity: part.Entity, Classifier: part.Classifier, Coverage: string(part.Coverage),
		Ver: part.Ver, AsOf: rfc(part.AsOf), Counts: part.Counts, Evidence: part.Evidence, Alerts: part.Alerts, Kinds: chatKinds(part.Kinds, n)}
	if part.Native != nil {
		inc := part.Native.Included
		roles := part.Native.Roles
		cp.NeedAttention, cp.NativeRoles, cp.Word = &inc, &roles, part.Native.Word
	}
	return cp
}

// partProblems states what limits a part's counts: coverage that is not
// live, and capped snapshots (a sample, read at a time of its own).
func partProblems(area string, part observe.Part) []string {
	var out []string
	if part.Coverage != observe.Live {
		out = append(out, area+"/"+part.ID+": coverage "+string(part.Coverage)+" - its counts may be stale or incomplete")
	}
	for _, k := range part.Kinds {
		if k.Truncated {
			msg := fmt.Sprintf("%s/%s: %s is a capped snapshot of %d objects - a sample", area, part.ID, k.Kind, k.Objects)
			if k.AsOf != nil {
				msg += ", read at " + rfc(k.AsOf)
			}
			out = append(out, msg)
		}
	}
	return out
}

type chatTask struct {
	ID         string     `json:"id"`
	Coverage   string     `json:"coverage"`
	Controller string     `json:"controller,omitempty"`
	Parts      []chatPart `json:"parts"`
}

func (t *chatTools) overview() ChatToolResult {
	sum, ok := t.conn.ChatSummary()
	if !ok {
		return t.failed(toolOverview, "observer", "", errors.New("Klyx has no health summary for this cluster yet"))
	}
	p := &projector{}
	var tasks []chatTask
	var problems []string
	for _, task := range sum.Tasks {
		ct := chatTask{ID: task.ID, Coverage: string(task.Coverage)}
		if task.Controller != nil {
			ct.Controller = task.Controller.State
			if task.Controller.Reason != "" {
				ct.Controller += ": " + p.str(task.Controller.Reason, chatStringMax)
			}
		}
		for _, part := range task.Parts {
			ct.Parts = append(ct.Parts, toChatPart("", part, &p.redacted))
			problems = append(problems, partProblems(task.ID, part)...)
		}
		tasks = append(tasks, ct)
	}
	data := map[string]any{"connection": p.str(sum.Connection, chatStringMax), "incarnation": sum.Incarnation, "seq": sum.Seq, "tasks": tasks}
	if sum.Namespaces != nil {
		data["namespaces"] = *sum.Namespaces
	}
	env := chatEnvelope{Tool: toolOverview, Source: "observer", ReadAt: sum.EvaluatedAt.UTC().Format(time.RFC3339), Complete: len(problems) == 0, Data: data,
		Errors: problems, Note: "readAt is when Klyx evaluated the summary; a snapshot kind's asOf is when it was read"}
	return t.finish(env, ChatRead{Tool: toolOverview, Outcome: "completed", Redacted: p.redacted})
}

type chatAttentionData struct {
	Parts  []chatPart      `json:"parts"`
	Rows   []chatAttention `json:"rows"`
	Alerts *int            `json:"alerts,omitempty"` // active critical and warning alerts, apart from the objects (S8a §6)
}

type chatAttention struct {
	Area      string `json:"area"`
	Part      string `json:"part"`
	GVR       string `json:"gvr"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Label     string `json:"label"`
}

func (t *chatTools) attention(area string) ChatToolResult {
	sum, ok := t.conn.ChatSummary()
	if !ok {
		return t.failed(toolAttention, "observer", area, errors.New("Klyx has no health summary for this cluster yet"))
	}
	p := &projector{}
	var rows []chatAttention
	var refs []ChatRef
	var problems []string
	var parts []chatPart
	total, found := 0, area == ""
	var alerts *int
	for _, task := range sum.Tasks {
		if area != "" && task.ID != area {
			continue
		}
		found = true
		for _, part := range task.Parts {
			// An unreadable, stale or capped part's count is not proof of
			// absence (Codex reviews of aea77e7 and 4456049).
			problems = append(problems, partProblems(task.ID, part)...)
			parts = append(parts, toChatPart(task.ID, part, &p.redacted))
			if part.Alerts != nil {
				// alerts are counted apart: never objects, never in the total
				n := part.Alerts.Counted
				if alerts != nil {
					n += *alerts
				}
				alerts = &n
				continue
			}
			switch {
			case part.Native != nil:
				total += part.Native.Included
			case part.Counts != nil:
				total += part.Counts.Fault + part.Counts.Degraded
			}
			for _, r := range part.Top {
				if r.Role == "ok" {
					continue
				}
				rows = append(rows, chatAttention{Area: task.ID, Part: part.ID, GVR: r.GVR, Namespace: r.Namespace, Name: r.Name, Role: r.Role, Label: p.str(r.Label, chatStringMax)})
				refs = append(refs, ChatRef{GVR: r.GVR, Namespace: r.Namespace, Name: r.Name, UID: r.UID})
			}
		}
	}
	if !found {
		return t.refuse(toolAttention, area, fmt.Errorf("no area %q; see cluster_overview for area ids", area))
	}
	exact := len(problems) == 0
	env := chatEnvelope{Tool: toolAttention, Source: "observer", ReadAt: sum.EvaluatedAt.UTC().Format(time.RFC3339), Complete: exact && len(rows) >= total,
		Total: &chatTotal{Count: total, Exact: exact}, Errors: problems, Data: chatAttentionData{Parts: parts, Rows: rows, Alerts: alerts},
		Note: "total counts every object needing attention; the listed objects are the worst few per area; alerts are Alertmanager's active critical and warning alerts, counted apart from the objects"}
	return t.finish(env, ChatRead{Tool: toolAttention, Target: area, Outcome: "completed", Redacted: p.redacted, Refs: capRefs(refs)})
}

// resolve maps a model-named kind to exactly one served resource.
func (t *chatTools) resolve(ctx context.Context, kind, group string) (fleet.ChatKind, error) {
	ks, incomplete, err := t.conn.ChatResolveKind(ctx, kind, group)
	if err != nil {
		return fleet.ChatKind{}, err
	}
	// Failed discovery proves neither absence nor a unique match: the kind
	// may also be served by a group Klyx could not read (Codex review of
	// aea77e7).
	if len(incomplete) > 0 {
		sort.Strings(incomplete)
		if len(incomplete) > 5 {
			incomplete = append(incomplete[:5], "…")
		}
		return fleet.ChatKind{}, fmt.Errorf("discovery failed for %s, so Klyx cannot tell which resource %q names; give its group if you know it", strings.Join(incomplete, ", "), label(kind))
	}
	switch len(ks) {
	case 0:
		return fleet.ChatKind{}, fmt.Errorf("kind %q is not served by this cluster (or cannot be listed)", label(kind))
	case 1:
		return ks[0], nil
	}
	var choices []string
	for _, k := range ks {
		choices = append(choices, k.Kind+" ("+groupOrCore(k.GVR.Group)+")")
	}
	sort.Strings(choices)
	return fleet.ChatKind{}, fmt.Errorf("kind %q is ambiguous; give a group: %s", label(kind), strings.Join(choices, ", "))
}

func groupOrCore(g string) string {
	if g == "" {
		return "core"
	}
	return g
}

type chatRow struct {
	Namespace string       `json:"namespace,omitempty"`
	Name      string       `json:"name"`
	Age       string       `json:"age,omitempty"`
	Status    string       `json:"status,omitempty"`
	Verdict   *chatVerdict `json:"verdict,omitempty"`
}

func (t *chatTools) list(ctx context.Context, kind, group, ns, selector string, limit int) ChatToolResult {
	k, err := t.resolve(ctx, kind, group)
	if err != nil {
		return t.refuse(toolList, kind, err)
	}
	if !k.Namespaced && ns != "" {
		ns = ""
	}
	// Rows carry name, age, status and verdict only - for Secrets the status
	// is the type - so no value from a listed object crosses.
	l, err := t.conn.ChatList(ctx, k.GVR, ns, selector, int64(limit))
	if err != nil {
		return t.failed(toolList, "api", kind, err)
	}
	p := &projector{}
	verdicts := t.verdictsFor(ctx, k.GVR, &p.redacted)
	now := t.now()
	rows := make([]chatRow, 0, len(l.Items))
	var refs []ChatRef
	for i := range l.Items {
		u := &l.Items[i]
		row := chatRow{Namespace: u.GetNamespace(), Name: u.GetName(), Status: p.str(urlscrub.Text(statusSummary(k.GVR, u)).Text, chatStringMax)}
		if ts := u.GetCreationTimestamp(); !ts.IsZero() {
			row.Age = ageText(now.Sub(ts.Time))
		}
		if verdicts != nil {
			if v, ok := verdicts.byUID[string(u.GetUID())]; ok {
				row.Verdict = &v // joined by UID only, never by name
			}
		}
		rows = append(rows, row)
		refs = append(refs, ChatRef{GVR: gvrString(k.GVR), Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID())})
	}
	more := l.GetContinue() != ""
	env := chatEnvelope{Tool: toolList, Source: "api", ReadAt: t.stamp(), Identity: &chatIdentity{GVR: gvrString(k.GVR), Namespace: ns},
		Complete: !more, Total: &chatTotal{Count: len(rows), Exact: !more}, Data: rows}
	if more {
		env.Note = fmt.Sprintf("more than %d exist; narrow by namespace or label_selector", len(rows))
	}
	if verdicts != nil && verdicts.problem != "" {
		env.Errors = append(env.Errors, verdicts.problem)
	}
	return t.finish(env, ChatRead{Tool: toolList, Target: kindTarget(k, ns), Outcome: "completed", Redacted: p.redacted, Refs: capRefs(refs)})
}

func kindTarget(k fleet.ChatKind, ns string) string {
	if ns == "" {
		return k.Kind
	}
	return k.Kind + " in " + ns
}

// statusSummary is one line about an object's state from fields every list
// reader can see: phase, Ready, replica counts.
func statusSummary(gvr schema.GroupVersionResource, u *unstructured.Unstructured) string {
	var parts []string
	if ph, ok, _ := unstructured.NestedString(u.Object, "status", "phase"); ok && ph != "" {
		parts = append(parts, ph)
	}
	if gvr.Group == "" && gvr.Resource == "secrets" {
		t, _, _ := unstructured.NestedString(u.Object, "type")
		return t
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "Ready" || m["type"] == "Available" {
			s := fmt.Sprintf("%v=%v", m["type"], m["status"])
			if r, ok := m["reason"].(string); ok && r != "" && m["status"] != "True" {
				if karpenterResources[gvr.Group+"/"+gvr.Resource] {
					r = karpenterReason(r) // S10 §8: a token, or withheld
				}
				s += " (" + r + ")"
			}
			parts = append(parts, s)
		}
	}
	if r, ok, _ := unstructured.NestedInt64(u.Object, "spec", "replicas"); ok {
		ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
		parts = append(parts, fmt.Sprintf("%d/%d ready", ready, r))
	}
	return strings.Join(parts, ", ")
}

func ageText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// chatVerdict is Klyx's verdict for one object, joined by UID, with where it
// came from (spec §3.3): a verdict part's kind, or a native part's role and,
// separately, whether the lens counts it for attention.
type chatVerdict struct {
	Classifier          string        `json:"classifier"`
	Kind                string        `json:"kind,omitempty"`
	Role                string        `json:"role,omitempty"`
	IncludedInAttention *bool         `json:"includedInAttention,omitempty"`
	Label               string        `json:"label,omitempty"`
	Reason              string        `json:"reason,omitempty"`
	Generation          int64         `json:"generation,omitempty"`
	EvaluatedAt         string        `json:"evaluatedAt"`
	Incarnation         uint64        `json:"incarnation"`
	PartVer             uint64        `json:"partVer"`
	Coverage            string        `json:"coverage"`
	Kinds               []chatKindCov `json:"kinds,omitempty"`
	// at is the capture's evaluation time in full: EvaluatedAt is
	// second-precision text, and a reading must follow a write by its own
	// clock (AI A3 stage 4 review, finding 3). Never sent.
	at time.Time
}

// chatVerdicts is one part's verdicts by UID, with the part's coverage
// problem when it is not live.
type chatVerdicts struct {
	byUID map[string]chatVerdict
	// completed is a KEDA scaler row's own evidence of a completed full
	// pause (keda.Verdict.Completed), by UID (AI A3 stage 3 re-review)
	completed map[string]bool
	// byName is every row's UID by resource, namespace and name, and
	// complete says the part is live with no capped kind: a UID absent from
	// it is gone (AI A3 §5.3)
	byName   map[string]string
	complete bool
	// at is when the capture was evaluated: an absence it shows is dated by it
	at      time.Time
	problem string
	// capped names a capture's kinds that are capped snapshots: their
	// verdicts may be missing even in a live part (A1.1 §4)
	capped string
}

func verdictOf(rows observe.Rows, r capturedRow, n *int) (chatVerdict, bool) {
	v, err := anchorVerdict(rows, r)
	if err != nil {
		return chatVerdict{}, false
	}
	return chatVerdict{
		Classifier: v.Classifier, Kind: v.Kind, Role: v.Role, IncludedInAttention: v.IncludedInAttention,
		Label: chatScrub(v.Label, n), Reason: chatScrub(v.Reason, n), Generation: r.ref.Generation,
		EvaluatedAt: v.EvaluatedAt, Incarnation: rows.Incarnation, PartVer: rows.Part.Ver, Coverage: string(rows.Part.Coverage),
		Kinds: chatKinds(rows.Part.Kinds, n), at: rows.EvaluatedAt,
	}, true
}

func partProblem(task string, rows observe.Rows) string {
	if rows.Part.Coverage == observe.Live {
		return ""
	}
	msg := task + "/" + rows.Part.ID + ": coverage " + string(rows.Part.Coverage)
	for _, k := range rows.Part.Kinds {
		if k.Class != "" {
			msg += " (" + k.Kind + ": " + k.Class + ")"
		}
	}
	return msg + " - Klyx's verdicts may be stale or missing"
}

// chatParts maps a served resource to the Observer part that classifies it.
var chatParts = map[string][2]string{
	"apps/deployments":  {healthsrc.TaskWorkloads, "workloads"},
	"apps/statefulsets": {healthsrc.TaskWorkloads, "workloads"},
	"apps/daemonsets":   {healthsrc.TaskWorkloads, "workloads"},
	"/pods":             {healthsrc.TaskPods, "pods"},
	"/nodes":            {healthsrc.TaskNodes, "nodes"},
	"kustomize.toolkit.fluxcd.io/kustomizations": {healthsrc.TaskFlux, "reconciliations"},
	"helm.toolkit.fluxcd.io/helmreleases":        {healthsrc.TaskFlux, "reconciliations"},
	"argoproj.io/applications":                   {healthsrc.TaskArgo, "applications"},
	"argoproj.io/applicationsets":                {healthsrc.TaskArgo, "applicationsets"},
	"gateway.networking.k8s.io/gateways":         {healthsrc.TaskGateway, "gateways"},
	"gateway.networking.k8s.io/listenersets":     {healthsrc.TaskGateway, "listenersets"}, // coverage S7a
	"cert-manager.io/certificates":               {healthsrc.TaskCerts, "certificates"},
	"cert-manager.io/issuers":                    {healthsrc.TaskCerts, "issuers"},
	"cert-manager.io/clusterissuers":             {healthsrc.TaskCerts, "issuers"},
	"external-secrets.io/externalsecrets":        {healthsrc.TaskESO, "externalsecrets"},
	"karpenter.sh/nodeclaims":                    {healthsrc.TaskKarpenter, "nodeclaims"},
	"karpenter.sh/nodepools":                     {healthsrc.TaskKarpenter, "nodepools"},
	// coverage S10: a class row is a fact (its pools carry the verdict)
	"karpenter.k8s.aws/ec2nodeclasses":   {healthsrc.TaskKarpenter, "nodeclasses"},
	"karpenter.azure.com/aksnodeclasses": {healthsrc.TaskKarpenter, "nodeclasses"},
	"eks.amazonaws.com/nodeclasses":      {healthsrc.TaskKarpenter, "nodeclasses"},
	// coverage S1b and S1c: the image, composition and Receiver parts
	"image.toolkit.fluxcd.io/imagerepositories":      {healthsrc.TaskFlux, "images"},
	"image.toolkit.fluxcd.io/imagepolicies":          {healthsrc.TaskFlux, "images"},
	"image.toolkit.fluxcd.io/imageupdateautomations": {healthsrc.TaskFlux, "images"},
	"source.extensions.fluxcd.io/artifactgenerators": {healthsrc.TaskFlux, "composition"},
	"source.toolkit.fluxcd.io/externalartifacts":     {healthsrc.TaskFlux, "composition"},
	"notification.toolkit.fluxcd.io/receivers":       {healthsrc.TaskFlux, "receivers"},
	// coverage S2a: a scheduled Backup has no verdict of its own (its
	// schedule's verdict covers it); the backups part is the recent
	// unscheduled ones
	"velero.io/schedules":              {healthsrc.TaskVelero, "schedules"},
	"velero.io/backupstoragelocations": {healthsrc.TaskVelero, "locations"},
	"velero.io/backups":                {healthsrc.TaskVelero, "backups"},
	// coverage S3a: a binding's row in the policies part is a fact, never a
	// verdict (the FactRow gate)
	"admissionregistration.k8s.io/validatingwebhookconfigurations":   {healthsrc.TaskAdmission, "webhooks"},
	"admissionregistration.k8s.io/mutatingwebhookconfigurations":     {healthsrc.TaskAdmission, "webhooks"},
	"admissionregistration.k8s.io/validatingadmissionpolicies":       {healthsrc.TaskAdmission, "policies"},
	"admissionregistration.k8s.io/mutatingadmissionpolicies":         {healthsrc.TaskAdmission, "policies"},
	"admissionregistration.k8s.io/validatingadmissionpolicybindings": {healthsrc.TaskAdmission, "policies"},
	"admissionregistration.k8s.io/mutatingadmissionpolicybindings":   {healthsrc.TaskAdmission, "policies"},
	// coverage S4: only a LoadBalancer Service has a verdict
	"/services":                           {healthsrc.TaskLB, "services"},
	"metallb.io/ipaddresspools":           {healthsrc.TaskLB, "pools"},
	"cilium.io/ciliumloadbalancerippools": {healthsrc.TaskLB, "pools"},
	"metallb.io/configurationstates":      {healthsrc.TaskLB, "metallb"},
	// coverage S9: a KEDA-owned HPA's row is a fact (its ScaledObject
	// carries the verdict); the authentications are facts
	"autoscaling/horizontalpodautoscalers": {healthsrc.TaskAutoscaling, "hpas"},
	"keda.sh/scaledobjects":                {healthsrc.TaskKEDA, "scaledobjects"},
	"keda.sh/scaledjobs":                   {healthsrc.TaskKEDA, "scaledjobs"},
}

// verdictsFor reads Klyx's verdicts by UID for a kind the Observer
// classifies; nil when it does not. A part that is not live says so.
func (t *chatTools) verdictsFor(ctx context.Context, gvr schema.GroupVersionResource, n *int) *chatVerdicts {
	tp, ok := chatParts[gvr.Group+"/"+gvr.Resource]
	if !ok {
		return nil
	}
	rows, ok := t.conn.HealthRowsNow(ctx, tp[0], tp[1])
	if !ok {
		return &chatVerdicts{problem: tp[0] + "/" + tp[1] + ": Klyx's verdicts are not available"}
	}
	out := &chatVerdicts{byUID: map[string]chatVerdict{}, completed: map[string]bool{}, byName: map[string]string{}, complete: rows.Part.Coverage == observe.Live, at: rows.EvaluatedAt, problem: partProblem(tp[0], rows)}
	var capped []string
	for _, k := range rows.Part.Kinds {
		if k.Truncated {
			capped = append(capped, fmt.Sprintf("%s/%s: %s is a capped snapshot of %d objects; Klyx's verdicts may be missing", tp[0], rows.Part.ID, k.Kind, k.Objects))
			out.complete = false
		}
	}
	out.capped = strings.Join(capped, "; ")
	forEachRow(rows.Rows, func(r capturedRow) {
		out.byName[rowName(r.ref.GVR, r.ref.Namespace, r.ref.Name)] = r.ref.UID
		if v, ok := verdictOf(rows, r, n); ok {
			out.byUID[r.ref.UID] = v
		}
		switch d := r.data.(type) {
		case healthsrc.ScaledObjectRow:
			out.completed[r.ref.UID] = d.Verdict.Completed
		case healthsrc.ScaledJobRow:
			out.completed[r.ref.UID] = d.Verdict.Completed
		}
	})
	return out
}

// rowName keys a row by its resource (a GVR's last segment), namespace and
// name.
func rowName(gvr, ns, name string) string {
	return gvr[strings.LastIndex(gvr, "/")+1:] + "|" + ns + "/" + name
}

// forEachRow walks a part's healthsrc.RowSet[T] (any T).
func forEachRow(rows any, f func(capturedRow)) {
	v := reflect.ValueOf(rows)
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return
	}
	rs := v.FieldByName("Rows")
	if !rs.IsValid() || rs.Kind() != reflect.Slice {
		return
	}
	for i := 0; i < rs.Len(); i++ {
		r := rs.Index(i)
		ref, ok := r.FieldByName("Ref").Interface().(healthsrc.RowRef)
		if !ok {
			continue
		}
		row := capturedRow{ref: ref, data: r.FieldByName("Data").Interface()}
		if e := r.FieldByName("Eval"); e.IsValid() {
			row.eval, _ = e.Interface().(health.NativeEval)
		}
		f(row)
	}
}

type chatEvent struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Count     int32  `json:"count,omitempty"`
	Object    string `json:"object"`
	ObjectUID string `json:"objectUID,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	LastSeen  string `json:"lastSeen,omitempty"`
}

// eventsFor reads events in a namespace (all when ""), filtered and sorted
// newest first. complete is false when the read was capped.
// eventsFor reads events in a namespace (all when ""), filtered and sorted
// newest first; complete is false when the read was capped. uid, when set,
// keeps only events about that exact object: a same-name replacement's
// events never attach to it (Codex review of aea77e7).
func (t *chatTools) eventsFor(ctx context.Context, ns, kind, name, uid string, warnings bool, limit int, p *projector) ([]chatEvent, int, bool, error) {
	evs, capped, err := t.conn.ListEvents(ctx, ns)
	if err != nil {
		return nil, 0, false, err
	}
	var keep []workloads.EventSummary
	for _, e := range evs {
		if warnings && e.Type != "Warning" {
			continue
		}
		if kind != "" && (!strings.EqualFold(e.Kind, kind) || e.Name != name) {
			continue
		}
		if uid != "" && e.UID != uid {
			continue
		}
		keep = append(keep, e)
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].LastSeenUnix > keep[j].LastSeenUnix })
	total := len(keep)
	if len(keep) > limit {
		keep = keep[:limit]
	}
	out := make([]chatEvent, 0, len(keep))
	for _, e := range keep {
		ce := chatEvent{Type: e.Type, Reason: p.str(e.Reason, chatStringMax), Message: p.str(e.Message, chatStringMax), Count: e.Count, Object: e.Kind + "/" + e.Name, ObjectUID: e.UID, Namespace: e.Namespace}
		if karpenterKinds[e.Kind] {
			// S10 §8: a Karpenter object's event crosses as its reason
			ce.Reason, ce.Message = karpenterReason(e.Reason), ""
		}
		if e.LastSeenUnix > 0 {
			ce.LastSeen = time.Unix(e.LastSeenUnix, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, ce)
	}
	return out, total, !capped, nil
}

func (t *chatTools) events(ctx context.Context, ns, kind, name string, warnings bool, limit int) ChatToolResult {
	p := &projector{}
	evs, total, complete, err := t.eventsFor(ctx, ns, kind, name, "", warnings, limit, p)
	if err != nil {
		return t.failed(toolEvents, "api", ns, err)
	}
	env := chatEnvelope{Tool: toolEvents, Source: "api", ReadAt: t.stamp(), Complete: complete, Total: &chatTotal{Count: total, Exact: complete}, Data: evs, Checks: capChecks(eventChecks(evs))}
	if !complete {
		env.Note = "the event read was capped: this is a sample, newest first among what was read"
	}
	if kind != "" {
		env.Note = strings.TrimSpace(env.Note + " events are matched by kind and name, so they can include a deleted object of the same name; objectUID tells them apart")
	}
	target := ns
	if kind != "" {
		target = strings.TrimPrefix(ns+" "+kind+"/"+name, " ")
	}
	return t.finish(env, ChatRead{Tool: toolEvents, Target: target, Outcome: "completed", Redacted: p.redacted})
}

type chatGetData struct {
	Object  ChatObject   `json:"object"`
	Verdict *chatVerdict `json:"verdict,omitempty"`
	Events  []chatEvent  `json:"warningEvents,omitempty"`
}

func (t *chatTools) get(ctx context.Context, kind, group, ns, name string) ChatToolResult {
	k, err := t.resolve(ctx, kind, group)
	if err != nil {
		return t.refuse(toolGet, kind+" "+name, err)
	}
	if k.Namespaced && ns == "" {
		return t.refuse(toolGet, kind+" "+name, fmt.Errorf("%s is namespaced: give a namespace", k.Kind))
	}
	if !k.Namespaced {
		ns = ""
	}
	return t.getResolved(ctx, k.GVR, ns, name, "")
}

// getGVR reads an object Klyx already identified (a follow-up seed): no
// discovery, and wantUID fences a same-name replacement - a different UID
// returns no object data at all.
func (t *chatTools) getGVR(ctx context.Context, gvr schema.GroupVersionResource, ns, name, wantUID string) ChatToolResult {
	return t.getResolved(ctx, gvr, ns, name, wantUID)
}

func (t *chatTools) getResolved(ctx context.Context, gvr schema.GroupVersionResource, ns, name, wantUID string) ChatToolResult {
	target := strings.TrimPrefix(ns+"/"+name, "/")
	u, err := t.conn.ChatGet(ctx, gvr, ns, name)
	if err != nil {
		return t.failed(toolGet, "api", gvr.Resource+" "+target, err)
	}
	if wantUID != "" && string(u.GetUID()) != wantUID {
		return t.refuse(toolGet, gvr.Resource+" "+target, errors.New("the object was replaced since you asked about it (its UID changed); nothing about the new object was read"))
	}
	read, acquired := u, acquisition()
	// coverage S0b §3: the sanitiser runs before the structural projection,
	// so a registered surface never reaches a model, scrubbed or not
	u = &unstructured.Unstructured{Object: crd.SanitizeEmbedded(gvr.Group, u.GetKind(), u.Object).Object}
	o := chatProject(gvr, u)
	p := &projector{redacted: o.Redacted}
	data := chatGetData{Object: o}
	env := chatEnvelope{Tool: toolGet, Source: "api", ReadAt: t.stamp(), Complete: true,
		Identity: &chatIdentity{GVR: gvrString(gvr), Namespace: ns, Name: name, UID: o.UID, Generation: o.Generation}}
	if vs := t.verdictsFor(ctx, gvr, &p.redacted); vs != nil {
		if v, ok := vs.byUID[o.UID]; ok {
			data.Verdict = &v
			if v.Generation != 0 && o.Generation != 0 && v.Generation != o.Generation {
				env.Note = strings.TrimSpace(env.Note + fmt.Sprintf(" Klyx's verdict is for generation %d; the object is now at %d, so it may be out of date.", v.Generation, o.Generation))
			}
		}
		if vs.problem != "" {
			env.Errors = append(env.Errors, vs.problem)
		}
	}
	if evs, _, complete, err := t.eventsFor(ctx, ns, o.Kind, name, o.UID, true, 10, p); err != nil {
		env.Errors = append(env.Errors, "events: "+chatScrub(readError(err), &p.redacted))
		env.Complete = false
	} else {
		data.Events = evs
		if !complete {
			env.Note = strings.TrimSpace(env.Note + " The event read was capped: warning events may be a sample.")
		}
	}
	env.Data = data
	// AI A1 §2: what Klyx itself reads off the object, its verdict and its
	// warning events, all by the object's UID
	subj := ChatCheckSubject{Kind: o.Kind, Namespace: ns, Name: name, UID: o.UID, Identity: identityUID}
	var checks []ChatCheck
	if data.Verdict != nil {
		if c := verdictCheck(subj, *data.Verdict, p); c != nil {
			checks = append(checks, *c)
		}
	}
	checks = append(checks, conditionChecks(u, subj, env.ReadAt, p)...)
	if gvr.Group == "" && gvr.Resource == "pods" {
		checks = append(checks, podStatusChecks(u, subj, env.ReadAt, p)...)
	}
	checks = append(checks, eventChecks(data.Events)...)
	env.Checks = capChecks(checks)
	ref := ChatRef{GVR: gvrString(gvr), Namespace: ns, Name: name, UID: o.UID}
	res := t.finish(env, ChatRead{Tool: toolGet, Target: o.Kind + " " + target, Outcome: "completed", Redacted: p.redacted, Refs: []ChatRef{ref}})
	res.facts = []actionFact{factOf(gvr, read, t.readID, env.ReadAt, acquired)}
	return res
}

type chatLogData struct {
	PodUID    string `json:"podUID"`
	Container string `json:"container"`
	Instance  string `json:"instance"` // current | previous
	Lines     int    `json:"lines"`
	Text      string `json:"text"`
}

func (t *chatTools) logs(ctx context.Context, ns, pod, container string, previous bool, tail int) ChatToolResult {
	target := ns + "/" + pod
	if container != "" {
		target += "/" + container
	}
	snap, err := t.conn.PodLogSnapshot(ctx, ns, pod, container, previous, int64(tail))
	if err != nil {
		if len(snap.Containers) > 0 {
			return t.refuse(toolLogs, target, fmt.Errorf("%s; containers: %s", readError(err), strings.Join(snap.Containers, ", ")))
		}
		return t.failed(toolLogs, "api", target, err)
	}
	if snap.Text == "" && len(snap.Containers) > 0 {
		return t.refuse(toolLogs, target, fmt.Errorf("pod %s has several containers; name one of: %s", pod, strings.Join(snap.Containers, ", ")))
	}
	// Scrub the whole text first, so a credential split across lines is
	// still matched; then cap each line (spec §4).
	text, n := ai.ScrubEvidence(snap.Text, len(snap.Text)+1)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		if r := []rune(l); len(r) > chatLogLineMax {
			lines[i] = string(r[:chatLogLineMax]) + "…[line truncated]"
			n++
		}
	}
	instance := "current"
	if snap.Previous {
		instance = "previous"
	}
	data := chatLogData{PodUID: snap.PodUID, Container: snap.Container, Instance: instance, Lines: len(lines), Text: strings.Join(lines, "\n")}
	env := chatEnvelope{Tool: toolLogs, Source: "api", ReadAt: snap.ReadAt.UTC().Format(time.RFC3339), Complete: !snap.Truncated, Truncated: snap.Truncated,
		Identity: &chatIdentity{GVR: "v1/pods", Namespace: ns, Name: pod, UID: snap.PodUID}, Data: data,
		Note: "log text is scrubbed of recognized credential shapes; it is not guaranteed secret-free"}
	// a pod's log carries no timestamps: when its lines were written is not
	// known, the previous container's above all
	read := ChatRead{Tool: toolLogs, Target: ns + "/" + pod + "/" + snap.Container, Outcome: "completed", Redacted: n,
		Detail: fmt.Sprintf("%d lines", len(lines)), Refs: []ChatRef{{GVR: "v1/pods", Namespace: ns, Name: pod, UID: snap.PodUID}}, ObservedUnknown: len(lines) > 0}
	return t.finish(env, read)
}

func (t *chatTools) gitops(ctx context.Context, ns, name string) ChatToolResult {
	p := &projector{}
	var rows []map[string]any
	var errs []string
	var refs []ChatRef
	observed := map[string]bool{}
	if sum, ok := t.conn.ChatSummary(); ok {
		for _, task := range sum.Tasks {
			observed[task.ID] = true
		}
	}
	read := false
	for _, tp := range [][2]string{{healthsrc.TaskFlux, "reconciliations"}, {healthsrc.TaskArgo, "applications"}} {
		rs, ok := t.conn.HealthRowsNow(ctx, tp[0], tp[1])
		if !ok {
			if observed[tp[0]] {
				// Observed but unreadable is an error, never "not installed"
				// (Codex review of aea77e7).
				errs = append(errs, tp[0]+"/"+tp[1]+": Klyx's rows are not available")
			}
			continue
		}
		read = true
		if pr := partProblem(tp[0], rs); pr != "" {
			errs = append(errs, pr)
		}
		forEachRow(rs.Rows, func(r capturedRow) {
			if (ns != "" && r.ref.Namespace != ns) || (name != "" && r.ref.Name != name) {
				return
			}
			row := map[string]any{"tool": tp[0], "gvr": r.ref.GVR, "namespace": r.ref.Namespace, "name": r.ref.Name, "uid": r.ref.UID}
			if v, ok := verdictOf(rs, r, &p.redacted); ok {
				row["verdict"] = v
			}
			if d := p.tree(rowData(r.data), ".row", 0, 2, chatStringMax, false); d != nil {
				row["detail"] = d
			}
			rows = append(rows, row)
			refs = append(refs, ChatRef{GVR: r.ref.GVR, Namespace: r.ref.Namespace, Name: r.ref.Name, UID: r.ref.UID})
		})
	}
	if !read && len(errs) == 0 {
		return t.failed(toolGitOps, "observer", ns, errors.New("Klyx observes no Flux or Argo CD on this cluster"))
	}
	env := chatEnvelope{Tool: toolGitOps, Source: "observer", ReadAt: t.stamp(), Complete: len(errs) == 0, Total: &chatTotal{Count: len(rows), Exact: len(errs) == 0}, Errors: errs, Data: rows}
	return t.finish(env, ChatRead{Tool: toolGitOps, Target: strings.TrimSpace(ns + " " + name), Outcome: "completed", Redacted: p.redacted, Refs: capRefs(refs)})
}

// rowData turns a row's Data into a generic map through JSON, so the
// projector's structural rules apply to it like any other object.
func rowData(d any) map[string]any {
	b, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// capRefs bounds the ledger's reference list.
func capRefs(r []ChatRef) []ChatRef {
	if len(r) > 100 {
		return r[:100]
	}
	return r
}
