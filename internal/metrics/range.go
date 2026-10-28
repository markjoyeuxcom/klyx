package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RangeQuerier executes a PromQL range query (query_range). Both production
// transports implement it; it is separate from Querier so existing fakes that
// only answer instant queries keep compiling.
type RangeQuerier interface {
	RangeQuery(ctx context.Context, promql string, start, end time.Time, step time.Duration) (status int, body []byte, err error)
}

// Point is one timestamped sample of a range series. Unix is seconds.
// Consecutive points are NOT guaranteed to be one step apart: Prometheus omits
// steps with no data, and RangeSeries preserves that. Gaps stay gaps.
type Point struct {
	Unix  int64   `json:"t"`
	Value float64 `json:"v"`
}

type matrixElem struct {
	Metric map[string]string `json:"metric"`
	Values []json.RawMessage `json:"values"` // [[ts, "val"], ...]
}

// RangeSeries runs a range query expecting AT MOST one series (callers
// aggregate with sum()/avg() so the matrix has 0 or 1 elements). An empty
// matrix returns an empty slice and nil error; >1 series is an error.
// Non-finite (NaN/Inf) points are skipped, leaving a gap.
func (c *Client) RangeSeries(ctx context.Context, q Query, start, end time.Time, step time.Duration) (pts []Point, err error) {
	pts, _, err = c.RangeSeriesSeen(ctx, q, start, end, step)
	return pts, err
}

// RangeSeriesSeen is RangeSeries with the number of series the matrix held
// (0 or 1): zero is an answer with no series, told apart from one series
// whose points were all gaps (Settings S3b §3.3).
func (c *Client) RangeSeriesSeen(ctx context.Context, q Query, start, end time.Time, step time.Duration) (pts []Point, n int, err error) {
	defer func() { err = c.redact(err) }()
	series, err := c.RangeMatrix(ctx, q, start, end, step)
	if err != nil {
		return nil, 0, err
	}
	if len(series) == 0 {
		return []Point{}, 0, nil
	}
	if len(series) > 1 {
		return nil, 0, fmt.Errorf("expected single series, got %d (aggregate the query)", len(series))
	}
	return series[0].Points, 1, nil
}

// LabeledSeries is one element of a range query's matrix: its labels and its
// points, gaps kept.
type LabeledSeries struct {
	Labels map[string]string
	Points []Point
}

// RangeMatrix runs a range query and returns every series with its labels.
// Non-finite points are skipped, leaving a gap. An empty matrix returns an
// empty slice and nil error.
func (c *Client) RangeMatrix(ctx context.Context, q Query, start, end time.Time, step time.Duration) (res []LabeledSeries, err error) {
	defer func() { err = c.redact(err) }()
	promql, err := q.text(c.scope)
	if err != nil {
		return nil, err
	}
	rq, ok := c.q.(RangeQuerier)
	if !ok {
		return nil, fmt.Errorf("transport does not support range queries")
	}
	status, body, err := rq.RangeQuery(ctx, promql, start, end, step)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("prometheus returned HTTP %d", status)
	}
	var env promEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("not a Prometheus API response: %w", err)
	}
	if env.Status == "" {
		return nil, fmt.Errorf("not a Prometheus API response (empty status field)")
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s", env.Error)
	}
	if env.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("expected matrix result, got %q", env.Data.ResultType)
	}
	var elems []matrixElem
	if err := json.Unmarshal(env.Data.Result, &elems); err != nil {
		return nil, fmt.Errorf("parse matrix: %w", err)
	}
	out := make([]LabeledSeries, 0, len(elems))
	for _, e := range elems {
		pts := make([]Point, 0, len(e.Values))
		for _, raw := range e.Values {
			ts, v, err := parseRangePoint(raw)
			if errors.Is(err, errNonFinite) {
				continue // skip NaN/Inf — the gap is the honest signal
			}
			if err != nil {
				return nil, fmt.Errorf("parse matrix value: %w", err)
			}
			pts = append(pts, Point{Unix: ts, Value: v})
		}
		out = append(out, LabeledSeries{Labels: e.Metric, Points: pts})
	}
	return out, nil
}

// parseRangePoint reads one [ts, "val"] pair from a matrix values array,
// returning the integer timestamp alongside the parsed float. Value semantics
// match parseValueTuple (errNonFinite for NaN/Inf).
func parseRangePoint(raw json.RawMessage) (int64, float64, error) {
	var pair []json.RawMessage
	if err := json.Unmarshal(raw, &pair); err != nil {
		return 0, 0, err
	}
	if len(pair) != 2 {
		return 0, 0, fmt.Errorf("expected [timestamp, value] tuple, got %d elements", len(pair))
	}
	var ts float64
	if err := json.Unmarshal(pair[0], &ts); err != nil {
		return 0, 0, err
	}
	v, err := parseValueTuple(raw)
	if err != nil {
		return 0, 0, err
	}
	return int64(ts), v, nil
}
