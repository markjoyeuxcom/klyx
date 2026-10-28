package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// LabeledSample is one element of an instant vector query result.
type LabeledSample struct {
	Labels map[string]string
	Value  float64
}

type vectorElem struct {
	Metric map[string]string `json:"metric"`
	Value  json.RawMessage   `json:"value"` // [ts, "val"]
}

// InstantVector runs an instant query expecting a vector and returns every
// element with its labels. NaN/Inf values are SKIPPED (a non-finite sample is
// "not meaningful", consistent with InstantScalar's absent handling), so they
// never reach the reducer or the DTO. An empty vector returns an empty slice
// and nil error.
func (c *Client) InstantVector(ctx context.Context, q Query) (res []LabeledSample, err error) {
	res, _, err = c.InstantVectorSeen(ctx, q)
	return res, err
}

// InstantVectorSeen is InstantVector with the number of series the vector
// held, a non-finite one included: zero is an answer with no series at all,
// told apart from one whose every value was non-finite (Settings S3b §3.3).
func (c *Client) InstantVectorSeen(ctx context.Context, q Query) (res []LabeledSample, series int, err error) {
	defer func() { err = c.redact(err) }()
	promql, err := q.text(c.scope)
	if err != nil {
		return nil, 0, err
	}
	status, body, err := c.q.InstantQuery(ctx, promql)
	if err != nil {
		return nil, 0, err
	}
	if status != 200 {
		return nil, 0, fmt.Errorf("prometheus returned HTTP %d", status)
	}
	var env promEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, 0, fmt.Errorf("not a Prometheus API response: %w", err)
	}
	if env.Status == "" {
		return nil, 0, fmt.Errorf("not a Prometheus API response (empty status field)")
	}
	if env.Status != "success" {
		return nil, 0, fmt.Errorf("prometheus error: %s", env.Error)
	}
	if env.Data.ResultType != "vector" {
		return nil, 0, fmt.Errorf("expected vector result, got %q", env.Data.ResultType)
	}
	var elems []vectorElem
	if err := json.Unmarshal(env.Data.Result, &elems); err != nil {
		return nil, 0, fmt.Errorf("parse vector: %w", err)
	}
	out := make([]LabeledSample, 0, len(elems))
	series = len(elems)
	for _, e := range elems {
		v, err := parseValueTuple(e.Value)
		if errors.Is(err, errNonFinite) {
			continue // skip NaN/Inf
		}
		if err != nil {
			return nil, 0, fmt.Errorf("parse vector value: %w", err)
		}
		out = append(out, LabeledSample{Labels: e.Metric, Value: v})
	}
	return out, series, nil
}
