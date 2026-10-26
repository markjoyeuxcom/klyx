package appbridge

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/helmcli"
)

// Settings S2a §2.3: leases. Work is admitted only on an Active cluster's
// current connection, listed from before its I/O, and ended - or continued
// - when that connection is retired; a retired lease cannot settle.

// leasedVia leases through the rig's lifecycle and hands the worker conn,
// a fake, as the pinned connection: retirement still follows the real
// connection's identity.
func (r *lifeRig) leasedVia(conn any) leaser {
	return func(cluster string, w Work) (*Lease, error) {
		le, err := r.life.Acquire(cluster, w)
		if err == nil {
			le.Conn = conn
		}
		return le, err
	}
}

type pendingForward struct {
	*fakeForwardsConn
	reached, release chan struct{}
}

func (c *pendingForward) ResolveServicePod(ctx context.Context, ns, svc string, port int) (string, int, error) {
	close(c.reached)
	<-c.release // ignores its context: the worst case
	return c.fakeForwardsConn.ResolveServicePod(ctx, ns, svc, port)
}

// Codex's design-review probe, against leases: a forward still opening on
// a connection that is retired cannot register after the teardown - its
// lease cannot settle, and what it opened is closed.
func TestAPendingForwardCannotOutliveItsConnection(t *testing.T) {
	r := newLifeRig(t, true)
	c := &pendingForward{fakeForwardsConn: &fakeForwardsConn{}, reached: make(chan struct{}), release: make(chan struct{})}
	s := NewForwardsService(fwLookup(c), &fwEmitter{})
	s.setLeaser(r.leasedVia(ForwardsConn(c)))
	defer s.StopAll()
	done := make(chan StartForwardResultDTO, 1)
	go func() { done <- s.StartForward("prd", "shop", "Service", "web", 0, 80) }()
	<-c.reached
	ends, _ := r.life.ClusterWork("prd")
	if len(ends) != 1 || !strings.Contains(ends[0].What, "opening") || ends[0].Kind != "forward" {
		t.Fatalf("a pending forward is listed: %+v", ends)
	}
	r.reconnect()
	close(c.release)
	res := <-done
	if res.Error == "" || len(s.ListForwards()) != 0 {
		t.Fatalf("a forward opened on a retired connection survived: %+v, %+v", res, s.ListForwards())
	}
	if ends, _ := r.life.ClusterWork("prd"); len(ends) != 0 {
		t.Fatalf("its lease is still listed: %+v", ends)
	}
}

func TestASettledForwardEndsWithItsConnectionAndSaysWhy(t *testing.T) {
	r := newLifeRig(t, true)
	c := &fakeForwardsConn{}
	s := NewForwardsService(fwLookup(c), &fwEmitter{})
	s.setLeaser(r.leasedVia(ForwardsConn(c)))
	defer s.StopAll()
	if res := s.StartForward("prd", "shop", "Pod", "web-0", 0, 8080); res.Error != "" {
		t.Fatal(res.Error)
	}
	ends, _ := r.life.ClusterWork("prd")
	if len(ends) != 1 || !strings.HasPrefix(ends[0].What, "forward of localhost:") {
		t.Fatalf("the settled forward is listed by what it is: %+v", ends)
	}
	r.reconnect()
	if !waitUntil(t, 3*time.Second, func() bool {
		l := s.ListForwards()
		return len(l) == 1 && l[0].Status == "ended" && strings.Contains(l[0].Reason, "prd reconnected")
	}) {
		t.Fatalf("not ended with its reason: %+v", s.ListForwards())
	}
	if !waitUntil(t, 3*time.Second, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.stopped == 1
	}) {
		t.Fatal("the tunnel was not torn down")
	}
	if ends, _ := r.life.ClusterWork("prd"); len(ends) != 0 {
		t.Fatalf("an ended forward's lease is still listed: %+v", ends)
	}
}

// A continuing work (a drain, a rollback, a confirmed request) is not
// cancelled by its connection's retirement: it is listed under Continues
// until it ends, and keeps its outcome; an ending work's context is
// cancelled after its End is told why.
func TestAContinuingLeaseRunsToItsEnd(t *testing.T) {
	r := newLifeRig(t, true)
	cont, err := r.life.Acquire("prd", Work{Kind: "drain", What: "the drain of node-3", Continues: true})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var told string
	var ctxAtEnd error
	var ending *Lease
	ending, err = r.life.Acquire("prd", Work{Kind: "logs", What: "logs of shop/web", End: func(why string) {
		mu.Lock()
		told, ctxAtEnd = why, ending.Ctx.Err()
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	r.reconnect()
	mu.Lock()
	if told != "prd reconnected" || ctxAtEnd != nil {
		t.Fatalf("End told %q, its context already %v (End first, then the cancel)", told, ctxAtEnd)
	}
	mu.Unlock()
	if ending.Ctx.Err() == nil || ending.Settle("too late") != ErrRetired {
		t.Fatal("an ending lease survived its connection")
	}
	if cont.Ctx.Err() != nil || cont.Settle("the drain of node-3, done") != nil {
		t.Fatal("a continuing lease was ended")
	}
	ends, continues := r.life.ClusterWork("prd")
	if len(ends) != 0 || len(continues) != 1 || continues[0].Kind != "drain" {
		t.Fatalf("ends %+v continues %+v", ends, continues)
	}
	cont.Release()
	if _, continues := r.life.ClusterWork("prd"); len(continues) != 0 {
		t.Fatal("a released continuing lease is still listed")
	}
}

func TestALeaseIsAdmittedOnlyOnAnActiveClustersCurrentConnection(t *testing.T) {
	r := newLifeRig(t, true)
	if _, err := r.life.Acquire("nowhere", Work{Kind: "logs"}); err == nil {
		t.Fatal("admitted on an unknown cluster")
	}
	if _, err := r.pol.Hold("prd", "saved, not connected yet"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.life.Acquire("prd", Work{Kind: "logs"}); err == nil || !strings.Contains(err.Error(), "saved, not connected yet") {
		t.Fatalf("admitted while held: %v", err)
	}
	if _, err := r.pol.Release("prd", true); err != nil {
		t.Fatal(err)
	}
	le, err := r.life.Acquire("prd", Work{Kind: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	le.Release()
	if err := r.life.Remove("prd", "prd removed"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.life.Acquire("prd", Work{Kind: "logs"}); err == nil {
		t.Fatal("admitted on a removed cluster")
	}
}

type chatEndRec struct {
	mu    sync.Mutex
	ended []ChatEndedDTO
}

func (e *chatEndRec) Emit(name string, data any) {
	if name != ChatEndedEvent {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ended = append(e.ended, data.(ChatEndedDTO))
}

// A conversation ends explicitly with its connection, with the reason: its
// running turn ends with it, never as merely stopped, and the page is told.
func TestAConversationEndsExplicitlyWithItsConnection(t *testing.T) {
	r := newLifeRig(t, true)
	started, release := make(chan struct{}), make(chan struct{})
	f := newToolsFixture(t, func(ai.ConvRequest) (ai.Turn, error) {
		close(started)
		<-release
		return ai.Turn{Text: "late", Done: true, ModelUsed: "m", Continuation: &ai.Continuation{Provider: ai.ProviderAnthropic, Raw: json.RawMessage(`{}`)}}, nil
	})
	rec := &chatEndRec{}
	f.svc.em = rec
	f.svc.setLeaser(r.leasedVia(ChatConn(f.conn)))
	st := f.svc.Start("prd")
	if st.Error != "" {
		t.Fatal(st.Error)
	}
	if ends, _ := r.life.ClusterWork("prd"); len(ends) != 1 || ends[0].Kind != kindChat {
		t.Fatalf("the conversation is listed: %+v", ends)
	}
	done := make(chan ChatStateDTO, 1)
	go func() { done <- f.svc.Ask("prd", st.ConversationID, "how is it?", nil) }()
	<-started
	r.reconnect()
	close(release)
	got := <-done
	if !strings.Contains(got.Ended, "prd reconnected") || len(got.Exchanges) != 1 || got.Exchanges[0].Answer != "" || !strings.Contains(got.Exchanges[0].Error, "prd reconnected") {
		t.Fatalf("the turn did not end with its connection's reason: ended %q, %+v", got.Ended, got.Exchanges)
	}
	rec.mu.Lock()
	n := len(rec.ended)
	rec.mu.Unlock()
	if n != 1 {
		t.Fatalf("the page was told %d times", n)
	}
	if again := f.svc.Ask("prd", st.ConversationID, "and now?", nil); !strings.Contains(again.Notice, "has ended") {
		t.Fatalf("an ended conversation took a question: %+v", again)
	}
}

// A log stream ends with its connection, and its final chunk says why.
func TestALogStreamEndsWithItsConnectionAndSaysWhy(t *testing.T) {
	r := newLifeRig(t, true)
	pr, pw := io.Pipe()
	defer pw.Close()
	c := &pipeConn{r: pr}
	em := &captureEmitter{}
	s := NewLogsService(lookupOf(c), em)
	s.setLeaser(r.leasedVia(LogsConn(c)))
	defer s.CloseAll()
	if res := s.OpenLogStream("prd", "shop", "web-0", "web", false, 100, "s1"); res.Error != "" {
		t.Fatal(res.Error)
	}
	r.reconnect()
	if !waitUntil(t, 3*time.Second, em.sawEOF) {
		t.Fatal("the stream did not end")
	}
	var why string
	for _, ev := range em.snapshot() {
		if ev.chunk.EOF {
			why = ev.chunk.Error
		}
	}
	if !strings.Contains(why, "prd reconnected") {
		t.Fatalf("the final chunk does not say why: %q", why)
	}
	if ends, _ := r.life.ClusterWork("prd"); len(ends) != 0 {
		t.Fatalf("an ended stream's lease is still listed: %+v", ends)
	}
}

type liveStatusRec struct {
	mu    sync.Mutex
	ended []string
}

func (e *liveStatusRec) Emit(name string, data any) {
	if st, ok := data.(liveStatusDTO); ok && st.Ended != "" {
		e.mu.Lock()
		e.ended = append(e.ended, st.Ended)
		e.mu.Unlock()
	}
}

// A live events subscription ends with its connection, and its status says
// why.
func TestLiveEventsEndWithTheirConnection(t *testing.T) {
	r := newLifeRig(t, true)
	c := &fakeEventsConn{}
	em := &liveStatusRec{}
	s := NewEventsService(func(string) (EventsConn, bool) { return c, true }, em)
	s.setLeaser(r.leasedVia(EventsConn(c)))
	defer s.CloseAll()
	if res := s.OpenLiveEvents("prd", "shop"); !res.OK {
		t.Fatal(res.Error)
	}
	if ends, _ := r.life.ClusterWork("prd"); len(ends) != 1 || ends[0].What != "live events in shop" {
		t.Fatalf("listed: %+v", ends)
	}
	r.reconnect()
	if !waitUntil(t, 3*time.Second, func() bool {
		em.mu.Lock()
		defer em.mu.Unlock()
		return len(em.ended) == 1 && strings.Contains(em.ended[0], "prd reconnected")
	}) {
		t.Fatalf("not ended with its reason: %+v", em.ended)
	}
	if ends, _ := r.life.ClusterWork("prd"); len(ends) != 0 {
		t.Fatalf("its lease is still listed: %+v", ends)
	}
}

type blockingHelm struct {
	*fakeHelmConn
	reached, release chan struct{}
}

func (c *blockingHelm) HelmRollback(ctx context.Context, namespace, release string, revision int, opts helmcli.RollbackOptions) error {
	close(c.reached)
	<-c.release
	return c.fakeHelmConn.HelmRollback(ctx, namespace, release, revision, opts)
}

// A drain and a Helm rollback continue through their connection's
// retirement (S2 §10): listed under Continues from their start, never
// ended by a reconnect, and gone once they finish.
func TestADrainAndARollbackContinueThroughTheirConnection(t *testing.T) {
	r := newLifeRig(t, true)
	node := &fakeNodeOpsConn{cmdFunc: func(string) (*exec.Cmd, error) { return exec.Command("sh", "-c", "sleep 30"), nil }}
	ops, em := newNodeOpsTestSvc(node)
	ops.setLeaser(r.leasedVia(NodeOpsConn(node)))
	drain := ops.StartDrain("prd", "node-3", "uid-3", DrainOptionsDTO{}, tid())
	if drain.Error != "" {
		t.Fatal(drain.Error)
	}
	defer ops.CancelDrain(drain.StreamID)
	hc := &blockingHelm{fakeHelmConn: &fakeHelmConn{}, reached: make(chan struct{}), release: make(chan struct{})}
	helm := newHelmTestSvc(hc)
	helm.setLeaser(r.leasedVia(HelmConn(hc)))
	done := make(chan ActionResultDTO, 1)
	go func() { done <- helm.HelmRollback("prd", "shop", "web", 3, RollbackOptionsDTO{}) }()
	<-hc.reached
	kinds := func() (ends, continues []string) {
		e, c := r.life.ClusterWork("prd")
		for _, w := range e {
			ends = append(ends, w.Kind)
		}
		for _, w := range c {
			continues = append(continues, w.Kind)
		}
		sort.Strings(continues)
		return ends, continues
	}
	if ends, continues := kinds(); len(ends) != 0 || strings.Join(continues, ",") != "drain,rollback" {
		t.Fatalf("ends %v continues %v", ends, continues)
	}
	r.reconnect()
	if ends, continues := kinds(); len(ends) != 0 || strings.Join(continues, ",") != "drain,rollback" {
		t.Fatalf("after the reconnect: ends %v continues %v", ends, continues)
	}
	for _, ev := range em.events_() {
		if c, ok := ev.data.(LogChunkDTO); ok && c.EOF {
			t.Fatalf("the drain ended with its connection: %+v", c)
		}
	}
	close(hc.release)
	if res := <-done; !res.OK {
		t.Fatalf("the rollback did not run to its end: %+v", res)
	}
	ops.CancelDrain(drain.StreamID)
	if !waitUntil(t, 5*time.Second, func() bool { _, c := kinds(); return len(c) == 0 }) {
		t.Fatal("finished work is still listed")
	}
}

// pinConn is an AI connection whose identity is all a pin compares.
type pinConn struct {
	AIConn
	n int
}

// Explain and Suggest-fix run only on the connection their precheck found:
// a replaced one is refused before anything is sent, and an admitted call
// is listed and ends with its connection.
func TestAnAICallRunsOnlyOnTheConnectionItsPrecheckFound(t *testing.T) {
	r := newLifeRig(t, true)
	current, found := &pinConn{n: 1}, &pinConn{n: 2}
	s := NewAIService(func(string) (AIConn, bool) { return current, true }, &AIState{}, nil, nil)
	s.setLeaser(r.leasedVia(AIConn(current)))
	if _, _, refusal := s.admitCall("explain", "prd", found); !strings.Contains(refusal, "connection changed") {
		t.Fatalf("a replaced connection was admitted: %q", refusal)
	}
	if ends, _ := r.life.ClusterWork("prd"); len(ends) != 0 {
		t.Fatalf("a refused call is listed: %+v", ends)
	}
	done, le, refusal := s.admitCall("suggest-fix", "prd", current)
	if refusal != "" {
		t.Fatal(refusal)
	}
	defer done()
	if ends, _ := r.life.ClusterWork("prd"); len(ends) != 1 || ends[0].Kind != "suggest-fix" {
		t.Fatalf("the call is listed: %+v", ends)
	}
	r.reconnect()
	if le.Ctx.Err() == nil {
		t.Fatal("an AI call outlived its connection")
	}
}
