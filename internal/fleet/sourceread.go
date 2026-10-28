package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/readfence"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3b §3.1: Loki and metrics read through an installation per
// subscription, with no buckets. A read pins its terms once (§13); every
// request is fenced by them, before and after; a hosted request holds one
// of its host's slots and runs on a lease of the host's current
// connection. The installation readers (alertBuckets) carry the shared
// parts: the installations in force, the credentials, the host lookup and
// its admission.

const (
	hostMetricsSlots = 4 // hosted metrics requests in flight through one host
	hostLokiSlots    = 2 // hosted Loki pages in flight through one host
)

// hostSlots bound the hosted requests in flight through one host, per
// kind: shared by every installation and subscriber using that host, and
// keyed by the host's name, so a reconnect never resets the count while
// requests of its earlier connection run (§3.1).
type hostSlots struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

// take waits within ctx for one of host's slots of a kind; a cancelled
// wait leaves without one. release gives it back once.
func (h *hostSlots) take(ctx context.Context, kind, host string) (release func(), err error) {
	h.mu.Lock()
	if h.ch == nil {
		h.ch = map[string]chan struct{}{}
	}
	k := kind + "|" + host
	ch := h.ch[k]
	if ch == nil {
		n := hostMetricsSlots
		if kind == config.EndpointLoki {
			n = hostLokiSlots
		}
		ch = make(chan struct{}, n)
		h.ch[k] = ch
	}
	h.mu.Unlock()
	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sourceTerms are a subscribed read's terms, pinned once for a logical
// result (§13): the subscription, the installation's access generation,
// the host connection it was pinned to and a direct installation's
// credential revision.
type sourceTerms struct {
	sub      Subscription
	access   string
	hostConn ConnID
	credRev  uint64
}

func (t sourceTerms) direct() bool { return t.sub.Source.Endpoint != "" }

// key is the read's lineage key (§3.2): a moved installation, subscription
// or access generation re-keys it; a credential's revision does not.
func (t sourceTerms) key() string {
	scope := "match=" + strings.Join(alertmanager.FilterParams(t.sub.Match), ",")
	if t.sub.ScopeTenant {
		scope = "scope=tenant"
	}
	return fmt.Sprintf("%s|source|%s|r%d|s%d|a%s|tenant=%s|%s", t.sub.Kind, t.sub.Source.ID, t.sub.InstRev, t.sub.SubRev, t.access, t.sub.Tenant, scope)
}

// scope is the read's scope: its effective match, none for scope: tenant.
func (t sourceTerms) scope() metrics.Scope {
	if t.sub.ScopeTenant {
		return metrics.Scope{}
	}
	return metrics.ScopeOf(t.sub.Match)
}

// termsOf pins a subscription's terms now.
func (b *alertBuckets) termsOf(sub Subscription) sourceTerms {
	t := sourceTerms{sub: sub, access: b.access(sub.Source)}
	if t.direct() {
		t.credRev = b.creds.Revisions.Revision(endpointcred.SourceAccount(sub.Source.ID), sub.Kind)
	} else if c, ok := b.host(sub.Source.Host); ok {
		t.hostConn = c.ID()
	}
	return t
}

// The ways a read's terms move: each discards what the read had.
var (
	errAccessMoved     = errors.New("the installation's access changed")
	errSourceCredMoved = errors.New("the installation's credential changed")
)

// termsMoved: err is a move of a read's terms (its subscription, access or
// credential), not a host that is down.
func termsMoved(err error) bool {
	return errors.Is(err, errSubscriptionMoved) || errors.Is(err, errAccessMoved) || errors.Is(err, errSourceCredMoved)
}

// checkTerms is nil while a read's terms are in force: its subscription at
// its revisions, the host connected and usable, the installation's access
// generation - which names the host's connection, so a reconnect moves it
// - and a direct installation's credential revision.
func (b *alertBuckets) checkTerms(t sourceTerms) error {
	cur, ok := b.sources.SubscriptionOf(t.sub.Kind, t.sub.Cluster)
	if !ok || cur.SubRev != t.sub.SubRev || cur.InstRev != t.sub.InstRev || cur.Source.ID != t.sub.Source.ID {
		return errSubscriptionMoved
	}
	if err := b.hostDown(t.sub.Source); err != nil {
		return err
	}
	if b.access(t.sub.Source) != t.access {
		return errAccessMoved
	}
	if t.direct() && b.creds.Revisions.Revision(endpointcred.SourceAccount(t.sub.Source.ID), t.sub.Kind) != t.credRev {
		return errSourceCredMoved
	}
	return nil
}

// sourceCredential is a direct installation's credential under t, gated
// on its revision; an unusable one says why.
func (b *alertBuckets) sourceCredential(t sourceTerms) (metrics.Auth, error) {
	src := t.sub.Source
	account := endpointcred.SourceAccount(src.ID)
	revs := b.creds.Revisions
	cred := endpointcred.Resolve(b.creds.Store, endpointcred.Input{Cluster: account, Kind: t.sub.Kind, Account: account,
		Spec: config.EndpointConfig{Endpoint: src.Endpoint, Auth: src.Auth}, Revoked: revs.Revoked(account, t.sub.Kind)})
	if !cred.Usable() {
		why := cred.Why
		if why == "" {
			why = "the credential is " + string(cred.State)
		}
		return metrics.Auth{}, fmt.Errorf("%s: %s", src.ID, why)
	}
	return metrics.Auth{Tenant: t.sub.Tenant, Header: cred.Header(), Redactor: cred.Redactor(), Live: revs.Live(account, t.sub.Kind, t.credRev)}, nil
}

// metricsSource is a subscribed member's metrics transport for one read
// under t: a direct installation's endpoint with its own credential and
// the tenant; or a lease on the host's current connection - whose end
// cancels the read's requests - its guarded proxy, and the Service: the
// serviceRef, or the host's discovery. Every request is fenced by t, and a
// hosted one holds a host slot. release ends the lease; display names
// where the installation is read.
func (b *alertBuckets) metricsSource(ctx context.Context, t sourceTerms, what string) (q metrics.Transport, display string, release func(), err error) {
	src := t.sub.Source
	fence := metrics.Fence{
		Check:  func() error { return b.checkTerms(t) },
		Denied: func() { b.sources.Denied(src.ID) },
	}
	if t.direct() {
		at, err := endpointAt(src.Endpoint)
		if err != nil {
			return metrics.Transport{}, "", nil, fmt.Errorf("%s: %w", src.ID, err)
		}
		auth, err := b.sourceCredential(t)
		if err != nil {
			return metrics.Transport{}, at, nil, err
		}
		client := safehttp.NewClient
		if b.client != nil {
			client = b.client
		}
		d := metrics.NewDirectTransportAuth(strings.TrimRight(src.Endpoint, "/"), auth, client(metrics.DirectPolicy(metricsHTTPTimeout, src.TLSSkipVerify)))
		return metrics.Fenced(d, fence), at, func() {}, nil
	}
	conn, life, rel, err := b.lease(src.Host, what)
	if err != nil {
		return metrics.Transport{}, "", nil, fmt.Errorf("%s's host %s is not connected: %w", src.ID, src.Host, err)
	}
	host, ok := conn.(*ClusterConn)
	if !ok {
		rel()
		return metrics.Transport{}, "", nil, fmt.Errorf("%s's host %s cannot proxy a Service", src.ID, src.Host)
	}
	// the lease is on the connection the terms pinned: a reconnect in
	// between is a move
	if host.ID() != t.hostConn {
		rel()
		return metrics.Transport{}, "", nil, errAccessMoved
	}
	var cand metrics.ServiceCandidate
	if sr := src.ServiceRef; sr != nil {
		cand = metrics.ServiceCandidate{Namespace: sr.Namespace, Name: sr.Name, Port: sr.Port, Scheme: sr.Scheme, Path: sr.Path}
	} else {
		d, err := b.discoverMetrics(ctx, life, host, t.sub.InstRev, fence.Check)
		switch {
		case err != nil:
			rel()
			if termsMoved(err) {
				return metrics.Transport{}, "", nil, err
			}
			return metrics.Transport{}, "", nil, fmt.Errorf("%s's discovery in %s did not finish: %w", src.ID, src.Host, err)
		case d.MultiMatch:
			rel()
			return metrics.Transport{}, "", nil, fmt.Errorf("%s found several candidate Prometheus Services in %s: give it a serviceRef", src.ID, src.Host)
		case d.Chosen == nil:
			rel()
			return metrics.Transport{}, "", nil, fmt.Errorf("%s found no Prometheus in %s", src.ID, src.Host)
		}
		cand = *d.Chosen
	}
	if cand.Scheme == "" {
		cand.Scheme = "http"
	}
	rc, err := host.proxyREST()
	if err != nil {
		rel()
		return metrics.Transport{}, "", nil, fmt.Errorf("%s: the service-proxy client could not be built: %w", src.ID, err)
	}
	fence.Slot = func(ctx context.Context) (func(), error) { return b.slots.take(ctx, config.EndpointMetrics, src.Host) }
	fence.Life = life
	p := metrics.NewProxyTransportTenant(rc, cand, t.sub.Tenant)
	svc := serviceAt(serviceOf(&config.MetricsServiceRef{Namespace: cand.Namespace, Name: cand.Name, Port: cand.Port, Scheme: cand.Scheme, Path: cand.Path}))
	return metrics.Fenced(p, fence), "Service " + svc + " in " + src.Host, rel, nil
}

// metricsDisc is a host's metrics discovery for an installation: found,
// kept for the host connection's life; none or several, for the
// unavailable TTL.
type metricsDisc struct {
	res metrics.DiscoveryResult
	at  time.Time
}

// discoverMetrics runs the existing discovery in the host, cached per host
// connection and installation revision (§3.1). Its reads are hosted reads
// (Codex's review of S3b-2b, R2): under the host lease, whose end cancels
// them, each in one of the host's metrics slots, released before the next
// is taken, and each with the read's terms checked before, after, and at
// every round trip. A refused or ended discovery is returned, never
// cached.
func (b *alertBuckets) discoverMetrics(ctx context.Context, life context.Context, host *ClusterConn, instRev uint64, check func() error) (metrics.DiscoveryResult, error) {
	k := discKey{host: host.ID(), inst: instRev}
	now := b.clk.Now()
	b.mu.Lock()
	if d, ok := b.mdisc[k]; ok && (d.res.Chosen != nil || now.Sub(d.at) < metricsUnavailableTTL) {
		b.mu.Unlock()
		return d.res, nil
	}
	b.mu.Unlock()
	ctx, cancel := context.WithCancel(readfence.With(ctx, check))
	defer cancel()
	if life != nil {
		defer context.AfterFunc(life, cancel)()
	}
	res, err := host.discoverWith(ctx, func(ctx context.Context) (func() error, error) {
		release, err := b.slots.take(ctx, config.EndpointMetrics, host.name)
		if err != nil {
			return nil, err
		}
		if err := check(); err != nil {
			release()
			return nil, err
		}
		return func() error { release(); return check() }, nil
	})
	if err != nil {
		return metrics.DiscoveryResult{}, err
	}
	b.mu.Lock()
	b.mdisc[k] = metricsDisc{res: res, at: now}
	b.mu.Unlock()
	return res, nil
}
