package fleet

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/clustermesh"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/externalsecrets"
	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/routemetrics"
	"github.com/moomora/klyx/internal/workloads"
)

type fakeConn struct {
	name  string
	snap  Snapshot
	start func()
}

func (f *fakeConn) Name() string { return f.name }
func (f *fakeConn) Start(ctx context.Context) {
	if f.start != nil {
		f.start()
	}
}
func (f *fakeConn) Snapshot() Snapshot               { return f.snap }
func (f *fakeConn) OpenGitOps()                      {}
func (f *fakeConn) CloseGitOps()                     {}
func (f *fakeConn) GitOpsResources() []flux.Resource { return nil }
func (f *fakeConn) GitOpsObject(kind, namespace, name string) (*unstructured.Unstructured, bool) {
	return nil, false
}
func (f *fakeConn) GitOpsWatchHealth() (bool, string)      { return true, "" }
func (f *fakeConn) CertManagerWatchHealth() (bool, string) { return true, "" }
func (f *fakeConn) GitOpsSources() []flux.Source           { return nil }
func (f *fakeConn) GitOpsSourceObject(kind, namespace, name string) (*unstructured.Unstructured, bool) {
	return nil, false
}
func (f *fakeConn) OpenCertManager()                                   {}
func (f *fakeConn) CloseCertManager()                                  {}
func (f *fakeConn) CertManagerCertificates() []certmanager.Certificate { return nil }
func (f *fakeConn) CertManagerIssuers() []certmanager.Issuer           { return nil }
func (f *fakeConn) CertManagerObject(kind, namespace, name string) (*unstructured.Unstructured, bool) {
	return nil, false
}
func (f *fakeConn) GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error) {
	return certmanager.Chain{}, nil
}
func (f *fakeConn) OpenExternalSecrets()                                  {}
func (f *fakeConn) CloseExternalSecrets()                                 {}
func (f *fakeConn) ExternalSecretsList() []externalsecrets.ExternalSecret { return nil }
func (f *fakeConn) ExternalSecretsStores() []externalsecrets.Store        { return nil }
func (f *fakeConn) ExternalSecretsWatchHealth() (bool, string)            { return true, "" }
func (f *fakeConn) FluxEvents(ctx context.Context, kind, ns, name string) ([]workloads.EventSummary, error) {
	return nil, nil
}
func (f *fakeConn) FluxAvailable() bool { return false }
func (f *fakeConn) FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error) {
	return fluxcli.DiffResult{}, nil
}
func (f *fakeConn) Reconcile(ctx context.Context, kind, ns, name string) error { return nil }
func (f *fakeConn) ReconcileWithSource(ctx context.Context, kind, ns, name string) error {
	return nil
}
func (f *fakeConn) SetSuspend(ctx context.Context, kind, ns, name string, suspend bool) error {
	return nil
}
func (f *fakeConn) SourceURL(ctx context.Context, kind, ns, name string) (string, bool) {
	return "", false
}
func (f *fakeConn) ListCRDs(ctx context.Context) ([]crd.Info, error) { return nil, nil }
func (f *fakeConn) ListWorkloads(context.Context, string) ([]workloads.Workload, bool, bool, error) {
	return nil, false, false, nil
}
func (f *fakeConn) ListNodes(context.Context) ([]workloads.NodeSummary, error) {
	return nil, nil
}
func (f *fakeConn) NodeDetail(context.Context, string) (NodeDetail, error) {
	return NodeDetail{}, nil
}
func (f *fakeConn) ListPods(context.Context, string) ([]workloads.PodSummary, bool, error) {
	return nil, false, nil
}
func (f *fakeConn) DeletePod(context.Context, string, string) error { return nil }
func (f *fakeConn) ListEvents(context.Context, string) ([]workloads.EventSummary, bool, error) {
	return nil, false, nil
}
func (f *fakeConn) WatchDirty(context.Context, string, []string, func(), func(bool)) (func(), error) {
	return func() {}, nil
}
func (f *fakeConn) PodDetail(context.Context, string, string) (PodDetail, error) {
	return PodDetail{}, nil
}
func (f *fakeConn) PodLogStream(context.Context, string, string, string, bool, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeConn) WorkloadPods(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}
func (f *fakeConn) WorkloadMetrics(context.Context, string) (map[string]workloads.Usage, workloads.UsageStatus) {
	return nil, workloads.UsageStatus{}
}
func (f *fakeConn) WorkloadSparklines(context.Context, string, string, string) (SparklineSet, error) {
	return SparklineSet{}, nil
}
func (f *fakeConn) ClusterSparklines(context.Context) (SparklineSet, error) {
	return SparklineSet{}, nil
}
func (f *fakeConn) RolloutRestart(context.Context, string, string, string) error { return nil }
func (f *fakeConn) ScaleWorkload(context.Context, string, string, string, int32) error {
	return nil
}
func (f *fakeConn) CountResource(ctx context.Context, group, version, plural string) (int, bool, error) {
	return 0, false, nil
}
func (f *fakeConn) ListInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	return nil, "", nil
}
func (f *fakeConn) GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error) {
	return crd.InstanceDetail{}, nil
}
func (f *fakeConn) ApplyResourceYAML(ctx context.Context, group, version, plural, kind, scope, ns, name, yaml string, dryRun bool) error {
	return nil
}
func (f *fakeConn) ListGateways(ctx context.Context) ([]gwapi.GatewayRef, bool, error) {
	return nil, false, nil
}
func (f *fakeConn) GetGatewayTopology(ctx context.Context, namespace, name string) (gwapi.Topology, error) {
	return gwapi.Topology{}, nil
}
func (f *fakeConn) MeshMember(ctx context.Context) (clustermesh.Member, MeshReadStatus) {
	return clustermesh.Member{Cluster: f.name, Present: true}, MeshReadStatus{}
}
func (f *fakeConn) HasGlobalService(ctx context.Context, ns, name string) bool { return false }
func (f *fakeConn) ClusterMetrics(ctx context.Context, forceReprobe bool) (metrics.ClusterMetrics, metrics.MetricsCapability) {
	return metrics.ClusterMetrics{}, metrics.MetricsCapability{}
}
func (f *fakeConn) RouteMetrics(context.Context, []string) (map[string]routemetrics.RouteMetrics, routemetrics.Status) {
	return nil, routemetrics.Status{}
}
func (f *fakeConn) RevealSecretKey(ctx context.Context, ns, name, key string) (string, error) {
	return "", nil
}
func (f *fakeConn) SetCordon(context.Context, string, bool) error { return nil }
func (f *fakeConn) DrainNodeCmd(nodeName string, opts DrainOptions) (*exec.Cmd, error) {
	return exec.Command("true"), nil
}
func (f *fakeConn) DescribeCmd(ctx context.Context, kindArg, namespace, name string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "true"), nil
}
func (f *fakeConn) DebugCommand(namespace, pod, container string) ([]string, error) {
	return nil, nil
}
func (f *fakeConn) ExecCommand(namespace, pod, container string) ([]string, error) {
	return []string{"kubectl", "exec", pod}, nil
}
func (f *fakeConn) PortForward(context.Context, string, string, int, int) (func(), int, <-chan error, error) {
	return func() {}, 0, nil, nil
}
func (f *fakeConn) ResolveServicePod(context.Context, string, string, int) (string, int, error) {
	return "", 0, nil
}
func (f *fakeConn) HelmReleases(context.Context) ([]helmcli.Release, error) { return nil, nil }
func (f *fakeConn) HelmHistory(context.Context, string, string) ([]helmcli.HistoryEntry, error) {
	return nil, nil
}
func (f *fakeConn) HelmValues(context.Context, string, string) (string, error) { return "", nil }
func (f *fakeConn) HelmRollback(context.Context, string, string, int, helmcli.RollbackOptions) error {
	return nil
}
func (f *fakeConn) ListArgoApps(context.Context) ([]argo.App, error)         { return nil, nil }
func (f *fakeConn) ListArgoProjects(context.Context) ([]argo.Project, error) { return nil, nil }
func (f *fakeConn) ListArgoApplicationSets(context.Context) ([]argo.ApplicationSet, error) {
	return nil, nil
}
func (f *fakeConn) RefreshArgoApp(context.Context, string, string) error { return nil }
func (f *fakeConn) SyncArgoApp(context.Context, string, string, argo.SyncOptions) error {
	return nil
}
func (f *fakeConn) GitOpsSummary(context.Context) (GitOpsSummary, error) {
	return GitOpsSummary{}, nil
}
func (f *fakeConn) GitOpsSummaryFlux(context.Context) (bool, int, int, int, error) {
	return false, 0, 0, 0, nil
}

func TestRegistryStartsAllConnsAndIsolatesFailure(t *testing.T) {
	cfg := &config.Config{Clusters: []config.ClusterConfig{
		{Name: "good-1"}, {Name: "bad"}, {Name: "good-2"},
	}}

	factory := func(cc config.ClusterConfig) (Conn, error) {
		switch cc.Name {
		case "bad":
			// Simulate a conn that fails to construct (e.g. bad kubeconfig).
			return nil, context.DeadlineExceeded
		case "good-1":
			return &fakeConn{name: "good-1", snap: Snapshot{Name: "good-1", State: Synced}}, nil
		default:
			return &fakeConn{name: "good-2", snap: Snapshot{Name: "good-2", State: Synced}}, nil
		}
	}

	reg := NewRegistry(cfg, factory)
	reg.Start(context.Background())

	snaps := reg.Snapshots()
	if len(snaps) != 3 {
		t.Fatalf("want 3 snapshots, got %d", len(snaps))
	}
	byName := map[string]Snapshot{}
	for _, s := range snaps {
		byName[s.Name] = s
	}
	if byName["good-1"].State != Synced || byName["good-2"].State != Synced {
		t.Fatalf("good conns should be Synced: %+v", byName)
	}
	if byName["bad"].State != Failed {
		t.Fatalf("bad conn should be Failed, got %v", byName["bad"].State)
	}
	if byName["bad"].Reason == "" {
		t.Fatal("failed conn must carry a reason")
	}
}

func TestRegistryStartIsIdempotent(t *testing.T) {
	cfg := &config.Config{Clusters: []config.ClusterConfig{{Name: "a"}, {Name: "b"}}}
	factory := func(cc config.ClusterConfig) (Conn, error) {
		return &fakeConn{name: cc.Name, snap: Snapshot{Name: cc.Name, State: Synced}}, nil
	}
	reg := NewRegistry(cfg, factory)
	reg.Start(context.Background())
	reg.Start(context.Background()) // second call must be a no-op
	if got := len(reg.Snapshots()); got != 2 {
		t.Fatalf("want 2 snapshots after double Start, got %d", got)
	}
}

func TestRegistryAddStartsConnAtRuntime(t *testing.T) {
	cfg := &config.Config{Clusters: []config.ClusterConfig{{Name: "a"}}}
	started := map[string]bool{}
	factory := func(cc config.ClusterConfig) (Conn, error) {
		started[cc.Name] = true
		return &fakeConn{name: cc.Name, snap: Snapshot{Name: cc.Name, State: Synced}}, nil
	}
	reg := NewRegistry(cfg, factory)
	reg.Start(context.Background())

	if err := reg.Add(context.Background(), config.ClusterConfig{Name: "hot", Context: "hot"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !started["hot"] {
		t.Fatal("factory not invoked for hot-added cluster")
	}
	if _, ok := reg.Conn("hot"); !ok {
		t.Fatal("hot-added conn not retrievable")
	}
	if got := len(reg.Snapshots()); got != 2 {
		t.Fatalf("want 2 snapshots after Add, got %d", got)
	}
}

func TestRegistryAddRefusesDuplicates(t *testing.T) {
	cfg := &config.Config{Clusters: []config.ClusterConfig{{Name: "a"}, {Name: "broken"}}}
	factory := func(cc config.ClusterConfig) (Conn, error) {
		if cc.Name == "broken" {
			return nil, context.DeadlineExceeded
		}
		return &fakeConn{name: cc.Name, snap: Snapshot{Name: cc.Name, State: Synced}}, nil
	}
	reg := NewRegistry(cfg, factory)
	reg.Start(context.Background())

	if err := reg.Add(context.Background(), config.ClusterConfig{Name: "a"}); err == nil {
		t.Fatal("Add must refuse a live duplicate")
	}
	// Failed startup entries are also reserved - a restart retries them.
	if err := reg.Add(context.Background(), config.ClusterConfig{Name: "broken"}); err == nil {
		t.Fatal("Add must refuse a failed-entry duplicate")
	}
}

func TestRegistryAddReturnsFactoryError(t *testing.T) {
	cfg := &config.Config{}
	factory := func(cc config.ClusterConfig) (Conn, error) { return nil, context.DeadlineExceeded }
	reg := NewRegistry(cfg, factory)
	reg.Start(context.Background())

	if err := reg.Add(context.Background(), config.ClusterConfig{Name: "x"}); err == nil {
		t.Fatal("factory error must surface")
	}
	// The error is the caller's to show; no Failed ghost entry is recorded.
	if got := len(reg.Snapshots()); got != 0 {
		t.Fatalf("want 0 snapshots after failed Add, got %d", got)
	}
}

func TestRegistrySurfacesFriendlyFactoryError(t *testing.T) {
	cfg := &config.Config{Clusters: []config.ClusterConfig{{Name: "eks"}}}
	factory := func(config.ClusterConfig) (Conn, error) {
		return nil, errors.New(`getting credentials: exec: "aws": executable file not found in $PATH`)
	}
	reg := NewRegistry(cfg, factory)
	reg.Start(context.Background())

	snaps := reg.Snapshots()
	if len(snaps) != 1 || !strings.Contains(snaps[0].Reason, "AWS CLI not found") {
		t.Fatalf("want friendly AWS failure, got %+v", snaps)
	}
}
