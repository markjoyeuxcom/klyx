package appbridge

import (
	"log"
	"runtime/debug"
)

// recoverGuard is the deferred half of the backend panic net. Placed at the top
// of a detached goroutine's body (or supplied by safeGo), it converts a panic -
// which in Go terminates the ENTIRE process, and here would drop every live
// watch, port-forward, and log tail across all connected clusters at once - into
// a logged, contained event. The affected lens stops updating; the rest of the
// console survives.
//
// onPanic (optional) lets the caller degrade the affected lens honestly - e.g.
// emit live:false so a frozen list is not shown as healthy (the error-honesty
// invariant) - instead of leaving the last state latched.
//
// The parse code these goroutines run is defensively written (comma-ok
// assertions, guarded indexing), so this is a net for the whole class of "a
// malformed CRD panics a loop", not a fix for a specific known crash.
func recoverGuard(label string, onPanic func(recovered any)) {
	if r := recover(); r != nil {
		log.Printf("klyx: recovered panic in %s: %v\n%s", label, r, debug.Stack())
		if onPanic != nil {
			onPanic(r)
		}
	}
}

// safeGo starts fn on a new goroutine under recoverGuard (log-only).
func safeGo(label string, fn func()) {
	go func() {
		defer recoverGuard(label, nil)
		fn()
	}()
}

// safeGoOnPanic is safeGo with a panic callback, for goroutines that must degrade
// their lens honestly on a recovered panic (e.g. the live-list compute loop
// emitting live:false and stopping its watch).
func safeGoOnPanic(label string, fn func(), onPanic func(recovered any)) {
	go func() {
		defer recoverGuard(label, onPanic)
		fn()
	}()
}
