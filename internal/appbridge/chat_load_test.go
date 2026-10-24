package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A1 §8, the load test: chat's limit of investigations at once, each at
// its bounds - 24 reads in two batches of 12 whose results reach their
// caps, a git_changes that renders both revisions through the confined
// build, and the largest findings report - measuring the read fan-out, the
// render admission, every result's size, retained bytes and the heap. By
// default the renders are small; KLYX_A1_LOAD=1 makes each one near the
// confined build's 64 MiB output bound, the declared worst shape.

// loadConn is the clusters' connection: concurrency-safe, counting the
// reads in flight.
type loadConn struct {
	*fakeChatConn
	mu           sync.Mutex
	active, peak int
}

func (l *loadConn) enter() func() {
	l.mu.Lock()
	l.active++
	l.peak = max(l.peak, l.active)
	l.mu.Unlock()
	time.Sleep(3 * time.Millisecond) // a read takes time, so reads overlap
	return func() {
		l.mu.Lock()
		l.active--
		l.mu.Unlock()
	}
}

func (l *loadConn) ChatGet(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	defer l.enter()()
	u, err := l.fakeChatConn.ChatGet(ctx, gvr, ns, name)
	if err != nil {
		return nil, err
	}
	return u.DeepCopy(), nil
}

func (l *loadConn) ChatList(ctx context.Context, gvr schema.GroupVersionResource, ns, sel string, limit int64) (*unstructured.UnstructuredList, error) {
	defer l.enter()()
	list, err := l.fakeChatConn.ChatList(ctx, gvr, ns, sel, limit)
	if err != nil {
		return nil, err
	}
	return list.DeepCopy(), nil
}

func (l *loadConn) ListEvents(ctx context.Context, ns string) ([]workloads.EventSummary, bool, error) {
	defer l.enter()()
	return l.fakeChatConn.ListEvents(ctx, ns)
}

func (l *loadConn) FluxObject(_ context.Context, kind, ns, name string) (*unstructured.Unstructured, error) {
	defer l.enter()()
	if u, ok := l.flux[kind+"|"+ns+"|"+name]; ok {
		return u.DeepCopy(), nil
	}
	return nil, fmt.Errorf("no %s %s/%s", kind, ns, name)
}

// loadProvider plays every investigation from its own requests, as the
// lab's scripted model does: two batches of reads, the report, the answer.
type loadProvider struct {
	mu      sync.Mutex
	maxSent map[string]int // the largest result sent, by tool
	sends   int
}

func (p *loadProvider) Complete(context.Context, ai.Request) (ai.Result, error) {
	return ai.Result{}, fmt.Errorf("the load test converses")
}

func loadCall(i int, name string, args map[string]any) ai.ToolCall {
	b, _ := json.Marshal(args)
	return ai.ToolCall{ID: fmt.Sprintf("c%d", i), Name: name, Args: b}
}

func (p *loadProvider) Converse(_ context.Context, req ai.ConvRequest) (ai.Turn, error) {
	steps := 0
	p.mu.Lock()
	p.sends++
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			steps++
		}
		for _, r := range m.Results {
			p.maxSent[r.Name] = max(p.maxSent[r.Name], len(r.Content))
		}
	}
	p.mu.Unlock()
	unit := map[string]any{"kind": "Kustomization", "namespace": "flux-system", "name": "app"}
	var cs []ai.ToolCall
	switch steps {
	case 0:
		cs = append(cs, loadCall(0, toolUnit, unit), loadCall(1, toolChanges, unit))
		for i := 2; i < 7; i++ {
			cs = append(cs, loadCall(i, toolList, map[string]any{"kind": "ConfigMap", "namespace": "shop"}))
		}
		for i := 7; i < 12; i++ {
			cs = append(cs, loadCall(i, toolEvents, map[string]any{"namespace": "shop"}))
		}
	case 1:
		for i := 0; i < 12; i++ {
			cs = append(cs, loadCall(i, toolGet, map[string]any{"kind": "ConfigMap", "namespace": "shop", "name": fmt.Sprintf("cm-%03d", i)}))
		}
	case 2:
		// the worst shape under the 4 KiB argument bound: every finding
		// citing ten reads, which Klyx expands into the checks they hold
		var fs []string
		for i := 0; i < findingsMax; i++ {
			var cites []string
			for j := 0; j < findingCitesMax; j++ {
				cites = append(cites, fmt.Sprintf("r%d", j+1))
			}
			fs = append(fs, finding("cause", "Kustomization", "flux-system", "app", "s", cites...))
		}
		cs = []ai.ToolCall{{ID: "c0", Name: toolFindings, Args: json.RawMessage(`{"findings":[` + strings.Join(fs, ",") + `]}`)}}
	default:
		return ai.Turn{Text: strings.Repeat("a", 6000), Done: true, ModelUsed: "m", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: json.RawMessage(`{}`)}}, nil
	}
	raw, _ := json.Marshal(cs)
	return ai.Turn{Calls: cs, ModelUsed: "m", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: raw}}, nil
}

// loadCache is the history store, safe for the investigations' fetches at
// once.
type loadCache struct {
	*cacheFake
	mu sync.Mutex
}

func (c *loadCache) EnsureCommit(ctx context.Context, url, sha string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cacheFake.EnsureCommit(ctx, url, sha)
}

func TestInvestigationsAtTheirBoundsAtOnce(t *testing.T) {
	heavy := os.Getenv("KLYX_A1_LOAD") == "1"
	if testing.Short() && !heavy {
		t.Skip("the load test")
	}
	// the repository: n ConfigMaps of size each, all changed between the
	// two revisions
	n, size := 12, 4<<10
	if heavy {
		n, size = 56, 1<<20 // ~56 MiB of output per render, under the 64 MiB bound
	}
	cm := func(i int, v string) string {
		return fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: big-%03d\ndata:\n  blob: %s\n", i, strings.Repeat(v, size))
	}
	first, second := map[string]string{}, map[string]string{}
	var res []string
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("clusters/prd/cm-%03d.yaml", i)
		first[p], second[p] = cm(i, "a"), cm(i, "b")
		res = append(res, filepath.Base(p))
	}
	first["clusters/prd/kustomization.yaml"] = "resources: [" + strings.Join(res, ", ") + "]\n"
	store, shas := newHistoryFixture(t, first, second)
	cache := &loadCache{cacheFake: store}
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(diffFleet), 0o644); err != nil {
		t.Fatal(err)
	}
	git := NewGitService(context.Background(), GitDeps{ConfigPath: path,
		StartGit: func(context.Context) (gitcli.Runner, gitcli.Helpers, error) {
			return gitcli.Runner{Git: gitcli.Git{Path: "/usr/bin/git"}}, gitcli.Helpers{}, nil
		},
		NewStore: func(gitcli.Runner) gitStore { return cache }})
	for end := time.Now().Add(5 * time.Second); !git.GetGitSettings().Binary.Ready; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("Git never started")
		}
	}

	// the cluster every investigation reads: its Kustomization at the two
	// revisions, 300 ConfigMaps with long labels and 500 events
	ks := unitObj("", "shop_web_apps_Deployment")
	st := ks.Object["status"].(map[string]any)
	st["lastAppliedRevision"], st["lastAttemptedRevision"] = "main@sha1:"+shas[0], "main@sha1:"+shas[1]
	ks.Object["spec"].(map[string]any)["path"] = "./clusters/prd"
	fc := newFakeChat()
	fc.summary = &observe.Summary{EvaluatedAt: fixedNow(), Connection: "connected"}
	fc.kinds["kustomization"] = []fleet.ChatKind{{GVR: ksGVR, Kind: "Kustomization", Namespaced: true}}
	cmGVR := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	fc.kinds["configmap"] = []fleet.ChatKind{{GVR: cmGVR, Kind: "ConfigMap", Namespaced: true}}
	fc.objects[key(ksGVR, "flux-system", "app")] = ks
	fc.flux = map[string]*unstructured.Unstructured{"Kustomization|flux-system|app": ks,
		"GitRepository|flux-system|fleet": uobj(map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
			"metadata": map[string]any{"name": "fleet", "namespace": "flux-system"}, "spec": map[string]any{"url": "https://github.com/acme/fleet.git"}})}
	list := &unstructured.UnstructuredList{}
	for i := 0; i < 300; i++ {
		labels := map[string]any{}
		for j := 0; j < 8; j++ {
			labels[fmt.Sprintf("example.com/l%d", j)] = strings.Repeat("v", 60)
		}
		o := uobj(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": fmt.Sprintf("cm-%03d", i), "namespace": "shop",
			"uid": fmt.Sprintf("uid-cm-%d", i), "labels": labels}, "data": map[string]any{"k": strings.Repeat("x", 4000)}})
		list.Items = append(list.Items, *o)
		fc.objects[key(cmGVR, "shop", o.GetName())] = o
	}
	fc.lists[cmGVR.String()+"|shop"] = list
	for i := 0; i < 500; i++ {
		fc.events = append(fc.events, workloads.EventSummary{Type: "Warning", Reason: fmt.Sprintf("Reason%d", i%40), Message: strings.Repeat("m", 300),
			Count: 3, Namespace: "shop", Kind: "Pod", Name: fmt.Sprintf("web-%d", i), UID: fmt.Sprintf("uid-p%d", i), LastSeenUnix: fixedNow().Unix() - int64(i)})
	}
	conn := &loadConn{fakeChatConn: fc}

	prov := &loadProvider{maxSent: map[string]int{}}
	aiSvc := NewAIService(func(string) (AIConn, bool) { return nil, false },
		&AIState{Provider: prov, ProviderID: ai.ProviderAnthropic, Model: "m", Origin: "https://api.example"},
		func(string) bool { return false }, func(string) capability.Set { return capability.Set{} })
	svc := NewChatService(func(string) (ChatConn, bool) { return conn, true }, aiSvc, nil)
	svc.now = fixedNow
	SetChatGit(svc, git)

	// the samplers: renders admitted at once, and the heap
	stop := make(chan struct{})
	var sampled sync.WaitGroup
	var peakComparisons, peakBuilds int
	var peakHeap uint64
	sampled.Add(1)
	go func() {
		defer sampled.Done()
		var ms runtime.MemStats
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			peakComparisons = max(peakComparisons, len(chatRenderAdmission))
			peakBuilds = max(peakBuilds, int(chatBuilding.Load()))
			if i%4 == 0 {
				runtime.ReadMemStats(&ms)
				peakHeap = max(peakHeap, ms.HeapInuse)
			}
		}
	}()

	start := time.Now()
	var wg sync.WaitGroup
	states := make([]ChatStateDTO, chatMaxConvs)
	for i := range states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cluster := fmt.Sprintf("c%d", i)
			inv := svc.StartInvestigation(cluster, ksAnchor)
			if inv.Error != "" {
				t.Errorf("%s: %s", cluster, inv.Error)
				return
			}
			states[i] = svc.Ask(cluster, inv.ConversationID, "", nil)
		}()
	}
	wg.Wait()
	close(stop)
	sampled.Wait()
	took := time.Since(start)

	for i, st := range states {
		if len(st.Exchanges) != 1 {
			t.Fatalf("c%d: %+v", i, st)
		}
		ex := st.Exchanges[0]
		var compared bool
		reads := 0
		for _, r := range ex.Reads {
			if r.Tool == toolChanges && r.Outcome == "completed" && strings.Contains(r.Detail, "objects changed") {
				compared = true
			}
			if r.ID != "" && r.Tool != "mode" {
				reads++
			}
		}
		if ex.Outcome != "answered" || !compared || reads != 1+invMaxToolCalls || ex.Findings == nil || len(ex.Findings.Findings) != findingsMax {
			t.Fatalf("c%d: every investigation reads its 24, compares both renders and reports: %s %s %d %v %+v", i, ex.Outcome, ex.Error, reads, compared, ex.Findings)
		}
	}
	svc.mu.Lock()
	retained := 0
	for _, c := range svc.convs {
		n := 0
		for _, e := range c.exchanges {
			n += exchangeBytes(e)
		}
		if n > chatRetainBytes {
			t.Errorf("%s retains %d bytes", c.key, n)
		}
		retained += n
	}
	svc.mu.Unlock()
	conn.mu.Lock()
	peakReads := conn.peak
	conn.mu.Unlock()
	prov.mu.Lock()
	sent := prov.maxSent
	prov.mu.Unlock()
	for tool, n := range sent {
		limit := chatResultMax
		if tool == toolChanges {
			limit = chatChangesResultMax
		}
		if n > limit {
			t.Errorf("a %s result of %d bytes passed its %d", tool, n, limit)
		}
	}
	if peakComparisons == 0 || peakBuilds == 0 || peakReads <= invReadWorkers {
		t.Errorf("the samplers saw no comparison (%d), no build (%d), or no reads beyond one investigation's (%d)", peakComparisons, peakBuilds, peakReads)
	}
	if peakReads > chatMaxConvs*invReadWorkers || peakComparisons > 1 || peakBuilds > 1 {
		t.Errorf("reads in flight %d (bound %d), comparisons admitted %d (bound 1), builds %d (bound 1)",
			peakReads, chatMaxConvs*invReadWorkers, peakComparisons, peakBuilds)
	}
	t.Logf("%d investigations in %s: renders of %d x %d KiB; reads in flight at most %d (bound %d); comparisons admitted at most %d (bound 1), builds at most %d (bound 1); largest results %v; retained %d KiB in all; heap in use at most %d MiB",
		chatMaxConvs, took.Round(time.Millisecond), n, size>>10, peakReads, chatMaxConvs*invReadWorkers, peakComparisons, peakBuilds, sent, retained>>10, peakHeap>>20)
}
