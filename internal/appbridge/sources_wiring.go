package appbridge

import (
	"context"

	"github.com/moomora/klyx/internal/fleet"
)

// AlertHostLeaser admits a bucket's read on its host through S2's
// admission (Settings S3a §13): a lease on the host's current connection,
// listed under the host's plans, whose context ends when that connection
// is retired. A host that is reserved, held or retiring admits nothing.
func AlertHostLeaser(l *Lifecycle) fleet.HostLeaser {
	return func(host, what string) (fleet.Conn, context.Context, func(), error) {
		le, err := l.Acquire(host, Work{Kind: "alerts", What: what})
		if err != nil {
			return nil, nil, nil, err
		}
		c, _ := le.Conn.(fleet.Conn)
		return c, le.Ctx, le.Release, nil
	}
}
