package appbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/depgraph"
	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
	"github.com/moomora/klyx/internal/observe"
)

// AI A1 §3.3 and §8: get_dependencies maps each drawing to typed nodes and
// edges, holds a UID only where the drawing read one, attaches verdicts by
// UID, keeps the drawing's gaps, and never sends a drawing's messages.

func depsData(t *testing.T, env chatEnvelope) chatDepsData {
	t.Helper()
	var d chatDepsData
	b, _ := json.Marshal(env.Data)
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// edgesText is "from-kind/name -type-> to-kind/name" per edge.
func edgesText(d chatDepsData) string {
	by := map[string]string{}
	for _, n := range d.Nodes {
		by[n.ID] = n.Kind + "/" + n.Name
	}
	var out []string
	for _, e := range d.Edges {
		s := by[e.From] + " -" + e.Type + "-> " + by[e.To]
		if e.Note != "" {
			s += " (" + e.Note + ")"
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

func nodeOf(d chatDepsData, kind, name string) *chatDepNode {
	for i := range d.Nodes {
		if d.Nodes[i].Kind == kind && d.Nodes[i].Name == name {
			return &d.Nodes[i]
		}
	}
	return nil
}

func TestDepsFluxGraph(t *testing.T) {
	f := unitChat(unitObj("x"))
	f.kinds["kustomization"] = []fleet.ChatKind{{GVR: ksGVR, Kind: "Kustomization", Namespaced: true}}
	ref := func(kind, name string) depgraph.Ref {
		return depgraph.Ref{Kind: kind, Namespace: "flux-system", Name: name}
	}
	f.graph = depgraph.Graph{
		Nodes: []depgraph.Node{
			{ID: "Kustomization/flux-system/app", Ref: ref("Kustomization", "app"), UID: "uid-ks", Ready: "False", Reason: "DependencyNotReady", Message: "NeedleDepMsg"},
			{ID: "Kustomization/flux-system/infra", Ref: ref("Kustomization", "infra"), UID: "uid-infra", Ready: "False", Reason: "HealthCheckFailed", Suspended: true},
			{ID: "GitRepository/flux-system/fleet", Ref: ref("GitRepository", "fleet"), UID: "uid-g", Ready: "True"},
			{ID: "summary", Stub: depgraph.StubReads, Note: "2 more"},
		},
		Edges: []depgraph.Edge{
			{From: "Kustomization/flux-system/app", To: "Kustomization/flux-system/infra", Type: depgraph.EdgeDependsOn, Reported: true},
			{From: "Kustomization/flux-system/infra", To: "GitRepository/flux-system/fleet", Type: depgraph.EdgeSource},
			{From: "Kustomization/flux-system/infra", To: "summary", Type: depgraph.EdgeDependsOn},
		},
		Incomplete: []string{"the read limit stopped the walk"},
	}
	env, read := run(t, f, toolDeps, `{"kind":"kustomization","namespace":"flux-system","name":"app"}`)
	b, _ := json.Marshal(env)
	if strings.Contains(string(b), "NeedleDepMsg") {
		t.Fatalf("a drawing's messages never cross: %s", b)
	}
	d := depsData(t, env)
	if len(f.graphUIDs) != 1 || f.graphUIDs[0] != "uid-ks" || len(d.Nodes) != 4 || env.Complete || d.Incomplete[0] != "the read limit stopped the walk" {
		t.Fatalf("the walk starts at the unit by its UID, its gaps kept: %v %+v", f.graphUIDs, d)
	}
	if got := edgesText(d); got != "Kustomization/app -dependsOn-> Kustomization/infra (the downstream controller reports this edge blocks it); Kustomization/infra -source-> GitRepository/fleet; Kustomization/infra -dependsOn-> (summary)/" {
		t.Fatalf("%s", got)
	}
	app, infra, sum := nodeOf(d, "Kustomization", "app"), nodeOf(d, "Kustomization", "infra"), nodeOf(d, "(summary)", "")
	if app.UID != "uid-ks" || app.Identity != identityUID || app.State != "Ready=False, reason DependencyNotReady" || app.Verdict == nil || app.Verdict.Label != "HealthCheckFailed" ||
		infra.State != "Ready=False, reason HealthCheckFailed; suspended" || sum.UID != "" || sum.Identity != identityName || sum.State != "evidence stops: read limit (2 more)" {
		t.Fatalf("%+v %+v %+v", app, infra, sum)
	}
	if len(read.Refs) != 2 || read.Refs[0].GVR != "kustomize.toolkit.fluxcd.io/v1/kustomizations" {
		t.Fatalf("references for the nodes held by UID of a kind Klyx classifies, each a whole GVR: %+v", read.Refs)
	}
}

func TestDepsWorkloadOwnerChainAndPods(t *testing.T) {
	f := alertsChat(observe.KindCoverage{})
	widgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	f.kinds["widget"] = []fleet.ChatKind{{GVR: widgets, Kind: "Widget", Namespaced: true}}
	dep := f.objects[key(deployGVR, "shop", "web")]
	_ = unstructured.SetNestedSlice(dep.Object, []any{map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "name": "w", "uid": "uid-w", "controller": true}}, "metadata", "ownerReferences")
	f.objects[key(widgets, "shop", "w")] = uobj(map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]any{"name": "w", "namespace": "shop", "uid": "uid-w"}})
	f.rows[healthsrc.TaskWorkloads+"/workloads"] = workloadRows("uid-d", "Degraded")
	env, _ := run(t, f, toolDeps, `{"kind":"deployment","namespace":"shop","name":"web"}`)
	d := depsData(t, env)
	if got := edgesText(d); got != "Deployment/web -owner-> Widget/w; ReplicaSet/web-1 -owner-> Deployment/web; Pod/web-1-a -owner-> ReplicaSet/web-1" {
		t.Fatalf("the owner chain up, and the ReplicaSets and pods by controller UID: %s", got)
	}
	if n := nodeOf(d, "Widget", "w"); n.UID != "uid-w" || nodeOf(d, "Deployment", "web").Verdict.Label != "Degraded" || !env.Complete {
		t.Fatalf("%+v", d)
	}
	// an owner replaced since its reference was written is said by name
	f.objects[key(widgets, "shop", "w")] = uobj(map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]any{"name": "w", "namespace": "shop", "uid": "uid-w2"}})
	env, _ = run(t, f, toolDeps, `{"kind":"deployment","namespace":"shop","name":"web"}`)
	d = depsData(t, env)
	if n := nodeOf(d, "Widget", "w"); n.UID != "" || n.Identity != identityName || env.Complete || !strings.Contains(d.Incomplete[0], "could not be confirmed by its UID") {
		t.Fatalf("%+v %+v", n, d.Incomplete)
	}
}

func TestDepsIssuanceChain(t *testing.T) {
	f := newFakeChat()
	f.kinds["certificate"] = f.kinds["certificate"][:1]
	f.chain = &certmanager.Chain{
		Certificate: certmanager.Certificate{UID: "uid-c", Namespace: "shop", Name: "web-tls", Ready: certmanager.Issuing, Reason: "Issuing"},
		Issuer:      &certmanager.Issuer{UID: "uid-i", Kind: certmanager.ClusterIssuerKind, Name: "letsencrypt", Ready: certmanager.Ready},
		Requests: []certmanager.ChainNode{{Kind: certmanager.CertificateRequestKind, Namespace: "shop", Name: "web-tls-1", Ready: certmanager.Failed, Message: "NeedleChainMsg",
			Children: []certmanager.ChainNode{{Kind: certmanager.OrderKind, Namespace: "shop", Name: "web-tls-1-o", Children: []certmanager.ChainNode{
				{Kind: certmanager.ChallengeKind, Namespace: "shop", Name: "web-tls-1-ch", Reason: "pending"}}}}}},
		Warnings: []string{"orders could not be listed"},
	}
	f.chain.FailingLink = &f.chain.Requests[0].Children[0].Children[0]
	env, _ := run(t, f, toolDeps, `{"kind":"certificate","namespace":"shop","name":"web-tls"}`)
	b, _ := json.Marshal(env)
	d := depsData(t, env)
	if strings.Contains(string(b), "NeedleChainMsg") || env.Complete || d.Incomplete[0] != "orders could not be listed" {
		t.Fatalf("%s", b)
	}
	if got := edgesText(d); got != "Certificate/web-tls -issuer-> ClusterIssuer/letsencrypt; CertificateRequest/web-tls-1 -owner-> Certificate/web-tls; Order/web-tls-1-o -owner-> CertificateRequest/web-tls-1; Challenge/web-tls-1-ch -owner-> Order/web-tls-1-o" {
		t.Fatalf("%s", got)
	}
	if nodeOf(d, "ClusterIssuer", "letsencrypt").UID != "uid-i" || nodeOf(d, "Order", "web-tls-1-o").Identity != identityName ||
		!strings.HasSuffix(nodeOf(d, "Challenge", "web-tls-1-ch").State, "the deepest stuck link") {
		t.Fatalf("%+v", d.Nodes)
	}
}

func TestDepsGatewayAndRoute(t *testing.T) {
	f := routeChat("u-r")
	f.kinds["gateway"] = []fleet.ChatKind{{GVR: schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}, Kind: "Gateway", Namespaced: true}}
	f.topo = &gwapi.Topology{Gateway: gwapi.GatewayNode{Namespace: "infra", Name: "gw", Accepted: true, Programmed: true},
		Routes:      []gwapi.RouteNode{{Namespace: "shop", Name: "web", UID: "u-r", Accepted: true, Backends: []gwapi.Backend{{Name: "svc-a", Rule: 1}}}},
		RoutesError: "forbidden: list httproutes"}
	env, _ := run(t, f, toolDeps, `{"kind":"gateway","namespace":"infra","name":"gw"}`)
	d := depsData(t, env)
	if got := edgesText(d); got != "HTTPRoute/web -route-> Gateway/gw; HTTPRoute/web -backendRef-> Service/svc-a (rule 1)" || nodeOf(d, "Service", "svc-a").Namespace != "shop" ||
		env.Complete || !strings.Contains(d.Incomplete[0], "forbidden") || nodeOf(d, "HTTPRoute", "web").UID != "u-r" {
		t.Fatalf("%s %+v", edgesText(d), d)
	}
	route := f.objects[key(routesGVR, "shop", "web")]
	_ = unstructured.SetNestedSlice(route.Object, []any{map[string]any{"backendRefs": []any{map[string]any{"name": "svc-b", "port": int64(80)}}}}, "spec", "rules")
	env, _ = run(t, f, toolDeps, `{"kind":"httproute","namespace":"shop","name":"web"}`)
	d = depsData(t, env)
	if got := edgesText(d); got != "HTTPRoute/web -route-> Gateway/gw; HTTPRoute/web -route-> Gateway/gw2; HTTPRoute/web -backendRef-> Service/svc-b (rule 0)" {
		t.Fatalf("parents and backends as the spec names them: %s", got)
	}
}

func TestDepsScaledObjectAndHPA(t *testing.T) {
	f := newFakeChat()
	sos := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	hpas := schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
	f.kinds["scaledobject"] = []fleet.ChatKind{{GVR: sos, Kind: "ScaledObject", Namespaced: true}}
	f.kinds["hpa"] = []fleet.ChatKind{{GVR: hpas, Kind: "HorizontalPodAutoscaler", Namespaced: true}}
	f.rows = map[string]observe.Rows{healthsrc.TaskKEDA + "/scaledobjects": {Task: healthsrc.TaskKEDA, Part: observe.Part{ID: "scaledobjects", Classifier: "native", Coverage: observe.Live},
		Rows: healthsrc.RowSet[healthsrc.ScaledObjectRow]{Rows: []healthsrc.Row[healthsrc.ScaledObjectRow]{{
			Ref:  healthsrc.RowRef{UID: "u-so", GVR: "keda.sh/v1alpha1/scaledobjects", Namespace: "shop", Name: "web"},
			Eval: health.NativeEval{Included: true, Role: health.Fault, Label: "Fallback"},
			Data: healthsrc.ScaledObjectRow{ScaledObject: &keda.ScaledObject{TargetName: "web"}, HPA: &hpa.HPA{Name: "keda-hpa-web", UID: "u-h"}},
		}}}}}
	env, _ := run(t, f, toolDeps, `{"kind":"scaledobject","namespace":"shop","name":"web"}`)
	d := depsData(t, env)
	if got := edgesText(d); got != "ScaledObject/web -scaler-> Deployment/web; HorizontalPodAutoscaler/keda-hpa-web -owner-> ScaledObject/web" ||
		nodeOf(d, "ScaledObject", "web").Verdict.Label != "Fallback" || nodeOf(d, "HorizontalPodAutoscaler", "keda-hpa-web").State != "the HPA as last read; not current" {
		t.Fatalf("%s %+v", edgesText(d), d.Nodes)
	}
	f.objects[key(hpas, "shop", "keda-hpa-web")] = uobj(map[string]any{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler",
		"metadata": map[string]any{"name": "keda-hpa-web", "namespace": "shop", "uid": "u-h",
			"ownerReferences": []any{map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject", "name": "web", "uid": "u-so", "controller": true}}},
		"spec": map[string]any{"scaleTargetRef": map[string]any{"kind": "Deployment", "name": "web"}}})
	env, _ = run(t, f, toolDeps, `{"kind":"hpa","namespace":"shop","name":"keda-hpa-web"}`)
	if got := edgesText(depsData(t, env)); got != "HorizontalPodAutoscaler/keda-hpa-web -scaler-> Deployment/web; HorizontalPodAutoscaler/keda-hpa-web -owner-> ScaledObject/web" {
		t.Fatalf("%s", got)
	}
}

func TestDepsBoundAndRefusal(t *testing.T) {
	f := unitChat(unitObj("x"))
	f.kinds["kustomization"] = []fleet.ChatKind{{GVR: ksGVR, Kind: "Kustomization", Namespaced: true}}
	for i := 0; i < 50; i++ {
		f.graph.Nodes = append(f.graph.Nodes, depgraph.Node{ID: fmt.Sprintf("n%d", i), Ref: depgraph.Ref{Kind: "Kustomization", Namespace: "flux-system", Name: fmt.Sprintf("k%02d", i)}, UID: fmt.Sprintf("u%d", i)})
		if i > 0 {
			f.graph.Edges = append(f.graph.Edges, depgraph.Edge{From: fmt.Sprintf("n%d", i-1), To: fmt.Sprintf("n%d", i), Type: depgraph.EdgeDependsOn})
		}
	}
	env, _ := run(t, f, toolDeps, `{"kind":"kustomization","namespace":"flux-system","name":"app"}`)
	d := depsData(t, env)
	if len(d.Nodes) != chatDepNodesMax || d.NodesTotal != 50 || env.Complete || len(d.Edges) != chatDepNodesMax-1 || !strings.Contains(d.Note, "50 nodes; the first 40") {
		t.Fatalf("%d nodes of %d, %d edges", len(d.Nodes), d.NodesTotal, len(d.Edges))
	}
	if shrunk := shrink(d).(chatDepsData); len(shrunk.Nodes) != 20 || len(shrunk.Edges) != 19 {
		t.Fatalf("a shrink keeps only edges between kept nodes: %d %d", len(shrunk.Nodes), len(shrunk.Edges))
	}
	f.kinds["service"] = []fleet.ChatKind{{GVR: schema.GroupVersionResource{Version: "v1", Resource: "services"}, Kind: "Service", Namespaced: true}}
	if env, read := run(t, f, toolDeps, `{"kind":"service","namespace":"shop","name":"web"}`); read.Outcome != "failed" || !strings.Contains(env.Errors[0], "draws no dependencies for a Service") {
		t.Fatalf("%+v", env)
	}
}
