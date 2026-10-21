package appbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/depgraph"
	"github.com/moomora/klyx/internal/healthsrc"
)

// AI A1 §3.3: get_dependencies reads the drawing Klyx already makes for the
// kind - the Flux dependency graph, a workload's owner chain and pods, a
// Certificate's issuance chain, a Gateway's topology or a route's parents
// and backends, a ScaledObject's or HPA's join - as typed nodes and edges,
// at most 40 nodes, with the drawing's own gaps kept. A node holds a UID
// only where the drawing read one (identity uid); the rest are by name.
// Klyx's verdicts attach by UID. A drawing reads inside the cluster layer:
// the turn's guard runs before it, not between its own reads.

const (
	toolDeps         = "get_dependencies"
	chatDepNodesMax  = 40
	chatOwnerHopsMax = 3
)

type chatDepNode struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	Namespace string       `json:"namespace,omitempty"`
	Name      string       `json:"name"`
	UID       string       `json:"uid,omitempty"`
	Identity  string       `json:"identity"`
	State     string       `json:"state,omitempty"` // the drawing's own reading
	Verdict   *chatVerdict `json:"verdict,omitempty"`
}

type chatDepEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"` // dependsOn | source | producer | owner | issuer | route | backendRef | scaler
	Note string `json:"note,omitempty"`
}

type chatDepsData struct {
	Drawing    string        `json:"drawing"`
	Nodes      []chatDepNode `json:"nodes"`
	NodesTotal int           `json:"nodesTotal"`
	Edges      []chatDepEdge `json:"edges"`
	Incomplete []string      `json:"incomplete,omitempty"`
	Note       string        `json:"note,omitempty"`
}

// depBuilder collects one drawing's nodes, once each, and its edges.
type depBuilder struct {
	p     *projector
	data  chatDepsData
	byKey map[string]string
}

func newDepBuilder(drawing string, p *projector) *depBuilder {
	return &depBuilder{p: p, data: chatDepsData{Drawing: drawing, Nodes: []chatDepNode{}, Edges: []chatDepEdge{}}, byKey: map[string]string{}}
}

// node adds a node, or finds it again; "" when past the bound (counted).
func (b *depBuilder) node(kind, ns, name, uid, state string) string {
	key := kind + "/" + ns + "/" + name + "/" + uid
	if id, ok := b.byKey[key]; ok {
		return id
	}
	b.data.NodesTotal++
	if len(b.data.Nodes) == chatDepNodesMax {
		b.byKey[key] = ""
		return ""
	}
	id := fmt.Sprintf("n%d", len(b.data.Nodes)+1)
	n := chatDepNode{ID: id, Kind: b.p.str(kind, chatCheckReasonMax), Namespace: b.p.str(ns, chatStringMax), Name: b.p.str(name, chatStringMax),
		UID: uid, Identity: identityUID, State: b.p.str(state, chatStringMax)}
	if uid == "" {
		n.Identity = identityName
	}
	b.data.Nodes = append(b.data.Nodes, n)
	b.byKey[key] = id
	return id
}

func (b *depBuilder) edge(from, to, typ, note string) {
	if from == "" || to == "" {
		return
	}
	b.data.Edges = append(b.data.Edges, chatDepEdge{From: from, To: to, Type: typ, Note: note})
}

func (b *depBuilder) incomplete(s string) {
	b.data.Incomplete = append(b.data.Incomplete, b.p.str(s, chatStringMax))
}

// depParts names the Observer part that classifies a drawing's kind, at the
// version a reference to it names (the kinds' stable versions).
var depParts = map[string]schema.GroupVersionResource{
	"Kustomization":           {Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"},
	"HelmRelease":             {Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
	"ExternalArtifact":        {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "externalartifacts"},
	"ArtifactGenerator":       {Group: "source.extensions.fluxcd.io", Version: "v1beta1", Resource: "artifactgenerators"},
	"Deployment":              {Group: "apps", Version: "v1", Resource: "deployments"},
	"StatefulSet":             {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"DaemonSet":               {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"Pod":                     {Version: "v1", Resource: "pods"},
	"Certificate":             {Group: "cert-manager.io", Version: "v1", Resource: "certificates"},
	"Issuer":                  {Group: "cert-manager.io", Version: "v1", Resource: "issuers"},
	"ClusterIssuer":           {Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"},
	"Gateway":                 {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
	"ScaledObject":            {Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"},
	"HorizontalPodAutoscaler": {Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
}

// verdicts attaches Klyx's verdicts to the nodes that hold a UID.
func (t *chatTools) verdicts(ctx context.Context, b *depBuilder) []string {
	parts := map[string]*chatVerdicts{}
	var problems []string
	for i := range b.data.Nodes {
		n := &b.data.Nodes[i]
		gvr, ok := depParts[n.Kind]
		if !ok || n.UID == "" {
			continue
		}
		key := gvr.Group + "/" + gvr.Resource
		vs, seen := parts[key]
		if !seen {
			vs = t.verdictsFor(ctx, gvr, &b.p.redacted)
			parts[key] = vs
			if vs != nil && vs.problem != "" {
				problems = append(problems, vs.problem)
			}
		}
		if vs == nil {
			continue
		}
		if v, ok := vs.byUID[n.UID]; ok {
			v.Kinds = nil
			n.Verdict = &v
		}
	}
	return problems
}

func (t *chatTools) deps(ctx context.Context, kind, ns, name string) ChatToolResult {
	k, err := t.resolve(ctx, kind, "")
	if err != nil {
		return t.refuse(toolDeps, kind+" "+name, err)
	}
	if k.Namespaced && ns == "" {
		return t.refuse(toolDeps, k.Kind+" "+name, fmt.Errorf("%s is namespaced: give a namespace", k.Kind))
	}
	target := k.Kind + " " + strings.TrimPrefix(ns+"/"+name, "/")
	p := &projector{}
	var b *depBuilder
	switch {
	case k.Kind == "Kustomization" || k.Kind == "HelmRelease":
		b, err = t.fluxDeps(ctx, k.Kind, ns, name, p)
	case workloadKinds[k.GVR.Group+"/"+k.Kind] && k.Kind != "ReplicaSet":
		b, err = t.workloadDeps(ctx, k, ns, name, p)
	case k.GVR.Group == "cert-manager.io" && k.Kind == "Certificate":
		b, err = t.certDeps(ctx, ns, name, p)
	case k.GVR.Group == gatewayAPIGroup && k.Kind == "Gateway":
		b, err = t.gatewayDeps(ctx, ns, name, p)
	case k.GVR.Group == gatewayAPIGroup && k.Kind == "HTTPRoute":
		b, err = t.routeDeps(ctx, k, ns, name, p)
	case k.GVR.Group == "keda.sh" && k.Kind == "ScaledObject":
		b, err = t.scaledObjectDeps(ctx, ns, name, p)
	case k.GVR.Group == "autoscaling" && k.Kind == "HorizontalPodAutoscaler":
		b, err = t.hpaDeps(ctx, k, ns, name, p)
	default:
		return t.refuse(toolDeps, target, fmt.Errorf("Klyx draws no dependencies for a %s; it does for Kustomization, HelmRelease, Deployment, StatefulSet, DaemonSet, Job, Certificate, Gateway, HTTPRoute, ScaledObject and HorizontalPodAutoscaler", k.Kind))
	}
	if err != nil {
		return t.failed(toolDeps, "api", target, err)
	}
	env := chatEnvelope{Tool: toolDeps, Source: "api", ReadAt: t.stamp(), Complete: len(b.data.Incomplete) == 0 && b.data.NodesTotal <= len(b.data.Nodes)}
	env.Errors = append(env.Errors, t.verdicts(ctx, b)...)
	if b.data.NodesTotal > len(b.data.Nodes) {
		b.data.Note = strings.TrimSpace(b.data.Note + fmt.Sprintf(" %d nodes; the first %d are listed.", b.data.NodesTotal, len(b.data.Nodes)))
	}
	env.Total = &chatTotal{Count: b.data.NodesTotal, Exact: true}
	env.Data = b.data
	var refs []ChatRef
	for _, n := range b.data.Nodes {
		if gvr, ok := depParts[n.Kind]; ok && n.UID != "" {
			refs = append(refs, ChatRef{GVR: gvrString(gvr), Namespace: n.Namespace, Name: n.Name, UID: n.UID})
		}
	}
	return t.finish(env, ChatRead{Tool: toolDeps, Target: target, Outcome: "completed", Redacted: p.redacted, Refs: capRefs(refs),
		Detail: fmt.Sprintf("%s: %d nodes, %d edges", b.data.Drawing, b.data.NodesTotal, len(b.data.Edges))})
}

// fluxDeps is the Flux dependency graph, started at the unit by its UID.
func (t *chatTools) fluxDeps(ctx context.Context, kind, ns, name string, p *projector) (*depBuilder, error) {
	u, err := t.conn.FluxObject(ctx, kind, ns, name)
	if err != nil {
		return nil, err
	}
	g := t.conn.DependencyGraph(ctx, kind, ns, name, string(u.GetUID()))
	b := newDepBuilder("the Flux dependency graph", p)
	ids := map[string]string{}
	for _, n := range g.Nodes {
		state := ""
		switch {
		case n.Stub != "":
			state = "evidence stops: " + string(n.Stub)
			if n.Note != "" {
				state += " (" + n.Note + ")"
			}
		case n.Ready != "":
			state = "Ready=" + n.Ready
			if n.Reason != "" {
				state += ", reason " + n.Reason
			}
		}
		if n.Suspended {
			state = strings.TrimPrefix(state+"; suspended", "; ")
		}
		if n.Stale {
			state = strings.TrimPrefix(state+"; the controller has not observed its latest generation", "; ")
		}
		uid := n.UID
		if n.Stub != "" && n.Stub != depgraph.StubNotFound {
			uid = ""
		}
		kind := n.Ref.Kind
		if kind == "" {
			kind = "(summary)" // the walk's one summary of what its bounds stopped
		}
		ids[n.ID] = b.node(kind, n.Ref.Namespace, n.Ref.Name, uid, state)
	}
	for _, e := range g.Edges {
		var notes []string
		if e.Reported {
			notes = append(notes, "the downstream controller reports this edge blocks it")
		}
		if e.Cycle {
			notes = append(notes, "a cycle: cut here")
		}
		if e.Producer != "" {
			notes = append(notes, "producer "+e.Producer)
		}
		b.edge(ids[e.From], ids[e.To], string(e.Type), strings.Join(notes, "; "))
	}
	for _, s := range g.Incomplete {
		b.incomplete(s)
	}
	return b, nil
}

// workloadDeps is a workload's owner chain, up to three hops, each owner
// read and its UID checked, and its ReplicaSets and pods by controller UID.
func (t *chatTools) workloadDeps(ctx context.Context, k fleet.ChatKind, ns, name string, p *projector) (*depBuilder, error) {
	w, err := t.workloadPods(ctx, k, ns, name)
	if err != nil {
		return nil, err
	}
	b := newDepBuilder("the workload's owner chain and pods", p)
	self := b.node(k.Kind, ns, name, string(w.Object.GetUID()), "")
	cur, curID := w.Object, self
	for hop := 0; hop < chatOwnerHopsMax; hop++ {
		var ref *struct{ apiVersion, kind, name, uid string }
		for _, o := range cur.GetOwnerReferences() {
			if o.Controller != nil && *o.Controller {
				ref = &struct{ apiVersion, kind, name, uid string }{o.APIVersion, o.Kind, o.Name, string(o.UID)}
			}
		}
		if ref == nil {
			break
		}
		gv, _ := schema.ParseGroupVersion(ref.apiVersion)
		owner, err := t.readOwner(ctx, gv.Group, ref.kind, ns, ref.name)
		if err != nil || string(owner.GetUID()) != ref.uid {
			// the owner as named, by name: unread, or replaced since
			id := b.node(ref.kind, ns, ref.name, "", "named by the owner reference; not read as that UID")
			b.edge(curID, id, "owner", "")
			b.incomplete(fmt.Sprintf("the owner %s %s could not be confirmed by its UID", ref.kind, ref.name))
			break
		}
		id := b.node(ref.kind, ns, ref.name, ref.uid, "")
		b.edge(curID, id, "owner", "")
		cur, curID = owner, id
	}
	rsByUID := map[string]string{}
	for _, rs := range w.ReplicaSets {
		id := b.node("ReplicaSet", ns, rs.Name, rs.UID, "")
		rsByUID[rs.UID] = id
		b.edge(id, self, "owner", "")
	}
	for _, pod := range w.Pods {
		// a pod's controller is the workload or one of its ReplicaSets, by UID
		parent := self
		if id, ok := rsByUID[pod.OwnerUID]; ok {
			parent = id
		}
		b.edge(b.node("Pod", ns, pod.Name, pod.UID, ""), parent, "owner", "")
	}
	if w.Partial {
		b.incomplete("a list of the workload's ReplicaSets or pods was capped")
	}
	return b, nil
}

// readOwner reads an owner of one served kind in a group.
func (t *chatTools) readOwner(ctx context.Context, group, kind, ns, name string) (*unstructured.Unstructured, error) {
	ks, _, err := t.conn.ChatResolveKind(ctx, kind, group)
	if err != nil {
		return nil, err
	}
	for _, k := range ks {
		if k.GVR.Group == group && k.Kind == kind {
			if !k.Namespaced {
				ns = ""
			}
			return t.conn.ChatGet(ctx, k.GVR, ns, name)
		}
	}
	return nil, fmt.Errorf("%s is not served", kind)
}

// certDeps is a Certificate's issuance chain: the Certificate and its bound
// issuer by UID, its requests, orders and challenges by name.
func (t *chatTools) certDeps(ctx context.Context, ns, name string, p *projector) (*depBuilder, error) {
	ch, err := t.conn.GetCertificateChain(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	b := newDepBuilder("the issuance chain", p)
	ready := func(r certmanager.ReadyState, reason string) string {
		s := string(r)
		if reason != "" {
			s += ", reason " + reason
		}
		return s
	}
	cert := b.node("Certificate", ch.Certificate.Namespace, ch.Certificate.Name, ch.Certificate.UID, ready(ch.Certificate.Ready, ch.Certificate.Reason))
	if ch.Issuer != nil {
		iss := b.node(string(ch.Issuer.Kind), ch.Issuer.Namespace, ch.Issuer.Name, ch.Issuer.UID, ready(ch.Issuer.Ready, ch.Issuer.Reason))
		b.edge(cert, iss, "issuer", "")
	}
	var walk func(parent string, ns []certmanager.ChainNode)
	walk = func(parent string, nodes []certmanager.ChainNode) {
		for _, n := range nodes {
			state := ready(n.Ready, n.Reason)
			if ch.FailingLink != nil && ch.FailingLink.Kind == n.Kind && ch.FailingLink.Namespace == n.Namespace && ch.FailingLink.Name == n.Name {
				state += "; the deepest stuck link"
			}
			id := b.node(string(n.Kind), n.Namespace, n.Name, "", state)
			b.edge(id, parent, "owner", "")
			walk(id, n.Children)
		}
	}
	walk(cert, ch.Requests)
	for _, w := range ch.Warnings {
		b.incomplete(w)
	}
	return b, nil
}

// gatewayDeps is a Gateway's topology: its routes by UID, their backends by
// name.
func (t *chatTools) gatewayDeps(ctx context.Context, ns, name string, p *projector) (*depBuilder, error) {
	topo, err := t.conn.GetGatewayTopology(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	b := newDepBuilder("the Gateway topology", p)
	state := fmt.Sprintf("accepted %v, programmed %v", topo.Gateway.Accepted, topo.Gateway.Programmed)
	gw := b.node("Gateway", ns, name, "", state)
	for _, r := range topo.Routes {
		rid := b.node("HTTPRoute", r.Namespace, r.Name, r.UID, fmt.Sprintf("accepted %v, refs resolved %v", r.Accepted, r.ResolvedRefs))
		b.edge(rid, gw, "route", "")
		for _, be := range r.Backends {
			kind := be.Kind
			if kind == "" {
				kind = "Service"
			}
			bns := be.Namespace
			if bns == "" {
				bns = r.Namespace
			}
			b.edge(rid, b.node(kind, bns, be.Name, "", ""), "backendRef", fmt.Sprintf("rule %d", be.Rule))
		}
	}
	if !topo.RoutesComplete {
		b.incomplete("the routes could not be listed: " + topo.RoutesError)
	}
	for _, w := range topo.Warnings {
		b.incomplete(w)
	}
	return b, nil
}

// routeDeps is an HTTPRoute's parent Gateways and backends, as its spec
// names them: by name.
func (t *chatTools) routeDeps(ctx context.Context, k fleet.ChatKind, ns, name string, p *projector) (*depBuilder, error) {
	route, err := t.conn.ChatGet(ctx, k.GVR, ns, name)
	if err != nil {
		return nil, err
	}
	b := newDepBuilder("the route's parents and backends, as its spec names them", p)
	rid := b.node("HTTPRoute", ns, name, string(route.GetUID()), "")
	for _, gw := range parentGateways(route) {
		b.edge(rid, b.node("Gateway", gw.ns, gw.name, "", ""), "route", "")
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	for i, r := range rules {
		rm, _ := r.(map[string]any)
		refs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
		for _, ref := range refs {
			m, _ := ref.(map[string]any)
			kind, _ := m["kind"].(string)
			if kind == "" {
				kind = "Service"
			}
			bns, _ := m["namespace"].(string)
			if bns == "" {
				bns = ns
			}
			bname, _ := m["name"].(string)
			b.edge(rid, b.node(kind, bns, bname, "", ""), "backendRef", fmt.Sprintf("rule %d", i))
		}
	}
	b.data.Note = "Parents and backends are as the route's spec names them; whether each accepted or resolved is in its status (get_resource)."
	return b, nil
}

// scaledObjectDeps is the Observer's join: the ScaledObject by UID, its
// target by name, and the HPA it drives as read.
func (t *chatTools) scaledObjectDeps(ctx context.Context, ns, name string, p *projector) (*depBuilder, error) {
	rows, ok := t.conn.HealthRowsNow(ctx, healthsrc.TaskKEDA, "scaledobjects")
	if !ok {
		return nil, errors.New("Klyx's KEDA rows are not available")
	}
	b := newDepBuilder("KEDA's join", p)
	found := false
	forEachRow(rows.Rows, func(r capturedRow) {
		row, ok := r.data.(healthsrc.ScaledObjectRow)
		if !ok || found || r.ref.Namespace != ns || r.ref.Name != name || row.ScaledObject == nil {
			return
		}
		found = true
		so := b.node("ScaledObject", ns, name, r.ref.UID, "")
		tk := row.ScaledObject.TargetKind
		if tk == "" {
			tk = "Deployment"
		}
		b.edge(so, b.node(tk, ns, row.ScaledObject.TargetName, "", ""), "scaler", "")
		if row.HPA != nil {
			state := ""
			if !row.HPACurrent {
				state = "the HPA as last read; not current"
			}
			b.edge(b.node("HorizontalPodAutoscaler", ns, row.HPA.Name, row.HPA.UID, state), so, "owner", "")
		}
	})
	if !found {
		return nil, errors.New("not found (in Klyx's KEDA rows)")
	}
	if pr := partProblem(healthsrc.TaskKEDA, rows); pr != "" {
		b.incomplete(pr)
	}
	return b, nil
}

// hpaDeps is an HPA's scale target by name and its controlling owner by UID.
func (t *chatTools) hpaDeps(ctx context.Context, k fleet.ChatKind, ns, name string, p *projector) (*depBuilder, error) {
	u, err := t.conn.ChatGet(ctx, k.GVR, ns, name)
	if err != nil {
		return nil, err
	}
	b := newDepBuilder("the HPA's target and owner", p)
	h := b.node("HorizontalPodAutoscaler", ns, name, string(u.GetUID()), "")
	tk, _, _ := unstructured.NestedString(u.Object, "spec", "scaleTargetRef", "kind")
	tn, _, _ := unstructured.NestedString(u.Object, "spec", "scaleTargetRef", "name")
	if tn != "" {
		b.edge(h, b.node(tk, ns, tn, "", ""), "scaler", "")
	}
	for _, o := range u.GetOwnerReferences() {
		if o.Controller != nil && *o.Controller {
			b.edge(h, b.node(o.Kind, ns, o.Name, string(o.UID), "the controlling owner reference"), "owner", "")
		}
	}
	return b, nil
}
