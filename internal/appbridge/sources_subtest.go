package appbridge

import (
	"context"
	"strconv"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3b §7: a subscription's Test, on a running member's row. The
// page sends the member's name and the revisions its row showed, never a
// tenant or a match: the backend resolves the member's installation,
// effective tenant and match, and reads the label names of its streams
// over the last hour under them, never a line. Each member's Test is its
// own, so one tenant working and another denied are said apart.

// subscriptionTestTimeout bounds one Test, the host's slots waited for
// included.
const subscriptionTestTimeout = 30 * time.Second

// TestSubscription tests cluster's subscription of kind at the revisions
// its row showed. Only a Loki subscription has its own Test: an
// Alertmanager or metrics member is tested through its installation.
func (s *SourcesService) TestSubscription(cluster, kind, instRevision, subRevision string) EndpointActionDTO {
	k, err := kindOf(kind)
	switch {
	case err != nil:
		return EndpointActionDTO{Message: err.Error()}
	case k != config.EndpointLoki:
		return EndpointActionDTO{Message: "a subscription's own Test is Loki's: test this member's installation instead"}
	case s.d.TestLoki == nil:
		return EndpointActionDTO{Message: "Test is not available"}
	}
	inst, err1 := strconv.ParseUint(instRevision, 10, 64)
	sub, err2 := strconv.ParseUint(subRevision, 10, 64)
	if err1 != nil || err2 != nil {
		return EndpointActionDTO{Message: cluster + "'s Loki subscription changed since it was shown: review it again"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), subscriptionTestTimeout)
	defer cancel()
	res, err := s.d.TestLoki(ctx, cluster, inst, sub)
	if err != nil {
		return EndpointActionDTO{Message: "Unavailable: " + safehttp.NewRedactor().Text(err.Error())}
	}
	head := "Available: Loki " + res.Version + ". " + cluster + "'s logs, read " + res.Sentence + ": "
	if !res.Data {
		return EndpointActionDTO{OK: true, Message: head + "no stream answered in the last hour. An unknown tenant, or a match that names no stream, reads the same."}
	}
	return EndpointActionDTO{OK: true, Message: head + "streams answered in the last hour."}
}
