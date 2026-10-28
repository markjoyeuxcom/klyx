package metrics

import (
	"context"

	"github.com/moomora/klyx/internal/safehttp"
)

// Probe resolves a candidate then liveness-checks it. Available is true only
// after a passing vector(1). A resolved-but-unreachable backend reports
// unavailable with the real error.
func Probe(ctx context.Context, res Resolution) MetricsCapability {
	out := MetricsCapability{Mode: res.Mode, Source: res.Source, Warning: res.Warning, Reason: res.Reason}
	if res.Transport == nil {
		return out // Available stays false; Reason already set for unavailable
	}
	if err := NewClient(res.Transport, Scope{}).Liveness(ctx); err != nil {
		// the transport already redacted its own credential; the general
		// credential shapes go too before the reason reaches the page (C5 §5)
		out.Reason = safehttp.NewRedactor().Text(err.Error())
		return out
	}
	out.Available = true
	return out
}
