package appbridge

// LogChunkDTO is one batched emission on the podlogs:<streamID> event. Lines is
// a batch of log lines (never per-line; the reader coalesces). EOF marks the
// stream's end (pod gone, or previous-container logs fully drained). Error
// carries a terminal message and is "" on a clean end. Cancelled marks a
// terminal chunk of an operation the user cancelled (a drain): neither a clean
// end nor a failure.
type LogChunkDTO struct {
	Lines     []string `json:"lines"`
	EOF       bool     `json:"eof"`
	Error     string   `json:"error,omitempty"`
	Cancelled bool     `json:"cancelled,omitempty"`
}

// OpenLogStreamResultDTO is returned synchronously from OpenLogStream (and the
// aggregate/drain opens). On success StreamID echoes the frontend-chosen id and
// Error is ""; on failure StreamID is "" and Error explains. The frontend has
// already subscribed to "podlogs:"+id before opening.
type OpenLogStreamResultDTO struct {
	StreamID string `json:"streamId"`
	Error    string `json:"error,omitempty"`
}
