package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/endpointhttp"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/safehttp"
)

// InstallationProbe tests a Loki or metrics installation once, as the
// person tests it (Settings S3b §7): reachability, never anything of a
// member. Loki's build info, which needs no tenant; metrics' liveness
// probe, which reads no series. The installation is the one reviewed, at
// that revision, and the request is fenced before and after by that
// revision, its host, its access and its credential, as an Alertmanager
// installation's Test is. It answers the words the answer said and where
// the installation was read; an Alertmanager installation is
// InstallationStatus's.
func (r *Registry) InstallationProbe(ctx context.Context, id string, revision uint64) (words, where string, err error) {
	a := r.AlertSources()
	r.mu.RLock()
	b := r.alerts
	r.mu.RUnlock()
	if a == nil || b == nil {
		return "", "", errors.New("no installation is in force")
	}
	src, rev, ok := a.Installation(id)
	switch {
	case !ok:
		return "", "", fmt.Errorf("%s is not in force", id)
	case rev != revision:
		return "", "", fmt.Errorf("%s changed since it was shown: review it again", id)
	case src.Kind != config.SourceKindLoki && src.Kind != config.SourceKindMetrics:
		return "", "", fmt.Errorf("%s is tested by its status", id)
	}
	if err := b.hostDown(src); err != nil {
		return "", "", err
	}
	// the installation's own terms: no member, so its own tenant
	t := b.termsOf(Subscription{Kind: src.Kind, Source: src, InstRev: rev, Tenant: src.Tenant})
	check := func() error {
		if _, now, ok := a.Installation(id); !ok || now != revision {
			return fmt.Errorf("%s changed during the test, so nothing is said of it", id)
		}
		if err := b.hostDown(src); err != nil {
			return err
		}
		if b.access(src) != t.access {
			return fmt.Errorf("%s's access changed during the test", id)
		}
		if t.direct() && b.creds.Revisions.Revision(endpointcred.SourceAccount(id), src.Kind) != t.credRev {
			return fmt.Errorf("%s's credential changed during the test", id)
		}
		return nil
	}
	// an observed 401 or 403 is reported, never counted as a loss of
	// access (as InstallationStatus)
	var refused atomic.Bool
	fence := endpointhttp.Fence{Check: check, Denied: func() { refused.Store(true) }}
	said := func(err error) error {
		if refused.Load() {
			return fmt.Errorf("%s refused Klyx's read (401 or 403)", id)
		}
		return err
	}
	what := "a test of " + id
	if src.Kind == config.SourceKindMetrics {
		tr, at, release, err := b.metricsTransport(ctx, t, fence, what)
		if err != nil {
			return "", at, err
		}
		defer release()
		// the fence checks the terms before and after the request, so the
		// result is settled before the lease is released
		if err := metrics.NewClient(tr, metrics.Scope{}).Liveness(ctx); err != nil {
			return "", at, said(err)
		}
		return "the Prometheus API answered", at, nil
	}
	client := safehttp.NewClient
	if b.client != nil {
		client = b.client
	}
	get, at, red, err := b.lokiSource(t, fence, what, client)
	if err != nil {
		return "", at, err
	}
	select {
	case lokiPages <- struct{}{}:
		defer func() { <-lokiPages }()
	case <-ctx.Done():
		return "", at, fmt.Errorf("%s was not read in time: %w", id, ctx.Err())
	}
	code, body, err := get(ctx, lokiBuildInfo, nil, nil)
	if err != nil {
		return "", at, said(red.Err(err, context.Canceled, context.DeadlineExceeded))
	}
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return "", at, fmt.Errorf("%s refused Klyx's read (401 or 403)", id)
	case code < 200 || code > 299:
		return "", at, fmt.Errorf("%s answered %d", id, code)
	}
	v, err := buildVersion(body, red)
	if err != nil {
		return "", at, err
	}
	return "Loki " + v + " answered", at, nil
}
