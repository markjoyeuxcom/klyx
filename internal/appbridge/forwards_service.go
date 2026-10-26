package appbridge

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ForwardsConn is the per-cluster surface ForwardsService needs. The lookup
// closure bridges to the real fleet.Conn; the interface keeps fleet out of
// appbridge and lets tests inject a fake.
type ForwardsConn interface {
	// PortForward starts a pod port-forward. done is closed-with-error when the
	// tunnel dies on its own; stop tears it down idempotently.
	PortForward(ctx context.Context, namespace, pod string, localPort, targetPort int) (stop func(), actualLocal int, done <-chan error, err error)
	// ResolveServicePod resolves a Service to a ready backing pod + container port.
	ResolveServicePod(ctx context.Context, namespace, service string, port int) (pod string, targetPort int, err error)
}

const (
	// maxActiveForwards caps simultaneous forwards. Unlike log tails (which evict
	// the oldest on a new open), forwards are user-managed long-lived resources:
	// silently killing one the user is actively using would be hostile. At cap we
	// refuse the new forward and tell the user to stop one first.
	maxActiveForwards = 16

	// forwardResolveTimeout bounds the Service->pod resolution + tunnel
	// establishment. PortForward's own readiness wait is 10s; this wraps it.
	forwardResolveTimeout = 15 * time.Second

	// ForwardsChangedEvent carries the full forward list on every mutation. N is
	// small (cap 16), so a full-list replace is the simplest honest sync and
	// avoids per-field reconciliation on the frontend.
	ForwardsChangedEvent = "forwards:changed"
)

// forward is one live port-forward. stop tears down the SPDY tunnel (idempotent
// in the fleet layer via sync.Once). cancelSup stops the supervisor goroutine;
// supDone closes when the supervisor has fully exited. status is "active" or
// "broken" and is guarded by the service mutex.
type forward struct {
	dto       ForwardDTO
	stop      func()
	cancelSup context.CancelFunc
	supDone   chan struct{}
	lease     *Lease // released when the forward ends
}

// ForwardsService manages long-lived port-forwards on behalf of the frontend.
//
// Lifecycle / leak-safety (mirrors LogsService discipline): every forward owns
// exactly one supervisor goroutine. The supervisor exits on any of {forward
// dies on its own -> mark broken + emit, StopForward/StopAll cancels it}. On
// exit it closes supDone. stop() is idempotent. The registry never grows past
// maxActiveForwards because the cap check and the add happen under one lock.
type ForwardsService struct {
	lease leaser // Settings S2 §2.3: every forward takes a lease
	em    Emitter

	seq atomic.Uint64

	mu       sync.Mutex
	forwards map[string]*forward
	order    []string // insertion order, for stable list rendering
}

// NewForwardsService wires the cluster lookup and the event emitter.
func NewForwardsService(lookup func(string) (ForwardsConn, bool), em Emitter) *ForwardsService {
	return &ForwardsService{
		lease:    plainLeaser(lookup),
		em:       em,
		forwards: map[string]*forward{},
	}
}

func (s *ForwardsService) setLeaser(l leaser) { s.lease = l }

// StartForward begins forwarding localPort -> the target's port. kind is "Pod"
// or "Service"; for a Service the backing pod is resolved first (TargetKind
// stays "Service" in the DTO for display). localPort 0 requests an ephemeral
// local port; the resolved port is returned in the DTO. At cap, returns an
// error and starts nothing.
func (s *ForwardsService) StartForward(cluster, namespace, kind, name string, localPort, targetPort int) StartForwardResultDTO {
	id := fmt.Sprintf("%s/%s/%s#%d", cluster, namespace, name, s.seq.Add(1))
	// the lease first, before any I/O (S2 §2.3): a forward still opening is
	// listed, and ended with its connection
	le, err := s.lease(cluster, Work{Kind: "forward",
		What: fmt.Sprintf("a port-forward to %s %s/%s, opening", kind, namespace, name),
		End:  func(why string) { s.endForward(id, why) }})
	if err != nil {
		return StartForwardResultDTO{Error: err.Error()}
	}
	conn, ok := le.Conn.(ForwardsConn)
	if !ok {
		le.Release()
		return StartForwardResultDTO{Error: "cluster not connected: " + cluster}
	}
	opened := false
	defer func() {
		if !opened {
			le.Release()
		}
	}()

	ctx, cancel := context.WithTimeout(le.Ctx, forwardResolveTimeout)
	defer cancel()

	// Resolve the forward target to a concrete pod + container port.
	pod := name
	resolvedTarget := targetPort
	if kind == "Service" {
		rp, rtp, err := conn.ResolveServicePod(ctx, namespace, name, targetPort)
		if err != nil {
			return StartForwardResultDTO{Error: err.Error()}
		}
		pod = rp
		resolvedTarget = rtp
	}

	stop, actualLocal, done, err := conn.PortForward(ctx, namespace, pod, localPort, resolvedTarget)
	if err != nil {
		return StartForwardResultDTO{Error: err.Error()}
	}

	dto := ForwardDTO{
		ID:          id,
		Cluster:     cluster,
		Namespace:   namespace,
		TargetKind:  kind,
		TargetName:  name,
		LocalPort:   actualLocal,
		TargetPort:  resolvedTarget,
		StartedUnix: time.Now().Unix(),
		Status:      "active",
	}

	supCtx, cancelSup := context.WithCancel(context.Background())
	fwd := &forward{dto: dto, stop: stop, cancelSup: cancelSup, supDone: make(chan struct{}), lease: le}

	s.mu.Lock()
	if len(s.forwards) >= maxActiveForwards {
		s.mu.Unlock()
		// Refuse: tear down the tunnel we just opened and stop nothing else.
		stop()
		cancelSup()
		return StartForwardResultDTO{Error: "too many active forwards (max 16); stop one first"}
	}
	s.forwards[id] = fwd
	s.order = append(s.order, id)
	s.mu.Unlock()
	opened = true
	// settled after the I/O: a connection retired while this opened ends it
	// here, closing what was opened (S2 §2.3)
	if err := le.Settle(fmt.Sprintf("forward of localhost:%d to %s %s/%s:%d", actualLocal, kind, namespace, name, resolvedTarget)); err != nil {
		s.StopForward(id)
		return StartForwardResultDTO{Error: err.Error()}
	}

	// Supervisor: the ONLY place a forward transitions to broken. It waits for the
	// tunnel to die on its own (done) OR for an explicit stop (supCtx cancelled by
	// StopForward/StopAll). On natural death it marks broken and emits; on explicit
	// stop it just exits (the stopper already removed the entry and emitted).
	go func() {
		defer close(fwd.supDone)
		select {
		case <-done:
			// Tunnel died on its own. Flip to broken if still registered.
			s.mu.Lock()
			if cur, present := s.forwards[id]; present && cur.dto.Status == "active" {
				cur.dto.Status = "broken"
			}
			changed := s.snapshotLocked()
			s.mu.Unlock()
			fwd.lease.Release()
			s.em.Emit(ForwardsChangedEvent, changed)
		case <-supCtx.Done():
			// Explicit stop; nothing to emit here (the stopper owns the emit).
		}
	}()

	s.emitChanged()
	out := dto
	return StartForwardResultDTO{Forward: &out}
}

// ListForwards returns the current forwards in insertion order. Safe to call
// from the UI thread; it snapshots under the lock.
func (s *ForwardsService) ListForwards() []ForwardDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// StopForward tears down one forward and removes it from the registry.
// Idempotent: an unknown or already-stopped id is a no-op (but still returns
// OK, since the desired end state - that forward gone - holds). Emits the
// updated list only when something actually changed.
func (s *ForwardsService) StopForward(id string) ActionResultDTO {
	s.mu.Lock()
	fwd := s.forwards[id]
	if fwd == nil {
		s.mu.Unlock()
		return ActionResultDTO{OK: true}
	}
	delete(s.forwards, id)
	for i, oid := range s.order {
		if oid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Unlock()

	s.teardown(fwd)
	s.emitChanged()
	return ActionResultDTO{OK: true}
}

// StopAll tears down every forward. Called on app shutdown (if wired) or when
// the user clicks "stop all". Waits briefly for each supervisor to exit so the
// caller observes a fully drained registry.
func (s *ForwardsService) StopAll() {
	s.mu.Lock()
	fwds := make([]*forward, 0, len(s.forwards))
	for _, f := range s.forwards {
		fwds = append(fwds, f)
	}
	s.forwards = map[string]*forward{}
	s.order = nil
	s.mu.Unlock()

	for _, f := range fwds {
		s.teardown(f)
	}
	if len(fwds) > 0 {
		s.emitChanged()
	}
}

// teardown stops the tunnel, cancels the supervisor, and waits briefly for it to
// exit. stop() is idempotent (sync.Once in the fleet layer); cancelSup unblocks
// the supervisor's select on supCtx.Done. Bounded so a wedged supervisor can
// never block the UI thread.
func (s *ForwardsService) teardown(f *forward) {
	f.stop()
	f.cancelSup()
	f.lease.Release()
	select {
	case <-f.supDone:
	case <-time.After(closeWaitTimeout):
	}
}

// snapshotLocked builds the ordered DTO list. Caller holds s.mu.
func (s *ForwardsService) snapshotLocked() []ForwardDTO {
	out := make([]ForwardDTO, 0, len(s.order))
	for _, id := range s.order {
		if f := s.forwards[id]; f != nil {
			out = append(out, f.dto)
		}
	}
	// order already reflects insertion order, but guard against any drift by
	// keeping a stable secondary sort on StartedUnix then ID.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedUnix != out[j].StartedUnix {
			return out[i].StartedUnix < out[j].StartedUnix
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// emitChanged pushes the full current forward list to the frontend.
func (s *ForwardsService) emitChanged() {
	s.mu.Lock()
	list := s.snapshotLocked()
	s.mu.Unlock()
	s.em.Emit(ForwardsChangedEvent, list)
}

// endForward ends a forward whose cluster's connection was retired (S2
// §2.3): the tunnel is torn down, and the forward stays listed as ended,
// saying why, until it is stopped. A forward still opening is not listed
// yet: its Settle fails, and its opener closes it.
func (s *ForwardsService) endForward(id, why string) {
	s.mu.Lock()
	f := s.forwards[id]
	if f == nil || f.dto.Status == "ended" {
		s.mu.Unlock()
		return
	}
	f.dto.Status, f.dto.Reason = "ended", why
	s.mu.Unlock()
	go func() {
		s.teardown(f)
		s.emitChanged()
	}()
}
