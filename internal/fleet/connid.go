package fleet

import "github.com/moomora/klyx/internal/observe"

// ConnID identifies one cluster connection for its lifetime (Settings S2
// §2.2). It is drawn from the process-wide incarnation counter, so a
// replacement, or a cluster removed and added again under its name, is
// always above every identity before it; the connection's Observer
// publishes under the same number (§2.4).
type ConnID uint64

func newConnID() ConnID { return ConnID(observe.NextIncarnation()) }
