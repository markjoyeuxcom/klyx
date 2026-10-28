package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/moomora/klyx/internal/safehttp"
)

// Client runs queries through a Transport and parses Prometheus responses.
// Every error it returns passes, after decoding, through the redactor of the
// credential its transport carries (coverage C5 §5): a JSON-escaped echo of
// the secret is whole again only once decoded. Samples - label names and
// values, timestamps, values - are returned as the endpoint sent them: they
// are what joins and measurements use, and a credential that happens to
// equal one of them must not change the data. A decoded string Klyx shows
// passes through Display.
//
// A Client is the pinned reader's: it renders every query with the scope
// of the reader's own terms (Settings S3b §3.3), fixed when the reader makes
// it, never one a caller chooses.
type Client struct {
	q     Querier // its Transport's
	scope Scope
}

// NewClient is a client reading through q under scope.
func NewClient(t Transport, scope Scope) *Client { return &Client{q: t.q, scope: scope} }

// Scope is the scope this client renders every query with.
func (c *Client) Scope() Scope { return c.scope }

// credentialed is a Querier that carries a credential.
type credentialed interface{ redactor() safehttp.Redactor }

func (c *Client) red() safehttp.Redactor {
	if r, ok := c.q.(credentialed); ok {
		return r.redactor()
	}
	return safehttp.Redactor{}
}

// redact is err as it may leave the package.
func (c *Client) redact(err error) error {
	if err == nil {
		return nil
	}
	return c.red().Err(err, ErrCredentialChanged, ErrResponseTooLarge, ErrUnregisteredQuery, ErrInvalidScope)
}

// Display is a decoded string from a response (a label value Klyx shows)
// without the credential the transport carries.
func (c *Client) Display(s string) string { return c.red().Exact(s) }

type promEnvelope struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

type vectorSample struct {
	Value json.RawMessage `json:"value"` // [ts, "val"]
}

// errNonFinite signals a NaN/Inf sample value. It is no-data, not a malformed
// response, so callers map it to Sample{Absent:true}.
var errNonFinite = errors.New("non-finite sample value")

// parseValueTuple reads a Prometheus [ts, "val"] pair and returns the float.
// Values are JSON strings; we compare the PARSED float, never the raw string.
// Non-finite values (NaN/Inf) return errNonFinite so callers treat them as
// no-data rather than leaking them as real measurements.
func parseValueTuple(raw json.RawMessage) (float64, error) {
	var pair []json.RawMessage
	if err := json.Unmarshal(raw, &pair); err != nil {
		return 0, err
	}
	if len(pair) != 2 {
		return 0, fmt.Errorf("expected [timestamp, value] tuple, got %d elements", len(pair))
	}
	var vs string
	if err := json.Unmarshal(pair[1], &vs); err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(vs, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errNonFinite
	}
	return f, nil
}

// InstantScalar runs an instant query expecting a scalar or single-element
// vector. Empty vector or a non-finite (NaN/Inf) value → Sample{Absent:true}.
// Multi-element vector → error.
func (c *Client) InstantScalar(ctx context.Context, q Query) (s Sample, err error) {
	defer func() { err = c.redact(err) }()
	promql, err := q.text(c.scope)
	if err != nil {
		return Sample{}, err
	}
	status, body, err := c.q.InstantQuery(ctx, promql)
	if err != nil {
		return Sample{}, err
	}
	if status != 200 {
		return Sample{}, fmt.Errorf("prometheus returned HTTP %d", status)
	}
	var env promEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Sample{}, fmt.Errorf("not a Prometheus API response: %w", err)
	}
	if env.Status == "" {
		return Sample{}, fmt.Errorf("not a Prometheus API response (empty status field)")
	}
	if env.Status != "success" {
		return Sample{}, fmt.Errorf("prometheus error: %s", env.Error)
	}
	switch env.Data.ResultType {
	case "scalar":
		v, err := parseValueTuple(env.Data.Result)
		if errors.Is(err, errNonFinite) {
			return Sample{Absent: true}, nil
		}
		if err != nil {
			return Sample{}, fmt.Errorf("parse scalar: %w", err)
		}
		return Sample{Value: v}, nil
	case "vector":
		var vec []vectorSample
		if err := json.Unmarshal(env.Data.Result, &vec); err != nil {
			return Sample{}, fmt.Errorf("parse vector: %w", err)
		}
		if len(vec) == 0 {
			return Sample{Absent: true}, nil
		}
		if len(vec) > 1 {
			return Sample{}, fmt.Errorf("expected single-element vector, got %d", len(vec))
		}
		v, err := parseValueTuple(vec[0].Value)
		if errors.Is(err, errNonFinite) {
			return Sample{Absent: true}, nil
		}
		if err != nil {
			return Sample{}, fmt.Errorf("parse vector value: %w", err)
		}
		return Sample{Value: v}, nil
	default:
		return Sample{}, fmt.Errorf("unexpected resultType %q", env.Data.ResultType)
	}
}

// liveness is the one query outside data admission (Settings S3b §3.3): it
// reads no series, so no scope applies, and it is never a registered data
// template.
var liveness = Query{name: "liveness", render: func(string) string { return "vector(1)" }}

// Liveness runs vector(1) and returns nil only on a valid Prometheus 200 whose
// single value parses to 1.0. It proves the API answers, not that any
// series exists under the scope.
func (c *Client) Liveness(ctx context.Context) (err error) {
	defer func() { err = c.redact(err) }()
	s, err := c.InstantScalar(ctx, liveness)
	if err != nil {
		return err
	}
	if s.Absent || s.Value != 1 {
		return fmt.Errorf("liveness query did not return 1 (absent=%v value=%v)", s.Absent, s.Value)
	}
	return nil
}
