package fleet

// ConnState is the lifecycle state of a single cluster connection.
type ConnState int

const (
	Unconnected ConnState = iota
	Connecting
	Synced
	Degraded // connected and syncing, but a capability/metrics subsystem is unhealthy
	Stale    // watches dropped, last cache retained
	Failed   // connection or auth failed
	// Stopped: the connection was stopped for good - replaced or removed
	// (Settings S2 §2.1). It never moves again and is never usable.
	Stopped
)

func (s ConnState) String() string {
	switch s {
	case Unconnected:
		return "Unconnected"
	case Connecting:
		return "Connecting"
	case Synced:
		return "Synced"
	case Degraded:
		return "Degraded"
	case Stale:
		return "Stale"
	case Failed:
		return "Failed"
	case Stopped:
		return "Stopped"
	default:
		return "Unknown"
	}
}

// Event drives a state transition.
type Event int

const (
	EvStart Event = iota
	EvSynced
	EvConnError
	EvWatchDrop
	EvCapUnhealthy
	EvCapHealthy
)

// Usable reports whether reads can work on a connection in this state:
// Synced, Degraded or Stale. Crossing into it from a state that is not is a
// new incarnation (Settings slice F §1).
func (s ConnState) Usable() bool { return s == Synced || s == Degraded || s == Stale }

// Transition returns the next state and whether the transition is legal.
// Illegal transitions return the original state and false.
func Transition(from ConnState, ev Event) (ConnState, bool) {
	if from == Stopped {
		return from, false // stopped for good
	}
	// A connection error is terminal-to-Failed from any connected state.
	if ev == EvConnError {
		return Failed, true
	}
	switch from {
	case Unconnected:
		if ev == EvStart {
			return Connecting, true
		}
	case Failed:
		switch ev {
		case EvStart:
			return Connecting, true
		case EvSynced:
			return Synced, true // recovery after a successful relist
		}
	case Connecting:
		if ev == EvSynced {
			return Synced, true
		}
	case Synced:
		switch ev {
		case EvCapUnhealthy:
			return Degraded, true
		case EvWatchDrop:
			return Stale, true
		}
	case Degraded:
		switch ev {
		case EvCapHealthy:
			return Synced, true
		case EvWatchDrop:
			return Stale, true
		}
	case Stale:
		if ev == EvSynced {
			return Synced, true
		}
	}
	return from, false
}
