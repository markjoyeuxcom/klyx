package appbridge

// ForwardDTO is one active (or broken) port-forward as projected to the
// frontend. TargetKind is the user's chosen target ("Pod" or "Service") and is
// preserved for display even when a Service was resolved to a backing pod
// before forwarding. Status is "active" while the SPDY tunnel is up and
// "broken" once it has died on its own (the forward is kept in the registry so
// the user can see it failed and dismiss it).
type ForwardDTO struct {
	ID          string `json:"id"`
	Cluster     string `json:"cluster"`
	Namespace   string `json:"namespace"`
	TargetKind  string `json:"targetKind"` // "Pod" | "Service"
	TargetName  string `json:"targetName"`
	LocalPort   int    `json:"localPort"`
	TargetPort  int    `json:"targetPort"`
	StartedUnix int64  `json:"startedUnix"`
	Status      string `json:"status"` // "active" | "broken" | "ended"
	// Reason: why an ended forward ended - its cluster reconnected or was
	// removed (Settings S2 §2.3)
	Reason string `json:"reason,omitempty"`
}

// StartForwardResultDTO is returned synchronously from StartForward. On success
// Forward is set and Error is ""; on failure Forward is nil and Error explains
// (cluster miss, resolution failure, tunnel failure, or at-cap).
type StartForwardResultDTO struct {
	Forward *ForwardDTO `json:"forward"`
	Error   string      `json:"error,omitempty"`
}
