package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

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

type fakeChatConn struct {
	kinds        map[string][]fleet.ChatKind
	incomplete   []string
	rows         map[string]observe.Rows               // task/part
	objects      map[string]*unstructured.Unstructured // gvr|ns|name
	lists        map[string]*unstructured.UnstructuredList
	listErr      error
	summary      *observe.Summary
	events       []workloads.EventSummary
	eventsCap    bool
	eventsErr    error
	logs         fleet.LogSnapshot
	logsErr      error
	logCalls     int
	flux         map[string]*unstructured.Unstructured // Kind|ns|name
	fluxErr      error
	fluxCalls    int
	fluxHook     func(kind string) // runs after each Flux read
	compare      fleet.WorkloadCompareInfo
	compareErr   error
	compareCalls int
	compareHook  func() // runs after each compare read
	graph        depgraph.Graph
	graphUIDs    []string
	graphHook    func(ctx context.Context) // runs inside DependencyGraph
	chain        *certmanager.Chain
	topo         *gwapi.Topology
	rules        map[string]routemetrics.GatewayRules // by Gateway ns/name
	ruleCalls    []string
	series       map[string][]metrics.LabeledSeries // by query
	seriesErr    error
	queries      []string
	steps        []time.Duration
}

func (f *fakeChatConn) WorkloadCompare(_ context.Context, kind, ns, name string) (fleet.WorkloadCompareInfo, error) {
	f.compareCalls++
	if f.compareHook != nil {
		defer f.compareHook()
	}
	if f.compareErr != nil {
		return fleet.WorkloadCompareInfo{}, f.compareErr
	}
	return f.compare, nil
}

func (f *fakeChatConn) DependencyGraph(ctx context.Context, _, _, _, uid string) depgraph.Graph {
	if f.graphHook != nil {
		f.graphHook(ctx)
	}
	f.graphUIDs = append(f.graphUIDs, uid)
	return f.graph
}

func (f *fakeChatConn) GetCertificateChain(_ context.Context, ns, name string) (certmanager.Chain, error) {
	if f.chain == nil {
		return certmanager.Chain{}, apierrors.NewNotFound(schema.GroupResource{Resource: "certificates"}, name)
	}
	return *f.chain, nil
}

func (f *fakeChatConn) GetGatewayTopology(_ context.Context, ns, name string) (gwapi.Topology, error) {
	if f.topo == nil {
		return gwapi.Topology{}, apierrors.NewNotFound(schema.GroupResource{Resource: "gateways"}, name)
	}
	return *f.topo, nil
}

func (f *fakeChatConn) RuleMetrics(_ context.Context, ns, name string) (routemetrics.GatewayRules, error) {
	f.ruleCalls = append(f.ruleCalls, ns+"/"+name)
	r, ok := f.rules[ns+"/"+name]
	if !ok {
		return routemetrics.GatewayRules{}, apierrors.NewNotFound(schema.GroupResource{Resource: "gateways"}, name)
	}
	return r, nil
}

func (f *fakeChatConn) ChatMetricsRange(_ context.Context, promql string, start, end time.Time, step time.Duration) ([]metrics.LabeledSeries, error) {
	f.queries, f.steps = append(f.queries, promql), append(f.steps, step)
	if f.seriesErr != nil {
		return nil, f.seriesErr
	}
	return f.series[promql], nil
}

func (f *fakeChatConn) ChatResolveKind(_ context.Context, kind, group string) ([]fleet.ChatKind, []string, error) {
	var out []fleet.ChatKind
	for _, k := range f.kinds[strings.ToLower(kind)] {
		if group == "" || k.GVR.Group == group {
			out = append(out, k)
		}
	}
	return out, f.incomplete, nil
}

func key(gvr schema.GroupVersionResource, ns, name string) string {
	return gvr.String() + "|" + ns + "|" + name
}

func (f *fakeChatConn) ChatGet(_ context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	if u, ok := f.objects[key(gvr, ns, name)]; ok {
		return u, nil
	}
	return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
}

func (f *fakeChatConn) ChatList(_ context.Context, gvr schema.GroupVersionResource, ns, _ string, _ int64) (*unstructured.UnstructuredList, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if l, ok := f.lists[gvr.String()+"|"+ns]; ok {
		return l, nil
	}
	return &unstructured.UnstructuredList{}, nil
}

func (f *fakeChatConn) ChatSummary() (observe.Summary, bool) {
	if f.summary == nil {
		return observe.Summary{}, false
	}
	return *f.summary, true
}

func (f *fakeChatConn) HealthRowsNow(_ context.Context, task, part string) (observe.Rows, bool) {
	r, ok := f.rows[task+"/"+part]
	return r, ok
}

func (f *fakeChatConn) ListEvents(context.Context, string) ([]workloads.EventSummary, bool, error) {
	return f.events, f.eventsCap, f.eventsErr
}

func (f *fakeChatConn) FluxObject(_ context.Context, kind, ns, name string) (*unstructured.Unstructured, error) {
	f.fluxCalls++
	if f.fluxHook != nil {
		defer f.fluxHook(kind)
	}
	if f.fluxErr != nil {
		return nil, f.fluxErr
	}
	if u, ok := f.flux[kind+"|"+ns+"|"+name]; ok {
		return u, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: kind}, name)
}

func (f *fakeChatConn) PodLogSnapshot(context.Context, string, string, string, bool, int64) (fleet.LogSnapshot, error) {
	f.logCalls++
	return f.logs, f.logsErr
}

var (
	podsGVR   = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	deployGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	fixedNow  = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
)

func newFakeChat() *fakeChatConn {
	return &fakeChatConn{
		kinds: map[string][]fleet.ChatKind{
			"pods":        {{GVR: podsGVR, Kind: "Pod", Namespaced: true}},
			"pod":         {{GVR: podsGVR, Kind: "Pod", Namespaced: true}},
			"deployment":  {{GVR: deployGVR, Kind: "Deployment", Namespaced: true}},
			"certificate": {{GVR: schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}, Kind: "Certificate", Namespaced: true}, {GVR: schema.GroupVersionResource{Group: "networking.internal.knative.dev", Version: "v1alpha1", Resource: "certificates"}, Kind: "Certificate", Namespaced: true}},
		},
		objects: map[string]*unstructured.Unstructured{},
		lists:   map[string]*unstructured.UnstructuredList{},
	}
}

func run(t *testing.T, f *fakeChatConn, tool, args string) (chatEnvelope, ChatRead) {
	t.Helper()
	res := newChatTools(f, fixedNow).Run(context.Background(), tool, json.RawMessage(args))
	if len(res.JSON) > chatResultMax {
		t.Fatalf("%s result is %d bytes, over the %d cap", tool, len(res.JSON), chatResultMax)
	}
	var env chatEnvelope
	if err := json.Unmarshal(res.JSON, &env); err != nil {
		t.Fatalf("%s result is not an envelope: %v %s", tool, err, res.JSON)
	}
	return env, res.Read
}

// AI A1 implementation review, finding 10: the last resort keeps the
// envelope and as many checks as fit, failures first, and says how many it
// left out; the ledger holds the checks sent.
func TestFinishFallsBackWithinItsBound(t *testing.T) {
	tools := newChatTools(newFakeChat(), fixedNow).as("r7")
	var checks []ChatCheck
	for i := 0; i < chatChecksMax; i++ {
		checks = append(checks, ChatCheck{Class: checkEvent, Subject: ChatCheckSubject{Kind: "Pod", Namespace: "shop", Name: fmt.Sprintf("p%d", i), Identity: identityName},
			Statement: strings.Repeat("s", 4000)})
	}
	env := chatEnvelope{Tool: toolEvents, Source: "api", Checks: checks, Data: strings.Repeat("d", 9000)}
	res := tools.finishWithin(env, ChatRead{Tool: toolEvents, Outcome: "completed"}, 4096)
	var got chatEnvelope
	if err := json.Unmarshal(res.JSON, &got); err != nil || len(res.JSON) > 4096 {
		t.Fatalf("%d bytes: %v", len(res.JSON), err)
	}
	if len(got.Checks) == 0 || len(got.Checks) == chatChecksMax || len(res.Read.Checks) != len(got.Checks) || got.Checks[0].ID != "r7.c1" ||
		!strings.Contains(got.Errors[0], fmt.Sprintf("of its %d checks were left out", chatChecksMax)) || res.Read.Outcome != "truncated" {
		t.Fatalf("%+v %+v", got, res.Read)
	}
}

func TestChatToolsRefuseBadArguments(t *testing.T) {
	f := newFakeChat()
	cases := []struct{ tool, args, want string }{
		{toolList, `{"kind":"pods","extra":1}`, "invalid arguments"},
		{toolList, `{"kind":"pods","limit":500}`, "limit must be between 1 and 100"},
		{toolList, `{"kind":"pods","limit":"ten"}`, "invalid arguments"},
		{toolList, `{"kind":"pods","namespace":"../etc"}`, "not a valid Kubernetes name"},
		{toolList, `{"kind":"pods","label_selector":"a in (("}`, "not a valid label selector"},
		{toolGet, `{"kind":"pods"}`, "name is required"},
		{toolLogs, `{"namespace":"shop","pod":"web","tail":0}`, "tail must be between 1 and 200"},
		{toolEvents, `{"kind":"Pod"}`, "kind and name go together"},
		{toolList, `{"kind":"pods"} {"kind":"pods"}`, "trailing data"},
		{"delete_pod", `{}`, "no tool named"},
	}
	for _, c := range cases {
		env, read := run(t, f, c.tool, c.args)
		if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], c.want) || read.Outcome != "failed" {
			t.Errorf("%s %s: want refusal %q, got %+v / %+v", c.tool, c.args, c.want, env, read)
		}
	}
	if f.logCalls != 0 {
		t.Fatal("a refused call must read nothing")
	}
}

func TestChatToolsAmbiguousKindReadsNothing(t *testing.T) {
	f := newFakeChat()
	env, _ := run(t, f, toolGet, `{"kind":"certificate","namespace":"shop","name":"web"}`)
	if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "ambiguous") || !strings.Contains(env.Errors[0], "cert-manager.io") {
		t.Fatalf("an ambiguous kind lists its choices: %+v", env)
	}
	env, _ = run(t, f, toolGet, `{"kind":"certificate","group":"cert-manager.io","namespace":"shop","name":"web"}`)
	if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "not found") {
		t.Fatalf("a group resolves it, then the scoped miss says not found: %+v", env)
	}
}

func TestChatToolsListReportsALowerBoundWhenMoreExist(t *testing.T) {
	f := newFakeChat()
	l := &unstructured.UnstructuredList{}
	for i := 0; i < 3; i++ {
		u := unstructured.Unstructured{}
		u.SetName(fmt.Sprintf("web-%d", i))
		u.SetNamespace("shop")
		u.SetUID("u")
		l.Items = append(l.Items, u)
	}
	l.SetContinue("more")
	f.lists[podsGVR.String()+"|shop"] = l
	env, read := run(t, f, toolList, `{"kind":"pods","namespace":"shop","limit":3}`)
	if env.Complete || env.Total == nil || env.Total.Exact || env.Total.Count != 3 || !strings.Contains(env.Note, "more than 3") {
		t.Fatalf("a page with a continue token is a lower bound: %+v", env)
	}
	if read.Outcome != "completed" || len(read.Refs) != 3 {
		t.Fatalf("the ledger records the listed objects: %+v", read)
	}

	f.listErr = apierrors.NewForbidden(podsGVR.GroupResource(), "", errors.New("no"))
	env, read = run(t, f, toolList, `{"kind":"pods"}`)
	if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "forbidden") || env.Data != nil || read.Outcome != "failed" {
		t.Fatalf("a forbidden list is an error, never an empty list: %+v", env)
	}
}

func TestChatToolsGetKeepsAnEventErrorBesideTheObject(t *testing.T) {
	f := newFakeChat()
	u := uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web", "generation": int64(4)},
		"spec": map[string]any{"replicas": int64(2)}})
	f.objects[key(deployGVR, "shop", "web")] = u
	f.eventsErr = errors.New("events are unreadable")
	env, read := run(t, f, toolGet, `{"kind":"deployment","namespace":"shop","name":"web"}`)
	if env.Identity == nil || env.Identity.UID != "uid-web" || env.Identity.Generation != 4 {
		t.Fatalf("the envelope names the exact object read: %+v", env.Identity)
	}
	if env.Complete || !strings.Contains(strings.Join(env.Errors, "|"), "events: events are unreadable") || env.Data == nil {
		t.Fatalf("an unreadable component sits beside a successful read: %+v", env)
	}
	if len(read.Refs) != 1 || read.Refs[0].UID != "uid-web" {
		t.Fatalf("the ledger carries the validated reference: %+v", read)
	}
	env, _ = run(t, f, toolGet, `{"kind":"deployment","name":"web"}`)
	if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "namespaced") {
		t.Fatalf("a namespaced kind without a namespace is refused: %+v", env)
	}
}

func TestChatToolsLogsScrubAcrossLinesAndCapLines(t *testing.T) {
	f := newFakeChat()
	f.logs = fleet.LogSnapshot{PodUID: "uid-p", Container: "app", ReadAt: fixedNow(),
		Text: "start\n-----BEGIN RSA PRIVATE KEY-----\nMIIEsecretbody\n-----END RSA PRIVATE KEY-----\n" + strings.Repeat("y", 5000) + "\nAuthorization: Bearer abcdefghijkl\n"}
	env, read := run(t, f, toolLogs, `{"namespace":"shop","pod":"web","tail":50}`)
	b, _ := json.Marshal(env)
	if strings.Contains(string(b), "MIIEsecretbody") || strings.Contains(string(b), "abcdefghijkl") {
		t.Fatalf("a credential split across lines is still scrubbed: %s", b)
	}
	if !strings.Contains(string(b), "line truncated") || read.Redacted < 3 {
		t.Fatalf("a long line is capped and the redactions counted: %+v", read)
	}
	if env.Identity == nil || env.Identity.UID != "uid-p" || !strings.Contains(env.Note, "not guaranteed secret-free") {
		t.Fatalf("the log result names the pod it read and says it is scrubbed, not proven: %+v", env)
	}

	f.logs = fleet.LogSnapshot{PodUID: "uid-p", Containers: []string{"app", "sidecar"}}
	env, read = run(t, f, toolLogs, `{"namespace":"shop","pod":"web"}`)
	if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "app, sidecar") || read.Outcome != "failed" {
		t.Fatalf("an ambiguous container reads nothing and lists them: %+v", env)
	}

	f.logs, f.logsErr = fleet.LogSnapshot{}, fleet.ErrPodReplaced
	env, _ = run(t, f, toolLogs, `{"namespace":"shop","pod":"web","container":"app"}`)
	if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "replaced") {
		t.Fatalf("a replaced pod's log is discarded: %+v", env)
	}
}

func TestChatToolsResultStaysUnderTheCap(t *testing.T) {
	f := newFakeChat()
	var evs []workloads.EventSummary
	for i := 0; i < 50; i++ {
		evs = append(evs, workloads.EventSummary{Type: "Warning", Reason: "BackOff", Message: strings.Repeat("m", 480), Kind: "Pod", Name: fmt.Sprintf("p-%d", i), LastSeenUnix: int64(i)})
	}
	f.events = evs
	env, read := run(t, f, toolEvents, `{"limit":50}`)
	if !env.Truncated || read.Outcome != "truncated" {
		t.Fatalf("an oversized result is shrunk and marked truncated: %+v", read)
	}
	data, _ := json.Marshal(env.Data)
	var rows []chatEvent
	_ = json.Unmarshal(data, &rows)
	if len(rows) == 0 || rows[0].Object != "Pod/p-49" {
		t.Fatalf("events are newest first, and the newest survive shrinking: %+v", rows)
	}
}

func TestChatToolsOverviewWithoutSummaryIsAnError(t *testing.T) {
	env, read := run(t, newFakeChat(), toolOverview, `{}`)
	if len(env.Errors) == 0 || read.Outcome != "failed" {
		t.Fatalf("no summary is an error, never an empty overview: %+v", env)
	}
	f := newFakeChat()
	n := 3
	f.summary = &observe.Summary{EvaluatedAt: fixedNow(), Connection: "connected", Tasks: []observe.Task{{
		ID: "pods", Coverage: observe.Live, Parts: []observe.Part{{ID: "pods", Classifier: "verdict", Coverage: observe.Live,
			Counts: &observe.Counts{Total: 10, Fault: 2, Degraded: 1}, Top: []observe.Ref{{GVR: "v1/pods", Namespace: "shop", Name: "web-1", Role: "fault", Label: "CrashLoopBackOff"}}}}},
	}, Namespaces: &n}
	env, read = run(t, f, toolAttention, `{"area":"pods"}`)
	if env.Total == nil || env.Total.Count != 3 || env.Complete || len(read.Refs) != 1 {
		t.Fatalf("attention totals every object needing it and lists the worst: %+v %+v", env, read)
	}
	env, _ = run(t, f, toolAttention, `{"area":"flux"}`)
	if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "no area") {
		t.Fatalf("an unknown area says so: %+v", env)
	}
}

// ---- Codex review of aea77e7 --------------------------------------------------

func TestChatStrictArgsRefusesNullAndDuplicates(t *testing.T) {
	f := newFakeChat()
	for _, args := range []string{
		`null`,
		`[]`,
		`{"kind":"pods","namespace":null}`,
		`{"kind":"pods","limit":null}`,
		`{"kind":"secrets","kind":"pods"}`,
		`{"kind":"pods","label_selector":"a=b","label_selector":"c=d"}`,
	} {
		env, read := run(t, f, toolList, args)
		if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "invalid arguments") || read.Outcome != "failed" {
			t.Errorf("%s must be refused before any read: %+v", args, env)
		}
	}
}

func TestChatScrubsReadErrorsAndConnectionReasons(t *testing.T) {
	f := newFakeChat()
	f.listErr = errors.New(`Get "https://admin:` + sentArbitrary + `@10.0.0.1/api": dial tcp: password=` + sentArbitrary)
	res := newChatTools(f, fixedNow).Run(context.Background(), toolList, json.RawMessage(`{"kind":"pods"}`))
	if strings.Contains(string(res.JSON), sentArbitrary) || strings.Contains(res.Read.Detail, sentArbitrary) || res.Read.Redacted == 0 {
		t.Fatalf("a read error is evidence-scrubbed in the result and the ledger: %s / %+v", res.JSON, res.Read)
	}
	f.summary = &observe.Summary{EvaluatedAt: fixedNow(), Connection: "degraded: token=" + sentArbitrary}
	res = newChatTools(f, fixedNow).Run(context.Background(), toolOverview, json.RawMessage(`{}`))
	if strings.Contains(string(res.JSON), sentArbitrary) {
		t.Fatalf("a connection reason is scrubbed: %s", res.JSON)
	}
}

func TestChatIncompleteDiscoveryResolvesNothing(t *testing.T) {
	f := newFakeChat()
	f.incomplete = []string{"cert-manager.io/v1"}
	env, _ := run(t, f, toolGet, `{"kind":"pods","namespace":"shop","name":"web"}`)
	if len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "discovery failed for cert-manager.io/v1") {
		t.Fatalf("incomplete discovery proves neither absence nor a unique match: %+v", env)
	}
}

func podVerdictRows(uid string, gen int64, cov observe.Coverage) observe.Rows {
	return observe.Rows{Task: "pods", Incarnation: 3, EvaluatedAt: fixedNow(),
		Part: observe.Part{ID: "pods", Classifier: "verdict", Coverage: cov, Ver: 7},
		Rows: healthsrc.RowSet[workloads.PodSummary]{Rows: []healthsrc.Row[workloads.PodSummary]{{
			Ref:  healthsrc.RowRef{UID: uid, GVR: "v1/pods", Namespace: "shop", Name: "web", Generation: gen},
			Data: workloads.PodSummary{Health: health.Verdict{Kind: health.Fault, Label: "CrashLoopBackOff"}},
		}}}}
}

func TestChatGetJoinsTheVerdictByUIDAndKeepsOnlyItsEvents(t *testing.T) {
	f := newFakeChat()
	f.objects[key(podsGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-new", "generation": int64(2)}})
	f.rows = map[string]observe.Rows{"pods/pods": podVerdictRows("uid-old", 1, observe.Live)}
	f.events = []workloads.EventSummary{
		{Type: "Warning", Reason: "OldCrash", Kind: "Pod", Name: "web", UID: "uid-old", LastSeenUnix: 2},
		{Type: "Warning", Reason: "NewCrash", Kind: "Pod", Name: "web", UID: "uid-new", LastSeenUnix: 1},
	}
	env, _ := run(t, f, toolGet, `{"kind":"pod","namespace":"shop","name":"web"}`)
	b, _ := json.Marshal(env.Data)
	if strings.Contains(string(b), "OldCrash") || !strings.Contains(string(b), "NewCrash") {
		t.Fatalf("only the fetched object's events attach: %s", b)
	}
	if strings.Contains(string(b), "CrashLoopBackOff") {
		t.Fatalf("an old UID's verdict never attaches to a replacement: %s", b)
	}

	f.rows = map[string]observe.Rows{"pods/pods": podVerdictRows("uid-new", 1, observe.Stale)}
	env, _ = run(t, f, toolGet, `{"kind":"pod","namespace":"shop","name":"web"}`)
	b, _ = json.Marshal(env)
	for _, want := range []string{`"classifier":"verdict"`, `"kind":"fault"`, `"incarnation":3`, `"partVer":7`, `"coverage":"stale"`, "generation 1; the object is now at 2", "coverage stale"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("the joined verdict carries %s: %s", want, b)
		}
	}
}

type nativeData struct{ Sync string }

func TestChatGitOpsKeepsNativeRoleAndReportsMissingParts(t *testing.T) {
	f := newFakeChat()
	f.summary = &observe.Summary{EvaluatedAt: fixedNow(), Tasks: []observe.Task{{ID: "flux"}, {ID: "argo"}}}
	f.rows = map[string]observe.Rows{"argo/applications": {Task: "argo", Incarnation: 1, EvaluatedAt: fixedNow(),
		Part: observe.Part{ID: "applications", Classifier: "native", Coverage: observe.Stale, Ver: 2},
		Rows: healthsrc.RowSet[nativeData]{Rows: []healthsrc.Row[nativeData]{{
			Ref:  healthsrc.RowRef{UID: "uid-app", GVR: "argoproj.io/v1alpha1/applications", Namespace: "argocd", Name: "shop"},
			Eval: health.NativeEval{Included: true, Role: health.Fault, Label: "Degraded"},
			Data: nativeData{Sync: "OutOfSync"},
		}}}}}
	env, _ := run(t, f, toolGitOps, `{}`)
	b, _ := json.Marshal(env)
	for _, want := range []string{`"role":"fault"`, `"includedInAttention":true`, `"label":"Degraded"`, "flux/reconciliations: Klyx's rows are not available", "argo/applications: coverage stale"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("gitops keeps %s: %s", want, b)
		}
	}
	if env.Complete || env.Total == nil || env.Total.Exact {
		t.Fatalf("a missing or stale part is never complete: %+v", env)
	}
}

func TestChatAttentionNeverClaimsExactAbsenceFromUnreadableParts(t *testing.T) {
	f := newFakeChat()
	f.summary = &observe.Summary{EvaluatedAt: fixedNow(), Tasks: []observe.Task{{ID: "pods", Parts: []observe.Part{{ID: "pods", Classifier: "verdict", Coverage: observe.Unreadable}}}}}
	env, _ := run(t, f, toolAttention, `{"area":"pods"}`)
	if env.Complete || env.Total == nil || env.Total.Exact || len(env.Errors) == 0 || !strings.Contains(env.Errors[0], "unreadable") {
		t.Fatalf("an unreadable part's zero is not proof of absence: %+v", env)
	}
}

// ---- Codex review of 4456049 ----------------------------------------------------

func TestChatScrubsComponentErrorsAndReplicaNames(t *testing.T) {
	f := newFakeChat()
	f.objects[key(podsGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "u"}})
	f.eventsErr = errors.New(`Get "https://owner:` + sentArbitrary + `@events.invalid/api"`)
	res := newChatTools(f, fixedNow).Run(context.Background(), toolGet, json.RawMessage(`{"kind":"pod","namespace":"shop","name":"web"}`))
	if strings.Contains(string(res.JSON), sentArbitrary) || res.Read.Redacted == 0 {
		t.Fatalf("a component error beside a successful read is scrubbed and counted: %s", res.JSON)
	}
	o := chatProject(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}, uobj(map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "a", "namespace": "argocd", "uid": "u"},
		"spec": map[string]any{"source": map[string]any{"kustomize": map[string]any{"replicas": []any{map[string]any{"name": "password=" + sentArbitrary, "count": int64(2)}}}}},
	}))
	mustNotLeak(t, "kustomize replicas", o, sentArbitrary)
}

func TestChatOverviewKeepsSnapshotProvenance(t *testing.T) {
	f := newFakeChat()
	readAt := fixedNow().Add(-90 * time.Second)
	ev := 500
	f.summary = &observe.Summary{EvaluatedAt: fixedNow(), Incarnation: 4, Seq: 9, Tasks: []observe.Task{{ID: "events", Parts: []observe.Part{{
		ID: "warnings", Classifier: "evidence", Coverage: observe.Live, Ver: 12, Evidence: &ev,
		Kinds: []observe.KindCoverage{{Kind: "Event", Role: "counted", Coverage: observe.Live, Objects: 500, Truncated: true, AsOf: &readAt, CadenceSec: 60}},
	}}}}}
	env, _ := run(t, f, toolOverview, `{}`)
	b, _ := json.Marshal(env)
	for _, want := range []string{`"evidence":500`, `"truncated":true`, `"cadenceSec":60`, `"asOf":"` + readAt.UTC().Format(time.RFC3339) + `"`, `"ver":12`, `"incarnation":4`, "capped snapshot of 500 objects - a sample"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("the overview keeps %s: %s", want, b)
		}
	}
	if env.Complete {
		t.Fatal("a capped snapshot is never complete")
	}
}
