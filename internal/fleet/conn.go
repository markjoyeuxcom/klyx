package fleet

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/cluster"
	"github.com/moomora/klyx/internal/clustermesh"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/execenv"
	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/depgraph"
	"github.com/moomora/klyx/internal/gitops/notify"
	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/hubble"
	"github.com/moomora/klyx/internal/identity"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/rbac"
	"github.com/moomora/klyx/internal/routemetrics"
	"github.com/moomora/klyx/internal/workloads"
)

// Conn is the interface the registry drives. ClusterConn is the production impl.
type Conn interface {
	Name() string
	Start(ctx context.Context)
	Snapshot() Snapshot
	FluxObject(ctx context.Context, kind, ns, name string) (*unstructured.Unstructured, error)
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
	// ListVeleroRestores reads the Restores on demand (coverage S2b §3).
	ListVeleroRestores(ctx context.Context) (VeleroRestores, error)
	// CheckAdmissionParams reads a policy's params on demand (coverage S3a §6).
	CheckAdmissionParams(ctx context.Context, policyKind, policyName string, paramGVR schema.GroupVersionResource, namespaced bool) (AdmissionParams, error)
	// Workload identity, read on demand (coverage S5 §6).
	ServiceAccountIdentity(ctx context.Context, ns, name, uid string) (identity.SAReport, error)
	PodIdentity(ctx context.Context, ns, name, uid string) (identity.PodInspectorReport, error)
	StoreIdentity(ctx context.Context, kind, ns, name, uid string) (identity.StoreReport, error)
	// coverage S9: KEDA's on-demand reads and verb
	KEDAInspect(ctx context.Context, resource, ns, name, uid string) (KEDAInspection, error)
	KEDAJobsOf(ctx context.Context, ns, name, uid string) (KEDAJobs, error)
	ReplicaOwner(ctx context.Context, kind, ns, name string) ReplicaOwnership
	KEDASetPaused(ctx context.Context, resource, ns, name, uid string, pause bool) error
	FluxEvents(ctx context.Context, kind, ns, name string) ([]workloads.EventSummary, error)
	FluxAvailable() bool
	FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error)
	ServerSideDiff(ctx context.Context, objs []*unstructured.Unstructured, inventory []string, prune bool) ([]cachediff.Change, error)
	// ObjectWrites reads each object's last recorded managed-field update
	// (The Line slice 7a): metadata only, UID-pinned.
	ObjectWrites(ctx context.Context, refs []ObjectRef) ([]WriteResult, error)
	Reconcile(ctx context.Context, kind, ns, name, uid string) error
	ReconcileWithSource(ctx context.Context, kind, ns, name, uid string) error
	ReconcileWithSourceSteps(ctx context.Context, kind, ns, name, uid string) ([]Step, error)
	SetSuspend(ctx context.Context, kind, ns, name, uid string, suspend bool) error
	ListCRDs(ctx context.Context) ([]crd.Info, error)
	ListCRDsFromDiscovery(ctx context.Context) ([]crd.Info, error)
	GetCRDDefinition(ctx context.Context, name, version string) (crd.Definition, error)
	WorkloadCompare(ctx context.Context, kind, ns, name string) (WorkloadCompareInfo, error)
	NodeDetail(ctx context.Context, name string) (NodeDetail, error)
	DeletePod(ctx context.Context, namespace, name, uid string) error
	ListEvents(ctx context.Context, namespace string) ([]workloads.EventSummary, bool, error)
	WatchDirty(ctx context.Context, namespace string, kinds []string, onDirty func(), onLive func(bool)) (stop func(), err error)
	PodDetail(ctx context.Context, namespace, name string) (PodDetail, error)
	PodLogStream(ctx context.Context, namespace, pod, container string, previous bool, tailLines int64) (io.ReadCloser, error)
	WorkloadPods(ctx context.Context, kind, namespace, name string) ([]string, error)
	WorkloadMetrics(ctx context.Context, namespace string) (map[string]workloads.Usage, workloads.UsageStatus)
	NodeUtilization(ctx context.Context) ([]workloads.NodeUsage, workloads.UsageStatus)
	PodContainerUsage(ctx context.Context, namespace, pod string) (map[string]workloads.Usage, workloads.UsageStatus)
	WorkloadSparklines(ctx context.Context, kind, namespace, name string) (SparklineSet, error)
	ClusterSparklines(ctx context.Context) (SparklineSet, error)
	RolloutRestart(ctx context.Context, kind, namespace, name, uid string) error
	RolloutHistory(ctx context.Context, kind, namespace, name string) ([]workloads.RolloutRevision, types.UID, error)
	RolloutUndo(ctx context.Context, kind, namespace, name, uid string, revision int64) error
	RolloutPause(ctx context.Context, namespace, name, uid string, pause bool) error
	ScaleWorkload(ctx context.Context, kind, namespace, name, uid string, replicas int32) error
	CountResource(ctx context.Context, group, version, plural string) (int, bool, error)
	ListInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) (crd.InstancePage, error)
	GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error)
	HealthRowsNow(ctx context.Context, task, part string) (observe.Rows, bool)
	ApplyResourceYAML(ctx context.Context, group, version, plural, kind, scope, ns, name, expectedUID, yaml string, dryRun bool) error
	CreateResourceYAML(ctx context.Context, yamlDocs string, dryRun bool) []AdhocResult
	LaunchDiagnosticPod(ctx context.Context, name, image string, ttlSeconds int64) AdhocResult
	ListAdhocObjects(ctx context.Context) (AdhocInventory, error)
	DeleteAdhocObject(ctx context.Context, group, version, plural, ns, name, uid string) error
	GitOpsOwnership(ctx context.Context, group, kind, ns, name string) GitOpsOwnership
	AccessCanI(ctx context.Context, spec rbac.CheckSpec, saNamespace, saName string) (rbac.Verdict, error)
	AccessWhoCan(ctx context.Context, spec rbac.CheckSpec) (rbac.Enumeration, error)
	RevealSecretKey(ctx context.Context, ns, name, key string) (string, error)
	GetGatewayTopology(ctx context.Context, namespace, name string) (gwapi.Topology, error)
	MeshMember(ctx context.Context) (clustermesh.Member, MeshReadStatus)
	HasGlobalService(ctx context.Context, ns, name string) bool
	ClusterMetrics(ctx context.Context, forceReprobe bool) (metrics.ClusterMetrics, metrics.MetricsCapability)
	RouteMetrics(ctx context.Context, routeKeys []string) (map[string]routemetrics.RouteMetrics, routemetrics.Status)
	RuleMetrics(ctx context.Context, namespace, name string) (routemetrics.GatewayRules, error)
	DependencyGraph(ctx context.Context, kind, ns, name, uid string) depgraph.Graph
	HubbleFlowStats(ctx context.Context) (hubble.FlowStats, hubble.Status)
	SetCordon(ctx context.Context, nodeName, uid string, cordon bool) error
	CheckNodeArmed(ctx context.Context, nodeName, uid string) error
	DrainNodeCmd(nodeName string, opts DrainOptions) (*exec.Cmd, error)
	DescribeCmd(ctx context.Context, kindArg, namespace, name string) (*exec.Cmd, error)
	DescribeArgv(kindArg, namespace, name string, events bool) ([]string, error)
	DescribeTarget(ctx context.Context, kindArg string) (schema.GroupVersionResource, schema.GroupVersionKind, error)
	DescribeSnapshot(ctx context.Context, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, ns, name string) (uid, resourceVersion, hold string, err error)
	DescribePinnedCmd(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, events bool) (*exec.Cmd, func(), error)
	FluxOpAct(ctx context.Context, kind, ns, name, uid, verb string) error
	FluxOpObject(ctx context.Context, kind, ns, name string) (*unstructured.Unstructured, error)
	// FluxNotifications reads Providers and Alerts on demand (S1c §3).
	FluxNotifications(ctx context.Context) notify.Read
	ExecCommand(namespace, pod, container string) ([]string, error)
	DebugCommand(namespace, pod, container string, o DebugOptions) ([]string, error)
	DebugKubectlCheck(ctx context.Context) DebugKubectl
	CheckPodArmed(ctx context.Context, namespace, name, uid string) error
	PortForward(ctx context.Context, namespace, pod string, localPort, targetPort int) (stop func(), actualLocal int, done <-chan error, err error)
	ResolveServicePod(ctx context.Context, namespace, service string, port int) (pod string, targetPort int, err error)
	HelmReleases(ctx context.Context) ([]helmcli.Release, error)
	HelmHistory(ctx context.Context, namespace, release string) ([]helmcli.HistoryEntry, error)
	HelmValues(ctx context.Context, namespace, release string) (string, error)
	HelmRollback(ctx context.Context, namespace, release string, revision int, opts helmcli.RollbackOptions) error
	ListArgoProjects(ctx context.Context) ([]argo.Project, error)
	RefreshArgoApp(ctx context.Context, namespace, name, uid string) error
	SyncArgoApp(ctx context.Context, namespace, name, uid string, opts argo.SyncOptions) error
	GitOpsPromotion(ctx context.Context) (PromotionSnapshot, error)
	UpgradeReadiness(ctx context.Context) (UpgradeReport, error)
	ConnectivityCanI(ctx context.Context, srcNS, srcPod, dstNS, dstPod string, port int, protocol string) (ConnectivityReport, error)
	NodeHeadroom(ctx context.Context, loseKind, loseName string) (HeadroomReport, error)
	ExpiryScan(ctx context.Context) (ExpiryScanResult, error)
	KarpenterState(ctx context.Context) (KarpenterSnapshot, error)
}

var podGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

const defaultResync = 5 * time.Minute
const defaultConnectTimeout = 30 * time.Second

type ClusterConn struct {
	// payloadCols caches the registered payload kinds' printer columns
	// (coverage S0b §3); guarded by payloadColMu.
	payloadColMu sync.Mutex
	payloadCols  map[string]payloadColEntry

	name        string
	kubeContext string // kubeconfig context name for kubectl exec (e.g. drain)
	// kubeconfigPaths is the EFFECTIVE kubeconfig selection client-go resolved
	// this cluster through - absolute files in precedence order (configured
	// selection, else inherited KUBECONFIG, else ~/.kube/config). Every
	// kubectl/helm/flux subprocess is pinned to it. Nil only for conns built
	// outside the factory (tests), which then inherit the environment.
	kubeconfigPaths []string
	restConfig      *rest.Config // for SPDY transports (port-forward); nil in fake-client tests
	// guardedREST is the metrics service-proxy client built from restConfig
	// (C5 §4), once, under metricsMu.
	guardedREST    rest.Interface
	guardedRESTErr error
	// creds resolves external endpoints' credentials (C5 §3, §7).
	creds    Credentials
	typed    kubernetes.Interface
	meta     metadata.Interface
	dyn      dynamic.Interface
	detector *capability.Detector
	clk      clock.Clock

	mu             sync.RWMutex
	state          ConnState
	reason         string
	lastSync       time.Time
	caps           capability.Set
	snapNodesReady int
	snapNodesTotal int
	snapPods       int
	podsKnown      bool // a complete pod read has happened: snapPods is evidence, never a default zero
	podsStale      bool // snapPods is the last good count, not current
	snapVersion    string
	connectTimeout time.Duration
	metricsCfg     config.MetricsConfig
	refresh        chan struct{}             // buffered(1); coalesces informer events
	ctx            context.Context           // captured in Start; scopes lazy watches
	nodeInf        cache.SharedIndexInformer // eager node informer; ListNodes serves from its cache once synced
	nodeFailed     bool                      // without an Observer only: the node watch failed and no node event has arrived since
	nodeAt         time.Time                 // without an Observer only: the last node informer event
	observed       bool                      // an Observer serves the freshness of pods and nodes (the 1c seam)
	pods           observe.KindState         // the Observer's pods kind, newest state
	nodesKind      observe.KindState         // the Observer's nodes kind, newest state (the node freshness barrier)
	connectHook    func()                    // test seam: runs after detection, before the first commit
	recoverHook    func()                    // test seam: runs before a recovery commit
	podInf         cache.SharedIndexInformer // without an Observer only: the metadata pod informer it replaced
	podAt          time.Time                 // without an Observer only: the last pod event
	disc           discoveryLike             // TTL-cached discovery; built lazily via c.discovery()
	healthSink     observe.Sink              // set before Start; nil = no health Observer (tests)
	health         *observe.Observer         // the cluster's health observation (1c), from Start
	healthSyncMu   sync.Mutex                // serializes state->Observer handoffs (see syncHealth)
	healthSyncHook func()                    // test seam: runs between reading state and handing it over
	paramKindsKick func()                    // S3a: refreshes FactParamKinds (set once the Observer runs)
	lbServicesSeen bool                      // S4: a LoadBalancer Service exists (the lens's gate)
	index          *fleetindex.Index         // the fleet metadata index (The Line slice 9); nil without one

	// metricsMu guards the metrics cache. Note: ClusterMetrics holds it across
	// the probe + sample network I/O (up to a few seconds). This serializes
	// concurrent callers on the same conn, which also single-flights duplicate
	// probes of the same backend. Acceptable: UI calls are infrequent and the
	// appbridge bounds them with a 30s ctx. Revisit if many callers ever fan at
	// one conn (e.g. klyx serve).
	metricsMu    sync.Mutex
	metricsState metricsCache
	metricsTF    metrics.TransportFactory // nil in production; tests inject a fake

	// am is the Alertmanager endpoint (coverage S8a).
	am alertmanagerState
	// loki is the Loki endpoint (coverage S8b).
	loki lokiState
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

// WithKubeconfig records the EFFECTIVE kubeconfig selection for this cluster -
// configured is the fleet.yaml `kubeconfig:` value (a file, an OS path list, or
// "" for client-go's defaults) - resolved NOW, alongside the REST config, to
// the absolute files client-go read. Every kubectl/helm/flux subprocess is then
// pinned to exactly those files. Without that, a context name that also exists
// in another kubeconfig (k3s's "default" is the classic collision) resolves to
// a different cluster when a child sees a different KUBECONFIG (a terminal's
// own shell environment) or a different working directory (relative paths).
// Call it right after NewClusterConn in the factory; it is not threadsafe after
// Start.
func (c *ClusterConn) WithKubeconfig(configured string) *ClusterConn {
	c.kubeconfigPaths = cluster.EffectiveKubeconfigPaths(configured)
	return c
}

// cliEnv is the extra environment for kubectl/helm/flux child processes:
// KUBECONFIG pinned to the effective selection, or nil (inherit) for a conn
// with no recorded selection.
func (c *ClusterConn) cliEnv() []string {
	return execenv.KubeconfigEnv(strings.Join(c.kubeconfigPaths, string(os.PathListSeparator)))
}

// WithCredentials sets where external endpoints' credentials come from.
func (c *ClusterConn) WithCredentials(cr Credentials) *ClusterConn {
	c.creds = cr
	return c
}

// SetMetricsConfig replaces the metrics endpoint's configuration (a Move to
// the keychain rewrote it, C5 §6; Settings reviewed a newer one) and drops
// every cached client built from the old one. The endpoint's revision moves
// in the same critical section (C5 §7): a reader already holding the old
// transport drops what it reads, and none rebuilds from the old
// configuration in between.
func (c *ClusterConn) SetMetricsConfig(mc config.MetricsConfig) {
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	c.metricsCfg = mc
	c.metricsState = metricsCache{}
	if c.creds.Revisions != nil {
		c.creds.Revisions.Bump(c.name, config.EndpointMetrics, false)
	}
}

// ChangeMetricsCredential installs a credential and the configuration it
// was reviewed with as one step for this connection's readers (C5 §7): the
// metrics lock is held from before install runs until the revision has
// moved, so no reader builds a client from the new credential with the old
// configuration, or the old credential with the new one. A client built
// before keeps its own consistent pair until the move refuses it. When
// install fails, nothing changes.
func (c *ClusterConn) ChangeMetricsCredential(mc config.MetricsConfig, install func() error) error {
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	if err := install(); err != nil {
		return err
	}
	c.metricsCfg = mc
	c.metricsState = metricsCache{}
	if c.creds.Revisions != nil {
		c.creds.Revisions.Bump(c.name, config.EndpointMetrics, true)
	}
	return nil
}

// MetricsConfig is the metrics endpoint's configuration as this connection
// uses it.
func (c *ClusterConn) MetricsConfig() config.MetricsConfig {
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	return c.metricsCfg
}

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
	defer c.syncHealth() // after the unlock below
	c.mu.Lock()
	defer c.mu.Unlock()
	if next, ok := Transition(c.state, ev); ok {
		c.state = next
		c.reason = reason
		c.overlayLocked()
	}
}

// overlayLocked applies the capability overlay to a cluster that has just
// become Synced - at the first sync and on every recovery (Stale or Failed
// -> Synced): it is Degraded at once while GitOps or the Gateway API data
// plane still requires it, so a relist can never wipe a known fault. It
// only ever acts on Synced: capability state never recovers a connection.
// Caller holds c.mu.
func (c *ClusterConn) overlayLocked() {
	if c.state != Synced {
		return
	}
	if c.caps.GitOps.Tier == capability.Degraded || c.caps.Network.Tier == capability.Degraded {
		if next, ok := Transition(c.state, EvCapUnhealthy); ok {
			c.state, c.reason = next, capabilityReason(c.caps)
		}
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
	defer c.syncHealth() // after the unlock below
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
	c.startHealth(ctx)
	c.setState(EvStart, "")
	c.ctx = ctx

	h := c.Health()
	c.mu.Lock()
	c.observed = h != nil
	c.mu.Unlock()

	// Nodes: with an Observer its nodes kind is the barrier, the freshness
	// and - through its native Ready tally, from the same capture - the
	// counts (1c slice 3 §6): no node informer of the connection's own; the
	// Nodes lens reads the nodes part's rows. Without an Observer (tests,
	// tools) the eager node informer keeps its pre-1c role.
	if h == nil {
		c.startNodeInformer(ctx)
	}

	// Pods: the health Observer's pods kind is the barrier, the count and
	// the freshness (1c 2d) - one pod watch, not two. Without an Observer
	// (tests, tools) the metadata pod informer it replaced still serves.
	if h != nil {
		h.WatchKind(healthsrc.KindPods, c.onKindState(&c.pods))       // ends with the Observer
		h.WatchKind(healthsrc.KindNodes, c.onKindState(&c.nodesKind)) // ends with the Observer
	} else {
		c.startPodInformer(ctx)
	}

	go c.refreshLoop(ctx)
	go c.connectLoop(ctx)
}

// startNodeInformer runs the eager node informer - only without an
// Observer, where it is the node barrier, freshness and counts (pre-1c).
func (c *ClusterConn) startNodeInformer(ctx context.Context) {
	nodeFactory := informers.NewSharedInformerFactory(c.typed, defaultResync)
	nodeInformer := nodeFactory.Core().V1().Nodes().Informer()
	// Strip managedFields before caching: nothing in Klyx reads them.
	// Register handlers BEFORE starting the informers. SetWatchErrorHandler
	// errors only if called after Start, which we do not do; ignore defensively.
	_ = nodeInformer.SetTransform(stripManagedFields)
	_ = nodeInformer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		c.mu.Lock()
		c.nodeFailed = true
		c.mu.Unlock()
		c.onWatchError(err)
	})
	_, _ = nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(interface{}) { c.onNodeInformerEvent() },
		UpdateFunc: func(interface{}, interface{}) { c.onNodeInformerEvent() },
		DeleteFunc: func(interface{}) { c.onNodeInformerEvent() },
	})
	c.mu.Lock()
	c.nodeInf = nodeInformer
	c.mu.Unlock()
	nodeFactory.Start(ctx.Done())
}

// onKindState applies the Observer's newest state of one kind the
// connection's freshness rests on (pods or nodes). An older state (by the
// kind's watermark) is ignored. Anything but live after a sync is a watch
// drop (Stale) - a coalesced move to a new, still-checking generation too;
// before the first sync a failure names the connect-timeout reason.
func (c *ClusterConn) onKindState(dst *observe.KindState) func(observe.KindState) {
	return func(st observe.KindState) {
		c.mu.Lock()
		if dst.Kind != "" && st.Seq < dst.Seq {
			c.mu.Unlock()
			return
		}
		*dst = st
		synced := c.state == Synced || c.state == Degraded
		c.mu.Unlock()
		if st.Coverage != observe.Live && (synced || st.Err != "") {
			msg := st.Err
			if msg == "" {
				msg = "not current (" + string(st.Coverage) + ")"
			}
			c.onWatchError(fmt.Errorf("%s: %s", st.Kind, msg))
		}
		c.signalRefresh()
	}
}

// onNodeInformerEvent handles a node informer notification (no Observer):
// the node freshness signal and a count refresh.
func (c *ClusterConn) onNodeInformerEvent() {
	c.mu.Lock()
	c.nodeFailed, c.nodeAt = false, c.clk.Now()
	c.mu.Unlock()
	c.signalRefresh()
}

// startPodInformer is the pod source without an Observer.
func (c *ClusterConn) startPodInformer(ctx context.Context) {
	metaFactory := metadatainformer.NewSharedInformerFactory(c.meta, defaultResync)
	inf := metaFactory.ForResource(podGVR).Informer()
	_ = inf.SetTransform(stripManagedFields)
	_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) { c.onWatchError(err) })
	podEvent := func() {
		c.mu.Lock()
		c.podAt = c.clk.Now()
		c.mu.Unlock()
		c.signalRefresh()
	}
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(interface{}) { podEvent() },
		UpdateFunc: func(interface{}, interface{}) { podEvent() },
		DeleteFunc: func(interface{}) { podEvent() },
	})
	c.mu.Lock()
	c.podInf = inf
	c.mu.Unlock()
	metaFactory.Start(ctx.Done())
}

// sourcesCurrentLocked is the connection's barrier: nodes and pods are
// CURRENT. With an Observer, both kinds' current states are live (an old
// live state cannot satisfy a later failed generation) and the node cache
// has listed; without one, both informers have synced and no node watch
// failure is outstanding (the pre-1c contract). Caller holds c.mu.
func (c *ClusterConn) sourcesCurrentLocked() bool {
	if c.observed {
		return c.pods.Coverage == observe.Live && c.nodesKind.Coverage == observe.Live
	}
	if c.nodeInf == nil || !c.nodeInf.HasSynced() {
		return false
	}
	return !c.nodeFailed && c.podInf != nil && c.podInf.HasSynced()
}

func (c *ClusterConn) sourcesCurrent() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sourcesCurrentLocked()
}

// commitSynced is the one Synced decision: under c.mu, it re-checks that the
// sources are current at the moment of the transition and that the state is
// still one it may move from - so a failure delivered while detection or a
// recovery was in progress can never be overwritten. overlayLocked runs on
// the commit.
func (c *ClusterConn) commitSynced(from ...ConnState) bool {
	defer c.syncHealth() // after the unlock below
	c.mu.Lock()
	defer c.mu.Unlock()
	if !slices.Contains(from, c.state) || !c.sourcesCurrentLocked() {
		return false
	}
	next, ok := Transition(c.state, EvSynced)
	if !ok {
		return false
	}
	c.state, c.reason = next, ""
	c.overlayLocked()
	return true
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
func (c *ClusterConn) connectLoop(ctx context.Context) {
	for {
		tctx, cancel := context.WithTimeout(ctx, c.connectTimeout)
		ok := cache.WaitForCacheSync(tctx.Done(), c.sourcesCurrent)
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

			c.refreshCounts()
			if c.connectHook != nil {
				c.connectHook()
			}
			// The first sync applies the initial tier from the one-shot Detect
			// (the commit's overlay) - only if nodes and pods are still current
			// now; a failure during detection sends the loop round again.
			// startCapHealth below then keeps GitOps health live via the
			// controller-workload watch; the re-probe keeps the rest current.
			if c.commitSynced(Connecting, Failed) {
				c.startCapHealth(ctx, caps)
				go c.reprobeLoop(ctx)
				return
			}
			continue
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
func (c *ClusterConn) refreshLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.refresh:
			if !c.nodesCountable() {
				continue
			}
			c.refreshCounts() // retained counts show even while a source fails

			// Recovery needs CURRENT nodes and pods, decided at the commit:
			// a node event cannot recover a connection whose pods are
			// failing, nor the reverse.
			c.mu.RLock()
			st := c.state
			c.mu.RUnlock()
			if st == Stale {
				if c.recoverHook != nil {
					c.recoverHook()
				}
				c.commitSynced(Stale)
			}
		}
	}
}

// capabilityReason is the overlay's reason: a degraded tier's own (GitOps
// first). Only a degraded tier speaks - an absent tier's reason ("no Flux
// or Argo CRDs installed") is not a fault and used to be picked up here.
func capabilityReason(caps capability.Set) string {
	switch {
	case caps.GitOps.Tier == capability.Degraded:
		return caps.GitOps.Reason
	case caps.Network.Tier == capability.Degraded:
		return caps.Network.Reason
	}
	return ""
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
	defer c.syncHealth() // after the unlock below
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caps.GitOps = g
	// GitOps recovering must not clear a Degraded that ANOTHER capability still
	// causes: the cluster stays Degraded, now for that capability's reason
	// (review 2026-10-03 F - the overlay used to be wiped by the first healthy
	// GitOps recompute while the network fault remained).
	if ev == EvCapHealthy && c.caps.Network.Tier == capability.Degraded {
		if c.state == Degraded {
			c.reason = c.caps.Network.Reason
		}
		return
	}
	if next, ok := Transition(c.state, ev); ok {
		c.state = next
		c.reason = g.Reason
	}
}

// nodesCountable: the node counts have a source - the Observer's nodes
// kind, or (without one) the synced node informer.
func (c *ClusterConn) nodesCountable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.observed {
		return true
	}
	return c.nodeInf != nil && c.nodeInf.HasSynced()
}

// refreshCounts recomputes node and pod counts and lastSync. Called by
// connectLoop on the first sync and by refreshLoop on every coalesced
// signal. lastSync is the latest successful observation - a node event, or
// the pods kind's own observation time (a complete read or a mutation) -
// never a failure and never a summary publication. A pod count is evidence
// only after a complete read; until then it is unknown, never zero, and a
// stale one is the last good count, marked.
func (c *ClusterConn) refreshCounts() {
	c.mu.RLock()
	inf := c.nodeInf
	c.mu.RUnlock()
	ready, total, known := 0, 0, false
	if inf != nil {
		nodes := make([]*corev1.Node, 0)
		for _, obj := range inf.GetStore().List() {
			if n, ok := obj.(*corev1.Node); ok {
				nodes = append(nodes, n)
			}
		}
		ready, total = NodeReadiness(nodes)
		known = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.observed {
		// the nodes kind's native Ready tally: the same capture as its state;
		// without a complete read the last known counts stand
		if t := c.nodesKind.Tally; c.nodesKind.CountKnown && t != nil {
			ready, total, known = t[healthsrc.TallyNodesReady], t[healthsrc.TallyNodesTotal], true
		}
	}
	if known {
		c.snapNodesReady, c.snapNodesTotal = ready, total
	}
	podsAt, nodesAt := c.podAt, c.nodeAt
	if c.podInf != nil {
		if c.podInf.HasSynced() {
			c.snapPods, c.podsKnown, c.podsStale = len(c.podInf.GetStore().List()), true, false
		}
	} else {
		st := c.pods
		if st.CountKnown {
			c.snapPods, c.podsKnown = st.Objects, true
		}
		c.podsStale = c.podsKnown && st.Coverage != observe.Live
		podsAt, nodesAt = time.Time{}, time.Time{}
		if st.ObservedAt != nil {
			podsAt = *st.ObservedAt
		}
		if c.nodesKind.ObservedAt != nil {
			nodesAt = *c.nodesKind.ObservedAt
		}
	}
	latest := nodesAt
	if podsAt.After(latest) {
		latest = podsAt
	}
	if latest.After(c.lastSync) {
		c.lastSync = latest
	}
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
		PodsKnown:    c.podsKnown,
		PodsStale:    c.podsStale,
		Version:      c.snapVersion,
		Capabilities: c.capsLocked(),
	}
}

// capsLocked is the capability set with what the Observer adds (S4: a
// LoadBalancer Service seen). Called under c.mu.
func (c *ClusterConn) capsLocked() capability.Set {
	caps := c.caps
	caps.LoadBalancing.ServicesSeen = c.lbServicesSeen
	caps.Alerting.Present = c.alertsPresent() // lock-free: never c.am.mu under c.mu
	caps.LogHistory.Present = c.logHistoryPresent()
	return caps
}

// setLBServicesSeen records whether a LoadBalancer Service exists.
func (c *ClusterConn) setLBServicesSeen(seen bool) {
	c.mu.Lock()
	c.lbServicesSeen = seen
	c.mu.Unlock()
}

// SetHealthSink gives the conn its health publication sink; it must be called
// before Start. Without one (fake-client tests) no Observer runs.
func (c *ClusterConn) SetHealthSink(s observe.Sink) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.healthSink = s
}

// Health returns the cluster's health Observer (nil before Start or without
// a sink).
// HealthRowsNow returns one fresh capture of a part's rows from this
// cluster's Observer (the AI Explain anchor, spec 2026-10-13 §4.1). ok is
// false without an Observer, or once it has ended.
func (c *ClusterConn) HealthRowsNow(ctx context.Context, task, part string) (observe.Rows, bool) {
	h := c.Health()
	if h == nil {
		return observe.Rows{}, false
	}
	return h.RowsNowCtx(ctx, task, part)
}

func (c *ClusterConn) Health() *observe.Observer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

// startHealth builds and starts the cluster's health Observer (1c addendum).
// It owns its own reflectors; the eager informers above remain until their
// consumers move onto the Observer.
func (c *ClusterConn) startHealth(ctx context.Context) {
	c.mu.Lock()
	sink := c.healthSink
	c.mu.Unlock()
	if sink == nil || c.typed == nil || c.meta == nil {
		return
	}
	kinds, domains := healthsrc.Core(healthsrc.CoreDeps{Cluster: c.name, Typed: c.typed, Meta: c.meta})
	if c.dyn != nil {
		nd := healthsrc.NativeDeps{Cluster: c.name, Dyn: c.dyn, Served: c.crdServed, ServedAmong: c.crdServedAmong, Typed: c.typed, Meta: c.meta}
		gk, gd := healthsrc.GitOps(nd)
		wk, wd := healthsrc.Gateway(nd)
		nk, ndoms := healthsrc.Native(nd)
		vk, vd := healthsrc.Velero(nd)
		ak, ad := healthsrc.Admission(nd)
		lk, ld := healthsrc.LoadBalancing(nd)
		kk, kd := healthsrc.KEDA(nd) // S9
		rk, rd := healthsrc.DRA(nd)  // S11b
		// the shared network kinds (C3), once, gated by their consumers
		kinds = append(append(append(append(append(append(append(kinds, healthsrc.SharedNetworkKinds(nd)...), gk...), wk...), nk...), vk...), ak...), lk...)
		domains = append(append(append(append(append(append(domains, gd...), wd...), ndoms...), vd...), ad...), ld...)
		// S9: the shared HPA kind (C3: the standalone part and KEDA), KEDA's
		// kinds, and their tasks
		kinds = append(append(kinds, healthsrc.SharedHPAKind(nd)), kk...)
		domains = append(append(domains, healthsrc.Autoscaling(nd)...), kd...)
		kinds, domains = append(kinds, rk...), append(domains, rd...)
	}
	snaps, hd := healthsrc.Helm(healthsrc.HelmDeps{Cluster: c.name, Detect: helmDetect, List: c.listHelm})
	es, ed := healthsrc.Events(healthsrc.EventsDeps{Cluster: c.name, Typed: c.typed})
	as, ad := healthsrc.Alerts(healthsrc.AlertsDeps{Cluster: c.name, Read: c.ReadAlerts}) // S8a
	snaps = append(append(snaps, es...), as...)
	domains = append(append(append(domains, hd...), ed...), ad...)
	o := observe.New(observe.Config{Cluster: c.name, Sink: sink, Kinds: kinds, Snapshots: snaps, Domains: domains, NamespacesKind: healthsrc.KindNamespaces})
	// a change of the alert source re-keys this Observer's alerts snapshot
	c.setAlertsRekey(func(key string) { o.RekeySnapshot(healthsrc.SnapAlerts, key) })
	c.mu.Lock()
	c.health = o
	c.mu.Unlock()
	o.Start(ctx)
	if c.dyn != nil {
		c.startParamKinds(ctx, o)                               // S3a: FactParamKinds
		startLBEpisodes(ctx, o, c.clk.Now, c.setLBServicesSeen) // S4: FactLBEpisodes
		startKEDAEpisodes(ctx, o, c.clk.Now, c.kedaVersion)     // S9: FactKEDAEpisodes
		startDRAEpisodes(ctx, o, c.clk.Now, c.serverMinor)      // S11b: FactDRAEpisodes
	}
	c.attachIndex()
	c.syncHealth()
}

// syncHealth hands the Observer the connection word, the capability facts
// its tasks read and each task's controller issue. Handoffs are serialized and each re-reads the
// current state, so the last one to run carries the latest state - an
// older read can never overwrite a newer one. The word never sets kind
// freshness (1c addendum §3.1). Called after c.mu is released.
func (c *ClusterConn) syncHealth() {
	c.healthSyncMu.Lock()
	defer c.healthSyncMu.Unlock()
	c.mu.RLock()
	h, st, reason, caps, hook, ix := c.health, c.state, c.reason, c.caps, c.healthSyncHook, c.index
	c.mu.RUnlock()
	if h == nil {
		return
	}
	if hook != nil {
		hook()
	}
	word := connectionWord(st, reason)
	h.SetConnection(word)
	c.mu.RLock()
	kick := c.paramKindsKick
	c.mu.RUnlock()
	if kick != nil {
		kick() // the capability re-probe refreshes the param kinds too
	}
	if ix != nil {
		ix.SetConnection(c.name, word) // the same word, in the same handoff
	}
	h.SetFact(healthsrc.FactFluxPresent, caps.GitOps.Flux.Present)
	h.SetFact(healthsrc.FactFluxOperator, caps.GitOps.FluxOperator)
	h.SetFact(healthsrc.FactVelero, healthsrc.VeleroServers(caps.Velero))
	h.SetFact(healthsrc.FactLB, healthsrc.LoadBalancingFact(caps.LoadBalancing))
	h.SetFact(healthsrc.FactKEDA, healthsrc.KEDAFactsOf(caps.KEDA))
	h.SetFact(healthsrc.FactKarpenter, healthsrc.KarpenterFactsOf(caps.Karpenter)) // S10
	h.SetFact(healthsrc.FactServerMinor, c.serverMinor())                          // S11b
	issues := healthsrc.ControllerIssues(caps)
	for _, task := range healthsrc.ControllerTasks {
		h.SetController(task, issues[task])
	}
}

// connectionWord is the health spec §2 connection axis.
func connectionWord(st ConnState, reason string) string {
	switch st {
	case Synced:
		return "connected"
	case Degraded:
		if reason != "" {
			return "connected · " + reason
		}
		return "connected"
	case Stale:
		return "stale"
	case Connecting:
		return "connecting"
	case Unconnected, Failed:
		return "unreachable"
	default:
		return "unknown"
	}
}
