package fleet

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/cluster"
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

// Conn is the interface the registry drives. ClusterConn is the production impl.
type Conn interface {
	Name() string
	Start(ctx context.Context)
	Snapshot() Snapshot
	OpenGitOps()
	CloseGitOps()
	GitOpsResources() []flux.Resource
	GitOpsObject(kind, namespace, name string) (*unstructured.Unstructured, bool)
	GitOpsSources() []flux.Source
	GitOpsSourceObject(kind, namespace, name string) (*unstructured.Unstructured, bool)
	GitOpsWatchHealth() (synced bool, lastErr string)
	OpenCertManager()
	CloseCertManager()
	CertManagerCertificates() []certmanager.Certificate
	CertManagerIssuers() []certmanager.Issuer
	CertManagerObject(kind, namespace, name string) (*unstructured.Unstructured, bool)
	CertManagerWatchHealth() (synced bool, lastErr string)
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
	OpenExternalSecrets()
	CloseExternalSecrets()
	ExternalSecretsList() []externalsecrets.ExternalSecret
	ExternalSecretsStores() []externalsecrets.Store
	ExternalSecretsWatchHealth() (synced bool, lastErr string)
	FluxEvents(ctx context.Context, kind, ns, name string) ([]workloads.EventSummary, error)
	FluxAvailable() bool
	FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error)
	Reconcile(ctx context.Context, kind, ns, name string) error
	ReconcileWithSource(ctx context.Context, kind, ns, name string) error
	SetSuspend(ctx context.Context, kind, ns, name string, suspend bool) error
	SourceURL(ctx context.Context, kind, ns, name string) (string, bool)
	ListCRDs(ctx context.Context) ([]crd.Info, error)
	ListWorkloads(ctx context.Context, namespace string) ([]workloads.Workload, bool, bool, error)
	ListNodes(ctx context.Context) ([]workloads.NodeSummary, error)
	NodeDetail(ctx context.Context, name string) (NodeDetail, error)
	ListPods(ctx context.Context, namespace string) ([]workloads.PodSummary, bool, error)
	DeletePod(ctx context.Context, namespace, name string) error
	ListEvents(ctx context.Context, namespace string) ([]workloads.EventSummary, bool, error)
	WatchDirty(ctx context.Context, namespace string, kinds []string, onDirty func(), onLive func(bool)) (stop func(), err error)
	PodDetail(ctx context.Context, namespace, name string) (PodDetail, error)
	PodLogStream(ctx context.Context, namespace, pod, container string, previous bool, tailLines int64) (io.ReadCloser, error)
	WorkloadPods(ctx context.Context, kind, namespace, name string) ([]string, error)
	WorkloadMetrics(ctx context.Context, namespace string) (map[string]workloads.Usage, workloads.UsageStatus)
	WorkloadSparklines(ctx context.Context, kind, namespace, name string) (SparklineSet, error)
	ClusterSparklines(ctx context.Context) (SparklineSet, error)
	RolloutRestart(ctx context.Context, kind, namespace, name string) error
	ScaleWorkload(ctx context.Context, kind, namespace, name string, replicas int32) error
	CountResource(ctx context.Context, group, version, plural string) (int, bool, error)
	ListInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) ([]crd.InstanceMeta, string, error)
	GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error)
	ApplyResourceYAML(ctx context.Context, group, version, plural, kind, scope, ns, name, yaml string, dryRun bool) error
	RevealSecretKey(ctx context.Context, ns, name, key string) (string, error)
	ListGateways(ctx context.Context) ([]gwapi.GatewayRef, bool, error)
	GetGatewayTopology(ctx context.Context, namespace, name string) (gwapi.Topology, error)
	MeshMember(ctx context.Context) (clustermesh.Member, MeshReadStatus)
	HasGlobalService(ctx context.Context, ns, name string) bool
	ClusterMetrics(ctx context.Context, forceReprobe bool) (metrics.ClusterMetrics, metrics.MetricsCapability)
	RouteMetrics(ctx context.Context, routeKeys []string) (map[string]routemetrics.RouteMetrics, routemetrics.Status)
	SetCordon(ctx context.Context, nodeName string, cordon bool) error
	DrainNodeCmd(nodeName string, opts DrainOptions) (*exec.Cmd, error)
	DescribeCmd(ctx context.Context, kindArg, namespace, name string) (*exec.Cmd, error)
	ExecCommand(namespace, pod, container string) ([]string, error)
	DebugCommand(namespace, pod, container string) ([]string, error)
	PortForward(ctx context.Context, namespace, pod string, localPort, targetPort int) (stop func(), actualLocal int, done <-chan error, err error)
	ResolveServicePod(ctx context.Context, namespace, service string, port int) (pod string, targetPort int, err error)
	HelmReleases(ctx context.Context) ([]helmcli.Release, error)
	HelmHistory(ctx context.Context, namespace, release string) ([]helmcli.HistoryEntry, error)
	HelmValues(ctx context.Context, namespace, release string) (string, error)
	HelmRollback(ctx context.Context, namespace, release string, revision int, opts helmcli.RollbackOptions) error
	ListArgoApps(ctx context.Context) ([]argo.App, error)
	ListArgoProjects(ctx context.Context) ([]argo.Project, error)
	ListArgoApplicationSets(ctx context.Context) ([]argo.ApplicationSet, error)
	RefreshArgoApp(ctx context.Context, namespace, name string) error
	SyncArgoApp(ctx context.Context, namespace, name string, opts argo.SyncOptions) error
	GitOpsSummary(ctx context.Context) (GitOpsSummary, error)
	GitOpsSummaryFlux(ctx context.Context) (fluxPresent bool, total, notReady, suspended int, err error)
}

var podGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

const defaultResync = 5 * time.Minute
const defaultConnectTimeout = 30 * time.Second

type ClusterConn struct {
	name        string
	kubeContext string       // kubeconfig context name for kubectl exec (e.g. drain)
	restConfig  *rest.Config // for SPDY transports (port-forward); nil in fake-client tests
	typed       kubernetes.Interface
	meta        metadata.Interface
	dyn         dynamic.Interface
	detector    *capability.Detector
	clk         clock.Clock

	mu             sync.RWMutex
	state          ConnState
	reason         string
	lastSync       time.Time
	caps           capability.Set
	snapNodesReady int
	snapNodesTotal int
	snapPods       int
	snapVersion    string
	connectTimeout time.Duration
	metricsCfg     config.MetricsConfig
	refresh        chan struct{}             // buffered(1); coalesces informer events
	ctx            context.Context           // captured in Start; scopes lazy watches
	nodeInf        cache.SharedIndexInformer // eager node informer; ListNodes serves from its cache once synced
	disc           discoveryLike             // TTL-cached discovery; built lazily via c.discovery()
	gitops         *gitopsWatch              // lazy; nil until OpenGitOps
	gitopsOpening  bool                      // single-flights OpenGitOps' out-of-lock install
	gitopsWanted   bool                      // desired state; an in-flight open aborts if false
	certmgr        *certmanagerWatch         // lazy; nil until OpenCertManager
	certmgrOpening bool                      // single-flights OpenCertManager's out-of-lock install
	certmgrWanted  bool                      // desired state; an in-flight open aborts if false
	eso            *esoWatch                 // lazy; nil until OpenExternalSecrets
	esoOpening     bool                      // single-flights OpenExternalSecrets' out-of-lock install
	esoWanted      bool                      // desired state; an in-flight open aborts if false

	// metricsMu guards the metrics cache. Note: ClusterMetrics holds it across
	// the probe + sample network I/O (up to a few seconds). This serializes
	// concurrent callers on the same conn, which also single-flights duplicate
	// probes of the same backend. Acceptable: UI calls are infrequent and the
	// appbridge bounds them with a 30s ctx. Revisit if many callers ever fan at
	// one conn (e.g. klyx serve).
	metricsMu    sync.Mutex
	metricsState metricsCache
	metricsTF    metrics.TransportFactory // nil in production; tests inject a fake
}

func NewClusterConn(name string, typed kubernetes.Interface, meta metadata.Interface,
	dyn dynamic.Interface, detector *capability.Detector, clk clock.Clock, metricsCfg config.MetricsConfig) *ClusterConn {
	return &ClusterConn{
		name: name, typed: typed, meta: meta, dyn: dyn, detector: detector, clk: clk,
		metricsCfg:     metricsCfg,
		state:          Unconnected,
		connectTimeout: defaultConnectTimeout,
		refresh:        make(chan struct{}, 1),
	}
}

// WithKubeContext sets the kubeconfig context name used for kubectl-based
// operations (e.g. drain). Call this right after NewClusterConn in the
// factory; it is not threadsafe after Start.
func (c *ClusterConn) WithKubeContext(ctx string) *ClusterConn {
	c.kubeContext = ctx
	return c
}

// KubeContext returns the kubeconfig context name.
func (c *ClusterConn) KubeContext() string { return c.kubeContext }

// WithRESTConfig sets the *rest.Config used to build SPDY transports for
// port-forwarding. Call it right after NewClusterConn in the factory; it is not
// threadsafe after Start. Fake-client tests leave it nil (PortForward is not
// fake-testable - see portforward.go).
func (c *ClusterConn) WithRESTConfig(rc *rest.Config) *ClusterConn {
	c.restConfig = rc
	return c
}

func (c *ClusterConn) Name() string { return c.name }

func (c *ClusterConn) setState(ev Event, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if next, ok := Transition(c.state, ev); ok {
		c.state = next
		c.reason = reason
	}
}

// signalRefresh nudges the refresh loop without blocking. The buffered(1)
// channel coalesces bursts (e.g. the initial list of many pods) into one wake.
func (c *ClusterConn) signalRefresh() {
	select {
	case c.refresh <- struct{}{}:
	default:
	}
}

// onWatchError flips a synced connection to Stale when a list/watch fails.
// Pre-sync errors are remembered so connect-timeout can report auth/tooling
// failures instead of hiding them behind a generic timeout.
func (c *ClusterConn) onWatchError(err error) {
	msg := cluster.FriendlyErrorMessage(err)
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state
	if st == Synced || st == Degraded {
		if next, ok := Transition(c.state, EvWatchDrop); ok {
			c.state = next
			c.reason = "watch error: " + msg
		}
		return
	}
	if st == Connecting || st == Failed {
		c.reason = msg
	}
}

// Start launches the eager-set informers, wiring watch-error and event handlers
// before start, then runs the connect and refresh goroutines. All three loops
// are bound to ctx; the informers retry in the background, so a connection that
// fails to sync within connectTimeout is marked Failed but self-heals to Synced
// when a later relist succeeds.
func (c *ClusterConn) Start(ctx context.Context) {
	c.setState(EvStart, "")
	c.ctx = ctx

	nodeFactory := informers.NewSharedInformerFactory(c.typed, defaultResync)
	nodeInformer := nodeFactory.Core().V1().Nodes().Informer()

	metaFactory := metadatainformer.NewSharedInformerFactory(c.meta, defaultResync)
	podInformer := metaFactory.ForResource(podGVR).Informer()

	// Register handlers BEFORE starting the informers. SetWatchErrorHandler
	// errors only if called after Start, which we do not do; ignore defensively.
	for _, inf := range []cache.SharedIndexInformer{nodeInformer, podInformer} {
		// Strip managedFields before caching: the eager set lives for the app's
		// lifetime and nothing in Klyx reads them (same for every cached kind).
		_ = inf.SetTransform(stripManagedFields)
		_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
			c.onWatchError(err)
		})
		_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(interface{}) { c.signalRefresh() },
			UpdateFunc: func(interface{}, interface{}) { c.signalRefresh() },
			DeleteFunc: func(interface{}) { c.signalRefresh() },
		})
	}

	c.mu.Lock()
	c.nodeInf = nodeInformer
	c.mu.Unlock()

	nodeFactory.Start(ctx.Done())
	metaFactory.Start(ctx.Done())

	go c.refreshLoop(ctx, nodeInformer, podInformer)
	go c.connectLoop(ctx, nodeInformer, podInformer)
}

// stripManagedFields is a cache.TransformFunc that drops ObjectMeta.ManagedFields
// before an object enters an informer store. managedFields are server-side-apply
// bookkeeping nothing in Klyx reads, and on busy objects they can dominate the
// cached size. Non-metav1 objects (e.g. DeletedFinalStateUnknown tombstones)
// pass through untouched.
func stripManagedFields(obj interface{}) (interface{}, error) {
	if m, ok := obj.(metav1.Object); ok {
		m.SetManagedFields(nil)
	}
	return obj, nil
}

// connectLoop owns the initial connect. It bounds each sync attempt by
// connectTimeout; on success it runs detection and announces Synced (this is the
// only place the first EvSynced is emitted, so Detect always precedes it), then
// returns. On timeout it marks Failed and retries within the same loop, so a
// cluster that has NOT YET synced self-heals (Failed -> Synced) once
// connectivity returns. Note: this loop exits after the first successful sync,
// so post-sync recovery is handled by refreshLoop (Stale -> Synced), not here.
func (c *ClusterConn) connectLoop(ctx context.Context, nodeInformer, podInformer cache.SharedIndexInformer) {
	for {
		tctx, cancel := context.WithTimeout(ctx, c.connectTimeout)
		ok := cache.WaitForCacheSync(tctx.Done(), nodeInformer.HasSynced, podInformer.HasSynced)
		cancel()

		if ctx.Err() != nil {
			return // parent cancelled
		}
		if ok {
			caps := c.detector.Detect(ctx)

			ver := ""
			if vi, verr := c.typed.Discovery().ServerVersion(); verr == nil && vi != nil {
				ver = vi.GitVersion
			}

			c.mu.Lock()
			c.caps = caps
			c.snapVersion = ver
			c.mu.Unlock()

			c.refreshCounts(nodeInformer, podInformer)
			c.setState(EvSynced, "")

			// Apply the initial tier from the one-shot Detect. startCapHealth
			// below then keeps GitOps health live (Healthy <-> Degraded) via the
			// controller-workload watch.
			if caps.GitOps.Tier == capability.Degraded || caps.Network.Tier == capability.Degraded {
				c.setState(EvCapUnhealthy, capabilityReason(caps))
			}
			c.startCapHealth(ctx, caps)
			return
		}
		reason := c.connectFailureReason()
		msg := "connect timed out after " + c.connectTimeout.String()
		if reason != "" {
			msg += ": " + reason
		}
		c.setState(EvConnError, msg)
	}
}

func (c *ClusterConn) connectFailureReason() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reason
}

// refreshLoop recomputes counts on coalesced informer events for the lifetime of
// ctx. It does not drive the initial Connecting -> Synced (connectLoop owns that,
// to avoid racing ahead of Detect). It does recover Stale -> Synced when a relist
// resumes after a dropped watch. If the cluster was Degraded by a crashlooping
// GitOps controller, the cap-health watch relists on the same recovery and
// re-applies Degraded, so the overlay is restored without recovery-specific code.
func (c *ClusterConn) refreshLoop(ctx context.Context, nodeInformer, podInformer cache.SharedIndexInformer) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.refresh:
			if !nodeInformer.HasSynced() || !podInformer.HasSynced() {
				continue
			}
			c.refreshCounts(nodeInformer, podInformer)

			c.mu.RLock()
			st := c.state
			c.mu.RUnlock()
			if st == Stale {
				c.setState(EvSynced, "")
			}
		}
	}
}

func capabilityReason(caps capability.Set) string {
	if caps.GitOps.Reason != "" {
		return caps.GitOps.Reason
	}
	return caps.Network.Reason
}

// applyGitOpsHealth updates the GitOps capability and drives the cap-state edges
// (EvCapUnhealthy / EvCapHealthy) atomically under one lock. Reason is set on
// Degraded and cleared on Healthy. Outside Synced/Degraded the transition is a
// no-op, but caps are still updated so recovery reflects the latest health.
func (c *ClusterConn) applyGitOpsHealth(g capability.GitOpsCapability) {
	var ev Event
	switch g.Tier {
	case capability.Degraded:
		ev = EvCapUnhealthy
	case capability.Healthy:
		ev = EvCapHealthy
	default: // Absent: should not occur for a present tool
		c.mu.Lock()
		c.caps.GitOps = g
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.caps.GitOps = g
	if next, ok := Transition(c.state, ev); ok {
		c.state = next
		c.reason = g.Reason
	}
	c.mu.Unlock()
}

// refreshCounts recomputes node/pod counts and lastSync from the informer
// stores. Called by connectLoop on initial sync and by refreshLoop on every
// coalesced watch event.
func (c *ClusterConn) refreshCounts(nodeInformer, podInformer cache.SharedIndexInformer) {
	nodes := make([]*corev1.Node, 0)
	for _, obj := range nodeInformer.GetStore().List() {
		if n, ok := obj.(*corev1.Node); ok {
			nodes = append(nodes, n)
		}
	}
	ready, total := NodeReadiness(nodes)
	pods := len(podInformer.GetStore().List())

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSync = c.clk.Now()
	c.snapNodesReady, c.snapNodesTotal, c.snapPods = ready, total, pods
}

func (c *ClusterConn) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Snapshot{
		Name:         c.name,
		State:        c.state,
		Reason:       c.reason,
		LastSync:     c.lastSync,
		NodesReady:   c.snapNodesReady,
		NodesTotal:   c.snapNodesTotal,
		Pods:         c.snapPods,
		Version:      c.snapVersion,
		Capabilities: c.caps,
	}
}
