package appbridge

// EventRowDTO is one row in the events lens. Distinct from the per-object
// EventDTO in crd_dto.go (different shape and context).
type EventRowDTO struct {
	Type          string `json:"type"` // "Normal" | "Warning"
	Reason        string `json:"reason"`
	Message       string `json:"message"`
	Count         int32  `json:"count"`
	Namespace     string `json:"namespace"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	LastSeenUnix  int64  `json:"lastSeenUnix"`
	FirstSeenUnix int64  `json:"firstSeenUnix"`
}

// EventsResultDTO is the response shape for the events lens.
// Namespaces is the sorted distinct set of namespaces, populated ONLY when
// the request was for all namespaces (namespace == "").
type EventsResultDTO struct {
	Namespaces []string      `json:"namespaces"`
	Events     []EventRowDTO `json:"events"`
	// Truncated reports that the apiserver held more events than the 500 cap.
	// The cap slices in etcd key order, so the newest warnings can be missing -
	// the view must say so rather than present the subset as complete.
	Truncated bool `json:"truncated"`
	// Error is set when the read FAILED (cluster not connected, list/get
	// error). The collections stay empty-but-non-nil, so a caller that ignores
	// it still cannot crash - but an error must never read as "nothing found":
	// the triage tape and Overview treat a source with Error as unreadable,
	// not quiet (review 2026-10-03 #10).
	Error string `json:"error,omitempty"`
}
