package appbridge

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
)

func testConfigService(t *testing.T, ctxs []string, scanErr error) *ConfigService {
	t.Helper()
	cfg := &config.Config{Clusters: []config.ClusterConfig{
		{Name: "homelab-nelli", Context: "kubernetes-admin@homelab-nelli", Tags: map[string]string{"env": "homelab"}, Protected: false},
		{Name: "prd-weu", Context: "prd-weu", Group: "prd", Protected: true, Metrics: &config.MetricsConfig{Endpoint: "http://prom"}},
	}}
	s := NewConfigService("/tmp/fleet.yaml", cfg, nil)
	s.kubeconfigPath = func() string { return "/tmp/kubeconfig" }
	s.kubeContexts = func(string) ([]string, error) { return ctxs, scanErr }
	return s
}

func TestGetFleetConfig(t *testing.T) {
	s := testConfigService(t, []string{"kubernetes-admin@homelab-nelli", "kubernetes-admin@homelab-orange", "prd-weu"}, nil)
	dto := s.GetFleetConfig()

	if dto.Path != "/tmp/fleet.yaml" || dto.KubeconfigPath != "/tmp/kubeconfig" {
		t.Fatalf("paths wrong: %+v", dto)
	}
	if len(dto.Clusters) != 2 || dto.Clusters[0].Name != "homelab-nelli" || dto.Clusters[0].Env != "homelab" {
		t.Fatalf("clusters wrong: %+v", dto.Clusters)
	}
	if !dto.Clusters[1].Protected || !dto.Clusters[1].HasMetrics {
		t.Fatalf("protected/metrics flags wrong: %+v", dto.Clusters[1])
	}
	// nelli + prd-weu are in fleet (by context/name); orange is not.
	want := map[string]bool{
		"kubernetes-admin@homelab-nelli":  true,
		"kubernetes-admin@homelab-orange": false,
		"prd-weu":                         true,
	}
	for _, c := range dto.Contexts {
		if c.InFleet != want[c.Name] {
			t.Errorf("context %q inFleet: got %v, want %v", c.Name, c.InFleet, want[c.Name])
		}
	}
	if n := s.NewContextCount(); n != 1 {
		t.Fatalf("new-context badge: got %d, want 1", n)
	}
}

func TestGetFleetConfigScanError(t *testing.T) {
	s := testConfigService(t, nil, errors.New("kubeconfig unreadable"))
	dto := s.GetFleetConfig()
	if dto.ScanError == "" || len(dto.Contexts) != 0 {
		t.Fatalf("scan error must surface, contexts empty: %+v", dto)
	}
	if s.NewContextCount() != 0 {
		t.Fatal("badge must be 0 on scan failure, never invented")
	}
}

func TestAddClusters(t *testing.T) {
	s := testConfigService(t, nil, nil)
	var got []string
	s.appendClusters = func(path string, ctxs []string) error {
		if path != "/tmp/fleet.yaml" {
			t.Fatalf("path: %s", path)
		}
		got = ctxs
		return nil
	}
	r := s.AddClusters([]string{"ctx-new"})
	if !r.OK || len(got) != 1 || got[0] != "ctx-new" {
		t.Fatalf("add: %+v / %v", r, got)
	}
	if r := s.AddClusters(nil); r.OK || r.Error == "" {
		t.Fatalf("empty add must fail: %+v", r)
	}
	s.appendClusters = func(string, []string) error { return errors.New("boom") }
	if r := s.AddClusters([]string{"x"}); r.OK || r.Error != "boom" {
		t.Fatalf("append error must surface: %+v", r)
	}
}

func TestAddClustersHotAddsToRunningFleet(t *testing.T) {
	s := testConfigService(t, []string{"ctx-new"}, nil)
	s.appendClusters = func(string, []string) error { return nil }
	var connected []string
	s.connect = func(cc config.ClusterConfig) error {
		if cc.Name != cc.Context {
			t.Fatalf("minimal entry must use name=context, got %+v", cc)
		}
		connected = append(connected, cc.Name)
		return nil
	}

	if r := s.AddClusters([]string{"ctx-new"}); !r.OK {
		t.Fatalf("AddClusters failed: %+v", r)
	}
	if len(connected) != 1 || connected[0] != "ctx-new" {
		t.Fatalf("connect not called: %v", connected)
	}
	// The session view reflects the addition without a restart.
	dto := s.GetFleetConfig()
	if len(dto.Clusters) != 3 {
		t.Fatalf("hot-added cluster missing from GetFleetConfig: %+v", dto.Clusters)
	}
	for _, c := range dto.Contexts {
		if c.Name == "ctx-new" && !c.InFleet {
			t.Fatal("hot-added context must report inFleet")
		}
	}
	if got := s.NewContextCount(); got != 0 {
		t.Fatalf("badge must drop to 0 after hot-add, got %d", got)
	}
}

func TestAddClustersHotAddsEKSContextWithDerivedIdentity(t *testing.T) {
	ctx := "arn:aws:eks:us-east-1:934692410245:cluster/eks-tooling"
	s := testConfigService(t, []string{ctx}, nil)
	s.appendClusters = func(string, []string) error { return nil }
	var connected config.ClusterConfig
	s.connect = func(cc config.ClusterConfig) error {
		connected = cc
		return nil
	}

	if r := s.AddClusters([]string{ctx}); !r.OK {
		t.Fatalf("AddClusters failed: %+v", r)
	}
	if connected.Name != "eks-tooling" || connected.Context != ctx {
		t.Fatalf("hot-added EKS identity wrong: %+v", connected)
	}
	if connected.Tags["provider"] != "eks" || connected.Tags["region"] != "us-east-1" || connected.Tags["account"] != "934692410245" {
		t.Fatalf("hot-added EKS tags wrong: %+v", connected.Tags)
	}

	dto := s.GetFleetConfig()
	var found FleetClusterDTO
	for _, c := range dto.Clusters {
		if c.Name == "eks-tooling" {
			found = c
		}
	}
	if found.Provider != "eks" || found.Region != "us-east-1" || found.Account != "934692410245" {
		t.Fatalf("settings DTO did not expose EKS tags: %+v", found)
	}
}

func TestAddClustersReportsConnectFailureHonestly(t *testing.T) {
	s := testConfigService(t, []string{"ctx-bad"}, nil)
	s.appendClusters = func(string, []string) error { return nil }
	s.connect = func(config.ClusterConfig) error { return errors.New("no such context") }

	r := s.AddClusters([]string{"ctx-bad"})
	if r.OK {
		t.Fatal("connect failure must not report full success")
	}
	for _, want := range []string{"fleet.yaml", "ctx-bad", "no such context", "Restart Klyx"} {
		if !strings.Contains(r.Error, want) {
			t.Fatalf("error %q missing %q", r.Error, want)
		}
	}
	// Not connected -> not claimed as in-fleet for the session.
	if got := len(s.GetFleetConfig().Clusters); got != 2 {
		t.Fatalf("failed connect must not join the session fleet, got %d clusters", got)
	}
}

// TestConfigService_IsProtected_LiveHotAdd is the P1 regression: the protection
// source is LIVE, so a prod-looking cluster hot-added mid-session is protected
// immediately - not fail-open until a restart.
func TestConfigService_IsProtected_LiveHotAdd(t *testing.T) {
	s := testConfigService(t, nil, nil)

	// Startup clusters resolve from the frozen config as before.
	if !s.IsProtected("prd-weu") {
		t.Fatal("explicitly-protected startup cluster must be protected")
	}
	if s.IsProtected("homelab-nelli") {
		t.Fatal("unprotected homelab cluster must not be protected")
	}
	if s.IsProtected("does-not-exist") {
		t.Fatal("unknown cluster must be treated as unprotected")
	}

	// Hot-add via the seams (no real file / registry needed).
	s.appendClusters = func(string, []string) error { return nil }
	s.connect = func(config.ClusterConfig) error { return nil }
	if r := s.AddClusters([]string{"prod-eu"}); !r.OK {
		t.Fatalf("hot-add prod failed: %+v", r)
	}
	if !s.IsProtected("prod-eu") {
		t.Fatal("hot-added prod cluster must be protected immediately (the P1 fix)")
	}
	if r := s.AddClusters([]string{"dev-eu"}); !r.OK {
		t.Fatalf("hot-add dev failed: %+v", r)
	}
	if s.IsProtected("dev-eu") {
		t.Fatal("hot-added non-prod cluster must stay unprotected")
	}
}

// TestProtectGuard_LiveSourceGatesHotAddedProdCluster wires the guard exactly as
// main does - over the live ConfigService.IsProtected - and proves a destructive
// verb on a hot-added prod cluster now requires the typed grant.
func TestProtectGuard_LiveSourceGatesHotAddedProdCluster(t *testing.T) {
	s := testConfigService(t, nil, nil)
	s.appendClusters = func(string, []string) error { return nil }
	s.connect = func(config.ClusterConfig) error { return nil }
	if r := s.AddClusters([]string{"prod-eu"}); !r.OK {
		t.Fatalf("hot-add: %+v", r)
	}

	SetProtectGuard(NewProtectGuard(s.IsProtected, time.Now))
	t.Cleanup(func() { SetProtectGuard(nil) })

	svc := NewPodsService(func(string) (PodsConn, bool) { return &fakeLivePodConn{}, true }, &anyEmitter{})

	if r := svc.DeletePod("prod-eu", "ns", "pod"); r.OK || r.Error == "" {
		t.Fatalf("destructive verb on hot-added prod cluster must require a typed grant, got %+v", r)
	}
	if r := s.ArmProtected("prod-eu", "prod-eu"); !r.OK {
		t.Fatalf("arm: %+v", r)
	}
	if r := svc.DeletePod("prod-eu", "ns", "pod"); !r.OK {
		t.Fatalf("armed delete on hot-added prod cluster must pass: %+v", r)
	}
}
