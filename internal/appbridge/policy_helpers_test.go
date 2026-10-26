package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/policy"
)

// lazyPolicy is the tests' policy source: a real registry, seeded with a
// cluster the first time it is asked about, protected when protected says
// so. Production's registry knows only the fleet and admits nothing else;
// this exists so a verb test need not seed every cluster name it uses.
type lazyPolicy struct {
	reg       *policy.Registry
	protected func(string) bool
}

func (l lazyPolicy) Snapshot(cluster string) (policy.Snapshot, bool) {
	l.reg.Seed(cluster, l.protected(cluster))
	return l.reg.Snapshot(cluster)
}

func newLazyPolicy(protected func(string) bool) lazyPolicy {
	return lazyPolicy{reg: policy.New(), protected: protected}
}

// newGuardFor is a guard over a lazy policy.
func newGuardFor(protected func(string) bool, now func() time.Time) *ProtectGuard {
	return NewProtectGuard(newLazyPolicy(protected), now)
}

// defaultTestGuard is installed for the whole package (TestMain): every
// cluster unprotected and Active. A test that installs its own restores it.
var defaultTestGuard = newGuardFor(func(string) bool { return false }, time.Now)
