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
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
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
	pods, workloads, slices, nodes, flux, certs, eso, gateways, routes, claims int
}

var (
	largeScale = loadScale{pods: 5000, workloads: 1200, slices: 1500, nodes: 50, flux: 200, certs: 300, eso: 400, gateways: 20, routes: 300, claims: 50}
	smallScale = loadScale{pods: 500, workloads: 120, slices: 150, nodes: 10, flux: 20, certs: 30, eso: 40, gateways: 2, routes: 30, claims: 5}
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
		pods = append(pods, p)
		typedObjs = append(typedObjs, p)
	}
	for i := 0; i < sc.nodes; i++ {
		typedObjs = append(typedObjs, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%02d", i), UID: types.UID(fmt.Sprintf("node-%02d", i)), Labels: labels("node", i)},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Message: long}}}})
	}
	for i := 0; i < 50; i++ {
		metaObjs = append(metaObjs, &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: ns(i), UID: types.UID(ns(i))}})
	}
	svcs := sc.routes
	for i := 0; i < svcs; i++ {
		metaObjs = append(metaObjs, &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("svc-%04d", i), Namespace: ns(i), UID: types.UID(fmt.Sprintf("svc-%04d", i))}})
	}
	for i := 0; i < sc.slices; i++ {
		svc := fmt.Sprintf("svc-%04d", i%svcs)
		ready := true
		es := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%05d", svc, i), Namespace: ns(i % svcs), UID: types.UID(fmt.Sprintf("es-%05d", i)),
			Labels: map[string]string{discoveryv1.LabelServiceName: svc}}, AddressType: discoveryv1.AddressTypeIPv4}
		for e := 0; e < 3; e++ {
			es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{Addresses: []string{fmt.Sprintf("10.%d.%d.%d", i/250, i%250, e)}, Conditions: discoveryv1.EndpointConditions{Ready: &ready},
				TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: ns(i % svcs), Name: fmt.Sprintf("p-%05d-%d", i, e), UID: types.UID(fmt.Sprintf("p-%05d-%d", i, e))}})
		}
		typedObjs = append(typedObjs, es)
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
		create("gateway.networking.k8s.io", "v1", "gateways", u)
	}
	for i := 0; i < sc.routes; i++ {
		u := httpRoute(fmt.Sprintf("route-%04d", i), []string{fmt.Sprintf("gw-%02d", i%sc.gateways)}, fmt.Sprintf("svc-%04d", i%svcs), nil)
		u.SetNamespace(ns(i))
		u.SetUID(types.UID(fmt.Sprintf("route-%04d", i)))
		create("gateway.networking.k8s.io", "v1", "httproutes", u)
	}
	create("karpenter.sh", "v1", "nodepools", cr("karpenter.sh/v1", "NodePool", "", "default", map[string]any{}, []any{cond("Ready", "True", "Ready")}, nil))
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
	for _, f := range []func(NativeDeps) ([]observe.KindSpec, []observe.DomainSpec){GitOps, Gateway, Native} {
		k, d := f(nd)
		kinds, domains = append(kinds, k...), append(domains, d...)
	}
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
