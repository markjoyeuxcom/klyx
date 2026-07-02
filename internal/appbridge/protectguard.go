package appbridge

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// armTTL bounds how long a typed confirmation stays valid. Long enough for the
// dialog's confirm click to reach the destructive verb, short enough that a
// stale arm cannot linger and authorize an unrelated later action.
const armTTL = 30 * time.Second

// ProtectGuard is the backend gate for destructive verbs on protected
// clusters. The frontend ConfirmDialog requires typing the cluster name on a
// protected cluster; on confirm it calls ConfigService.ArmProtected, which
// records a one-shot grant here. Each destructive service method then calls
// checkProtected before acting - defense in depth so a frontend bug (or a
// hostile webview call) can never fire a destructive verb on a protected
// cluster without the typed confirmation. Unprotected clusters always pass.
type ProtectGuard struct {
	isProtected func(cluster string) bool
	now         func() time.Time

	mu    sync.Mutex
	armed map[string]time.Time // cluster -> grant expiry; entries are one-shot
}

// NewProtectGuard wires the protection lookup (from the fleet config) and a
// clock (time.Now in production).
func NewProtectGuard(isProtected func(string) bool, now func() time.Time) *ProtectGuard {
	return &ProtectGuard{isProtected: isProtected, now: now, armed: map[string]time.Time{}}
}

// Arm records a one-shot grant for the cluster if the typed confirmation
// matches its name. Arming an unprotected cluster is a harmless no-op, so the
// frontend can call this unconditionally on every confirm.
func (g *ProtectGuard) Arm(cluster, typed string) error {
	if !g.isProtected(cluster) {
		return nil
	}
	if typed != cluster {
		return fmt.Errorf("confirmation mismatch for protected cluster %q", cluster)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed[cluster] = g.now().Add(armTTL)
	return nil
}

// Check consumes the cluster's grant (one-shot: pass or fail, the entry is
// cleared) and returns an error if the cluster is protected and no fresh grant
// exists. nil-receiver-safe so tests that never configure a guard pass freely.
func (g *ProtectGuard) Check(cluster string) error {
	if g == nil || g.isProtected == nil || !g.isProtected(cluster) {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.armed[cluster]
	delete(g.armed, cluster)
	if !ok || g.now().After(exp) {
		return fmt.Errorf("protected cluster %q: destructive action not armed - confirm it in the dialog first", cluster)
	}
	return nil
}

// activeProtectGuard is the process-wide guard, stored once at startup by
// main. Deliberately a package variable set through a plain function rather
// than a method on a bound service: exported service methods land in the JS
// binding surface, and the webview must not be able to replace the guard.
var activeProtectGuard atomic.Pointer[ProtectGuard]

// SetProtectGuard installs the guard. Call once at startup before the app runs.
func SetProtectGuard(g *ProtectGuard) { activeProtectGuard.Store(g) }

// checkProtected is the per-verb gate used by the destructive service methods.
func checkProtected(cluster string) error {
	return activeProtectGuard.Load().Check(cluster)
}
