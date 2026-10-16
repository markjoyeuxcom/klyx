package appbridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
)

type fakeCRDConn struct {
	infos     []crd.Info
	counts    map[string]int
	instances []crd.InstanceMeta
	nextToken string
	columns   []crd.Column
	// discovery fallback and definition reads (slice 15c)
	discInfos  []crd.Info
	discErr    error
	def        crd.Definition
	defErr     error
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
	adhocUnread    []string
	adhocListErr   error

	// read failures, for the error-honesty tests
	listErr, countErr, instErr, detailErr error
}

type applyCall struct {
	group, version, plural, kind, scope, ns, name, yaml string
	dryRun                                              bool
}

func (f *fakeCRDConn) ListCRDs(ctx context.Context) ([]crd.Info, error) { return f.infos, f.listErr }
func (f *fakeCRDConn) CountResource(ctx context.Context, group, version, plural string) (int, bool, error) {
	if f.countErr != nil {
		return 0, false, f.countErr
	}
	n, ok := f.counts[plural]
	if !ok {
		return 0, false, nil
	}
	return n, n >= crd.Cap, nil
}
func (f *fakeCRDConn) ListCRDsFromDiscovery(context.Context) ([]crd.Info, error) {
	return f.discInfos, f.discErr
}
func (f *fakeCRDConn) GetCRDDefinition(_ context.Context, name, version string) (crd.Definition, error) {
	return f.def, f.defErr
}
func (f *fakeCRDConn) ListInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) (crd.InstancePage, error) {
	return crd.InstancePage{Items: f.instances, Continue: f.nextToken, Columns: f.columns}, f.instErr
}
func (f *fakeCRDConn) GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error) {
	return f.detail, f.detailErr
}
func (f *fakeCRDConn) ApplyResourceYAML(ctx context.Context, group, version, plural, kind, scope, ns, name, expectedUID, yaml string, dryRun bool) error {
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

func (f *fakeCRDConn) ListAdhocObjects(context.Context) (fleet.AdhocInventory, error) {
	return fleet.AdhocInventory{Objects: f.adhocObjects, Unread: f.adhocUnread}, f.adhocListErr
}

func (f *fakeCRDConn) DeleteAdhocObject(_ context.Context, group, version, plural, ns, name, uid string) error {
	f.adhocDeleted = append(f.adhocDeleted, plural+"/"+ns+"/"+name+"@"+uid)
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
		{Kind: "Pod", Version: "v1", Plural: "pods", Scope: "Namespaced", Namespace: "klyx-scratch", Name: "dbg", UID: "u-1", AgeSeconds: 60},
	}, adhocUnread: []string{"clusterroles.rbac.authorization.k8s.io: forbidden"}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	inv := svc.ListAdhocObjects("x")
	if inv.Error != "" || len(inv.Objects) != 1 || inv.Objects[0].Name != "dbg" || inv.Objects[0].UID != "u-1" || inv.Objects[0].AgeSeconds != 60 {
		t.Fatalf("list not mapped: %+v", inv)
	}
	if len(inv.Unread) != 1 {
		t.Fatalf("unread kinds must reach the DTO: %+v", inv)
	}

	r := svc.DeleteAdhocObject("x", "", "v1", "pods", "klyx-scratch", "dbg", "u-1")
	if !r.OK || len(conn.adhocDeleted) != 1 || conn.adhocDeleted[0] != "pods/klyx-scratch/dbg@u-1" {
		t.Fatalf("delete not forwarded with its UID: %+v %v", r, conn.adhocDeleted)
	}

	conn.adhocDeleteErr = errors.New("not a Klyx ad-hoc object")
	if r := svc.DeleteAdhocObject("x", "", "v1", "pods", "prod", "app", "u-2"); r.OK || r.Error == "" {
		t.Fatalf("guard refusal must surface: %+v", r)
	}

	// A failed sweep is an error, never an empty inventory.
	conn.adhocListErr = errors.New("discovery failed: timeout")
	if inv := svc.ListAdhocObjects("x"); inv.Error == "" || len(inv.Objects) != 0 {
		t.Fatalf("sweep failure must surface: %+v", inv)
	}
	miss := NewCRDService(func(string) (CRDConn, bool) { return nil, false })
	if inv := miss.ListAdhocObjects("nope"); inv.Error == "" {
		t.Fatalf("cluster miss must surface: %+v", inv)
	}
}

func TestGitOpsOwnershipMapsToDTO(t *testing.T) {
	conn := &fakeCRDConn{ownership: fleet.GitOpsOwnership{
		OwnerKind: "Kustomization", OwnerNamespace: "flux-system", OwnerName: "apps",
		OwnerPrunes: true, Basis: "flux-inventory",
		NamespaceOwnerKind: "Kustomization", NamespaceOwnerName: "apps", NamespacePrunes: true, NamespaceBasis: "flux",
		NamespaceManaged: true,
		ArgoApps:         []fleet.ArgoAppRef{{Namespace: "argocd", Name: "shop", Prunes: true}},
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })

	dto := svc.GitOpsOwnership("x", "apps", "Deployment", "web", "api")
	if !dto.NamespaceManaged || len(dto.ArgoApps) != 1 || dto.ArgoApps[0].Name != "shop" || !dto.ArgoApps[0].Prunes {
		t.Fatalf("namespace management and reaching Argo apps not mapped: %+v", dto)
	}
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

	r := svc.ApplyResourceYAML("x", "", "v1", "configmaps", "ConfigMap", "Namespaced", "default", "app", "", "kind: ConfigMap\n", true)

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

	res := svc.ListCRDs("x")
	if res.Error != "" {
		t.Fatalf("unexpected error: %q", res.Error)
	}
	groups := res.Groups
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
	g := svc.ListCRDs("ghost")
	if len(g.Groups) != 0 || g.Groups == nil {
		t.Fatalf("want empty non-nil groups, got %v", g.Groups)
	}
	// Review 2026-10-03 #10: unreadable, never "no CRDs installed".
	if !strings.Contains(g.Error, "not connected") {
		t.Fatalf("a cluster miss must carry Error, got %q", g.Error)
	}
}

// Review 2026-10-03 #10: every CRD read carries Error when it fails, so a
// forbidden count, page or detail is never shown as zero / empty / blank.
func TestCRDReads_FailureCarriesError(t *testing.T) {
	boom := errors.New("forbidden")
	conn := &fakeCRDConn{listErr: boom, countErr: boom, instErr: boom, detailErr: boom, discErr: boom, defErr: boom}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })
	if r := svc.GetCRDDefinition("c", "things.g", ""); r.Error != "forbidden" {
		t.Errorf("GetCRDDefinition: %q", r.Error)
	}
	if r := svc.ListCRDs("c"); r.Error != "forbidden" {
		t.Errorf("ListCRDs: %q", r.Error)
	}
	if r := svc.CountKind("c", "g", "v1", "things"); r.Error != "forbidden" {
		t.Errorf("CountKind: %q", r.Error)
	}
	if r := svc.ListInstances("c", "g", "v1", "things", ""); r.Error != "forbidden" || r.Items == nil {
		t.Errorf("ListInstances: error=%q items-nil=%v", r.Error, r.Items == nil)
	}
	if r := svc.GetInstanceDetail("c", "g", "v1", "things", "ns", "x"); r.Error != "forbidden" {
		t.Errorf("GetInstanceDetail: %q", r.Error)
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

// A resource the cluster does not serve (CRD not installed) is a definitive
// zero, not an unreadable read - the Overview risk scan must not report every
// absent operator as a failure.
func TestListInstances_NotServedIsNotAnError(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "")
	conn := &fakeCRDConn{instErr: notFound}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })
	r := svc.ListInstances("c", "cert-manager.io", "v1", "certificates", "")
	if r.Error != "" || !r.NotServed || r.Items == nil || len(r.Items) != 0 {
		t.Fatalf("want NotServed with no error, got %+v", r)
	}
}

// The Line slice 9 note §4.2: an authoritative absence is told apart from
// a read that failed - an object opened by UID is removed only on NotFound.
func TestGetInstanceDetailNotFound(t *testing.T) {
	conn := &fakeCRDConn{detailErr: apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "web")}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })
	if d := svc.GetInstanceDetail("x", "", "v1", "configmaps", "shop", "web"); d.Error == "" || !d.NotFound {
		t.Fatalf("NotFound: %+v", d)
	}
	conn.detailErr = errors.New("connection refused")
	if d := svc.GetInstanceDetail("x", "", "v1", "configmaps", "shop", "web"); d.Error == "" || d.NotFound {
		t.Fatalf("another failure: %+v", d)
	}
}

// The Line slice 15c: a CRD list that fails falls back to discovery, said.
func TestListCRDs_DiscoveryFallback(t *testing.T) {
	conn := &fakeCRDConn{listErr: errors.New("customresourcedefinitions is forbidden"),
		discInfos: []crd.Info{{Group: "example.com", Kind: "Widget", Plural: "widgets", Scope: "Namespaced", Version: "v1"}}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })
	r := svc.ListCRDs("c")
	if r.Error != "" || r.Fallback != "customresourcedefinitions is forbidden" || len(r.Groups) != 1 || r.Groups[0].Kinds[0].Kind != "Widget" {
		t.Fatalf("fallback %+v", r)
	}
	// a partial discovery lists what it read and names what it could not
	conn.discErr = &fleet.PartialDiscoveryError{Failed: []string{"failed.example.com: 403 forbidden"}}
	r = svc.ListCRDs("c")
	if r.Error != "" || len(r.Groups) != 1 || len(r.FallbackMissing) != 1 || r.FallbackMissing[0] != "failed.example.com: 403 forbidden" {
		t.Fatalf("partial fallback %+v", r)
	}
	// a discovery that fails outright is the list's own error
	conn.discErr = errors.New("discovery timed out")
	if r = svc.ListCRDs("c"); r.Error != "customresourcedefinitions is forbidden" || len(r.Groups) != 0 {
		t.Fatalf("no fallback %+v", r)
	}
}

func TestGetCRDDefinition_DTO(t *testing.T) {
	conn := &fakeCRDConn{def: crd.Definition{Name: "widgets.example.com", Kind: "Widget", Versions: []crd.DefVersion{{Name: "v1", Served: true, Storage: true,
		Columns: []crd.PrinterColumn{{Name: "Ready", Type: "string", JSONPath: ".status.x", Priority: 1}}}},
		Conversion: crd.Conversion{Strategy: "Webhook", URL: "https://c.example/convert"},
		Conditions: []crd.Condition{{Type: "Established", Status: "True", ObservedGeneration: -1}}}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return conn, true })
	r := svc.GetCRDDefinition("c", "widgets.example.com", "")
	if r.Error != "" || r.Kind != "Widget" || r.Versions[0].Columns[0].Priority != 1 || r.Conversion.URL == "" || r.Conditions[0].Type != "Established" {
		t.Fatalf("dto %+v", r)
	}
	if r.Schema == nil || r.ShortNames == nil || r.StoredVersions == nil {
		t.Error("empty lists cross the bridge as [], never null")
	}
}
