package appbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moomora/klyx/internal/policy"
	"github.com/moomora/klyx/internal/readfence"
)

// armTTL bounds how long a typed confirmation stays valid. Long enough for the
// dialog's confirm click to reach the destructive verb, short enough that a
// stale arm cannot linger and authorize an unrelated later action.
const armTTL = 30 * time.Second

// ProtectGuard is the backend gate for destructive verbs on protected
// clusters. The frontend ConfirmDialog requires typing the cluster name on a
// protected cluster; on confirm it calls ConfigService.ArmProtected, which
// records a one-shot grant here. Each destructive service method then takes
// a Ticket from guardFor before acting, so a frontend BUG - a code path that
// reaches a verb without the confirm dialog - cannot fire it on a protected
// cluster.
//
// Settings slice F §1-§2: the guard reads one published policy snapshot -
// the cluster's protection, its epoch and whether it is Active - and every
// ticket captures that epoch, an unprotected pass included. The verb's
// write context checks the ticket on every request, the write itself
// included, so a protection change, a reconnect or a change being applied
// between the guard and the write refuses the write. A grant records the
// epoch it was armed under and fails once it has moved.
//
// Threat model, stated honestly (review 2026-10-03, J): this is a guard
// against mistakes, not a security boundary. ArmProtected is on the same
// JS-callable binding surface as the verbs, and the cluster name it checks is
// no secret, so code able to call bindings arbitrarily (a compromised webview)
// can arm and then act. And a grant is keyed by cluster only, not by verb or
// object: within armTTL it authorizes the next destructive verb on that
// cluster, whichever it is - which is why every verb must consume it, so none
// lingers for an unrelated action.
type ProtectGuard struct {
	src policySource
	now func() time.Time

	mu    sync.Mutex
	armed map[string]grant // cluster -> its grant; entries are one-shot
}

// policySource is the published admission state the guard and every ticket
// read: production's *policy.Registry.
type policySource interface {
	Snapshot(cluster string) (policy.Snapshot, bool)
}

type grant struct {
	exp   time.Time
	epoch policy.Epoch
}

// NewProtectGuard wires the policy registry and a clock (time.Now in
// production).
func NewProtectGuard(src policySource, now func() time.Time) *ProtectGuard {
	return &ProtectGuard{src: src, now: now, armed: map[string]grant{}}
}

// Arm records a one-shot grant for the cluster if the typed confirmation
// matches its name, under the cluster's current epoch. Arming an unprotected
// cluster is a harmless no-op, so the frontend can call this on every
// confirm. A mismatch clears any grant the cluster had.
func (g *ProtectGuard) Arm(cluster, typed string) error {
	snap, ok := g.src.Snapshot(cluster)
	if !ok {
		return fmt.Errorf("Klyx does not know cluster %q", cluster)
	}
	if !snap.Protected {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if typed != cluster {
		delete(g.armed, cluster)
		return fmt.Errorf("confirmation mismatch for protected cluster %q", cluster)
	}
	g.armed[cluster] = grant{exp: g.now().Add(armTTL), epoch: snap.Epoch}
	return nil
}

// Guard admits a destructive verb and returns its ticket. On a protected
// cluster it consumes the grant, pass or fail; a grant armed under another
// epoch is refused. A cluster that is not Active admits nothing. The ticket
// captures the epoch either way.
func (g *ProtectGuard) Guard(cluster string) (Ticket, error) {
	if g == nil {
		return Ticket{}, errNoGuard
	}
	snap, ok := g.src.Snapshot(cluster)
	if !ok {
		return Ticket{}, fmt.Errorf("Klyx does not know cluster %q, so nothing is admitted for it", cluster)
	}
	var gr grant
	var armed bool
	if snap.Protected {
		// consumed at entry, on every refused path too
		g.mu.Lock()
		gr, armed = g.armed[cluster]
		delete(g.armed, cluster)
		g.mu.Unlock()
	}
	// a change in progress, a hold or a reservation is the more useful
	// refusal
	if err := notAdmitting(cluster, snap); err != nil {
		return Ticket{}, err
	}
	if snap.Protected {
		if !armed || g.now().After(gr.exp) {
			return Ticket{}, fmt.Errorf("protected cluster %q: destructive action not armed - confirm it in the dialog first", cluster)
		}
		if gr.epoch != snap.Epoch {
			return Ticket{}, fmt.Errorf("protected cluster %q: %s since it was confirmed - confirm it again", cluster, moved(gr.epoch, snap.Epoch))
		}
	}
	return Ticket{cluster: cluster, epoch: snap.Epoch, src: g.src}, nil
}

var errNoGuard = errors.New("the protected-cluster guard is not installed, so nothing is admitted")

func notActive(cluster string, snap policy.Snapshot) error {
	switch snap.State {
	case policy.Applying:
		return fmt.Errorf("%s's settings are being applied; try again in a moment", cluster)
	case policy.Held:
		return fmt.Errorf("%s's settings are saved but not active (%s), so nothing is admitted for it", cluster, snap.Reason)
	}
	return nil
}

// notAdmitting is why new work on the cluster is refused now (Settings S2
// §4.1): it is not Active, or an Apply holds its reservation. Work admitted
// before asks notActive and its epoch instead, so a reservation alone
// leaves it valid.
func notAdmitting(cluster string, snap policy.Snapshot) error {
	if err := notActive(cluster, snap); err != nil {
		return err
	}
	if snap.Reserved {
		return fmt.Errorf("%s is applying a change to its settings; try again in a moment", cluster)
	}
	return nil
}

// moved says what changed between two epochs, in words.
func moved(was, now policy.Epoch) string {
	policyMoved, connMoved := now.Moved(was)
	switch {
	case policyMoved && connMoved:
		return "its policy changed and its connection restarted"
	case connMoved:
		return "its connection restarted"
	}
	return "its policy changed"
}

// Ticket is one admitted verb's capture of its cluster's epoch. The zero
// Ticket admits nothing.
type Ticket struct {
	cluster string
	epoch   policy.Epoch
	src     policySource
}

// Valid is nil while the cluster is Active under the captured epoch.
func (t Ticket) Valid() error {
	if t.src == nil {
		return errors.New("no ticket: nothing is admitted")
	}
	snap, ok := t.src.Snapshot(t.cluster)
	if !ok {
		return fmt.Errorf("Klyx no longer knows cluster %q", t.cluster)
	}
	if err := notActive(t.cluster, snap); err != nil {
		return err
	}
	if snap.Epoch != t.epoch {
		return fmt.Errorf("%s: %s since this was confirmed", t.cluster, moved(t.epoch, snap.Epoch))
	}
	return nil
}

// Write is the context for one logical write under the ticket: its fence,
// checked before every request (the reads before the write and every
// attempt of the write), and a forwarding tracker of its own, so a refusal
// before anything of this write was sent says it was not sent (slice F
// §2). A retry of the same write reuses the context, and so the tracker.
func (t Ticket) Write(ctx context.Context) context.Context {
	return readfence.WithWriteTracker(readfence.With(ctx, t.Valid))
}

// StartCLI is the check at a child process's actual start, after any
// preparation: a started child is not stopped by a later move (§2).
func (t Ticket) StartCLI() error { return t.Valid() }

// policyNow is the cluster's published admission state, for the
// admissions that take no ticket (an AI start, a review): false without an
// installed guard, or for a cluster the registry does not know.
func policyNow(cluster string) (policy.Snapshot, bool) {
	g := activeProtectGuard.Load()
	if g == nil {
		return policy.Snapshot{}, false
	}
	return g.src.Snapshot(cluster)
}

// admitNow is why new work on the cluster is refused now ("" when it is
// admitted), and the epoch to pin when it is.
func admitNow(cluster string) (policy.Epoch, string) {
	snap, ok := policyNow(cluster)
	if !ok {
		return policy.Epoch{}, "Klyx does not know cluster " + cluster + ", so nothing is admitted for it"
	}
	if err := notAdmitting(cluster, snap); err != nil {
		return policy.Epoch{}, err.Error()
	}
	return snap.Epoch, ""
}

// epochProblem is why work pinned to a cluster's epoch may not continue,
// "" while the cluster is Active under that epoch (slice F §3-§4).
func epochProblem(cluster string, pinned policy.Epoch) string {
	snap, ok := policyNow(cluster)
	if !ok {
		return "Klyx no longer knows cluster " + cluster
	}
	if err := notActive(cluster, snap); err != nil {
		return err.Error()
	}
	if snap.Epoch != pinned {
		return cluster + ": " + moved(pinned, snap.Epoch)
	}
	return ""
}

// activeProtectGuard is the process-wide guard, stored once at startup by
// main. Deliberately a package variable set through a plain function rather
// than a method on a bound service: exported service methods land in the JS
// binding surface, and the webview must not be able to replace the guard.
var activeProtectGuard atomic.Pointer[ProtectGuard]

// SetProtectGuard installs the guard. Call once at startup before the app runs.
func SetProtectGuard(g *ProtectGuard) { activeProtectGuard.Store(g) }

// guardFor is the per-verb gate the destructive service methods take their
// ticket from. Without an installed guard it admits nothing.
func guardFor(cluster string) (Ticket, error) {
	return activeProtectGuard.Load().Guard(cluster)
}

// Forget drops a removed cluster's grant (Settings S2 §2.5).
func (g *ProtectGuard) Forget(cluster string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.armed, cluster)
}
