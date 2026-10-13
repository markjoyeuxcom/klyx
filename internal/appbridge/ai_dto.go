package appbridge

// AIResultDTO is the result of an on-demand AI explanation. Read-only; any
// failure folds into Error (OK stays false) rather than throwing across the
// bridge, mirroring FluxDiffDTO. GroundedOn names the resolved nodes that were
// sent to the model, so the UI can show the operator exactly what it reasoned
// from.
type AIResultDTO struct {
	OK          bool   `json:"ok"`
	Explanation string `json:"explanation"`
	Model       string `json:"model"`   // the model the vendor reports having used
	Backend     string `json:"backend"` // the provider id
	Origin      string `json:"origin"`  // where the prompt went
	GroundedOn  string `json:"groundedOn"`
	Error       string `json:"error"`
	// Redacted counts the manifest values masked before a fix suggestion was
	// sent (credential-like keys; secret references stay readable).
	Redacted int `json:"redacted,omitempty"`
}

// AIAvailabilityDTO tells the frontend whether to render an AI affordance at all
// (Ready is false when the backend is off / no client is configured).
type AIAvailabilityDTO struct {
	Backend string `json:"backend"` // the configured provider id ("off" when none)
	Ready   bool   `json:"ready"`
	Model   string `json:"model"`
	Origin  string `json:"origin"`  // where prompts go
	Problem string `json:"problem"` // why it is not ready, when a provider is configured
}
