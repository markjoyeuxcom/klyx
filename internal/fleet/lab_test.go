package fleet

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/cluster"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/observe"
)

// The 1c spec §7 real-API-server measurement, through the production
// connection, on the removable kind fixture (scripts/lab/klyx-load.sh up):
//
//	KLYX_LAB_CONTEXT=kind-klyx go test ./internal/fleet -run TestLab -v -count=1 -timeout 20m
//
// It reports; budgets are judged in the report, not asserted here (the
// numbers go in the PR). Refuses any context that is not kind-*.

func TestLab(t *testing.T) {
	ctxName := os.Getenv("KLYX_LAB_CONTEXT")
	if ctxName == "" {
		t.Skip("set KLYX_LAB_CONTEXT=kind-klyx with the klyx-load fixture up")
	}
	if !strings.HasPrefix(ctxName, "kind-") {
		t.Fatalf("refusing %q: the lab runs only against kind-* contexts", ctxName)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cc := config.ClusterConfig{Name: ctxName, Context: ctxName}
	rc, err := cluster.RESTConfig(cc)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := kubernetes.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := admin.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: "klyx.dev/lab=load"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.Items) < 5000 {
		t.Skipf("the fixture is not up (%d klyx-load pods): scripts/lab/klyx-load.sh up", len(fixture.Items))
	}

	metrics0 := apiMetrics(t, admin)
	rss0 := rssKB(t)
	cpu0 := cpuTime()

	// The production connection, with its health Observer.
	conn, err := DefaultConnFactory(clock.Real{}, Credentials{})(cc)
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*ClusterConn)
	s := &labSink{}
	c.SetHealthSink(s)
	t0 := time.Now()
	c.Start(ctx)
	h := c.Health()
	defer func() { cancel(); <-h.Done() }()
	waitLab(t, 120*time.Second, "every task live or absent", func() bool { sum, ok := s.lastOf(); return ok && labComplete(sum) })
	synced := time.Since(t0)
	connSynced := time.Duration(0)
	waitLab(t, 120*time.Second, "the connection synced", func() bool {
		st := c.Snapshot().State
		return st == Synced || st == Degraded
	})
	connSynced = time.Since(t0)

	// Every task's rows subscribed, as the lenses will be.
	sum, _ := s.lastOf()
	owner := observe.Owner{Kind: "lab", ID: "rows"}
	for _, tk := range sum.Tasks {
		for _, p := range tk.Parts {
			defer h.SubscribeRows(tk.ID, p.ID, owner)()
		}
	}
	time.Sleep(10 * time.Second) // settle, with rows built
	metrics1 := apiMetrics(t, admin)
	rssSteady := rssKB(t)
	conns := tcpConns(t, rc.Host)

	// Idle CPU over 30s.
	cpuIdle0 := cpuTime()
	time.Sleep(30 * time.Second)
	idlePct := 100 * (cpuTime() - cpuIdle0).Seconds() / 30

	// 50 pod updates/s for 30s, from a separate process (its CPU is not ours).
	cpuChurn0 := cpuTime()
	churn := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLabChurn$", "-test.v")
	churn.Env = append(os.Environ(), "KLYX_LAB_CHURN=50,30s", "KLYX_LAB_CONTEXT="+ctxName)
	var churnOut bytes.Buffer
	churn.Stdout, churn.Stderr = &churnOut, &churnOut
	start := time.Now()
	if err := churn.Run(); err != nil {
		t.Fatalf("churn generator: %v\n%s", err, churnOut.String())
	}
	churnWindow := time.Since(start)
	churnPct := 100 * (cpuTime() - cpuChurn0).Seconds() / churnWindow.Seconds()
	rssPeak := maxRSSKB()
	stats := h.Stats()

	listReqs := metrics1.list - metrics0.list
	kinds := 0
	for _, tk := range sum.Tasks {
		if tk.Coverage == observe.Live {
			for _, p := range tk.Parts {
				for _, k := range p.Kinds {
					if k.Role == "counted" {
						kinds++
					}
				}
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n1c §7 kind lab (%s, %d fixture pods)\n", ctxName, len(fixture.Items))
	fmt.Fprintf(&b, "  initial sync: every task live or absent %v; connection Synced %v\n", synced.Round(time.Millisecond), connSynced.Round(time.Millisecond))
	fmt.Fprintf(&b, "  RSS: before %.0f MB, steady with every task's rows open %.0f MB (+%.0f MB), peak %.0f MB\n",
		float64(rss0)/1024, float64(rssSteady)/1024, float64(rssSteady-rss0)/1024, float64(rssPeak)/1024)
	fmt.Fprintf(&b, "  CPU: startup %.1fs; idle %.2f%% of one core; under 50 pod updates/s %.2f%% (%v window)\n",
		(cpuIdle0 - cpu0).Seconds(), idlePct, churnPct, churnWindow.Round(time.Millisecond))
	fmt.Fprintf(&b, "  API server: LIST requests during startup %.0f (whole server, all clients) for %d counted kind reads (+ the connection's own informers and discovery); WATCH streams %+.0f\n",
		listReqs, kinds, metrics1.watches-metrics0.watches)
	fmt.Fprintf(&b, "  TCP connections to the API server: %d\n", conns)
	fmt.Fprintf(&b, "  Observer: %d publishes, evaluation %v total\n", stats.Publishes, stats.EvalTime.Round(time.Millisecond))
	fmt.Fprintf(&b, "  churn generator: %s\n", strings.TrimSpace(lastLine(churnOut.String(), "churn:")))
	t.Log(b.String())
}

// TestLabChurn is the update generator TestLab runs as a separate process:
// rate pod patches per second, across the fixture's pods. By default they
// patch a label, which no lens row carries (metadata-only rows deltas);
// KLYX_LAB_CHURN_KIND=status patches each pod's status IP instead, so every
// update changes its pods row (the slice 3 addendum's row-visible churn).
// The fixture's pods are unschedulable, so no kubelet rewrites the status.
func TestLabChurn(t *testing.T) {
	spec := os.Getenv("KLYX_LAB_CHURN")
	if spec == "" {
		t.Skip("run by TestLab")
	}
	parts := strings.SplitN(spec, ",", 2)
	rate, _ := strconv.Atoi(parts[0])
	d, _ := time.ParseDuration(parts[1])
	ctxName := os.Getenv("KLYX_LAB_CONTEXT")
	if !strings.HasPrefix(ctxName, "kind-") {
		t.Fatalf("refusing %q", ctxName)
	}
	rc, err := cluster.RESTConfig(config.ClusterConfig{Name: ctxName, Context: ctxName})
	if err != nil {
		t.Fatal(err)
	}
	rc.QPS, rc.Burst = float32(rate*2), rate*2
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: "klyx.dev/lab=load"})
	if err != nil {
		t.Fatal(err)
	}
	// Values unique to this run: a patch to an unchanged value is a no-op
	// the API server emits no event for.
	run := time.Now().UnixNano()
	status := os.Getenv("KLYX_LAB_CHURN_KIND") == "status"
	kind := "label"
	if status {
		kind = "status"
	}
	tick := time.NewTicker(time.Second / time.Duration(rate))
	defer tick.Stop()
	var wg sync.WaitGroup
	var mu sync.Mutex
	fails := 0
	sent := 0
	for t0 := time.Now(); time.Since(t0) < d; sent++ {
		<-tick.C
		p := pods.Items[sent%len(pods.Items)]
		wg.Add(1)
		go func(ns, name string, n int) {
			defer wg.Done()
			patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{"klyx.dev/churn":"%d-%d"}}}`, run, n))
			var sub []string
			if status {
				ip := fmt.Sprintf("10.%d.%d.%d", 100+(n>>16)%100, (n>>8)%256, n%256)
				patch, sub = []byte(fmt.Sprintf(`{"status":{"podIP":%q,"podIPs":[{"ip":%q}]}}`, ip, ip)), []string{"status"}
			}
			if _, err := cs.CoreV1().Pods(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, sub...); err != nil {
				mu.Lock()
				fails++
				mu.Unlock()
			}
		}(p.Namespace, p.Name, sent)
	}
	wg.Wait()
	fmt.Printf("churn: %d %s patches in %v (%d failed)\n", sent, kind, d, fails)
}

type labSink struct {
	mu  sync.Mutex
	sum observe.Summary
	ok  bool
}

func (s *labSink) Summary(x observe.Summary) { s.mu.Lock(); s.sum, s.ok = x, true; s.mu.Unlock() }
func (s *labSink) Rows(observe.Rows)         {}
func (s *labSink) lastOf() (observe.Summary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sum, s.ok
}

func labComplete(sum observe.Summary) bool {
	if len(sum.Tasks) == 0 {
		return false
	}
	for _, t := range sum.Tasks {
		if t.Coverage != observe.Live && t.Coverage != observe.Absent {
			return false
		}
	}
	return true
}

func waitLab(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(limit); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

type apiCounters struct{ list, watches float64 }

// apiMetrics sums the API server's LIST request counter and its open WATCH
// gauge (whole server, every client).
func apiMetrics(t *testing.T, cs kubernetes.Interface) apiCounters {
	t.Helper()
	raw, err := cs.Discovery().RESTClient().Get().AbsPath("/metrics").DoRaw(context.Background())
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	var out apiCounters
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		val := func() float64 {
			f, _ := strconv.ParseFloat(line[strings.LastIndex(line, " ")+1:], 64)
			return f
		}
		switch {
		case strings.HasPrefix(line, "apiserver_request_total{") && strings.Contains(line, `verb="LIST"`):
			out.list += val()
		case strings.HasPrefix(line, "apiserver_longrunning_requests{") && strings.Contains(line, `verb="WATCH"`):
			out.watches += val()
		}
	}
	return out
}

func rssKB(t *testing.T) int64 {
	t.Helper()
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}

// maxRSSKB is the process's peak RSS (getrusage: bytes on darwin, KB on linux).
func maxRSSKB() int64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if isDarwin() {
		return ru.Maxrss / 1024
	}
	return ru.Maxrss
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func tcpConns(t *testing.T, host string) int {
	t.Helper()
	port := host[strings.LastIndex(host, ":")+1:]
	out, _ := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(os.Getpid()), "-iTCP", "-sTCP:ESTABLISHED").Output()
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, ":"+port) {
			n++
		}
	}
	return n
}

func lastLine(s, prefix string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return l
		}
	}
	return "(no report)"
}

func isDarwin() bool { return runtimeGOOS == "darwin" }

var runtimeGOOS = goruntime.GOOS
