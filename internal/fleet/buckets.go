package fleet

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/endpointhttp"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3a §3.2: an installation's alerts are read once per bucket -
// its subscriptions with one effective tenant - for the members reading
// it, then split among them. A bucket reads on its own cadence for its
// cohort; a member's read is served from the bucket's latest outcome when
// that read covered it, and waits for the early read its join asked for
// otherwise. Buckets take today's fleet-wide slots and add none.

// HostLeaser admits a bucket's read on a host's current connection through
// S2's admission (S3a §13): the connection, a context that ends when that
// connection is retired, and a release. Nil: the registry's current
// connection, with no admission (tests, the CLI).
type HostLeaser func(host, what string) (conn Conn, ctx context.Context, release func(), err error)

// bucketKey is a bucket: an installation at a revision and an access
// generation, and one effective tenant.
type bucketKey struct {
	Source  string
	InstRev uint64
	Access  string
	Tenant  string
}

// memberScope is one cohort member: the cluster, its subscription's
// revision and scope, and the key its snapshot is read under.
type memberScope struct {
	cluster     string
	subRev      uint64
	match       map[string]string
	scopeTenant bool
	key         string
}

// outcome is a bucket read's result (S3a §3.2): its acquisition time, the
// cohort it covered, each member's share (projected rows only), what
// matched no member, and the lineage. A failed read keeps the last good
// shares, with its failure.
type outcome struct {
	at        time.Time
	cohort    map[ConnID]uint64
	shares    map[ConnID][]alertmanager.Item
	truncated bool
	unmatched int
	err       error
	good      bool
	lineage   alertmanager.Lineage
	display   string
	redactor  safehttp.Redactor
	// credRev is a direct installation's credential revision when the read
	// began: the outcome is served only while it is current
	credRev uint64
}

type bucket struct {
	b      *alertBuckets
	key    bucketKey
	src    config.SourceConfig
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	early  chan struct{}

	mu      sync.Mutex
	cohort  map[ConnID]memberScope
	outcome *outcome
	waiters []chan struct{}
	// pending: an early read was asked for and has not started; inflight:
	// the cohort of the read in progress, each member at the subscription
	// revision it covers. A member asks once (Codex's review of S3a-2, R4),
	// and a new revision owes its own read (re-review, RR2).
	pending  bool
	inflight map[ConnID]uint64

	// the bucket's dependency on its host's connection for its whole life
	// (R6); its run goroutine's alone
	hostRelease func()
	hostStop    func() bool
}

// alertBuckets are the fleet's bucket readers.
type alertBuckets struct {
	ctx     context.Context
	sources *AlertSources
	creds   Credentials
	clk     clock.Clock
	host    func(name string) (*ClusterConn, bool)
	timeout time.Duration
	client  func(safehttp.Policy) *http.Client // tests: a direct endpoint's client

	mu      sync.Mutex
	cadence time.Duration
	leaser  HostLeaser
	buckets map[bucketKey]*bucket
	members map[ConnID]bucketKey
	// retired are the connections that stopped: they never join again, so
	// a late read of an ended generation registers nothing (R3). A
	// connection's identity is never reused.
	retired map[ConnID]bool
	gates   map[string]chan struct{}
	disc    map[discKey]*amDiscovery
	// mdisc are the hosts' metrics discoveries, and slots the hosted
	// requests in flight per host (Settings S3b §3.1)
	mdisc map[discKey]metricsDisc
	slots hostSlots
}

type discKey struct {
	host ConnID
	inst uint64
}

func newAlertBuckets(ctx context.Context, sources *AlertSources, creds Credentials, clk clock.Clock, host func(string) (*ClusterConn, bool)) *alertBuckets {
	return &alertBuckets{ctx: ctx, sources: sources, creds: creds, clk: clk, host: host,
		cadence: healthsrc.AlertsCadence, timeout: healthsrc.AlertsReadTimeout,
		buckets: map[bucketKey]*bucket{}, members: map[ConnID]bucketKey{}, retired: map[ConnID]bool{}, gates: map[string]chan struct{}{}, disc: map[discKey]*amDiscovery{}, mdisc: map[discKey]metricsDisc{}}
}

// access is an installation's access generation now (S3a §3.4): a hosted
// one's host connection identity and published usability incarnation,
// with the 401s and 403s observed; a direct one's observed denials. It
// moves, and never returns, at a reconnect, a recovery in place and an
// observed loss of access.
func (b *alertBuckets) access(src config.SourceConfig) string {
	den := b.sources.Denials(src.ID)
	if src.Endpoint != "" {
		return fmt.Sprintf("d%d", den)
	}
	c, ok := b.host(src.Host)
	if !ok {
		return fmt.Sprintf("none.%d", den)
	}
	inc, _ := c.publishedUsability()
	return fmt.Sprintf("%d.%d.%d", c.ID(), inc, den)
}

// memberKey is a member's snapshot key for its subscription in force: its
// lineage (S3a §3.4). ok is false when the cluster has no subscription in
// force.
func (b *alertBuckets) memberKey(cluster string) (key string, sub Subscription, access string, ok bool) {
	sub, ok = b.sources.Subscription(cluster)
	if !ok {
		return "source|unresolved|" + cluster, sub, "", false
	}
	access = b.access(sub.Source)
	scope := "match=" + strings.Join(alertmanager.FilterParams(sub.Match), ",")
	if sub.ScopeTenant {
		scope = "scope=tenant"
	}
	return fmt.Sprintf("source|%s|r%d|s%d|a%s|tenant=%s|%s", sub.Source.ID, sub.InstRev, sub.SubRev, access, sub.Tenant, scope), sub, access, true
}

// errSubscriptionMoved is a member whose subscription changed while it
// read: it is re-keyed, never served a share read under the old one.
var errSubscriptionMoved = errors.New("the subscription changed")

// errMemberStopped is a read of a connection that stopped.
var errMemberStopped = errors.New("the connection stopped")

// read is a member's read of its share (S3a §3.2): served from the
// bucket's latest outcome when that read covered the member under its
// subscription revision, still the one in force; otherwise the member
// waits, within ctx, for the early read its join asked for. A member that
// joins while a read is in flight is not in that read's cohort.
func (b *alertBuckets) read(ctx context.Context, member ConnID, sub Subscription, key, access string) (alertmanager.Read, time.Time, error) {
	if err := ctx.Err(); err != nil {
		// an ended read registers nothing (Codex's review of S3a-2, R3)
		return alertmanager.Read{Identity: key}, time.Time{}, fmt.Errorf("%s was not read in time: %w", sub.Source.ID, err)
	}
	bk := bucketKey{Source: sub.Source.ID, InstRev: sub.InstRev, Access: access, Tenant: sub.Tenant}
	bu, ok := b.join(member, bk, sub, key)
	if !ok {
		return alertmanager.Read{Identity: key}, time.Time{}, errMemberStopped
	}
	for {
		// the subscription in force now: a share read under another is
		// never served (R1)
		if cur, ok := b.sources.Subscription(sub.Cluster); !ok || cur.SubRev != sub.SubRev {
			return alertmanager.Read{Identity: key}, time.Time{}, errSubscriptionMoved
		}
		// a host known to be down serves nothing, a held share included: the
		// read fails with its reason, and what was read before goes stale
		// (S3a §3.3; Codex's re-review of S3a-2, RR1)
		if err := b.hostDown(sub.Source); err != nil {
			return alertmanager.Read{Identity: key}, time.Time{}, err
		}
		bu.mu.Lock()
		if o := bu.outcome; o != nil {
			if rev, in := o.cohort[member]; in && rev == sub.SubRev && bu.credCurrent(o) {
				r, at, err := bu.forMemberLocked(o, member)
				bu.mu.Unlock()
				return r, at, err
			}
		}
		ch := make(chan struct{})
		bu.waiters = append(bu.waiters, ch)
		// one early read per join: none while a read that covers the member
		// at this revision is in flight, or one is already asked for (R4,
		// RR2)
		if rev, in := bu.inflight[member]; !(in && rev == sub.SubRev) && !bu.pending {
			bu.pending = true
			bu.nudge()
		}
		bu.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return alertmanager.Read{Identity: key}, time.Time{}, fmt.Errorf("%s was not read in time: %w", sub.Source.ID, ctx.Err())
		case <-bu.done:
			return alertmanager.Read{Identity: key}, time.Time{}, ErrSourceChanged
		}
	}
}

// hostDown is why a hosted installation's host cannot serve now: not
// connected, or connected and not usable. nil for a direct installation.
func (b *alertBuckets) hostDown(src config.SourceConfig) error {
	if src.Host == "" {
		return nil
	}
	c, ok := b.host(src.Host)
	if !ok {
		return fmt.Errorf("%s's host %s is not connected", src.ID, src.Host)
	}
	if _, usable := c.publishedUsability(); !usable {
		return fmt.Errorf("%s's host %s is not connected", src.ID, src.Host)
	}
	return nil
}

// credCurrent is whether an outcome was read under the credential a direct
// installation holds now. One read before a credential was stored,
// replaced or removed is not served again: its lineage would not hold
// (S3a §4.3), so the member asks for a read under the new one, keeping its
// key.
func (bu *bucket) credCurrent(o *outcome) bool {
	return bu.src.Endpoint == "" || bu.b.creds.Revisions.Revision("source/"+bu.src.ID, config.EndpointAlertmanager) == o.credRev
}

// forMemberLocked is a member's share of an outcome, under bu.mu.
func (bu *bucket) forMemberLocked(o *outcome, member ConnID) (alertmanager.Read, time.Time, error) {
	s := bu.cohort[member]
	if o.err != nil {
		return alertmanager.Read{Identity: s.key}, time.Time{}, o.err
	}
	share := o.shares[member]
	if share == nil {
		share = []alertmanager.Item{}
	}
	lin := o.lineage
	lin.Identity, lin.SubRev = s.key, s.subRev
	return alertmanager.Read{Identity: s.key, Source: bu.sentence(o, s), Match: alertmanager.DisplayMatch(s.match, o.redactor),
		Alerts: share, Truncated: o.truncated, Lineage: lin}, o.at, nil
}

// sentence is the scope sentence (S3a §3.5): where the alerts come from,
// and what makes them this member's.
func (bu *bucket) sentence(o *outcome, s memberScope) string {
	parts := []string{"from " + bu.src.ID}
	if bu.src.Host != "" {
		parts[0] += ", hosted by " + bu.src.Host
	} else {
		parts[0] += " at " + o.display
	}
	if bu.key.Tenant != "" {
		parts = append(parts, "tenant "+bu.key.Tenant)
	}
	if s.scopeTenant {
		parts = append(parts, "everything in its tenant")
	} else {
		words := make([]string, 0, len(s.match))
		for k, v := range alertmanager.DisplayMatch(s.match, o.redactor) {
			words = append(words, k+"="+v)
		}
		sort.Strings(words)
		parts = append(parts, "filtered by "+strings.Join(words, ", "))
	}
	return strings.Join(parts, ", ")
}

// join puts a member in its bucket's cohort, leaving any other bucket
// first. Its read asks for the early read, once, when the latest outcome
// did not cover it. A retired connection joins nothing: ok is false. The
// cohort entry is added under the fleet lock, so a retirement or a leave
// never interleaves with it.
func (b *alertBuckets) join(member ConnID, bk bucketKey, sub Subscription, key string) (*bucket, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.retired[member] {
		return nil, false
	}
	if prev, ok := b.members[member]; ok && prev != bk {
		b.leaveLocked(member)
	}
	bu := b.buckets[bk]
	if bu == nil {
		bu = b.newBucketLocked(bk, sub.Source)
	}
	b.members[member] = bk
	bu.mu.Lock()
	bu.cohort[member] = memberScope{cluster: sub.Cluster, subRev: sub.SubRev, match: sub.Match, scopeTenant: sub.ScopeTenant, key: key}
	bu.mu.Unlock()
	return bu, true
}

// retire takes a stopped connection out of its bucket for good: it never
// joins again (R3).
func (b *alertBuckets) retire(member ConnID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retired[member] = true
	b.leaveLocked(member)
}

// leave takes a member out of its bucket: its snapshot generation stopped
// or re-keyed. A bucket whose cohort is empty is cancelled; its outcome is
// never served again.
func (b *alertBuckets) leave(member ConnID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.leaveLocked(member)
}

func (b *alertBuckets) leaveLocked(member ConnID) {
	bk, ok := b.members[member]
	if !ok {
		return
	}
	delete(b.members, member)
	bu := b.buckets[bk]
	if bu == nil {
		return
	}
	bu.mu.Lock()
	delete(bu.cohort, member)
	empty := len(bu.cohort) == 0
	bu.mu.Unlock()
	if empty {
		bu.cancel()
		delete(b.buckets, bk)
	}
}

func (b *alertBuckets) newBucketLocked(bk bucketKey, src config.SourceConfig) *bucket {
	ctx, cancel := context.WithCancel(b.ctx)
	bu := &bucket{b: b, key: bk, src: src, ctx: ctx, cancel: cancel, done: make(chan struct{}), early: make(chan struct{}, 1), cohort: map[ConnID]memberScope{}}
	b.buckets[bk] = bu
	go bu.run()
	return bu
}

// gate serializes an installation's acquisitions across its buckets: a
// re-keyed bucket's read never overlaps the previous one's worker, which
// may still be returning after its cancellation (S3a §13). A one-slot
// channel, so waiting for it is within the read's budget and ends with its
// bucket (Codex's review of S3a-2, R2).
func (b *alertBuckets) gate(id string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.gates[id]
	if g == nil {
		g = make(chan struct{}, 1)
		b.gates[id] = g
	}
	return g
}

func (bu *bucket) nudge() {
	select {
	case bu.early <- struct{}{}:
	default:
	}
}

// run reads on the bucket's cadence, and early when a join asks; the
// interval restarts from every read. It holds the bucket's dependency on
// its host for the bucket's life.
func (bu *bucket) run() {
	defer close(bu.done)
	defer bu.releaseHost()
	bu.holdHost()
	for {
		bu.b.mu.Lock()
		cadence := bu.b.cadence
		bu.b.mu.Unlock()
		t := time.NewTimer(cadence)
		select {
		case <-bu.ctx.Done():
			t.Stop()
			return
		case <-bu.early:
			t.Stop()
		case <-t.C:
		}
		bu.holdHost() // a host that could not be leased before is tried again
		bu.readOnce()
	}
}

// holdHost keeps a hosted bucket's dependency on its host's connection for
// its whole life, between reads too (Codex's review of S3a-2, R6): a lease
// listed under the host's plans, whose retirement ends the bucket. Each
// read still takes its own admission. A host that is reserved or not
// connected is tried again before the next read; its reads say why.
func (bu *bucket) holdHost() {
	if bu.src.Host == "" || bu.hostRelease != nil || bu.ctx.Err() != nil {
		return
	}
	conn, lctx, release, err := bu.b.lease(bu.src.Host, "the alerts "+bu.src.ID+" reads for its members")
	if err != nil {
		return
	}
	if c, ok := conn.(*ClusterConn); !ok || !strings.HasPrefix(bu.key.Access, fmt.Sprintf("%d.", c.ID())) {
		// the host's connection is not the one the bucket is keyed to: the
		// bucket is already obsolete, and its members re-key
		release()
		bu.retire()
		return
	}
	bu.hostRelease = release
	bu.hostStop = context.AfterFunc(lctx, bu.retire)
}

func (bu *bucket) releaseHost() {
	if bu.hostStop != nil {
		bu.hostStop()
	}
	if bu.hostRelease != nil {
		bu.hostRelease()
	}
}

// retire ends this bucket: its host's connection was retired, or is not
// the one it is keyed to. Its members leave it, and re-key at their next
// read; a newer bucket under the same key is never touched.
func (bu *bucket) retire() {
	b := bu.b
	b.mu.Lock()
	if b.buckets[bu.key] == bu {
		delete(b.buckets, bu.key)
		for m, k := range b.members {
			if k == bu.key {
				delete(b.members, m)
			}
		}
	}
	b.mu.Unlock()
	bu.cancel()
}

func (bu *bucket) endInflight() {
	bu.mu.Lock()
	bu.inflight = nil
	bu.mu.Unlock()
}

func (bu *bucket) readOnce() {
	bu.mu.Lock()
	// this read covers everyone in the cohort now, so an early read asked
	// for meanwhile is spent (R4)
	select {
	case <-bu.early:
	default:
	}
	cohort := maps.Clone(bu.cohort)
	bu.pending = false
	bu.inflight = make(map[ConnID]uint64, len(cohort))
	for m, s := range cohort {
		bu.inflight[m] = s.subRev
	}
	bu.mu.Unlock()
	if len(cohort) == 0 {
		bu.endInflight()
		return
	}
	o := bu.acquire(cohort)
	if bu.ctx.Err() != nil {
		bu.endInflight()
		return // a cancelled bucket's outcome is never served
	}
	bu.mu.Lock()
	// the read in flight ends as its outcome is published, in one step: a
	// waiter it wakes never sees it still in flight (RR2)
	bu.inflight = nil
	if o.err != nil && bu.outcome != nil && bu.outcome.good {
		prev := bu.outcome
		o.shares, o.truncated, o.unmatched, o.good, o.lineage, o.display, o.redactor = prev.shares, prev.truncated, prev.unmatched, true, prev.lineage, prev.display, prev.redactor
	}
	bu.outcome = o
	ws := bu.waiters
	bu.waiters = nil
	bu.mu.Unlock()
	for _, w := range ws {
		close(w)
	}
}

// errAccessDenied is an observed loss of access: the installation, or the
// host's API server, refused the read.
var errAccessDenied = errors.New("access denied")

// acquire is one bucket read for cohort (S3a §3.2): through the host's
// admitted connection or the endpoint, the alerts and groups with one
// filter, checked before and after each request against the terms the
// bucket is keyed to, reduced and capped once, then split.
func (bu *bucket) acquire(cohort map[ConnID]memberScope) *outcome {
	b, src := bu.b, bu.src
	o := &outcome{at: b.clk.Now(), cohort: map[ConnID]uint64{}, shares: map[ConnID][]alertmanager.Item{}}
	for m, s := range cohort {
		o.cohort[m] = s.subRev
	}
	if src.Endpoint != "" {
		o.credRev = b.creds.Revisions.Revision("source/"+src.ID, config.EndpointAlertmanager)
	}
	fail := func(err error) *outcome { o.err = err; return o }
	// the whole read's budget starts before its turn at the installation
	ctx, cancel := context.WithTimeout(bu.ctx, b.timeout)
	defer cancel()
	gate := b.gate(src.ID)
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return fail(fmt.Errorf("%s was not read in time: %w", src.ID, ctx.Err()))
	}
	select {
	case amReads <- struct{}{}:
		defer func() { <-amReads }()
	case <-ctx.Done():
		return fail(fmt.Errorf("%s was not read in time: %w", src.ID, ctx.Err()))
	}
	// a member whose subscription moved since the read began is not
	// served it, nor given its rows (Codex's review of S3a-2, R1)
	moved := map[ConnID]bool{}
	// still in force, at the access generation the bucket is keyed to, for
	// the subscriptions it read for
	check := func() error {
		for m, s := range cohort {
			if cur, ok := b.sources.Subscription(s.cluster); !ok || cur.SubRev != s.subRev {
				moved[m] = true
			}
		}
		if _, rev, ok := b.sources.Installation(src.ID); !ok || rev != bu.key.InstRev {
			return fmt.Errorf("%s changed; it is read again for its new terms", src.ID)
		}
		if err := b.hostDown(src); err != nil {
			return err
		}
		if b.access(src) != bu.key.Access {
			return fmt.Errorf("%s's access changed; it is read again", src.ID)
		}
		return nil
	}
	if err := check(); err != nil {
		return fail(err)
	}
	credRev := o.credRev
	tr, err := b.transport(ctx, cancel, src, bu.key.InstRev, bu.key.Tenant, credRev, "the alerts of "+src.ID)
	if err != nil {
		return fail(err)
	}
	defer tr.release()
	am, hostConn := tr.am, tr.hostConn
	o.display = tr.display
	filter := bu.filter(cohort)
	q := url.Values{"active": {"true"}, "silenced": {"true"}, "inhibited": {"true"}, "unprocessed": {"true"}, "filter": filter}
	gq := url.Values{"active": {"true"}, "muted": {"true"}, "silenced": {"false"}, "inhibited": {"false"}, "filter": filter}
	alerts, err := bucketGet(ctx, am, "/api/v2/alerts", q, alertmanager.ParseAlerts, check)
	if err == nil {
		var groups []alertmanager.Group
		groups, err = bucketGet(ctx, am, "/api/v2/alerts/groups", gq, alertmanager.ParseGroups, check)
		if err == nil {
			appearances := 0
			for _, g := range groups {
				appearances += len(g.Alerts)
			}
			if appearances > amMaxAppearances {
				return fail(fmt.Errorf("%s's alert groups held more than %d appearances; mutes would be incomplete", src.ID, amMaxAppearances))
			}
			live := maps.Clone(cohort)
			for m := range moved {
				delete(live, m)
				delete(o.cohort, m)
			}
			bu.split(o, live, alertmanager.Reduce(alerts, groups), am.redactor)
		}
	}
	if errors.Is(err, errAccessDenied) {
		b.sources.Denied(src.ID)
		return fail(fmt.Errorf("%s refused Klyx's read (401 or 403); it is read again under new access", src.ID))
	}
	if err != nil {
		return fail(err)
	}
	if src.Endpoint != "" && b.creds.Revisions.Revision("source/"+src.ID, config.EndpointAlertmanager) != credRev {
		return fail(fmt.Errorf("%s's credential changed during the read; it is read again next time", src.ID))
	}
	o.good, o.redactor = true, am.redactor
	o.lineage = alertmanager.Lineage{Mode: config.AMSubscribed, Installation: src.ID, InstRev: bu.key.InstRev, Host: src.Host,
		HostConn: uint64(hostConn), Access: bu.key.Access, Tenant: bu.key.Tenant, CredRev: credRev}
	return o
}

// amTransport is how one read reaches an installation (S3a §3.1).
type amTransport struct {
	am       amSource
	display  string
	hostConn ConnID
	release  func()
}

// transport builds an installation's transport for one read: a direct
// installation's endpoint with its own credential, at credRev; or a lease
// on its host's current connection, whose retirement cancels the read
// (cancel), and the Service it proxies - the serviceRef, or the host's
// discovery at the installation's revision.
func (b *alertBuckets) transport(ctx context.Context, cancel context.CancelFunc, src config.SourceConfig, instRev uint64, tenant string, credRev uint64, what string) (*amTransport, error) {
	if src.Endpoint != "" {
		account, revKey := endpointcred.SourceAccount(src.ID), "source/"+src.ID
		revs := b.creds.Revisions
		cred := endpointcred.Resolve(b.creds.Store, endpointcred.Input{Cluster: revKey, Kind: config.EndpointAlertmanager, Account: account,
			Spec: config.EndpointConfig{Endpoint: src.Endpoint, Auth: src.Auth}, Revoked: revs.Revoked(revKey, config.EndpointAlertmanager)})
		auth := endpointhttp.Auth{Tenant: tenant}
		if !cred.Usable() {
			why := cred.Why
			if why == "" {
				why = "the credential is " + string(cred.State)
			}
			return nil, fmt.Errorf("%s: %s", src.ID, why)
		}
		auth.Header, auth.Redactor, auth.Live = cred.Header(), cred.Redactor(), revs.Live(revKey, config.EndpointAlertmanager, credRev)
		client := safehttp.NewClient
		if b.client != nil {
			client = b.client
		}
		d := endpointhttp.NewDirect(strings.TrimRight(src.Endpoint, "/"), auth, client(endpointhttp.DirectPolicy(amLimits, amHTTPTimeout, src.TLSSkipVerify)), amLimits)
		at, err := endpointAt(src.Endpoint)
		if err != nil {
			return nil, err
		}
		return &amTransport{am: amSource{get: d.Get, redactor: d.Redactor()}, display: at, release: func() {}}, nil
	}
	conn, lctx, release, err := b.lease(src.Host, what)
	if err != nil {
		return nil, fmt.Errorf("%s's host %s is not connected: %w", src.ID, src.Host, err)
	}
	host, ok := conn.(*ClusterConn)
	if !ok {
		release()
		return nil, fmt.Errorf("%s's host %s cannot proxy a Service", src.ID, src.Host)
	}
	stop := context.AfterFunc(lctx, cancel) // the host's retirement ends the read
	done := func() {
		stop()
		release()
	}
	var svc endpointhttp.Service
	if src.ServiceRef != nil {
		svc = serviceOf(src.ServiceRef)
	} else {
		d := b.discover(ctx, host, instRev)
		switch {
		case d.err != nil:
			done()
			return nil, fmt.Errorf("%s's discovery in %s could not be read: %w", src.ID, src.Host, d.err)
		case d.ambiguous != "":
			done()
			return nil, fmt.Errorf("%s: %s in %s", src.ID, d.ambiguous, src.Host)
		case d.none:
			done()
			return nil, fmt.Errorf("%s found no Alertmanager in %s", src.ID, src.Host)
		}
		svc = d.svc
	}
	rc, err := host.amProxyREST()
	if err != nil {
		done()
		return nil, fmt.Errorf("%s: the service-proxy client could not be built: %w", src.ID, err)
	}
	p := endpointhttp.NewProxy(rc, svc, tenant, amLimits)
	return &amTransport{am: amSource{get: p.Get}, display: "Service " + serviceAt(svc), hostConn: host.ID(), release: done}, nil
}

// filter is the acquisition filter (S3a §3.2): none for scope: tenant;
// the cohort's member-label values as one anchored regular-expression
// matcher when the installation has a member label; else the lone
// subscription's full match.
func (bu *bucket) filter(cohort map[ConnID]memberScope) []string {
	for _, s := range cohort {
		if s.scopeTenant {
			return nil
		}
	}
	if label := bu.src.MemberLabel; label != "" {
		values := make([]string, 0, len(cohort))
		for _, s := range cohort {
			values = append(values, s.match[label])
		}
		return []string{alertmanager.RegexpFilter(label, values)}
	}
	for _, s := range cohort {
		return alertmanager.FilterParams(s.match)
	}
	return nil
}

// split gives each reduced row to the cohort member whose full scope it
// matches - at most one, scopes being disjoint - counts the rest as
// matching no member, and projects the members' rows only after.
func (bu *bucket) split(o *outcome, cohort map[ConnID]memberScope, reduced []alertmanager.Reduced, red safehttp.Redactor) {
	if len(reduced) > amMaxAlerts {
		reduced, o.truncated = reduced[:amMaxAlerts], true
	}
	members := make([]ConnID, 0, len(cohort))
	for m := range cohort {
		members = append(members, m)
	}
	sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
	for _, r := range reduced {
		owner, found := ConnID(0), false
		for _, m := range members {
			if s := cohort[m]; s.scopeTenant || alertmanager.Matches(r.Labels, s.match) {
				owner, found = m, true
				break
			}
		}
		if !found {
			o.unmatched++
			continue
		}
		o.shares[owner] = append(o.shares[owner], alertmanager.Item{Subject: alertmanager.SubjectOf(r.Labels), Counted: r.Counted(),
			Key: alertmanager.AlertKey(cohort[owner].key, r.Fingerprint), Display: alertmanager.Display(r, red)})
	}
}

// bucketGet is amGet with the bucket's terms checked before and after the
// request, and an observed 401 or 403 told apart.
func bucketGet[T any](ctx context.Context, src amSource, path string, q url.Values, parse func([]byte) (T, error), check func() error) (T, error) {
	var zero T
	if err := check(); err != nil {
		return zero, err
	}
	code, body, err := src.get(ctx, path, q)
	if err != nil {
		return zero, src.redactor.Err(err, errAMTooLarge, endpointhttp.ErrCredentialChanged, context.Canceled, context.DeadlineExceeded)
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return zero, errAccessDenied
	}
	if err := check(); err != nil {
		return zero, err
	}
	if code == http.StatusGone {
		return zero, errors.New("Alertmanager's v1 API is gone and this endpoint did not answer v2; Klyx reads v2")
	}
	if code < 200 || code > 299 {
		return zero, fmt.Errorf("Alertmanager answered HTTP %d for %s", code, path)
	}
	v, err := parse(body)
	if err != nil {
		return zero, src.redactor.Err(err)
	}
	return v, nil
}

// lease is the host's admitted connection for a read.
func (b *alertBuckets) lease(host, what string) (Conn, context.Context, func(), error) {
	b.mu.Lock()
	l := b.leaser
	b.mu.Unlock()
	if l != nil {
		return l(host, what)
	}
	c, ok := b.host(host)
	if !ok {
		return nil, nil, nil, errors.New("no current connection")
	}
	return c, context.Background(), func() {}, nil
}

// discover runs the existing discovery in the host, with its proof of one
// installation, cached for its five minutes per host connection and
// installation revision; a failure is not cached.
func (b *alertBuckets) discover(ctx context.Context, host *ClusterConn, instRev uint64) *amDiscovery {
	k := discKey{host: host.ID(), inst: instRev}
	now := b.clk.Now()
	b.mu.Lock()
	if d := b.disc[k]; d != nil && d.err == nil && now.Sub(d.at) < amDiscoveryTTL {
		b.mu.Unlock()
		return d
	}
	b.mu.Unlock()
	d := host.discoverAlertmanager(ctx)
	d.at = now
	b.mu.Lock()
	b.disc[k] = d
	b.mu.Unlock()
	return d
}

// InstallationRead is what an installation's last read said, for
// Observability (S3a §8): when it was read, the rows that matched no
// member's scope in that filtered read, whether it was cut at its cap, and
// its failure.
type InstallationRead struct {
	At        time.Time
	Unmatched int
	Truncated bool
	Err       string
}

// lastRead is an installation's latest outcome across its buckets.
func (b *alertBuckets) lastRead(id string) (InstallationRead, bool) {
	b.mu.Lock()
	buckets := make([]*bucket, 0, len(b.buckets))
	for k, bu := range b.buckets {
		if k.Source == id {
			buckets = append(buckets, bu)
		}
	}
	b.mu.Unlock()
	var out InstallationRead
	found := false
	for _, bu := range buckets {
		bu.mu.Lock()
		if o := bu.outcome; o != nil && (!found || o.at.After(out.At)) {
			out, found = InstallationRead{At: o.at, Unmatched: o.unmatched, Truncated: o.truncated}, true
			if o.err != nil {
				out.Err = o.err.Error()
			}
		}
		bu.mu.Unlock()
	}
	return out, found
}
