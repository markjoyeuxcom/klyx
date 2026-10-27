package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/secretstore"
)

// Settings S3a §3.2-§3.4: buckets read an installation once for their
// members, split the read, and pin the terms they read under.

const bucketAlerts = `[
 {"fingerprint":"d1","labels":{"alertname":"DevDown","severity":"critical","cluster":"dev"},"status":{"state":"active"},"receivers":[{"name":"r"}]},
 {"fingerprint":"s1","labels":{"alertname":"StgDown","severity":"critical","cluster":"stg"},"status":{"state":"active"},"receivers":[{"name":"r"}]},
 {"fingerprint":"o1","labels":{"alertname":"Elsewhere","severity":"critical","cluster":"other"},"status":{"state":"active"},"receivers":[{"name":"r"}]}
]`

type bucketRig struct {
	t       *testing.T
	am      *amFake
	host    *ClusterConn
	sources *AlertSources
	b       *alertBuckets
	conns   map[string]*ClusterConn
	store   *secretstore.Memory
	revs    *endpointcred.Registry
}

// usable makes a connection's usability published, at incarnation inc.
func usable(c *ClusterConn, inc uint64) {
	c.mu.Lock()
	c.state, c.pending, c.usableInc = Synced, false, inc
	c.mu.Unlock()
}

// newBucketRig hosts lgtm-we in prd behind a fake API server, serving dev
// and stg by name with the member label cluster.
func newBucketRig(t *testing.T, src config.SourceConfig, members ...string) *bucketRig {
	t.Helper()
	r := &bucketRig{t: t, am: newAMFake(), conns: map[string]*ClusterConn{}, store: &secretstore.Memory{}, revs: endpointcred.NewRegistry()}
	r.am.alerts = bucketAlerts
	api := httptest.NewServer(http.StripPrefix("/api/v1/namespaces/monitoring/services/http:am:web/proxy", r.am))
	t.Cleanup(api.Close)
	r.host = NewClusterConn("prd", nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	r.host.restConfig = &rest.Config{Host: api.URL}
	usable(r.host, 1)
	clusters := []config.ClusterConfig{{Name: "prd", Context: "prd"}}
	for _, m := range members {
		clusters = append(clusters, config.ClusterConfig{Name: m, Context: m})
	}
	var err error
	r.sources, err = NewAlertSources([]config.SourceConfig{src}, clusters)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.b = newAlertBuckets(ctx, r.sources, Credentials{Store: r.store, Revisions: r.revs}, clock.Real{}, func(name string) (*ClusterConn, bool) {
		if name == "prd" {
			return r.host, true
		}
		return nil, false
	})
	r.setCadence(time.Hour) // only joins read, unless a test says otherwise
	for _, m := range members {
		c := NewClusterConn(m, nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
		c.WithAlertmanager(config.ConnectionAlertmanager([]config.SourceConfig{src}, config.ClusterConfig{Name: m}))
		c.withAlertBuckets(r.b)
		r.conns[m] = c
	}
	return r
}

func (r *bucketRig) setCadence(d time.Duration) {
	r.b.mu.Lock()
	r.b.cadence = d
	r.b.mu.Unlock()
}

func hostedSource() config.SourceConfig {
	return config.SourceConfig{ID: "lgtm-we", Kind: config.SourceKindAlertmanager, Host: "prd",
		ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "am", Port: "web"}, MemberLabel: "cluster",
		Serves: []config.ServeConfig{{Cluster: "dev"}, {Cluster: "stg"}}}
}

// read reads a member under its own key, following one re-key.
func (r *bucketRig) read(m string) (alertmanager.Read, time.Time, error) {
	r.t.Helper()
	c := r.conns[m]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, at, err := c.ReadAlertsAt(ctx, c.AlertsIdentity())
	if errors.Is(err, ErrSourceChanged) {
		got, at, err = c.ReadAlertsAt(ctx, got.Identity)
	}
	return got, at, err
}

func (r *bucketRig) mustRead(m string) (alertmanager.Read, time.Time) {
	r.t.Helper()
	got, at, err := r.read(m)
	if err != nil {
		r.t.Fatalf("%s: %v", m, err)
	}
	return got, at
}

func names(r alertmanager.Read) []string {
	var out []string
	for _, a := range r.Alerts {
		out = append(out, a.Display.Name)
	}
	return out
}

func (r *bucketRig) reads() int {
	r.am.mu.Lock()
	defer r.am.mu.Unlock()
	return len(r.am.queries["/api/v2/alerts"])
}

func (r *bucketRig) unmatched() int {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	for _, bu := range r.b.buckets {
		bu.mu.Lock()
		defer bu.mu.Unlock()
		if bu.outcome != nil {
			return bu.outcome.unmatched
		}
	}
	return -1
}

func TestABucketSplitsOneReadAmongItsMembers(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev", "stg")
	dev, devAt := r.mustRead("dev")
	stg, _ := r.mustRead("stg") // stg's join asks for one early read
	if fmt.Sprint(names(dev)) != "[DevDown]" || fmt.Sprint(names(stg)) != "[StgDown]" {
		t.Fatalf("shares: dev %v, stg %v", names(dev), names(stg))
	}
	if n := r.unmatched(); n != 1 {
		t.Fatalf("the other cluster's alert is counted as matching no member: %d", n)
	}
	if !strings.Contains(r.am.lastFilter(), `cluster=~"dev|stg"`) {
		t.Fatalf("the acquisition filter: %q", r.am.lastFilter())
	}
	if !strings.HasPrefix(dev.Source, "from lgtm-we, hosted by prd, filtered by cluster=dev") || dev.Lineage.Installation != "lgtm-we" || dev.Lineage.Host != "prd" || dev.Lineage.HostConn != uint64(r.host.ID()) {
		t.Fatalf("scope sentence and lineage: %q %+v", dev.Source, dev.Lineage)
	}
	before := r.reads()
	again, againAt := r.mustRead("dev")
	_, _ = r.mustRead("stg")
	if r.reads() != before {
		t.Fatalf("steady state read again: %d then %d", before, r.reads())
	}
	if againAt.Before(devAt) || len(again.Alerts) != 1 {
		t.Fatalf("a served share keeps its read's time: %v then %v", devAt, againAt)
	}
	// the members' rows are told apart from each other's by their keys
	if dev.Alerts[0].Key == stg.Alerts[0].Key {
		t.Fatal("two members' rows share a key")
	}
}

func TestAJoinDuringAReadWaitsForItsOwn(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev", "stg")
	r.am.hold, r.am.entered = make(chan struct{}), make(chan struct{}, 4)
	devDone := make(chan alertmanager.Read, 1)
	go func() { d, _, _ := r.read("dev"); devDone <- d }()
	<-r.am.entered // dev's read is in flight, for dev alone
	stgDone := make(chan alertmanager.Read, 1)
	go func() { s, _, _ := r.read("stg"); stgDone <- s }()
	time.Sleep(50 * time.Millisecond)
	close(r.am.hold)
	d, s := <-devDone, <-stgDone
	if fmt.Sprint(names(d)) != "[DevDown]" || fmt.Sprint(names(s)) != "[StgDown]" {
		t.Fatalf("dev %v stg %v", names(d), names(s))
	}
	if r.reads() < 2 {
		t.Fatalf("stg was served dev's read: %d reads", r.reads())
	}
}

func TestAHostThatIsNotConnectedIsSaid(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	r.host.mu.Lock()
	r.host.state = Failed
	r.host.mu.Unlock()
	_, _, err := r.read("dev")
	if err == nil || !strings.Contains(err.Error(), "lgtm-we's host prd is not connected") {
		t.Fatalf("host down: %v", err)
	}
}

// A recovery in place moves the access generation: the member re-keys,
// and its next read is under the new terms (S3a §3.4).
func TestARecoveryInPlaceRekeysTheMembers(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	dev, _ := r.mustRead("dev")
	usable(r.host, 2)
	c := r.conns["dev"]
	_, _, err := c.ReadAlertsAt(context.Background(), dev.Identity)
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("the old key after a recovery in place: %v", err)
	}
	again, _ := r.mustRead("dev")
	if again.Identity == dev.Identity || again.Lineage.Access == dev.Lineage.Access {
		t.Fatalf("the same lineage across a recovery: %+v", again.Lineage)
	}
}

// An observed 401 or 403 moves the access generation, for good.
func TestADenialMovesTheAccessGeneration(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	r.setCadence(10 * time.Millisecond)
	dev, _ := r.mustRead("dev")
	r.am.mu.Lock()
	r.am.code = http.StatusForbidden
	r.am.mu.Unlock()
	c := r.conns["dev"]
	deadline := time.Now().Add(5 * time.Second)
	for r.sources.Denials("lgtm-we") == 0 && time.Now().Before(deadline) {
		_, _, _ = c.ReadAlertsAt(context.Background(), dev.Identity)
		time.Sleep(20 * time.Millisecond)
	}
	if r.sources.Denials("lgtm-we") == 0 {
		t.Fatal("a 403 was not counted")
	}
	if key, _, _, _ := r.b.memberKey("dev"); key == dev.Identity {
		t.Fatal("the key did not move after a 403")
	}
}

// A truncated read qualifies every member of its cohort.
func TestACappedReadQualifiesEveryMember(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev", "stg")
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i <= amMaxAlerts; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"fingerprint":"h%d","labels":{"alertname":"Hot%d","cluster":"dev"},"status":{"state":"active"},"receivers":[{"name":"r"}]}`, i, i)
	}
	b.WriteString(`,{"fingerprint":"s1","labels":{"alertname":"StgDown","cluster":"stg"},"status":{"state":"active"},"receivers":[{"name":"r"}]}]`)
	r.am.alerts = b.String()
	_, _ = r.mustRead("dev")
	stg, _ := r.mustRead("stg")
	if !stg.Truncated {
		t.Fatal("a member of a cut read is presented as complete")
	}
}

// A direct installation reads its endpoint with its own credential and the
// subscription's tenant.
func TestADirectInstallationUsesItsOwnCredential(t *testing.T) {
	src := config.SourceConfig{ID: "shared", Kind: config.SourceKindAlertmanager, Auth: &config.EndpointAuth{Type: "bearer"},
		MemberLabel: "cluster", Tenant: "ops", Serves: []config.ServeConfig{{Cluster: "dev"}}}
	f := newAMFake()
	f.alerts = bucketAlerts
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	src.Endpoint = srv.URL
	r := newBucketRig(t, src, "dev")
	r.setCadence(10 * time.Millisecond)
	if _, _, err := r.read("dev"); err == nil || !strings.Contains(err.Error(), "no credential is stored") {
		t.Fatalf("without its credential: %v", err)
	}
	origin := strings.TrimRight(srv.URL, "/")
	v, _ := endpointcred.Record{V: 1, Origin: origin, Type: "bearer", Secret: "S3A-SECRET"}.Encode()
	if err := r.store.Set(endpointcred.SourceAccount("shared"), v); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var dev alertmanager.Read
	var err error
	for time.Now().Before(deadline) {
		if dev, _, err = r.read("dev"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || fmt.Sprint(names(dev)) != "[DevDown]" {
		t.Fatalf("with it: %v %v", err, names(dev))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth[len(f.auth)-1] != "Bearer S3A-SECRET" || f.tenant[len(f.tenant)-1] != "ops" {
		t.Fatalf("sent: auth %q tenant %q", f.auth, f.tenant)
	}
	if strings.Contains(dev.Source, "S3A-SECRET") || !strings.Contains(dev.Source, "from shared at ") {
		t.Fatalf("scope sentence: %q", dev.Source)
	}
}

// The filters: a lone subscription without a member label sends its full
// match; scope: tenant sends none.
func TestTheAcquisitionFilters(t *testing.T) {
	bare := config.SourceConfig{ID: "solo", Kind: config.SourceKindAlertmanager, Host: "prd",
		ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "am", Port: "web"}}
	r := newBucketRig(t, bare)
	cohort := map[ConnID]memberScope{1: {match: map[string]string{"namespace": "payments", "team": "core"}}}
	bu := &bucket{src: bare}
	if got := fmt.Sprint(bu.filter(cohort)); got != `[namespace="payments" team="core"]` {
		t.Fatalf("lone subscription: %s", got)
	}
	if got := bu.filter(map[ConnID]memberScope{1: {scopeTenant: true}}); got != nil {
		t.Fatalf("scope: tenant: %v", got)
	}
	labelled := &bucket{src: hostedSource()}
	if got := fmt.Sprint(labelled.filter(map[ConnID]memberScope{1: {match: map[string]string{"cluster": "a.b"}}, 2: {match: map[string]string{"cluster": `c"d`}}})); got != `[cluster=~"a\\.b|c\"d"]` {
		t.Fatalf("escaped: %s", got)
	}
	if got := labelled.filter(map[ConnID]memberScope{1: {scopeTenant: true, match: map[string]string{"cluster": "a"}}}); got != nil {
		t.Fatalf("scope: tenant first, even with a member label: %v", got)
	}
	_ = r
}

// A stopped member leaves its bucket; the last one cancels it.
func TestTheLastMemberCancelsItsBucket(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	_, _ = r.mustRead("dev")
	r.b.mu.Lock()
	var bu *bucket
	for _, x := range r.b.buckets {
		bu = x
	}
	r.b.mu.Unlock()
	r.conns["dev"].Stop(time.Second)
	select {
	case <-bu.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the bucket outlived its last member")
	}
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	if len(r.b.buckets) != 0 || len(r.b.members) != 0 {
		t.Fatalf("left: %v %v", r.b.buckets, r.b.members)
	}
}

// A member under a changed subscription revision is never served the
// older outcome.
func TestAChangedSubscriptionIsNotServedTheOlderOutcome(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	_, _ = r.mustRead("dev")
	before := r.reads()
	src := hostedSource()
	if _, err := r.sources.Apply([]config.SourceConfig{src}, []config.ClusterConfig{{Name: "prd"},
		{Name: "dev", Alertmanager: &config.EndpointConfig{Source: "lgtm-we", Match: map[string]string{"team": "core"}}}}); err != nil {
		t.Fatal(err)
	}
	dev, _ := r.mustRead("dev")
	if r.reads() == before {
		t.Fatal("the changed subscription was served the outcome read before it")
	}
	if dev.Lineage.SubRev == 0 || !strings.Contains(dev.Source, "team=core") {
		t.Fatalf("the new subscription: %+v %q", dev.Lineage, dev.Source)
	}
}

// An S2 commit's running fleet: a cluster whose group an installation now
// serves adopts its subscription, and one that leaves it goes back to
// discovery (S3a §3.6).
func TestResubscribeFollowsTheRunningFleet(t *testing.T) {
	src := config.SourceConfig{ID: "lgtm-we", Kind: config.SourceKindAlertmanager, Host: "prd", Discover: true, MemberLabel: "cluster", Serves: []config.ServeConfig{{Group: "web"}}}
	cfg := &config.Config{Clusters: []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}, Sources: []config.SourceConfig{src}}
	var conns []*ClusterConn
	reg := NewRegistry(cfg, SubscribingFactory(cfg.Sources, func(cc config.ClusterConfig) (Conn, error) {
		// fake clients: a started connection's informers list and watch them
		c := fakeClusterConn(cc.Name)
		c.WithAlertmanager(cc.Alertmanager)
		conns = append(conns, c)
		return c, nil
	}))
	sources, err := NewAlertSources(cfg.Sources, cfg.Clusters)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.EnableAlertSources(ctx, sources, Credentials{}, clock.Real{})
	reg.Start(ctx)
	t.Cleanup(func() {
		for _, c := range conns {
			c.Stop(5 * time.Second)
		}
	})
	devConn := func() *ClusterConn { c, _ := reg.Conn("dev"); return c.(*ClusterConn) }
	if devConn().AlertmanagerConfig() != nil {
		t.Fatal("fixture: dev starts by discovery")
	}
	if err := reg.Resubscribe([]config.ClusterConfig{{Name: "prd"}, {Name: "dev", Group: "web"}}); err != nil {
		t.Fatal(err)
	}
	if b := devConn().AlertmanagerConfig(); b == nil || b.Source != "lgtm-we" {
		t.Fatalf("dev joined web: %+v", b)
	}
	if _, ok := sources.Subscription("dev"); !ok {
		t.Fatal("dev's subscription is not in force")
	}
	if err := reg.Resubscribe([]config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}); err != nil {
		t.Fatal(err)
	}
	if b := devConn().AlertmanagerConfig(); b != nil {
		t.Fatalf("dev left web: %+v", b)
	}
	if _, ok := sources.Subscription("dev"); ok {
		t.Fatal("dev's subscription stayed in force")
	}
}

// §3.2's memory bound, at a representative shape: hot members rotating
// through joins, then a failed read. Every row held - by the bucket's
// outcome, or as a member's last served share - is counted once, and the
// total stays within the cap per reading member plus one capped outcome.
func TestRetainedRowsStayWithinTheBound(t *testing.T) {
	src := hostedSource()
	members := []string{"m1", "m2", "m3", "m4"}
	src.Serves = nil
	for _, m := range members {
		src.Serves = append(src.Serves, config.ServeConfig{Cluster: m})
	}
	r := newBucketRig(t, src, members...)
	hot := func(m string) string {
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i < amMaxAlerts; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"fingerprint":"%s-%d","labels":{"alertname":"Hot%d","cluster":"%s"},"status":{"state":"active"},"receivers":[{"name":"r"}]}`, m, i, i, m)
		}
		b.WriteString("]")
		return b.String()
	}
	held := map[string][]alertmanager.Item{}
	for _, m := range members {
		r.am.mu.Lock()
		r.am.alerts = hot(m) // m is hot when it joins
		r.am.mu.Unlock()
		share, _ := r.mustRead(m)
		held[m] = share.Alerts
	}
	r.am.mu.Lock()
	r.am.code = http.StatusInternalServerError
	r.am.mu.Unlock()
	unique := map[string]bool{}
	for _, share := range held {
		for _, it := range share {
			unique[it.Key] = true
		}
	}
	r.b.mu.Lock()
	for _, bu := range r.b.buckets {
		bu.mu.Lock()
		if bu.outcome != nil {
			for _, share := range bu.outcome.shares {
				for _, it := range share {
					unique[it.Key] = true // a row shared with a member's held share counts once
				}
			}
		}
		bu.mu.Unlock()
	}
	r.b.mu.Unlock()
	bound := len(members)*amMaxAlerts + amMaxAlerts
	if len(unique) > bound || len(unique) < len(members)*amMaxAlerts {
		t.Fatalf("retained %d rows; the bound is %d", len(unique), bound)
	}
	t.Logf("retained %d rows, bound %d", len(unique), bound)
}

// A re-keyed installation's new bucket never reads while the old bucket's
// worker still runs (S3a §13): one acquisition at a time per installation.
func TestAReKeyedBucketNeverOverlapsTheOldRead(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev", "stg")
	r.am.hold, r.am.entered = make(chan struct{}), make(chan struct{}, 4)
	go func() { _, _, _ = r.read("dev") }()
	<-r.am.entered // dev's bucket is reading, held
	src := hostedSource()
	src.Tenant = "moved" // a new installation revision: a new bucket
	if _, err := r.sources.Apply([]config.SourceConfig{src}, []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}, {Name: "stg"}}); err != nil {
		t.Fatal(err)
	}
	stgDone := make(chan error, 1)
	go func() { _, _, err := r.read("stg"); stgDone <- err }()
	time.Sleep(100 * time.Millisecond)
	close(r.am.hold)
	if err := <-stgDone; err != nil {
		t.Fatal(err)
	}
	r.am.mu.Lock()
	defer r.am.mu.Unlock()
	if r.am.maxInFlight != 1 {
		t.Fatalf("two acquisitions of one installation overlapped: %d", r.am.maxInFlight)
	}
}

// An incarnation not yet published is not in force: the access generation
// is the published one, and the host is not usable until it is.
func TestAPendingIncarnationIsNotInForce(t *testing.T) {
	c := NewClusterConn("prd", nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	usable(c, 1)
	c.mu.Lock()
	c.usableInc, c.pending = 2, true
	c.mu.Unlock()
	if inc, ok := c.publishedUsability(); inc != 1 || ok {
		t.Fatalf("pending: %d %v", inc, ok)
	}
}

// Revisions move only with a change, and are never reused: an installation
// removed and re-created under its id gets a new one.
func TestSourceRevisions(t *testing.T) {
	src := hostedSource()
	clusters := []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}, {Name: "stg"}}
	a, err := NewAlertSources([]config.SourceConfig{src}, clusters)
	if err != nil {
		t.Fatal(err)
	}
	_, r1, _ := a.Installation("lgtm-we")
	s1, _ := a.Subscription("dev")
	if changed, _ := a.Apply([]config.SourceConfig{src}, clusters); len(changed) != 0 {
		t.Fatalf("nothing changed, yet %v moved", changed)
	}
	if _, r, _ := a.Installation("lgtm-we"); r != r1 {
		t.Fatal("an unchanged installation moved its revision")
	}
	if s, _ := a.Subscription("dev"); s.SubRev != s1.SubRev {
		t.Fatal("an unchanged subscription moved its revision")
	}
	moved := src
	moved.Tenant = "ops"
	_, _ = a.Apply([]config.SourceConfig{moved}, clusters)
	if _, r, _ := a.Installation("lgtm-we"); r == r1 {
		t.Fatal("a changed installation kept its revision")
	}
	_, _ = a.Apply(nil, clusters)
	if _, ok := a.Subscription("dev"); ok {
		t.Fatal("a removed installation's subscription stayed")
	}
	_, _ = a.Apply([]config.SourceConfig{src}, clusters)
	_, r2, _ := a.Installation("lgtm-we")
	s2, _ := a.Subscription("dev")
	if r2 == r1 || s2.SubRev == s1.SubRev {
		t.Fatalf("a re-created installation reused its revisions: %d %d, %d %d", r1, r2, s1.SubRev, s2.SubRev)
	}
}

// A read whose terms move while it is in flight is discarded: the host
// recovering in place, or the installation changing (S3a §4.3).
func TestAReadWhoseTermsMoveIsDiscarded(t *testing.T) {
	for _, move := range []string{"access", "installation"} {
		t.Run(move, func(t *testing.T) {
			r := newBucketRig(t, hostedSource(), "dev")
			r.am.hold, r.am.entered = make(chan struct{}), make(chan struct{}, 4)
			done := make(chan error, 1)
			go func() { _, _, err := r.read("dev"); done <- err }()
			<-r.am.entered
			if move == "access" {
				usable(r.host, 2)
			} else {
				src := hostedSource()
				src.Name = "renamed"
				if _, err := r.sources.Apply([]config.SourceConfig{src}, []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}); err != nil {
					t.Fatal(err)
				}
			}
			close(r.am.hold)
			if err := <-done; err == nil || !strings.Contains(err.Error(), "read again") {
				t.Fatalf("a read across a %s change was served: %v", move, err)
			}
		})
	}
}

// A cluster that stops being subscribed leaves its bucket.
func TestAnUnsubscribedClusterLeavesItsBucket(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	_, _ = r.mustRead("dev")
	r.conns["dev"].SetAlertmanagerConfig(nil)
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	if len(r.b.members) != 0 || len(r.b.buckets) != 0 {
		t.Fatalf("still a member: %v %v", r.b.members, r.b.buckets)
	}
}
