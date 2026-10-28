package fleet

import (
	"context"
	"errors"
	"fmt"

	"github.com/moomora/klyx/internal/config"
)

// SubscriptionTest is what a member's Loki subscription's Test read: the
// build info, and the label names of the member's streams over the last
// hour, under the scope its subscription resolves to; never a line.
type SubscriptionTest struct {
	LokiTestResult
	// Sentence is the scope the labels were read under (S3b §3.4)
	Sentence string
}

// TestLokiSubscription tests cluster's Loki subscription once (Settings
// S3b §7), as the person tests it from the member's row: the member's
// installation, effective tenant and match resolved here, never sent by
// the page, at the revisions the page showed. The member is leased, and
// its reader pins the terms from admission to settlement: every page is
// fenced by them under the host's lease and slot rules, and a move of the
// terms, or the member's reconnection or removal, discards the result.
func (r *Registry) TestLokiSubscription(ctx context.Context, cluster string, instRev, subRev uint64) (out SubscriptionTest, err error) {
	a := r.AlertSources()
	r.mu.RLock()
	b := r.alerts
	r.mu.RUnlock()
	if a == nil || b == nil {
		return out, errors.New("the installations are not available")
	}
	if _, ok := a.SubscriptionOf(config.EndpointLoki, cluster); !ok {
		return out, fmt.Errorf("%s has no Loki subscription in force", cluster)
	}
	conn, lctx, release, err := b.lease(cluster, "a test of "+cluster+"'s log history")
	if err != nil {
		return out, fmt.Errorf("%s is not connected: %w", cluster, err)
	}
	defer release()
	c, ok := conn.(*ClusterConn)
	if !ok {
		return out, errors.New(cluster + " cannot be tested")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(lctx, cancel)()
	rd, err := c.Loki()
	if err != nil {
		return out, err
	}
	// the terms the reader pinned are the ones the row showed
	if rd.terms == nil || rd.terms.sub.InstRev != instRev || rd.terms.sub.SubRev != subRev {
		return out, fmt.Errorf("%s's Loki subscription changed since it was shown: review it again", cluster)
	}
	// each page checks the pinned terms after it is read, the last one
	// included; the member's lease is settled here, before its release
	res, err := lokiTestWith(ctx, rd)
	switch {
	case lctx.Err() != nil:
		return SubscriptionTest{}, fmt.Errorf("%s was reconnected or removed during the test, so nothing is said of it", cluster)
	case errors.Is(err, ErrLokiSourceChanged):
		return SubscriptionTest{}, fmt.Errorf("%s's Loki subscription changed during the test, so nothing is said of it", cluster)
	case err != nil:
		return SubscriptionTest{}, err
	}
	return SubscriptionTest{LokiTestResult: res, Sentence: rd.Sentence()}, nil
}
