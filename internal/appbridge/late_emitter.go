package appbridge

import "sync/atomic"

// LateEmitter forwards to an Emitter installed after construction - the app
// handle exists only after every service is built, while cluster health
// Observers publish from their first evaluation. Install and Emit are safe
// to race; events before Install are dropped (HealthService.Summaries
// bootstraps the retained observations).
type LateEmitter struct {
	target atomic.Pointer[emitterBox]
}

type emitterBox struct{ em Emitter }

// Install sets the destination. A later Install replaces it.
func (l *LateEmitter) Install(em Emitter) { l.target.Store(&emitterBox{em: em}) }

func (l *LateEmitter) Emit(name string, data any) {
	if b := l.target.Load(); b != nil {
		b.em.Emit(name, data)
	}
}
