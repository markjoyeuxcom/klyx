package appbridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
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
