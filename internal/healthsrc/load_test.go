package healthsrc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	krt "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/observe"
)

// The 1c spec §7 Go load test: six clusters on fake clients - not an API
// server cost measurement (the kind lab is). It reports every number and
// fails on a missed budget. Opt-in, and without -race (the race detector
// distorts every timing):
//
//	KLYX_LOAD=1 go test ./internal/healthsrc -run TestLoad -v -timeout 10m

type loadScale struct {
	pods, workloads, slices, nodes, flux, certs, eso, gateways, routes, claims, listenersets int
	// coverage S9: ScaledObjects (each with its HPA), half as many plain
	// HPAs, a tenth as many ScaledJobs, a quarter as many authentications
	scalers int
	// coverage S10: NodePools spread over one class per provider adapter
	pools int
	// coverage S11a (no field): a quarter of the nodes advertise 8 GPUs,
	// half of them with one not allocatable, and a tenth of the pods
	// request one, some on nodes that do not advertise it
}

// Coverage S7 §8: the large cluster carries about 500 routes and 50
// ListenerSets, its Gateways and ListenerSets with listener status.
var (
	largeScale = loadScale{pods: 5000, workloads: 1200, slices: 1500, nodes: 50, flux: 200, certs: 300, eso: 400, gateways: 20, routes: 500, claims: 50, listenersets: 50, scalers: 200, pools: 20}
	smallScale = loadScale{pods: 500, workloads: 120, slices: 150, nodes: 10, flux: 20, certs: 30, eso: 40, gateways: 2, routes: 50, claims: 5, listenersets: 5, scalers: 20, pools: 3}
)

// chaos is an API outage switch for one cluster's list/watch: while down,
// lists and watches fail and every open watch is closed.
type chaos struct {
	mu      sync.Mutex
	down    bool
	watches []watch.Interface
}

var errAPIDown = errors.New("the server is currently unable to handle the request")

func (c *chaos) outage(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down = on
	if on {
		for _, w := range c.watches {
			w.Stop()
		}
		c.watches = nil
	}
}

type chaosLW struct {
	inner cache.ListerWatcherWithContext
	c     *chaos
}

func (l chaosLW) ListWithContext(ctx context.Context, o metav1.ListOptions) (krt.Object, error) {
	l.c.mu.Lock()
	down := l.c.down
	l.c.mu.Unlock()
	if down {
		return nil, errAPIDown
	}
	return l.inner.ListWithContext(ctx, o)
}

func (l chaosLW) WatchWithContext(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	l.c.mu.Lock()
	defer l.c.mu.Unlock()
	if l.c.down {
		return nil, errAPIDown
	}
	w, err := l.inner.WatchWithContext(ctx, o)
	if err == nil {
		l.c.watches = append(l.c.watches, w)
	}
	return w, err
}

// IsWatchListSemanticsUnSupported keeps the fake's opt-out visible through
// the wrapper.
func (l chaosLW) IsWatchListSemanticsUnSupported() bool {
	u, ok := l.inner.(interface{ IsWatchListSemanticsUnSupported() bool })
	return ok && u.IsWatchListSemanticsUnSupported()
}

// loadSink records every summary with its arrival time.
type loadSink struct {
	mu   sync.Mutex
	sums []timedSummary
}

type timedSummary struct {
	at time.Time
	s  observe.Summary
}

func (s *loadSink) Summary(x observe.Summary) {
	s.mu.Lock()
	s.sums = append(s.sums, timedSummary{time.Now(), x})
	s.mu.Unlock()
}
func (s *loadSink) Rows(observe.Rows) {}

func (s *loadSink) last() (observe.Summary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sums) == 0 {
		return observe.Summary{}, false
	}
	return s.sums[len(s.sums)-1].s, true
}

// complete: every task observed (live) or confirmed absent, and the
// lifetime Namespaces kind - not a task - live too (Summary.Namespaces is
// set only then).
func complete(s observe.Summary) bool {
	if len(s.Tasks) == 0 || s.Namespaces == nil {
		return false
	}
	for _, t := range s.Tasks {
		if t.Coverage != observe.Live && t.Coverage != observe.Absent {
			return false
		}
	}
	return true
}

type loadCluster struct {
	name  string
	o     *observe.Observer
	sink  *loadSink
	chaos *chaos
	typed *fake.Clientset
	pods  []*corev1.Pod
}

const loadFact = "loadEpoch"

func TestLoad(t *testing.T) {
	if os.Getenv("KLYX_LOAD") == "" {
		t.Skip("set KLYX_LOAD=1 to run the 1c §7 load test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build every cluster's fakes first, so the baseline holds them.
	var clusters []*loadCluster
	clusters = append(clusters, buildLoadCluster(t, "large", largeScale))
	for i := 1; i <= 5; i++ {
		clusters = append(clusters, buildLoadCluster(t, fmt.Sprintf("small-%d", i), smallScale))
	}
	baseline := heapAfterGC()
	baseLive := liveHeap() // the fakes alone: the API-server stand-in

	// 1-2. First complete summary per cluster; heap above baseline for the
	// large cluster alone.
	firstComplete := map[string]time.Duration{}
	start := func(c *loadCluster) {
		t0 := time.Now()
		c.o.Start(ctx)
		waitLoad(t, 60*time.Second, c.name+" first complete summary", func() bool { s, ok := c.sink.last(); return ok && complete(s) })
		firstComplete[c.name] = time.Since(t0)
	}
	start(clusters[0])
	largeHeap := heapAfterGC() - baseline
	for _, c := range clusters[1:] {
		start(c)
	}
	defer func() {
		cancel()
		for _, c := range clusters {
			<-c.o.Done()
		}
	}()
	allHeap := heapAfterGC() - baseline

	// 3. Full evaluation of the large cluster: every part made dirty by a
	// fact they all declare; the next cycle evaluates them all.
	large := clusters[0]
	var fullEval time.Duration
	evals := 5
	if n, _ := strconv.Atoi(os.Getenv("KLYX_LOAD_EVALS")); n > 0 {
		evals = n // profiling: KLYX_LOAD_EVALS=200 ... -cpuprofile
	}
	for i := 1; i <= evals; i++ {
		before := large.o.Stats().EvalTime
		large.o.SetFact(loadFact, i)
		waitLoad(t, 10*time.Second, "full evaluation", func() bool { return large.o.Stats().EvalTime > before })
		if e := large.o.Stats().LastEval; e > fullEval {
			fullEval = e
		}
	}

	// 4. 100 pod updates/s for 30s on the large cluster.
	storm := updateStorm(t, ctx, large, 100, 30*time.Second)

	// 5. Recovery storm: every cluster's API goes down for 5s, then back.
	// Memory is the live heap marked at each GC (not HeapAlloc, whose
	// garbage swings with GC pacing), less the fakes' own: the Observers'.
	heapAfterGC()
	steadyLive := liveHeap()
	steadyAlloc := sampleHeap(3 * time.Second)
	peakLive, peakAlloc, recovered := recoveryStorm(t, clusters, 5*time.Second)
	steady, peak := steadyLive-baseLive, peakLive-baseLive

	// Report, then judge.
	names := make([]string, 0, len(firstComplete))
	for n := range firstComplete {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "\n1c §7 Go load test (fake clients)\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  first complete summary %-8s %v\n", n, firstComplete[n].Round(time.Millisecond))
	}
	fmt.Fprintf(&b, "  heap above baseline: large %.1f MB, all six %.1f MB\n", mb(largeHeap), mb(allHeap))
	fmt.Fprintf(&b, "  full evaluation, large (max of 5): %v\n", fullEval.Round(time.Microsecond))
	fmt.Fprintf(&b, "  update storm: %d updates in %v; %d publishes (%.2f/s); delivery lag p50 %v p99 %v max %v; evaluation wall-time occupancy %.1f%% of the loop (a conservative bound on its CPU)\n",
		storm.updates, storm.window.Round(time.Millisecond), storm.publishes, storm.publishRate, storm.p50.Round(time.Millisecond),
		storm.p99.Round(time.Millisecond), storm.max.Round(time.Millisecond), storm.cpuPct)
	fmt.Fprintf(&b, "  recovery storm: Observers' retained (post-GC live) heap steady %.1f MB, peak %.1f MB (%.2fx); process live heap %.1f -> %.1f MB; raw HeapAlloc %.1f -> %.1f MB (not RSS); every kind live %v after the API recovered\n",
		mb(steady), mb(peak), float64(peak)/float64(steady), mb(steadyLive), mb(peakLive), mb(steadyAlloc), mb(peakAlloc), recovered.Round(time.Millisecond))
	t.Log(b.String())

	for n, d := range firstComplete {
		check(t, d <= 5*time.Second, "first complete summary %s %v > 5s", n, d)
	}
	check(t, largeHeap <= 96<<20, "large cluster heap %.1f MB > 96 MB", mb(largeHeap))
	check(t, fullEval <= 50*time.Millisecond, "full evaluation %v > 50ms", fullEval)
	check(t, storm.publishRate <= 1.05, "publishes %.2f/s > 1/s", storm.publishRate)
	check(t, storm.p99 <= 1200*time.Millisecond, "delivery lag p99 %v > 1.2s", storm.p99)
	check(t, storm.cpuPct <= 10, "evaluation wall-time occupancy %.1f%% > 10%%", storm.cpuPct)
	check(t, float64(peak) <= 1.5*float64(steady), "recovery peak heap %.2fx > 1.5x", float64(peak)/float64(steady))
	check(t, recovered <= 15*time.Second, "recovery %v > 15s", recovered)
}

func check(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Errorf("budget missed: "+format, args...)
	}
}

type stormResult struct {
	updates, publishes int
	window             time.Duration
	publishRate        float64
	p50, p99, max      time.Duration
	cpuPct             float64
}

// updateStorm changes one pod's status at rate/s for d. Each update is one
// model mutation of the pods kind, so the pods part's data watermark (Obs)
// counts them: update k is delivered by the first summary whose Obs has
// advanced k times.
func updateStorm(t *testing.T, ctx context.Context, c *loadCluster, rate int, d time.Duration) stormResult {
	t.Helper()
	podsObs := func(s observe.Summary) uint64 {
		for _, tk := range s.Tasks {
			if tk.ID == TaskPods {
				for _, p := range tk.Parts {
					return p.Obs
				}
			}
		}
		return 0
	}
	s0, _ := c.sink.last()
	base := podsObs(s0)
	c.sink.mu.Lock()
	firstIdx := len(c.sink.sums)
	c.sink.mu.Unlock()
	eval0 := c.o.Stats().EvalTime

	interval := time.Second / time.Duration(rate)
	var sent []time.Time
	t0 := time.Now()
	tick := time.NewTicker(interval)
	for i := 0; time.Since(t0) < d; i++ {
		<-tick.C
		p := c.pods[i%len(c.pods)].DeepCopy()
		p.Status.ContainerStatuses[0].RestartCount = int32(i + 1)
		if _, err := c.typed.CoreV1().Pods(p.Namespace).UpdateStatus(ctx, p, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		c.pods[i%len(c.pods)] = p
		sent = append(sent, time.Now())
	}
	tick.Stop()
	window := time.Since(t0)
	evalSpent := c.o.Stats().EvalTime - eval0
	want := base + uint64(len(sent))
	waitLoad(t, 10*time.Second, "the storm delivered", func() bool { s, _ := c.sink.last(); return podsObs(s) >= want })

	c.sink.mu.Lock()
	sums := append([]timedSummary(nil), c.sink.sums[firstIdx:]...)
	c.sink.mu.Unlock()
	var lags []time.Duration
	j := 0
	for k, at := range sent {
		for j < len(sums) && podsObs(sums[j].s) < base+uint64(k+1) {
			j++
		}
		if j < len(sums) {
			lags = append(lags, sums[j].at.Sub(at))
		}
	}
	sort.Slice(lags, func(a, b int) bool { return lags[a] < lags[b] })
	inWindow := 0
	for _, s := range sums {
		if s.at.Sub(t0) <= window {
			inWindow++
		}
	}
	pct := func(q float64) time.Duration { return lags[int(q*float64(len(lags)-1))] }
	return stormResult{updates: len(sent), publishes: inWindow, window: window, publishRate: float64(inWindow) / window.Seconds(),
		p50: pct(0.5), p99: pct(0.99), max: lags[len(lags)-1], cpuPct: 100 * evalSpent.Seconds() / window.Seconds()}
}

// recoveryStorm takes every cluster's API down at once, brings it back, and
// reports the peak heap during the storm and how long after recovery every
// kind was live again.
func recoveryStorm(t *testing.T, clusters []*loadCluster, outage time.Duration) (peakLive, peakAlloc uint64, recovered time.Duration) {
	t.Helper()
	stop := make(chan struct{})
	peakCh := make(chan [2]uint64, 1)
	go func() {
		var m runtime.MemStats
		var live, alloc uint64
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			runtime.ReadMemStats(&m)
			alloc = max(alloc, m.HeapAlloc)
			live = max(live, liveHeap())
			select {
			case <-stop:
				peakCh <- [2]uint64{live, alloc}
				return
			case <-tk.C:
			}
		}
	}()
	for _, c := range clusters {
		c.chaos.outage(true)
	}
	waitLoad(t, 60*time.Second, "every cluster sees the outage", func() bool {
		for _, c := range clusters {
			s, _ := c.sink.last()
			if complete(s) {
				return false
			}
		}
		return true
	})
	time.Sleep(outage)
	up := time.Now()
	for _, c := range clusters {
		c.chaos.outage(false)
	}
	waitLoad(t, 120*time.Second, "every kind live again", func() bool {
		for _, c := range clusters {
			if s, _ := c.sink.last(); !complete(s) {
				return false
			}
		}
		return true
	})
	recovered = time.Since(up)
	heapAfterGC() // the recovered state's own live heap counts too
	time.Sleep(50 * time.Millisecond)
	close(stop)
	p := <-peakCh
	return p[0], p[1], recovered
}

// liveHeap is the heap marked live by the most recent GC.
func liveHeap() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

func waitLoad(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(limit); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func heapAfterGC() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// sampleHeap: the highest live heap over d of normal operation (no forced
// GC), the fair comparison for a storm's peak.
func sampleHeap(d time.Duration) uint64 {
	var m runtime.MemStats
	var hi uint64
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		runtime.ReadMemStats(&m)
		if m.HeapAlloc > hi {
			hi = m.HeapAlloc
		}
	}
	return hi
}

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

// ---- fixtures ---------------------------------------------------------------

var loadGVRs = map[schema.GroupVersionResource]string{}

func init() {
	add := func(group, version, resource, list string) {
		loadGVRs[schema.GroupVersionResource{Group: group, Version: version, Resource: resource}] = list
	}
	add("cert-manager.io", "v1", "certificates", "CertificateList")
	add("cert-manager.io", "v1", "issuers", "IssuerList")
	add("cert-manager.io", "v1", "clusterissuers", "ClusterIssuerList")
	add("external-secrets.io", "v1beta1", "externalsecrets", "ExternalSecretList")
	add("external-secrets.io", "v1beta1", "secretstores", "SecretStoreList")
	add("external-secrets.io", "v1beta1", "clustersecretstores", "ClusterSecretStoreList")
	add("karpenter.sh", "v1", "nodepools", "NodePoolList")
	add("karpenter.sh", "v1", "nodeclaims", "NodeClaimList")
	// coverage S10: the class kinds
	add("karpenter.k8s.aws", "v1", "ec2nodeclasses", "EC2NodeClassList")
	add("karpenter.azure.com", "v1beta1", "aksnodeclasses", "AKSNodeClassList")
	add("eks.amazonaws.com", "v1", "nodeclasses", "NodeClassList")
	add("kustomize.toolkit.fluxcd.io", "v1", "kustomizations", "KustomizationList")
	add("helm.toolkit.fluxcd.io", "v2", "helmreleases", "HelmReleaseList")
	add("source.toolkit.fluxcd.io", "v1", "gitrepositories", "GitRepositoryList")
	add("source.toolkit.fluxcd.io", "v1beta2", "ocirepositories", "OCIRepositoryList")
	add("source.toolkit.fluxcd.io", "v1", "buckets", "BucketList")
	add("source.toolkit.fluxcd.io", "v1", "helmrepositories", "HelmRepositoryList")
	add("source.toolkit.fluxcd.io", "v1", "helmcharts", "HelmChartList")
	add("argoproj.io", "v1alpha1", "applications", "ApplicationList")
	add("argoproj.io", "v1alpha1", "applicationsets", "ApplicationSetList")
	add("gateway.networking.k8s.io", "v1", "gatewayclasses", "GatewayClassList")
	add("gateway.networking.k8s.io", "v1", "gateways", "GatewayList")
	add("gateway.networking.k8s.io", "v1", "httproutes", "HTTPRouteList")
	add("gateway.networking.k8s.io", "v1", "listenersets", "ListenerSetList")
	add("gateway.networking.k8s.io", "v1", "grpcroutes", "GRPCRouteList")
	add("gateway.networking.k8s.io", "v1", "tlsroutes", "TLSRouteList")
	add("gateway.networking.k8s.io", "v1", "tcproutes", "TCPRouteList")
	add("gateway.networking.k8s.io", "v1", "udproutes", "UDPRouteList")
	add("gateway.networking.k8s.io", "v1", "referencegrants", "ReferenceGrantList")
	// coverage S1a-S1c: every group is served here, so every watched kind
	// is registered
	add("fluxcd.controlplane.io", "v1", "fluxinstances", "FluxInstanceList")
	add("fluxcd.controlplane.io", "v1", "fluxreports", "FluxReportList")
	add("fluxcd.controlplane.io", "v1", "resourcesets", "ResourceSetList")
	add("fluxcd.controlplane.io", "v1", "resourcesetinputproviders", "ResourceSetInputProviderList")
	add("image.toolkit.fluxcd.io", "v1", "imagerepositories", "ImageRepositoryList")
	add("image.toolkit.fluxcd.io", "v1", "imagepolicies", "ImagePolicyList")
	add("image.toolkit.fluxcd.io", "v1", "imageupdateautomations", "ImageUpdateAutomationList")
	add("source.extensions.fluxcd.io", "v1beta1", "artifactgenerators", "ArtifactGeneratorList")
	add("source.toolkit.fluxcd.io", "v1", "externalartifacts", "ExternalArtifactList")
	add("notification.toolkit.fluxcd.io", "v1", "receivers", "ReceiverList")
	add("velero.io", "v1", "schedules", "ScheduleList")
	add("velero.io", "v1", "backupstoragelocations", "BackupStorageLocationList")
	add("velero.io", "v1", "backups", "BackupList")
	for _, r := range [][2]string{{"validatingwebhookconfigurations", "ValidatingWebhookConfigurationList"}, {"mutatingwebhookconfigurations", "MutatingWebhookConfigurationList"},
		{"validatingadmissionpolicies", "ValidatingAdmissionPolicyList"}, {"mutatingadmissionpolicies", "MutatingAdmissionPolicyList"},
		{"validatingadmissionpolicybindings", "ValidatingAdmissionPolicyBindingList"}, {"mutatingadmissionpolicybindings", "MutatingAdmissionPolicyBindingList"}} {
		add("admissionregistration.k8s.io", "v1", r[0], r[1])
	}
	add("metallb.io", "v1beta1", "ipaddresspools", "IPAddressPoolList")
	add("metallb.io", "v1beta1", "l2advertisements", "L2AdvertisementList")
	add("metallb.io", "v1beta1", "bgpadvertisements", "BGPAdvertisementList")
	add("metallb.io", "v1beta1", "configurationstates", "ConfigurationStateList")
	add("cilium.io", "v2", "ciliumloadbalancerippools", "CiliumLoadBalancerIPPoolList")
	// coverage S9
	add("keda.sh", "v1alpha1", "scaledobjects", "ScaledObjectList")
	add("keda.sh", "v1alpha1", "scaledjobs", "ScaledJobList")
	add("keda.sh", "v1alpha1", "triggerauthentications", "TriggerAuthenticationList")
	add("keda.sh", "v1alpha1", "clustertriggerauthentications", "ClusterTriggerAuthenticationList")
}

func buildLoadCluster(t *testing.T, name string, sc loadScale) *loadCluster {
	t.Helper()
	ctx := context.Background()
	created := metav1.NewTime(time.Now().Add(-48 * time.Hour))
	long := strings.Repeat("the container image could not be pulled from the registry because ", 4)
	labels := func(app string, i int) map[string]string {
		l := map[string]string{"app": app, "app.kubernetes.io/name": app, "app.kubernetes.io/instance": app + "-prd",
			"app.kubernetes.io/part-of": "shop", "app.kubernetes.io/managed-by": "Helm", "pod-template-hash": "7d9f8c6b5"}
		for k := len(l); k < 15; k++ {
			l[fmt.Sprintf("team.example.com/label-%d", k)] = fmt.Sprintf("value-%d-%d", i, k)
		}
		return l
	}
	ns := func(i int) string { return fmt.Sprintf("ns-%02d", i%50) }

	var typedObjs, metaObjs []krt.Object
	deps := sc.workloads * 10 / 12
	sts := sc.workloads / 8
	dss := sc.workloads - deps - sts
	one := int32(1)
	for i := 0; i < deps; i++ {
		app := fmt.Sprintf("app-%04d", i)
		typedObjs = append(typedObjs, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: ns(i), UID: types.UID("dep-" + app), Generation: 1, CreationTimestamp: created, Labels: labels(app, i)},
			Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "shop/" + app + ":1.0"}}}}},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable", Message: long}}},
		})
		metaObjs = append(metaObjs, &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"},
			ObjectMeta: metav1.ObjectMeta{Name: app + "-rs", Namespace: ns(i), UID: types.UID("rs-" + app),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: app, UID: types.UID("dep-" + app), Controller: ptr(true)}}}})
	}
	for i := 0; i < sts; i++ {
		app := fmt.Sprintf("db-%04d", i)
		typedObjs = append(typedObjs, &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: ns(i), UID: types.UID("sts-" + app), Generation: 1, CreationTimestamp: created, Labels: labels(app, i)},
			Spec:       appsv1.StatefulSetSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}},
			Status:     appsv1.StatefulSetStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1, CurrentReplicas: 1},
		})
	}
	for i := 0; i < dss; i++ {
		app := fmt.Sprintf("agent-%04d", i)
		typedObjs = append(typedObjs, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: ns(i), UID: types.UID("ds-" + app), Generation: 1, CreationTimestamp: created, Labels: labels(app, i)},
			Spec:       appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}},
			Status:     appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 3, NumberReady: 3, NumberAvailable: 3, UpdatedNumberScheduled: 3, CurrentNumberScheduled: 3},
		})
	}
	var pods []*corev1.Pod
	for i := 0; i < sc.pods; i++ {
		app := fmt.Sprintf("app-%04d", i%deps)
		status := corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &created,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: created, Message: long},
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: created, Message: long}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "main", Ready: true, Image: "shop/" + app + ":1.0", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: created}}}}}
		if i%20 == 0 { // 5% failing
			status.Phase = corev1.PodPending
			status.Conditions = nil
			status.ContainerStatuses[0].Ready = false
			status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: long}}
		}
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%05d", app, i), Namespace: ns(i % deps), UID: types.UID(fmt.Sprintf("pod-%05d", i)), Labels: labels(app, i), CreationTimestamp: created,
				Annotations:     map[string]string{"kubectl.kubernetes.io/restartedAt": created.String(), "checksum/config": strings.Repeat("a", 64)},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: app + "-rs", UID: types.UID("rs-" + app), Controller: ptr(true)}}},
			Spec: corev1.PodSpec{NodeName: fmt.Sprintf("node-%02d", i%sc.nodes), RestartPolicy: corev1.RestartPolicyAlways,
				Containers: []corev1.Container{{Name: "main", Image: "shop/" + app + ":1.0", Env: []corev1.EnvVar{{Name: "A", Value: long}}}}},
			Status: status,
		}
		if i%10 == 0 { // coverage S11a: a tenth of the pods request a GPU
			gpu := corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}
			p.Spec.Containers[0].Resources = corev1.ResourceRequirements{Requests: gpu, Limits: gpu}
		}
		pods = append(pods, p)
		typedObjs = append(typedObjs, p)
	}
	for i := 0; i < sc.nodes; i++ {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%02d", i), UID: types.UID(fmt.Sprintf("node-%02d", i)), Labels: labels("node", i)},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Message: long}}}}
		if i%4 == 0 { // coverage S11a: a quarter of the nodes advertise GPUs, half of those with one not allocatable
			alloc := "8"
			if i%8 == 4 {
				alloc = "7"
			}
			n.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("32"), corev1.ResourceMemory: resource.MustParse("256Gi"), "nvidia.com/gpu": resource.MustParse("8")}
			n.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("31"), corev1.ResourceMemory: resource.MustParse("250Gi"), "nvidia.com/gpu": resource.MustParse(alloc)}
			for k, v := range map[string]string{"nvidia.com/gpu.product": "NVIDIA-H100-80GB-HBM3", "nvidia.com/gpu.memory": "81559", "nvidia.com/gpu.count": "8",
				"nvidia.com/gpu.family": "hopper", "nvidia.com/cuda.driver-version.full": "550.54.15", "nvidia.com/mig.strategy": "none"} {
				n.Labels[k] = v
			}
		}
		typedObjs = append(typedObjs, n)
	}
	for i := 0; i < 50; i++ {
		metaObjs = append(metaObjs, &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: ns(i), UID: types.UID(ns(i)),
			Labels: map[string]string{"kubernetes.io/metadata.name": ns(i), "team": fmt.Sprintf("team-%d", i%8)}}})
	}
	svcs := sc.routes
	for i := 0; i < svcs; i++ {
		// the shared Services kind is a typed watch (C3)
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("svc-%04d", i), Namespace: ns(i), UID: types.UID(fmt.Sprintf("svc-%04d", i))},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "x"}, Ports: []corev1.ServicePort{{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP}}}}
		if i%10 == 0 { // coverage S4: every tenth a LoadBalancer, half of them pending
			svc.Spec.Type = corev1.ServiceTypeLoadBalancer
			svc.Annotations = map[string]string{"metallb.io/address-pool": "pool-00"}
			if i%20 == 0 {
				svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: fmt.Sprintf("192.168.%d.%d", i/250, i%250)}}
			}
		}
		typedObjs = append(typedObjs, svc)
	}
	for i := 0; i < sc.slices; i++ {
		svc := fmt.Sprintf("svc-%04d", i%svcs)
		ready := true
		es := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%05d", svc, i), Namespace: ns(i % svcs), UID: types.UID(fmt.Sprintf("es-%05d", i)),
			Labels: map[string]string{discoveryv1.LabelServiceName: svc}}, AddressType: discoveryv1.AddressTypeIPv4,
			Ports: []discoveryv1.EndpointPort{{Name: ptr("http"), Protocol: ptr(corev1.ProtocolTCP)}}}
		for e := 0; e < 3; e++ {
			es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{Addresses: []string{fmt.Sprintf("10.%d.%d.%d", i/250, i%250, e)}, Conditions: discoveryv1.EndpointConditions{Ready: &ready},
				TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: ns(i % svcs), Name: fmt.Sprintf("p-%05d-%d", i, e), UID: types.UID(fmt.Sprintf("p-%05d-%d", i, e))}})
		}
		typedObjs = append(typedObjs, es)
	}
	// coverage S9: each ScaledObject's HPA (controlled by it) and the plain
	// HPAs, healthy, as kind's HPA controller writes them
	hpaConds := []autoscalingv2.HorizontalPodAutoscalerCondition{
		{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ReadyForNewScale", Message: "recommended size matches current size", LastTransitionTime: created},
		{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue, Reason: "ValidMetricFound", Message: "the HPA was able to successfully calculate a replica count from external metric s0-prometheus", LastTransitionTime: created},
		{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionFalse, Reason: "DesiredWithinRange", Message: "the desired count is within the acceptable range", LastTransitionTime: created}}
	og, min := int64(1), int32(1)
	hpaOf := func(name, namespace, target string, owner *metav1.OwnerReference) *autoscalingv2.HorizontalPodAutoscaler {
		h := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name), Generation: 1, CreationTimestamp: created},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: target},
				MinReplicas: &min, MaxReplicas: 10, Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ExternalMetricSourceType}}},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{ObservedGeneration: &og, CurrentReplicas: 2, DesiredReplicas: 2, Conditions: hpaConds}}
		if owner != nil {
			h.OwnerReferences = []metav1.OwnerReference{*owner}
		}
		return h
	}
	for i := 0; i < sc.scalers; i++ {
		so := fmt.Sprintf("so-%04d", i)
		typedObjs = append(typedObjs, hpaOf("keda-hpa-"+so, ns(i), so, &metav1.OwnerReference{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: so, UID: types.UID(so), Controller: ptr(true)}))
	}
	for i := 0; i < sc.scalers/2; i++ {
		typedObjs = append(typedObjs, hpaOf(fmt.Sprintf("hpa-%04d", i), ns(i), fmt.Sprintf("plain-%04d", i), nil))
	}
	typed := fake.NewClientset(typedObjs...)
	scheme := krt.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	meta := metafake.NewSimpleMetadataClient(scheme, metaObjs...)

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(krt.NewScheme(), loadGVRs)
	create := func(group, version, resource string, u *unstructured.Unstructured) {
		gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
		if _, err := dyn.Resource(gvr).Namespace(u.GetNamespace()).Create(ctx, u, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// coverage S3a: webhook configurations (two webhooks each, on the
	// shared Services) and bound policies, scaled with the gateways
	for i := 0; i < 2*sc.gateways; i++ {
		hook := func(n int) map[string]any {
			return map[string]any{"name": fmt.Sprintf("h%d.load.klyx.dev", n), "failurePolicy": "Fail", "sideEffects": "None", "admissionReviewVersions": []any{"v1"},
				"clientConfig": map[string]any{"service": map[string]any{"namespace": ns(i), "name": fmt.Sprintf("svc-%04d", i%svcs), "port": int64(443)}},
				"rules":        []any{map[string]any{"operations": []any{"CREATE"}, "apiGroups": []any{"lab.klyx.dev"}, "apiVersions": []any{"v1"}, "resources": []any{"widgets"}}}}
		}
		create("admissionregistration.k8s.io", "v1", "validatingwebhookconfigurations", &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration",
			"metadata": map[string]any{"name": fmt.Sprintf("load-%03d", i), "uid": fmt.Sprintf("vwc-%03d", i)}, "webhooks": []any{hook(0), hook(1)}}})
		create("admissionregistration.k8s.io", "v1", "validatingadmissionpolicies", &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy",
			"metadata": map[string]any{"name": fmt.Sprintf("load-%03d", i), "uid": fmt.Sprintf("vap-%03d", i)}, "spec": map[string]any{"validations": []any{map[string]any{"expression": "true"}}}}})
		create("admissionregistration.k8s.io", "v1", "validatingadmissionpolicybindings", &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding",
			"metadata": map[string]any{"name": fmt.Sprintf("load-%03d-b", i), "uid": fmt.Sprintf("vapb-%03d", i)}, "spec": map[string]any{"policyName": fmt.Sprintf("load-%03d", i), "validationActions": []any{"Deny"}}}})
	}
	// coverage S4: MetalLB pools with an advertisement and a controller
	// ConfigurationState, and Cilium pools with their counts
	for i := 0; i < sc.gateways; i++ {
		create("metallb.io", "v1beta1", "ipaddresspools", &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metallb.io/v1beta1", "kind": "IPAddressPool",
			"metadata": map[string]any{"name": fmt.Sprintf("pool-%02d", i), "namespace": "metallb-system", "uid": fmt.Sprintf("mp-%02d", i), "labels": map[string]any{"tier": "gold"}},
			"spec":     map[string]any{"addresses": []any{fmt.Sprintf("192.168.%d.0/24", i)}},
			"status":   map[string]any{"assignedIPv4": int64(10), "availableIPv4": int64(244), "assignedIPv6": int64(0), "availableIPv6": int64(0)}}})
		create("cilium.io", "v2", "ciliumloadbalancerippools", &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cilium.io/v2", "kind": "CiliumLoadBalancerIPPool",
			"metadata": map[string]any{"name": fmt.Sprintf("cpool-%02d", i), "uid": fmt.Sprintf("cp-%02d", i), "generation": int64(1)},
			"spec":     map[string]any{"blocks": []any{map[string]any{"cidr": fmt.Sprintf("10.200.%d.0/24", i)}}},
			"status": map[string]any{"conditions": []any{
				map[string]any{"type": "cilium.io/IPsTotal", "status": "Unknown", "reason": "noreason", "message": "256", "observedGeneration": int64(1), "lastTransitionTime": "2026-10-18T00:00:00Z"},
				map[string]any{"type": "cilium.io/IPsAvailable", "status": "Unknown", "reason": "noreason", "message": "250", "observedGeneration": int64(1), "lastTransitionTime": "2026-10-18T00:00:00Z"},
				map[string]any{"type": "cilium.io/IPsUsed", "status": "Unknown", "reason": "noreason", "message": "6", "observedGeneration": int64(1), "lastTransitionTime": "2026-10-18T00:00:00Z"}}}}})
	}
	create("metallb.io", "v1beta1", "l2advertisements", &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metallb.io/v1beta1", "kind": "L2Advertisement",
		"metadata": map[string]any{"name": "l2", "namespace": "metallb-system", "uid": "l2-0"},
		"spec":     map[string]any{"ipAddressPoolSelectors": []any{map[string]any{"matchLabels": map[string]any{"tier": "gold"}}}}}})
	create("metallb.io", "v1beta1", "configurationstates", &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metallb.io/v1beta1", "kind": "ConfigurationState",
		"metadata": map[string]any{"name": "controller", "namespace": "metallb-system", "uid": "cs-0"}, "status": map[string]any{"result": "Valid"}}})
	readyIf := func(i int) string {
		if i%25 == 0 {
			return "False"
		}
		return "True"
	}
	for i := 0; i < sc.flux; i++ {
		switch i % 4 {
		case 0, 1:
			create("kustomize.toolkit.fluxcd.io", "v1", "kustomizations", cr("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", fmt.Sprintf("ks-%04d", i),
				map[string]any{"path": "./apps", "sourceRef": map[string]any{"kind": "GitRepository", "name": "repo-0000"}}, []any{cond("Ready", readyIf(i), "ReconciliationSucceeded")}, nil))
		case 2:
			create("helm.toolkit.fluxcd.io", "v2", "helmreleases", cr("helm.toolkit.fluxcd.io/v2", "HelmRelease", ns(i), fmt.Sprintf("hr-%04d", i),
				map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "web"}}}, []any{cond("Ready", readyIf(i), "UpgradeSucceeded")}, nil))
		default:
			create("source.toolkit.fluxcd.io", "v1", "gitrepositories", cr("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", fmt.Sprintf("repo-%04d", i),
				map[string]any{"url": "https://git.example/shop"}, []any{cond("Ready", readyIf(i), "Succeeded")}, nil))
		}
	}
	for i := 0; i < sc.flux/4; i++ {
		u := cr("argoproj.io/v1alpha1", "Application", "argocd", fmt.Sprintf("app-%04d", i), map[string]any{"project": "default",
			"source": map[string]any{"repoURL": "https://git.example/shop", "path": "deploy"}, "destination": map[string]any{"namespace": ns(i), "server": "https://kubernetes.default.svc"}}, nil, nil)
		u.Object["status"] = map[string]any{"sync": map[string]any{"status": "Synced"}, "health": map[string]any{"status": "Healthy"}}
		create("argoproj.io", "v1alpha1", "applications", u)
	}
	now := time.Now()
	for i := 0; i < sc.certs; i++ {
		create("cert-manager.io", "v1", "certificates", cr("cert-manager.io/v1", "Certificate", ns(i), fmt.Sprintf("tls-%04d", i),
			map[string]any{"secretName": "tls", "dnsNames": []any{"shop.example"}, "issuerRef": map[string]any{"name": "le", "kind": "ClusterIssuer"}},
			[]any{cond("Ready", readyIf(i), "Ready")}, map[string]any{"notAfter": now.Add(30 * 24 * time.Hour).Format(time.RFC3339)}))
	}
	create("cert-manager.io", "v1", "clusterissuers", cr("cert-manager.io/v1", "ClusterIssuer", "", "le", map[string]any{}, []any{cond("Ready", "True", "ACMEAccountRegistered")}, nil))
	for i := 0; i < 10; i++ {
		create("cert-manager.io", "v1", "issuers", cr("cert-manager.io/v1", "Issuer", ns(i), "ca", map[string]any{}, []any{cond("Ready", "True", "KeyPairVerified")}, nil))
	}
	for i := 0; i < sc.eso; i++ {
		create("external-secrets.io", "v1beta1", "externalsecrets", cr("external-secrets.io/v1beta1", "ExternalSecret", ns(i), fmt.Sprintf("es-%04d", i),
			map[string]any{"refreshInterval": "1h", "secretStoreRef": map[string]any{"name": "vault", "kind": "ClusterSecretStore"}},
			[]any{cond("Ready", readyIf(i), "SecretSynced")}, map[string]any{"refreshTime": now.Add(-10 * time.Minute).Format(time.RFC3339)}))
	}
	create("external-secrets.io", "v1beta1", "clustersecretstores", cr("external-secrets.io/v1beta1", "ClusterSecretStore", "", "vault",
		map[string]any{"provider": map[string]any{"vault": map[string]any{}}}, []any{cond("Ready", "True", "Valid")}, nil))
	create("gateway.networking.k8s.io", "v1", "gatewayclasses", cr("gateway.networking.k8s.io/v1", "GatewayClass", "", "eg", map[string]any{"controllerName": "gateway.envoyproxy.io/gatewayclass-controller"},
		[]any{cond("Accepted", "True", "Accepted")}, nil))
	for i := 0; i < sc.gateways; i++ {
		u := gw(fmt.Sprintf("gw-%02d", i), "True")
		u.SetUID(types.UID(fmt.Sprintf("gw-%02d", i)))
		loadListeners(u, 1)
		u.Object["spec"].(map[string]any)["allowedListeners"] = map[string]any{"namespaces": map[string]any{"from": "All"}}
		create("gateway.networking.k8s.io", "v1", "gateways", u)
	}
	for i := 0; i < sc.listenersets; i++ {
		u := cr("gateway.networking.k8s.io/v1", "ListenerSet", ns(i), fmt.Sprintf("ls-%03d", i), map[string]any{"parentRef": map[string]any{"name": fmt.Sprintf("gw-%02d", i%sc.gateways), "namespace": "infra"}},
			[]any{cond("Accepted", "True", "Accepted"), cond("Programmed", "True", "Programmed")}, nil)
		u.SetUID(types.UID(fmt.Sprintf("ls-%03d", i)))
		u.SetCreationTimestamp(created)
		loadListeners(u, 2)
		create("gateway.networking.k8s.io", "v1", "listenersets", u)
	}
	// coverage S7b: four routes in ten are of the other kinds, one each; the
	// UDPRoutes' backends publish TCP only (no-port), as a real mix would
	others := []struct{ kind, resource string }{{"GRPCRoute", "grpcroutes"}, {"TLSRoute", "tlsroutes"}, {"TCPRoute", "tcproutes"}, {"UDPRoute", "udproutes"}}
	for i := 0; i < sc.routes; i++ {
		u := httpRoute(fmt.Sprintf("route-%04d", i), []string{fmt.Sprintf("gw-%02d", i%sc.gateways)}, fmt.Sprintf("svc-%04d", i%svcs), nil)
		u.SetNamespace(ns(i))
		u.SetUID(types.UID(fmt.Sprintf("route-%04d", i)))
		resource := "httproutes"
		if k := i % 10; k >= 6 {
			u.SetKind(others[k-6].kind)
			resource = others[k-6].resource
		}
		create("gateway.networking.k8s.io", "v1", resource, u)
	}
	for i := 0; i < sc.gateways; i++ {
		create("gateway.networking.k8s.io", "v1", "referencegrants", cr("gateway.networking.k8s.io/v1", "ReferenceGrant", ns(i), fmt.Sprintf("grant-%02d", i), map[string]any{
			"from": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "namespace": "shop"}},
			"to":   []any{map[string]any{"group": "", "kind": "Service"}}}, nil, nil))
	}
	// coverage S9: ScaledObjects, ScaledJobs and authentications, as KEDA
	// 2.21 writes their status (no condition times)
	kc := func(t, status, reason, msg string) map[string]any {
		return map[string]any{"type": t, "status": status, "reason": reason, "message": msg}
	}
	soConds := []any{kc("Ready", "True", "ScaledObjectReady", "ScaledObject is defined correctly and is ready for scaling"),
		kc("Active", "True", "ScalerActive", "Scaling is performed because triggers are active"),
		kc("Fallback", "False", "NoFallbackFound", "No fallbacks are active on this scaled object"), kc("Paused", "False", "", ""), kc("HPAActive", "True", "HPAActive", "HPA is actively scaling")}
	trigger := []any{map[string]any{"type": "prometheus", "metadata": map[string]any{"serverAddress": "http://prometheus.monitoring.svc:9090", "query": "sum(rate(http_requests_total[1m]))", "threshold": "10"},
		"authenticationRef": map[string]any{"name": "auth-0000"}}}
	for i := 0; i < sc.scalers; i++ {
		name := fmt.Sprintf("so-%04d", i)
		u := cr("keda.sh/v1alpha1", "ScaledObject", ns(i), name, map[string]any{"scaleTargetRef": map[string]any{"name": name}, "minReplicaCount": int64(1), "maxReplicaCount": int64(10), "triggers": trigger},
			soConds, map[string]any{"hpaName": "keda-hpa-" + name, "externalMetricNames": []any{"s0-prometheus"}, "triggersActivity": map[string]any{"s0-prometheus": map[string]any{"isActive": true}},
				"scaleTargetGVKR": map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "resource": "deployments"}, "scaleTargetKind": "apps/v1.Deployment"})
		u.SetUID(types.UID(name))
		u.SetCreationTimestamp(created)
		create("keda.sh", "v1alpha1", "scaledobjects", u)
	}
	for i := 0; i < sc.scalers/10; i++ {
		u := cr("keda.sh/v1alpha1", "ScaledJob", ns(i), fmt.Sprintf("sj-%04d", i), map[string]any{"jobTargetRef": map[string]any{"template": map[string]any{}}, "triggers": trigger},
			[]any{kc("Ready", "True", "ScaledJobReady", "ScaledJob is defined correctly and is ready for scaling"), kc("Active", "False", "ScalerNotActive", "Scaling is not performed because triggers are not active")}, nil)
		u.SetCreationTimestamp(created)
		create("keda.sh", "v1alpha1", "scaledjobs", u)
	}
	for i := 0; i < sc.scalers/4; i++ {
		create("keda.sh", "v1alpha1", "triggerauthentications", cr("keda.sh/v1alpha1", "TriggerAuthentication", ns(i), fmt.Sprintf("auth-%04d", i),
			map[string]any{"secretTargetRef": []any{map[string]any{"parameter": "bearerToken", "name": "prom-token", "key": "token"}}}, nil, nil))
	}
	// coverage S10: one class per adapter, Ready, and pools spread over
	// them, each with its class link
	classes := []struct{ apiVersion, kind, resource string }{
		{"karpenter.k8s.aws/v1", "EC2NodeClass", "ec2nodeclasses"}, {"karpenter.azure.com/v1beta1", "AKSNodeClass", "aksnodeclasses"}, {"eks.amazonaws.com/v1", "NodeClass", "nodeclasses"}}
	for _, c := range classes {
		gv := strings.Split(c.apiVersion, "/")
		create(gv[0], gv[1], c.resource, cr(c.apiVersion, c.kind, "", "default", map[string]any{}, []any{cond("SubnetsReady", "True", "SubnetsReady"), cond("Ready", "True", "Ready")}, nil))
	}
	for i := 0; i < sc.pools; i++ {
		c := classes[i%len(classes)]
		name := "default"
		if i > 0 {
			name = fmt.Sprintf("pool-%02d", i)
		}
		create("karpenter.sh", "v1", "nodepools", cr("karpenter.sh/v1", "NodePool", "", name, map[string]any{"template": map[string]any{"spec": map[string]any{
			"nodeClassRef": map[string]any{"group": strings.Split(c.apiVersion, "/")[0], "kind": c.kind, "name": "default"}}}},
			[]any{cond("ValidationSucceeded", "True", "ValidationSucceeded"), cond("NodeClassReady", "True", "NodeClassReady"), cond("Ready", "True", "Ready")}, nil))
	}
	for i := 0; i < sc.claims; i++ {
		u := cr("karpenter.sh/v1", "NodeClaim", "", fmt.Sprintf("claim-%03d", i), map[string]any{}, []any{cond("Ready", "True", "Ready"), cond("Launched", "True", "Launched")},
			map[string]any{"nodeName": fmt.Sprintf("node-%02d", i%sc.nodes)})
		u.SetLabels(map[string]string{"karpenter.sh/nodepool": "default"})
		u.SetCreationTimestamp(created)
		create("karpenter.sh", "v1", "nodeclaims", u)
	}

	// The real sources, wrapped: an outage switch on every list/watch, and
	// one fact every part declares (to force a full evaluation).
	served := func(_ context.Context, _, _, fallback string) (observe.Probe, error) {
		return observe.Probe{Served: true, Version: fallback}, nil
	}
	nd := NativeDeps{Cluster: name, Dyn: dyn, Served: served, Typed: typed, Meta: meta}
	kinds, domains := Core(CoreDeps{Cluster: name, Typed: typed, Meta: meta})
	kinds = append(kinds, SharedNetworkKinds(nd)...)
	for _, f := range []func(NativeDeps) ([]observe.KindSpec, []observe.DomainSpec){GitOps, Gateway, Native, Velero, Admission, LoadBalancing, KEDA} {
		k, d := f(nd)
		kinds, domains = append(kinds, k...), append(domains, d...)
	}
	// coverage S9: the shared HPA kind and the standalone HPA part
	kinds, domains = append(kinds, SharedHPAKind(nd)), append(domains, Autoscaling(nd)...)
	snaps, ed := Events(EventsDeps{Cluster: name, Typed: typed})
	domains = append(domains, ed...)
	ch := &chaos{}
	for i := range kinds {
		inner := kinds[i].NewListWatch
		kinds[i].NewListWatch = func(v string) cache.ListerWatcherWithContext { return chaosLW{inner: inner(v), c: ch} }
	}
	for i := range domains {
		for j := range domains[i].Parts {
			domains[i].Parts[j].Facts = append(domains[i].Parts[j].Facts, loadFact)
		}
	}
	s := &loadSink{}
	o := observe.New(observe.Config{Cluster: name, Sink: s, Kinds: kinds, Snapshots: snaps, Domains: domains, NamespacesKind: KindNamespaces})
	return &loadCluster{name: name, o: o, sink: s, chaos: ch, typed: typed, pods: pods}
}

// The gate counts the lifetime Namespaces kind, which is not a task: every
// task live with Namespaces still recovering is not complete.
func TestLoadCompletenessWaitsForNamespaces(t *testing.T) {
	n := 3
	live := observe.Summary{Tasks: []observe.Task{{ID: TaskPods, Coverage: observe.Live}, {ID: TaskCerts, Coverage: observe.Absent}}}
	if complete(live) {
		t.Fatal("Namespaces not live: not complete")
	}
	live.Namespaces = &n
	if !complete(live) {
		t.Fatal("every task and Namespaces live: complete")
	}
	live.Tasks[0].Coverage = observe.Stale
	if complete(live) {
		t.Fatal("a stale task: not complete")
	}
}

// loadListeners gives an object n listeners and the status a controller
// writes for them (coverage S7 §8's load fixture).
func loadListeners(u *unstructured.Unstructured, n int) {
	var spec, status []any
	for l := 0; l < n; l++ {
		name := fmt.Sprintf("https-%d", l)
		spec = append(spec, map[string]any{"name": name, "protocol": "HTTPS", "port": int64(8443 + l), "hostname": fmt.Sprintf("%s-%d.apps.example.com", u.GetName(), l),
			"tls": map[string]any{"mode": "Terminate", "certificateRefs": []any{map[string]any{"name": u.GetName() + "-tls"}}}})
		var cs []any
		for _, c := range [][2]string{{"Accepted", "Accepted"}, {"Conflicted", "NoConflicts"}, {"ResolvedRefs", "ResolvedRefs"}, {"Programmed", "Programmed"}} {
			cs = append(cs, map[string]any{"type": c[0], "status": map[bool]string{true: "False", false: "True"}[c[0] == "Conflicted"], "reason": c[1],
				"message": "Listener has been successfully translated", "observedGeneration": int64(1), "lastTransitionTime": "2026-10-18T09:00:00Z"})
		}
		status = append(status, map[string]any{"name": name, "attachedRoutes": int64(3), "conditions": cs,
			"supportedKinds": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute"}}})
	}
	u.Object["spec"].(map[string]any)["listeners"] = spec
	st, _ := u.Object["status"].(map[string]any)
	if st == nil {
		st = map[string]any{}
		u.Object["status"] = st
	}
	st["listeners"] = status
}
