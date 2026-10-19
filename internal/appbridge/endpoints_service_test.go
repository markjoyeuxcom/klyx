package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/secretstore"
)

const endpointsDoc = `clusters:
  - name: prd
    context: prd
    metrics:
      endpoint: https://mimir.example/prometheus
      auth: {type: bearer}
      tenant: platform
    loki:
      endpoint: https://loki.example
      auth: {type: basic, username: klyx}
  - name: dev
    context: dev
    metrics:
      endpoint: https://prom.dev.example
      token: legacy-tok
`

type endpointConnFake struct {
	mu   sync.Mutex
	set  []config.MetricsConfig
	cfg  config.MetricsConfig
	capb metrics.MetricsCapability
	// Alertmanager
	amCfg       *config.EndpointConfig
	amSet       []*config.EndpointConfig
	amStatus    alertmanager.Status
	amSrc       string
	amErr       error
	amRefreshed int
	// Loki
	lokiCfg  *config.EndpointConfig
	lokiSet  []*config.EndpointConfig
	lokiTest fleet.LokiTestResult
	lokiErr  error
}

func (f *endpointConnFake) LokiConfig() *config.EndpointConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lokiCfg
}
func (f *endpointConnFake) SetLokiConfig(c *config.EndpointConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lokiSet = append(f.lokiSet, c)
	f.lokiCfg = c
}
func (f *endpointConnFake) ChangeLokiCredential(c *config.EndpointConfig, install func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := install(); err != nil {
		return err
	}
	f.lokiSet = append(f.lokiSet, c)
	f.lokiCfg = c
	return nil
}
func (f *endpointConnFake) LokiTest(context.Context) (fleet.LokiTestResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lokiTest, f.lokiErr
}

func (f *endpointConnFake) AlertmanagerConfig() *config.EndpointConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.amCfg
}
func (f *endpointConnFake) SetAlertmanagerConfig(c *config.EndpointConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.amSet = append(f.amSet, c)
	f.amCfg = c
}
func (f *endpointConnFake) ChangeAlertmanagerCredential(c *config.EndpointConfig, install func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := install(); err != nil {
		return err
	}
	f.amSet = append(f.amSet, c)
	f.amCfg = c
	return nil
}
func (f *endpointConnFake) RefreshAlerts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.amRefreshed++
}
func (f *endpointConnFake) AlertmanagerStatus(context.Context) (alertmanager.Status, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.amStatus, f.amSrc, f.amErr
}

func (f *endpointConnFake) ClusterMetrics(context.Context, bool) (metrics.ClusterMetrics, metrics.MetricsCapability) {
	return metrics.ClusterMetrics{}, f.capb
}
func (f *endpointConnFake) SetMetricsConfig(mc config.MetricsConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = append(f.set, mc)
	f.cfg = mc
}
func (f *endpointConnFake) MetricsConfig() config.MetricsConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}
func (f *endpointConnFake) ChangeMetricsCredential(mc config.MetricsConfig, install func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := install(); err != nil {
		return err
	}
	f.set = append(f.set, mc)
	f.cfg = mc
	return nil
}

// hookStore is a Memory store whose Set runs a hook first (an editor
// changing fleet.yaml mid-action, or a held action).
type hookStore struct {
	secretstore.Memory
	onSet func()
}

func (h *hookStore) Set(a, v string) error {
	if h.onSet != nil {
		h.onSet()
	}
	return h.Memory.Set(a, v)
}

type endpointsEnv struct {
	path  string
	store *hookStore
	revs  *endpointcred.Registry
	conn  *endpointConnFake
	svc   *EndpointsService
}

func newEndpointsEnv(t *testing.T) *endpointsEnv {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(p, []byte(endpointsDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &endpointsEnv{path: p, store: &hookStore{}, revs: endpointcred.NewRegistry(), conn: &endpointConnFake{}}
	e.svc = NewEndpointsService(p, e.store, e.revs, func(c string) (EndpointConn, bool) {
		return boundConnFake{endpointConnFake: e.conn, cluster: c, revs: e.revs}, true
	})
	return e
}

// boundConnFake is the fake as one cluster's connection: like
// fleet.ClusterConn, it moves the endpoint's revision when its
// configuration or credential changes.
type boundConnFake struct {
	*endpointConnFake
	cluster string
	revs    *endpointcred.Registry
}

func (b boundConnFake) SetMetricsConfig(mc config.MetricsConfig) {
	b.endpointConnFake.SetMetricsConfig(mc)
	b.revs.Bump(b.cluster, config.EndpointMetrics, false)
}

func (b boundConnFake) SetAlertmanagerConfig(c *config.EndpointConfig) {
	b.endpointConnFake.SetAlertmanagerConfig(c)
	b.revs.Bump(b.cluster, config.EndpointAlertmanager, false)
}

func (b boundConnFake) ChangeAlertmanagerCredential(c *config.EndpointConfig, install func() error) error {
	if err := b.endpointConnFake.ChangeAlertmanagerCredential(c, install); err != nil {
		return err
	}
	b.revs.Bump(b.cluster, config.EndpointAlertmanager, true)
	return nil
}

func (b boundConnFake) ChangeMetricsCredential(mc config.MetricsConfig, install func() error) error {
	if err := b.endpointConnFake.ChangeMetricsCredential(mc, install); err != nil {
		return err
	}
	b.revs.Bump(b.cluster, config.EndpointMetrics, true)
	return nil
}

func (e *endpointsEnv) row(t *testing.T, cluster, kind string) EndpointRowDTO {
	t.Helper()
	st := e.svc.GetEndpoints()
	for _, r := range st.Rows {
		if r.Cluster == cluster && r.Kind == kind {
			return r
		}
	}
	t.Fatalf("no row %s/%s in %+v", cluster, kind, st)
	return EndpointRowDTO{}
}

func (e *endpointsEnv) edit(t *testing.T, old, new string) {
	t.Helper()
	b, _ := os.ReadFile(e.path)
	if err := os.WriteFile(e.path, []byte(strings.Replace(string(b), old, new, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func noSecret(t *testing.T, v any, secrets ...string) {
	t.Helper()
	b, _ := json.Marshal(v)
	for _, s := range secrets {
		if strings.Contains(string(b), s) {
			t.Fatalf("a secret reached the page: %s", b)
		}
	}
}

// Coverage C5 §3, §8: every endpoint is listed with its state; Set binds
// the secret to the reviewed origin, moves the revision, and nothing reads
// it back.
func TestEndpointsSetAndState(t *testing.T) {
	e := newEndpointsEnv(t)
	r := e.row(t, "prd", "metrics")
	if r.State != string(endpointcred.Missing) || !r.CanSet || r.CanRemove || r.Source != "https://mimir.example/prometheus" || r.Tenant != "platform" {
		t.Fatalf("missing: %+v", r)
	}
	if l := e.row(t, "prd", "loki"); !l.CanTest || l.TestNote != "" || l.Username != "klyx" { // S8b: Loki has a Test
		t.Fatalf("loki: %+v", l)
	}
	before := e.revs.Revision("prd", "metrics")
	if res := e.svc.SetCredential("prd", "metrics", r.Revision, "s3cret-tok"); !res.OK {
		t.Fatalf("set: %+v", res)
	}
	if e.revs.Revision("prd", "metrics") == before {
		t.Fatal("the revision moved")
	}
	raw, _ := e.store.Get(endpointcred.Account("prd", "metrics"))
	var rec endpointcred.Record
	_ = json.Unmarshal([]byte(raw), &rec)
	if rec.Origin != "https://mimir.example:443" || rec.Type != "bearer" || rec.Secret != "s3cret-tok" {
		t.Fatalf("the record: %+v", rec)
	}
	st := e.svc.GetEndpoints()
	noSecret(t, st, "s3cret-tok")
	if r := e.row(t, "prd", "metrics"); r.State != string(endpointcred.Stored) || !r.CanRemove {
		t.Fatalf("stored: %+v", r)
	}
}

// An action carries the revision the person reviewed: an edit to fleet.yaml
// after the page was read refuses Set, Remove and Test, with nothing
// written.
func TestEndpointsRefuseAStaleReview(t *testing.T) {
	e := newEndpointsEnv(t)
	r := e.row(t, "prd", "metrics")
	e.edit(t, "https://mimir.example/prometheus", "https://mimir2.example/prometheus")
	if res := e.svc.SetCredential("prd", "metrics", r.Revision, "s3cret-tok"); res.OK || !strings.Contains(res.Message, "changed in fleet.yaml since you looked") {
		t.Fatalf("set on a stale review: %+v", res)
	}
	if _, err := e.store.Get(endpointcred.Account("prd", "metrics")); !errors.Is(err, secretstore.ErrNoKey) {
		t.Fatal("something was stored")
	}
	if res := e.svc.RemoveCredential("prd", "metrics", r.Revision); res.OK || e.revs.Revoked("prd", "metrics") {
		t.Fatalf("remove on a stale review: %+v", res)
	}
	if res := e.svc.TestEndpoint("prd", "metrics", r.Revision); res.OK || !strings.Contains(res.Message, "since you looked") {
		t.Fatalf("test on a stale review: %+v", res)
	}
	// a mode change alone is drift too
	r = e.row(t, "prd", "metrics")
	if err := os.Chmod(e.path, 0o640); err != nil {
		t.Fatal(err)
	}
	if res := e.svc.SetCredential("prd", "metrics", r.Revision, "x"); res.OK {
		t.Fatalf("a mode change: %+v", res)
	}
}

// An endpoint edited after a Set reads "for another origin": the stored
// secret is never sent to the new host.
func TestEndpointsEditAfterSet(t *testing.T) {
	e := newEndpointsEnv(t)
	r := e.row(t, "prd", "metrics")
	_ = e.svc.SetCredential("prd", "metrics", r.Revision, "s3cret-tok")
	e.edit(t, "https://mimir.example/prometheus", "https://evil.example/prometheus")
	if r := e.row(t, "prd", "metrics"); r.State != string(endpointcred.OtherOrigin) || !strings.Contains(r.Why, "https://mimir.example:443") {
		t.Fatalf("another origin: %+v", r)
	}
}

// Remove takes the credential out of use before the keychain delete, so a
// failed delete still stops it; a legacy endpoint's Remove says what it does.
func TestEndpointsRemove(t *testing.T) {
	e := newEndpointsEnv(t)
	r := e.row(t, "prd", "metrics")
	_ = e.svc.SetCredential("prd", "metrics", r.Revision, "s3cret-tok")
	r = e.row(t, "prd", "metrics")
	e.store.FailDelete = &secretstore.KeychainError{Err: errors.New("user interaction is not allowed")}
	res := e.svc.RemoveCredential("prd", "metrics", r.Revision)
	if res.OK || !strings.Contains(res.Message, "Removed from use; the keychain still holds it") || !e.revs.Revoked("prd", "metrics") {
		t.Fatalf("a failed delete: %+v", res)
	}
	if r := e.row(t, "prd", "metrics"); r.State != string(endpointcred.Revoked) {
		t.Fatalf("removed from use: %+v", r)
	}
	e.store.FailDelete = nil
	if res := e.svc.RemoveCredential("prd", "metrics", r.Revision); !res.OK {
		t.Fatalf("remove: %+v", res)
	}

	// dev: the legacy token wins, so Remove removes only the keychain copy
	v, _ := endpointcred.Record{V: 1, Origin: "https://prom.dev.example:443", Type: "bearer", Secret: "kc"}.Encode()
	_ = e.store.Memory.Set(endpointcred.Account("dev", "metrics"), v)
	d := e.row(t, "dev", "metrics")
	if d.RemoveLabel != removeLegacyLabel || !d.StoredToo || d.State != string(endpointcred.Legacy) {
		t.Fatalf("legacy row: %+v", d)
	}
	if res := e.svc.RemoveCredential("dev", "metrics", d.Revision); !res.OK || !strings.Contains(res.Message, "that is what Klyx sends") {
		t.Fatalf("legacy remove: %+v", res)
	}
}

// Coverage C5 §6: Move writes the keychain, then the file; the live
// connection is told and the revision moves.
func TestEndpointsMove(t *testing.T) {
	e := newEndpointsEnv(t)
	d := e.row(t, "dev", "metrics")
	if !d.CanMove || d.CanSet || d.State != string(endpointcred.Legacy) {
		t.Fatalf("legacy: %+v", d)
	}
	res := e.svc.MoveToKeychain("dev", d.Revision)
	if !res.OK {
		t.Fatalf("move: %+v", res)
	}
	noSecret(t, res, "legacy-tok")
	b, _ := os.ReadFile(e.path)
	if strings.Contains(string(b), "legacy-tok") || !strings.Contains(string(b), "auth: {type: bearer}") {
		t.Fatalf("the file:\n%s", b)
	}
	if len(e.conn.set) != 1 || e.conn.set[0].Token != "" || e.conn.set[0].Auth == nil {
		t.Fatalf("the live connection: %+v", e.conn.set)
	}
	if r := e.row(t, "dev", "metrics"); r.State != string(endpointcred.Stored) || r.Legacy {
		t.Fatalf("after the move: %+v", r)
	}
}

// A keychain failure changes nothing; an edit to fleet.yaml between the
// keychain write and the file edit leaves the newer file untouched, reports
// the staged item, and the move can be retried.
func TestEndpointsMoveFailures(t *testing.T) {
	e := newEndpointsEnv(t)
	d := e.row(t, "dev", "metrics")
	e.store.FailSet = &secretstore.KeychainError{Err: errors.New("locked: legacy-tok")}
	if res := e.svc.MoveToKeychain("dev", d.Revision); res.OK || !strings.Contains(res.Message, "Nothing changed") || strings.Contains(res.Message, "legacy-tok") {
		t.Fatalf("keychain failure: %+v", res)
	}
	if b, _ := os.ReadFile(e.path); string(b) != endpointsDoc {
		t.Fatal("the file changed")
	}
	e.store.FailSet = nil
	e.store.onSet = func() { e.edit(t, "context: dev", "context: dev-renamed") }
	res := e.svc.MoveToKeychain("dev", d.Revision)
	if res.OK || !strings.Contains(res.Message, "stored in the keychain for https://prom.dev.example:443; fleet.yaml changed meanwhile and still holds a token") {
		t.Fatalf("edit mid-move: %+v", res)
	}
	b, _ := os.ReadFile(e.path)
	if !strings.Contains(string(b), "dev-renamed") || !strings.Contains(string(b), "legacy-tok") {
		t.Fatal("the newer file was touched")
	}
	if len(e.conn.set) != 0 {
		t.Fatal("the live connection was told of a move that did not happen")
	}
	e.store.onSet = nil
	d = e.row(t, "dev", "metrics")
	if !d.StoredToo || !d.CanMove {
		t.Fatalf("both, retryable: %+v", d)
	}
	if res := e.svc.MoveToKeychain("dev", d.Revision); !res.OK {
		t.Fatalf("retry: %+v", res)
	}
}

// Endpoint actions are serialised: one held inside its keychain write keeps
// another from starting.
func TestEndpointsActionsAreSerialised(t *testing.T) {
	e := newEndpointsEnv(t)
	r := e.row(t, "prd", "metrics")
	held, release := make(chan struct{}), make(chan struct{})
	e.store.onSet = func() { close(held); <-release }
	go e.svc.SetCredential("prd", "metrics", r.Revision, "s3cret-tok")
	<-held
	e.store.onSet = nil
	done := make(chan struct{})
	go func() { e.svc.RemoveCredential("prd", "metrics", r.Revision); close(done) }()
	select {
	case <-done:
		t.Fatal("a second action ran while the first was in its keychain write")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	<-done
}

// Test reads metrics, and says the reason redacted as the transport left
// it; Loki's Test (S8b §6) reads its build info and label names - the
// connection synced to the reviewed endpoint first - and says each case.
func TestEndpointsTest(t *testing.T) {
	e := newEndpointsEnv(t)
	e.conn.capb = metrics.MetricsCapability{Available: true, Source: "https://mimir.example/prometheus"}
	r := e.row(t, "prd", "metrics")
	if res := e.svc.TestEndpoint("prd", "metrics", r.Revision); !res.OK || !strings.Contains(res.Message, "Available") {
		t.Fatalf("test: %+v", res)
	}
	l := e.row(t, "prd", "loki")
	e.conn.lokiTest = fleet.LokiTestResult{Version: "3.7.8", Source: "https://loki.example:443", Data: true, Namespace: []string{"k8s_namespace_name"}}
	if res := e.svc.TestEndpoint("prd", "loki", l.Revision); !res.OK ||
		res.Message != "Available: Loki 3.7.8 at https://loki.example:443; this cluster's streams carry the namespace label k8s_namespace_name." {
		t.Fatalf("loki: %+v", res)
	}
	if len(e.conn.lokiSet) != 1 {
		t.Fatalf("the connection was not synced to the reviewed Loki: %d", len(e.conn.lokiSet))
	}
	e.conn.lokiTest = fleet.LokiTestResult{Version: "3.7.8", Source: "https://loki.example:443", Tenant: "lab"}
	if res := e.svc.TestEndpoint("prd", "loki", l.Revision); !res.OK || !strings.Contains(res.Message, "no streams match this cluster's match in the last hour on tenant lab") {
		t.Fatalf("loki, no streams: %+v", res)
	}
	e.conn.lokiTest = fleet.LokiTestResult{Version: "3.7.8", Source: "s", Data: true}
	if res := e.svc.TestEndpoint("prd", "loki", l.Revision); !strings.Contains(res.Message, "carry no namespace label") {
		t.Fatalf("loki, no namespace label: %+v", res)
	}
	e.conn.lokiErr = errors.New("refused: Authorization: Bearer LOKI-SECRET")
	if res := e.svc.TestEndpoint("prd", "loki", l.Revision); res.OK || strings.Contains(res.Message, "LOKI-SECRET") || !strings.HasPrefix(res.Message, "Unavailable: ") {
		t.Fatalf("loki, failure: %+v", res)
	}
}

// Coverage S8b §6: Set for a connected cluster's Loki installs the
// credential through the connection with the reviewed configuration, as
// one step (C5 §7), as Alertmanager's does.
func TestEndpointsLokiSetThroughTheConnection(t *testing.T) {
	e := newEndpointsEnv(t)
	l := e.row(t, "prd", "loki")
	if res := e.svc.SetCredential("prd", "loki", l.Revision, "LOKI-PASS"); !res.OK {
		t.Fatalf("set: %+v", res)
	}
	if len(e.conn.lokiSet) != 1 || e.conn.lokiSet[0].Endpoint != "https://loki.example" {
		t.Fatalf("the connection did not take the credential with the reviewed Loki: %+v", e.conn.lokiSet)
	}
}
