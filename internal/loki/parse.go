package loki

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ErrShape: a Loki response of a shape Klyx does not read. The page fails
// whole; nothing of it is shown.
var ErrShape = errors.New("the Loki response is not one Klyx reads")

func shape(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrShape, fmt.Sprintf(format, a...))
}

// Entry is one log line of a response, raw.
type Entry struct {
	Stream map[string]string // the stream's index labels
	// Meta is the entry's structured metadata when the server categorized
	// labels; nil when it merged them into Stream instead.
	Meta map[string]string
	TS   int64 // Unix nanoseconds
	Line string
}

// UID is the k8s_pod_uid the entry carries: from its structured metadata,
// or from the stream when the server merged them (or indexed it).
func (e Entry) UID() string {
	if v := e.Meta[LabelPodUID]; v != "" {
		return v
	}
	return e.Stream[LabelPodUID]
}

type wireResponse struct {
	Status *string `json:"status"`
	Data   *struct {
		ResultType *string         `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type wireStream struct {
	Stream map[string]string `json:"stream"`
	Values []json.RawMessage `json:"values"`
}

// ParseStreams reads a query_range response strictly (S8b §5): status
// success, resultType streams, a result list, and every value
// [ns, line] or [ns, line, {structuredMetadata?, parsed?}]. Anything else
// fails the page whole; null is never an empty list.
func ParseStreams(body []byte) ([]Entry, error) {
	var w wireResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, shape("the response does not decode")
	}
	if w.Status == nil || *w.Status != "success" {
		return nil, shape("the status is not success")
	}
	if w.Data == nil || w.Data.ResultType == nil {
		return nil, shape("the response carries no data")
	}
	if *w.Data.ResultType != "streams" {
		return nil, shape("the result is %q, not streams", *w.Data.ResultType)
	}
	if b := bytes.TrimSpace(w.Data.Result); len(b) == 0 || b[0] != '[' {
		return nil, shape("the result is not a list")
	}
	var streams []*wireStream
	if err := json.Unmarshal(w.Data.Result, &streams); err != nil {
		return nil, shape("the streams do not decode")
	}
	var out []Entry
	for si, s := range streams {
		if s == nil || s.Stream == nil || s.Values == nil {
			return nil, shape("stream %d has no labels or values", si)
		}
		for vi, raw := range s.Values {
			e, err := entry(raw)
			if err != nil {
				return nil, shape("stream %d value %d: %v", si, vi, err)
			}
			e.Stream = s.Stream
			out = append(out, e)
		}
	}
	return out, nil
}

func entry(raw json.RawMessage) (Entry, error) {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || (len(parts) != 2 && len(parts) != 3) {
		return Entry{}, errors.New("not [timestamp, line] or [timestamp, line, labels]")
	}
	var ts, line string
	if err := json.Unmarshal(parts[0], &ts); err != nil {
		return Entry{}, errors.New("the timestamp is not a string")
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || n <= 0 {
		return Entry{}, errors.New("the timestamp is not Unix nanoseconds")
	}
	if err := json.Unmarshal(parts[1], &line); err != nil {
		return Entry{}, errors.New("the line is not a string")
	}
	e := Entry{TS: n, Line: line}
	if len(parts) == 3 {
		var cat struct {
			Meta   map[string]string `json:"structuredMetadata"`
			Parsed map[string]string `json:"parsed"`
		}
		if err := json.Unmarshal(parts[2], &cat); err != nil {
			return Entry{}, errors.New("the categorized labels do not decode")
		}
		e.Meta = cat.Meta
		if e.Meta == nil {
			e.Meta = map[string]string{} // categorized, and none: still categorized
		}
	}
	return e, nil
}
