package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
)

type fakeCRDConn struct {
	infos      []crd.Info
	counts     map[string]int
	instances  []crd.InstanceMeta
	nextToken  string
	detail     crd.InstanceDetail
	secretData map[string]string // key -> decoded value, for RevealSecretKey tests
	apply      applyCall
	applyErr   error
	ownership  fleet.GitOpsOwnership

	createYAML    string
	createDryRun  bool
	createResults []fleet.AdhocResult

	adhocObjects   []fleet.AdhocObject
	adhocDeleted   []string
	adhocDeleteErr error
}

type applyCall struct {
	group, version, plural, kind, scope, ns, name, yaml string
	dryRun                                              bool
}

func (f *fakeCRDConn) ListCRDs(ctx context.Context) ([]crd.Info, error) { return f.infos, nil }
func (f *fakeCRDConn) CountResource(ctx context.Context, group, version, plural string) (int, bool, error) {
	n, ok := f.counts[plural]
	if !ok {
		return 0, false, nil
	}
	return n, n >= crd.Cap, nil
}
func (f *fakeCRDConn) ListInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	return f.instances, f.nextToken, nil
}
func (f *fakeCRDConn) GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error) {
	return f.detail, nil
}
func (f *fakeCRDConn) ApplyResourceYAML(ctx context.Context, group, version, plural, kind, scope, ns, name, yaml string, dryRun bool) error {
	f.apply = applyCall{group: group, version: version, plural: plural, kind: kind, scope: scope, ns: ns, name: name, yaml: yaml, dryRun: dryRun}
	return f.applyErr
}
func (f *fakeCRDConn) GitOpsOwnership(context.Context, string, string, string, string) fleet.GitOpsOwnership {
	return f.ownership
}

func (f *fakeCRDConn) CreateResourceYAML(_ context.Context, yamlDocs string, dryRun bool) []fleet.AdhocResult {
	f.createYAML, f.createDryRun = yamlDocs, dryRun
	return f.createResults
}

func (f *fakeCRDConn) ListAdhocObjects(context.Context) []fleet.AdhocObject { return f.adhocObjects }

func (f *fakeCRDConn) DeleteAdhocObject(_ context.Context, group, version, plural, ns, name string) error {
	f.adhocDeleted = append(f.adhocDeleted, plural+"/"+ns+"/"+name)
	return f.adhocDeleteErr
}

func (f *fakeCRDConn) RevealSecretKey(ctx context.Context, ns, name, key string) (string, error) {
	if f.secretData == nil {
		return "", errors.New("key not found")
	}
	v, ok := f.secretData[key]
	if !ok {
		return "", errors.New("key not found")
	}
	return v, nil
}

func TestGetInstanceDetailCarriesMeterPct(t *testing.T) {
	// Regression: the meter Fact's Pct must survive the crd.Fact -> appbridge
	// FactDTO remap, or every quota bar renders at 0% fill next to correct text.
	conn := &fakeCRDConn{detail: crd.InstanceDetail{
		Facts: []crd.Fact{
			{Group: "Quota", Label: "requests.cpu", Value: "3 / 4 (75%)", Tone: "warning", Kind: "meter", Pct: 75},
			{Group: "Quota", Label: "scopes", Value: "NotTerminating", Kind: "row"},
		},
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	dto := svc.GetInstanceDetail("x", "", "v1", "resourcequotas", "team", "compute")
	var meter *FactDTO
	for i := range dto.Facts {
		if dto.Facts[i].Kind == "meter" {
			meter = &dto.Facts[i]
		}
	}
	if meter == nil {
		t.Fatalf("meter fact missing from DTO: %+v", dto.Facts)
	}
	if meter.Pct != 75 {
		t.Fatalf("Pct dropped at the bridge boundary: got %v, want 75", meter.Pct)
	}
}

func TestCreateResourceYAMLMapsToDTO(t *testing.T) {
	conn := &fakeCRDConn{createResults: []fleet.AdhocResult{
		{Kind: "Pod", Namespace: "scratch", Name: "dbg", OK: true,
			NamespaceOwnerKind: "Kustomization", NamespaceOwnerName: "web", NamespacePrunes: true},
		{Kind: "ConfigMap", Name: "cfg", Error: "set metadata.namespace explicitly"},
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	out := svc.CreateResourceYAML("x", "apiVersion: v1\nkind: Pod\n", true)
	if !conn.createDryRun || conn.createYAML == "" {
		t.Fatalf("args not forwarded: dryRun=%v yaml=%q", conn.createDryRun, conn.createYAML)
	}
	if len(out) != 2 || !out[0].OK || out[0].NamespaceOwnerKind != "Kustomization" || !out[0].NamespacePrunes {
		t.Fatalf("per-doc results not mapped: %+v", out)
	}
	if out[1].OK || out[1].Error == "" {
		t.Fatalf("error doc must carry its error: %+v", out[1])
	}

	// Cluster miss folds into a single error result.
	miss := NewCRDService(func(string) (CRDConn, bool) { return nil, false })
	if r := miss.CreateResourceYAML("nope", "x", true); len(r) != 1 || r[0].Error == "" {
		t.Fatalf("cluster miss must fold into an error result: %+v", r)
	}
}

func TestAdhocListAndDelete(t *testing.T) {
	conn := &fakeCRDConn{adhocObjects: []fleet.AdhocObject{
		{Kind: "Pod", Version: "v1", Plural: "pods", Scope: "Namespaced", Namespace: "klyx-scratch", Name: "dbg", AgeSeconds: 60},
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	objs := svc.ListAdhocObjects("x")
	if len(objs) != 1 || objs[0].Name != "dbg" || objs[0].AgeSeconds != 60 {
		t.Fatalf("list not mapped: %+v", objs)
	}

	r := svc.DeleteAdhocObject("x", "", "v1", "pods", "klyx-scratch", "dbg")
	if !r.OK || len(conn.adhocDeleted) != 1 || conn.adhocDeleted[0] != "pods/klyx-scratch/dbg" {
		t.Fatalf("delete not forwarded: %+v %v", r, conn.adhocDeleted)
	}

	conn.adhocDeleteErr = errors.New("not a Klyx ad-hoc object")
	if r := svc.DeleteAdhocObject("x", "", "v1", "pods", "prod", "app"); r.OK || r.Error == "" {
		t.Fatalf("guard refusal must surface: %+v", r)
	}
}

func TestGitOpsOwnershipMapsToDTO(t *testing.T) {
	conn := &fakeCRDConn{ownership: fleet.GitOpsOwnership{
		OwnerKind: "Kustomization", OwnerNamespace: "flux-system", OwnerName: "apps",
		OwnerPrunes: true, Basis: "flux-inventory",
		NamespaceOwnerKind: "Kustomization", NamespaceOwnerName: "apps", NamespacePrunes: true, NamespaceBasis: "flux",
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	dto := svc.GitOpsOwnership("x", "apps", "Deployment", "web", "api")
	if dto.OwnerKind != "Kustomization" || dto.OwnerName != "apps" || !dto.OwnerPrunes || dto.Basis != "flux-inventory" {
		t.Fatalf("owner fields not mapped: %+v", dto)
	}
	if dto.NamespaceOwnerKind != "Kustomization" || !dto.NamespacePrunes {
		t.Fatalf("namespace fields not mapped: %+v", dto)
	}
	if dto.Warnings == nil {
		t.Fatal("warnings must be non-nil ([] not null) for clean JSON")
	}

	// Cluster miss degrades to a warning, never a silent empty verdict.
	miss := NewCRDService(func(string) (CRDConn, bool) { return nil, false })
	if d := miss.GitOpsOwnership("nope", "", "ConfigMap", "ns", "n"); len(d.Warnings) == 0 {
		t.Fatalf("cluster miss must warn: %+v", d)
	}
}

func TestApplyResourceYAMLMapsToConn(t *testing.T) {
	conn := &fakeCRDConn{}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	r := svc.ApplyResourceYAML("x", "", "v1", "configmaps", "ConfigMap", "Namespaced", "default", "app", "kind: ConfigMap\n", true)

	if !r.OK || r.Error != "" {
		t.Fatalf("apply result: %+v", r)
	}
	if conn.apply.group != "" || conn.apply.version != "v1" || conn.apply.plural != "configmaps" || conn.apply.kind != "ConfigMap" {
		t.Fatalf("resource args not forwarded: %+v", conn.apply)
	}
	if conn.apply.ns != "default" || conn.apply.name != "app" || conn.apply.yaml != "kind: ConfigMap\n" || !conn.apply.dryRun {
		t.Fatalf("object args not forwarded: %+v", conn.apply)
	}
}

func TestListCRDsGroupsAndAttributes(t *testing.T) {
	conn := &fakeCRDConn{infos: []crd.Info{
		{Group: "cilium.io", Kind: "CiliumNode", Plural: "ciliumnodes", Scope: "Cluster", Version: "v2", Operator: "cilium"},
		{Group: "cilium.io", Kind: "CiliumEndpoint", Plural: "ciliumendpoints", Scope: "Namespaced", Version: "v2", Operator: "cilium"},
		{Group: "cert-manager.io", Kind: "Certificate", Plural: "certificates", Scope: "Namespaced", Version: "v1", Operator: "cert-manager"},
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	groups := svc.ListCRDs("x")
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if groups[0].Group != "cert-manager.io" || groups[1].Group != "cilium.io" {
		t.Fatalf("group order: %s, %s", groups[0].Group, groups[1].Group)
	}
	if groups[1].Category != "CNI" {
		t.Fatalf("cilium category: %q", groups[1].Category)
	}
	if groups[1].Kinds[0].Kind != "CiliumEndpoint" || groups[1].Kinds[1].Kind != "CiliumNode" {
		t.Fatalf("kind order: %+v", groups[1].Kinds)
	}
}

func TestListCRDsUnknownClusterEmpty(t *testing.T) {
	svc := NewCRDService(func(string) (CRDConn, bool) { return nil, false })
	if g := svc.ListCRDs("ghost"); len(g) != 0 {
		t.Fatalf("want empty, got %d", len(g))
	}
}

func TestListInstancesMapsDTO(t *testing.T) {
	created := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	conn := &fakeCRDConn{
		instances: []crd.InstanceMeta{
			{Namespace: "team-a", Name: "w1", Created: created, Fields: map[string]string{"type": "LoadBalancer"}},
			{Namespace: "", Name: "cluster-scoped", Created: time.Time{}},
		},
		nextToken: "tok-2",
	}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	page := svc.ListInstances("x", "example.com", "v1", "widgets", "")
	if page.NextToken != "tok-2" {
		t.Fatalf("nextToken: %q", page.NextToken)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items: %d", len(page.Items))
	}
	if page.Items[0].Created != "2026-06-01T09:00:00Z" {
		t.Fatalf("created RFC3339: %q", page.Items[0].Created)
	}
	if page.Items[1].Created != "" {
		t.Fatalf("zero time must map to empty string, got %q", page.Items[1].Created)
	}
	if page.Items[0].Fields["type"] != "LoadBalancer" {
		t.Fatalf("fields: %+v", page.Items[0].Fields)
	}
}

func TestListInstancesUnknownClusterEmpty(t *testing.T) {
	svc := NewCRDService(func(string) (CRDConn, bool) { return nil, false })
	if p := svc.ListInstances("ghost", "g", "v", "p", ""); len(p.Items) != 0 || p.NextToken != "" {
		t.Fatalf("want empty page, got %+v", p)
	}
}

func TestGetInstanceDetailMapsDTO(t *testing.T) {
	created := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	last := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	conn := &fakeCRDConn{detail: crd.InstanceDetail{
		Kind: "Widget", Namespace: "team-a", Name: "w1", Created: created,
		Labels:      map[string]string{"app": "w"},
		Annotations: map[string]string{"owner": "platform"},
		Conditions:  []crd.Condition{{Type: "Ready", Status: "True", Reason: "OK", Message: "ready"}},
		Events:      []crd.Event{{Type: "Warning", Reason: "Failed", Message: "boom", Count: 2, Last: last}},
		YAML:        "kind: Widget\n",
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	d := svc.GetInstanceDetail("x", "example.com", "v1", "widgets", "team-a", "w1")
	if d.Kind != "Widget" || d.Created != "2026-06-01T09:00:00Z" {
		t.Fatalf("header: %+v", d)
	}
	if len(d.Conditions) != 1 || d.Conditions[0].Type != "Ready" {
		t.Fatalf("conditions: %+v", d.Conditions)
	}
	if len(d.Events) != 1 || d.Events[0].Count != 2 || d.Events[0].LastSeen != "2026-06-02T10:00:00Z" {
		t.Fatalf("events: %+v", d.Events)
	}
	if d.YAML != "kind: Widget\n" || d.Labels["app"] != "w" || d.Annotations["owner"] != "platform" {
		t.Fatalf("yaml/metadata: %+v", d)
	}
}

func TestGetInstanceDetailUnknownClusterEmpty(t *testing.T) {
	svc := NewCRDService(func(string) (CRDConn, bool) { return nil, false })
	if d := svc.GetInstanceDetail("ghost", "g", "v", "p", "n", "x"); d.Kind != "" || len(d.Conditions) != 0 {
		t.Fatalf("want empty, got %+v", d)
	}
}

func TestRevealSecretKeySuccess(t *testing.T) {
	conn := &fakeCRDConn{secretData: map[string]string{"password": "hunter2"}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	r := svc.RevealSecretKey("x", "default", "app-secret", "password")
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if r.Value != "hunter2" {
		t.Fatalf("want 'hunter2', got %q", r.Value)
	}
}

func TestRevealSecretKeyMissingKey(t *testing.T) {
	conn := &fakeCRDConn{secretData: map[string]string{"token": "abc"}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	r := svc.RevealSecretKey("x", "default", "app-secret", "nonexistent")
	if r.Error == "" {
		t.Fatal("want error for missing key, got none")
	}
	if r.Value != "" {
		t.Fatalf("value must be empty on error, got %q", r.Value)
	}
}

func TestRevealSecretKeyClusterMiss(t *testing.T) {
	svc := NewCRDService(func(string) (CRDConn, bool) { return nil, false })

	r := svc.RevealSecretKey("ghost", "default", "app-secret", "password")
	if r.Error == "" {
		t.Fatal("want error on cluster miss, got none")
	}
}

func TestGetInstanceDetailSecretKeysProjected(t *testing.T) {
	conn := &fakeCRDConn{detail: crd.InstanceDetail{
		Kind:       "Secret",
		Namespace:  "default",
		Name:       "app-secret",
		SecretKeys: []crd.SecretKeyInfo{{Key: "password", Bytes: 7}, {Key: "token", Bytes: 3}},
		YAML:       "kind: Secret\n",
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	d := svc.GetInstanceDetail("x", "", "v1", "secrets", "default", "app-secret")
	if len(d.SecretKeys) != 2 {
		t.Fatalf("want 2 SecretKeys, got %d", len(d.SecretKeys))
	}
	if d.SecretKeys[0].Key != "password" || d.SecretKeys[0].Bytes != 7 {
		t.Fatalf("SecretKeys[0]: %+v", d.SecretKeys[0])
	}
}

func TestGetInstanceDetailServiceBackingProjected(t *testing.T) {
	backing := &crd.ServiceBacking{
		Ports:    []crd.ServicePort{{Name: "http", Port: 80, Protocol: "TCP"}},
		Ready:    2,
		NotReady: 1,
		Addresses: []crd.EndpointAddr{
			{IP: "10.0.0.1", Ready: true, TargetKind: "Pod", TargetName: "web-pod-1"},
			{IP: "10.0.0.2", Ready: true, TargetKind: "Pod", TargetName: "web-pod-2"},
			{IP: "10.0.0.3", Ready: false, TargetKind: "Pod", TargetName: "web-pod-3"},
		},
		Selector: map[string]string{"app": "web"},
	}
	conn := &fakeCRDConn{detail: crd.InstanceDetail{
		Kind:           "Service",
		Namespace:      "default",
		Name:           "web",
		YAML:           "kind: Service\n",
		ServiceBacking: backing,
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	d := svc.GetInstanceDetail("x", "", "v1", "services", "default", "web")
	if d.ServiceBacking == nil {
		t.Fatal("ServiceBacking must be projected to DTO")
	}
	b := d.ServiceBacking
	if b.Ready != 2 || b.NotReady != 1 {
		t.Fatalf("counts: ready=%d notReady=%d", b.Ready, b.NotReady)
	}
	if len(b.Ports) != 1 || b.Ports[0].Port != 80 || b.Ports[0].Protocol != "TCP" {
		t.Fatalf("ports: %+v", b.Ports)
	}
	if len(b.Addresses) != 3 || b.Addresses[0].IP != "10.0.0.1" || !b.Addresses[0].Ready {
		t.Fatalf("addresses: %+v", b.Addresses)
	}
	if b.Addresses[2].TargetKind != "Pod" || b.Addresses[2].TargetName != "web-pod-3" {
		t.Fatalf("target ref: %+v", b.Addresses[2])
	}
	if b.Selector["app"] != "web" {
		t.Fatalf("selector: %+v", b.Selector)
	}
}

func TestGetInstanceDetailNilServiceBackingOmitted(t *testing.T) {
	conn := &fakeCRDConn{detail: crd.InstanceDetail{
		Kind:           "Widget",
		Namespace:      "team-a",
		Name:           "w1",
		YAML:           "kind: Widget\n",
		ServiceBacking: nil,
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	d := svc.GetInstanceDetail("x", "example.com", "v1", "widgets", "team-a", "w1")
	if d.ServiceBacking != nil {
		t.Fatalf("ServiceBacking must be nil for non-service detail, got %+v", d.ServiceBacking)
	}
}

func TestGetInstanceDetailHPAScalingProjected(t *testing.T) {
	scaling := &crd.HPAScaling{
		MinReplicas:     2,
		MaxReplicas:     10,
		CurrentReplicas: 4,
		DesiredReplicas: 4,
		TargetKind:      "Deployment",
		TargetName:      "web",
		LastScaleUnix:   1780308000,
		Metrics: []crd.HPAMetric{
			{Name: "cpu", Type: "Resource", Target: "70%", Current: "43%"},
		},
	}
	conn := &fakeCRDConn{detail: crd.InstanceDetail{
		Kind:       "HorizontalPodAutoscaler",
		Namespace:  "default",
		Name:       "web-hpa",
		YAML:       "kind: HorizontalPodAutoscaler\n",
		HPAScaling: scaling,
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	d := svc.GetInstanceDetail("x", "autoscaling", "v2", "horizontalpodautoscalers", "default", "web-hpa")
	if d.HPAScaling == nil {
		t.Fatal("HPAScaling must be projected to DTO")
	}
	h := d.HPAScaling
	if h.MinReplicas != 2 || h.MaxReplicas != 10 {
		t.Errorf("replicas: min=%d max=%d", h.MinReplicas, h.MaxReplicas)
	}
	if h.TargetKind != "Deployment" || h.TargetName != "web" {
		t.Errorf("target: kind=%q name=%q", h.TargetKind, h.TargetName)
	}
	if h.LastScaleUnix != 1780308000 {
		t.Errorf("LastScaleUnix: want 1780308000, got %d", h.LastScaleUnix)
	}
	if len(h.Metrics) != 1 || h.Metrics[0].Name != "cpu" || h.Metrics[0].Target != "70%" || h.Metrics[0].Current != "43%" {
		t.Errorf("metrics: %+v", h.Metrics)
	}
}

func TestGetInstanceDetailNilHPAScalingOmitted(t *testing.T) {
	conn := &fakeCRDConn{detail: crd.InstanceDetail{
		Kind:       "Deployment",
		Namespace:  "default",
		Name:       "web",
		YAML:       "kind: Deployment\n",
		HPAScaling: nil,
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	d := svc.GetInstanceDetail("x", "apps", "v1", "deployments", "default", "web")
	if d.HPAScaling != nil {
		t.Fatalf("HPAScaling must be nil for non-HPA detail, got %+v", d.HPAScaling)
	}
}

func TestCountKind(t *testing.T) {
	conn := &fakeCRDConn{counts: map[string]int{"ciliumendpoints": crd.Cap, "certificates": 4}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	if c := svc.CountKind("x", "cilium.io", "v2", "ciliumendpoints"); c.Count != crd.Cap || !c.Capped {
		t.Fatalf("capped: %+v", c)
	}
	if c := svc.CountKind("x", "cert-manager.io", "v1", "certificates"); c.Count != 4 || c.Capped {
		t.Fatalf("exact: %+v", c)
	}
}
