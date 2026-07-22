package appbridge

// PromotionUnitDTO mirrors fleet.PromotionUnit.
type PromotionUnitDTO struct {
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	SourceKind string `json:"sourceKind"`
	SourceName string `json:"sourceName"`
	Revision   string `json:"revision"`
	Ready      string `json:"ready"`
	Suspended  bool   `json:"suspended"`
	Stale      bool   `json:"stale"`
	Message    string `json:"message"`
}

// PromotionSourceDTO mirrors fleet.PromotionSource.
type PromotionSourceDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Revision  string `json:"revision"`
	Ready     string `json:"ready"`
	Suspended bool   `json:"suspended"`
}

// PromotionSnapshotDTO mirrors fleet.PromotionSnapshot plus a fold-in error.
// Units/Sources are never nil.
type PromotionSnapshotDTO struct {
	FluxPresent bool                 `json:"fluxPresent"`
	ArgoPresent bool                 `json:"argoPresent"`
	Units       []PromotionUnitDTO   `json:"units"`
	Sources     []PromotionSourceDTO `json:"sources"`
	Error       string               `json:"error,omitempty"`
}
