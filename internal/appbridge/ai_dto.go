package appbridge

// AIResultDTO is the result of an on-demand AI explanation. Read-only; any
// failure folds into Error (OK stays false) rather than throwing across the
// bridge, mirroring FluxDiffDTO. GroundedOn names the resolved nodes that were
// sent to the model, so the UI can show the operator exactly what it reasoned
// from.
type AIResultDTO struct {
	OK          bool   `json:"ok"`
	Explanation string `json:"explanation"`
	Model       string `json:"model"`
	Backend     string `json:"backend"`
	GroundedOn  string `json:"groundedOn"`
	Error       string `json:"error"`
}

// AIAvailabilityDTO tells the frontend whether to render an AI affordance at all
// (Ready is false when the backend is off / no client is configured).
type AIAvailabilityDTO struct {
	Backend string `json:"backend"`
	Ready   bool   `json:"ready"`
}
