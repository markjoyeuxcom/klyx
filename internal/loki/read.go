package loki

import (
	"context"
	"errors"
	"fmt"
)

// StatusError is Loki's non-2xx answer: its status and its own words (a
// display value - the reader redacted them).
type StatusError struct {
	Code int
	Text string
}

func (e *StatusError) Error() string {
	if e.Text == "" {
		return fmt.Sprintf("Loki answered HTTP %d", e.Code)
	}
	return fmt.Sprintf("Loki answered HTTP %d: %s", e.Code, e.Text)
}

// QueryFunc reads one branch's query_range [start, end) backward with the
// limit, parsed.
type QueryFunc func(ctx context.Context, branch int, start, end int64, limit int) ([]Entry, error)

// Marker is a branch the page stopped at a saturated boundary (S8b §5):
// at least Count entries share the timestamp TS; whether more remain cannot
// be established. Refused carries the server's refusal of the re-read.
type Marker struct {
	Branch  int
	TS      int64
	Count   int
	Refused string
}

// ReadPage reads one page of a page set: every active branch at PageLimit,
// a saturated branch once more at RetryLimit, a branch still saturated (or
// whose re-read the server refused with a 4xx) stopped with its marker. A
// read failing otherwise fails the page whole: nothing is applied, and the
// page set is where it was. The page keeps at most PageLimit entries and
// PageBytes of them, each entry's share by size (nil: Raw); the markers are
// every stopped branch's, not only this page's. A page applied is the
// cursor moved: a caller that may not deliver the page reads it on a Clone
// and keeps the clone only when it does.
func ReadPage(ctx context.Context, read QueryFunc, ps *PageSet, size SizeFunc) ([]Kept, []Marker, error) {
	var reads []Read
	var stops []Read
	refused := map[int]string{}
	for i, b := range ps.Branches {
		if !b.Active() {
			continue
		}
		es, err := read(ctx, i, ps.Start, b.End, PageLimit)
		if err != nil {
			return nil, nil, err
		}
		r := Read{Branch: i, Limit: PageLimit, Entries: es}
		if ps.Saturated(r) {
			more, err := read(ctx, i, ps.Start, b.End, RetryLimit)
			var se *StatusError
			switch {
			case errors.As(err, &se) && se.Code >= 400 && se.Code < 500:
				refused[i] = se.Text
				stops = append(stops, r)
				continue
			case err != nil:
				return nil, nil, err
			}
			r = Read{Branch: i, Limit: RetryLimit, Entries: more}
			if ps.Saturated(r) {
				stops = append(stops, r)
				continue
			}
		}
		reads = append(reads, r)
	}
	for _, r := range stops {
		ps.Stop(r)
		ps.Branches[r.Branch].Refused = refused[r.Branch]
	}
	kept := ps.ApplyBounded(reads, PageLimit, PageBytes, size)
	return kept, ps.Markers(), nil
}
