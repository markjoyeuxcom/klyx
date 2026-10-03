package appbridge

import "fmt"

// maxStreamIDLen bounds a frontend-chosen stream id (it becomes part of an
// event name).
const maxStreamIDLen = 128

// validStreamID checks a stream id chosen by the FRONTEND. The frontend picks
// the id and subscribes to its event BEFORE asking for the stream, so no chunk
// can be emitted before a listener exists - a backend-generated id was only
// learned after the open returned, by which time a short (or 200-line) stream
// had already emitted into the void (review 2026-10-03 #8). Ids are opaque
// tokens of [A-Za-z0-9._:-].
func validStreamID(id string) error {
	if id == "" {
		return fmt.Errorf("stream id required")
	}
	if len(id) > maxStreamIDLen {
		return fmt.Errorf("stream id longer than %d characters", maxStreamIDLen)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == ':', r == '-':
		default:
			return fmt.Errorf("stream id %q: invalid character %q", id, r)
		}
	}
	return nil
}
