package appbridge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/secretstore"
)

// Coverage C5 §6, §8: Settings > Endpoints. It shows every configured
// endpoint with its credential's state and acts on credentials only - the
// endpoints themselves are authored in fleet.yaml. Every action carries the
// revision the person reviewed, runs under one lock, re-reads fleet.yaml and
// refuses on drift; no secret is ever read back into the page.

// EndpointConn is what the endpoints service needs of a cluster's
// connection. fleet.ClusterConn satisfies it; its SetMetricsConfig and
// ChangeMetricsCredential move the endpoint's revision with the
// configuration, under the lock its readers take (C5 §7).
type EndpointConn interface {
	ClusterMetrics(ctx context.Context, forceReprobe bool) (metrics.ClusterMetrics, metrics.MetricsCapability)
	SetMetricsConfig(config.MetricsConfig)
	MetricsConfig() config.MetricsConfig
	ChangeMetricsCredential(mc config.MetricsConfig, install func() error) error
	// Alertmanager (coverage S8a): the same guarantees for its endpoint.
	AlertmanagerConfig() *config.EndpointConfig
	SetAlertmanagerConfig(*config.EndpointConfig)
	ChangeAlertmanagerCredential(cfg *config.EndpointConfig, install func() error) error
	AlertmanagerStatus(ctx context.Context) (alertmanager.Status, string, error)
	RefreshAlerts()
}

// syncAlertmanager is syncMetrics for the Alertmanager endpoint (S8a §4).
// Caller holds s.mu.
func (s *EndpointsService) syncAlertmanager(cluster string, cl *config.ClusterConfig) {
	if cl == nil || cl.Alertmanager == nil {
		return
	}
	conn, ok := s.lookup(cluster)
	if !ok || reflect.DeepEqual(conn.AlertmanagerConfig(), cl.Alertmanager) {
		return
	}
	conn.SetAlertmanagerConfig(cl.Alertmanager)
}

// syncMetrics makes the live connection read the metrics endpoint the
// person reviewed (Codex's C5 implementation review, finding 7): a
// connection still on an older configuration adopts the snapshot's, which
// moves the revision so nothing read through the old one is shown. Caller
// holds s.mu.
func (s *EndpointsService) syncMetrics(cluster string, cl *config.ClusterConfig) {
	if cl == nil || cl.Metrics == nil {
		return
	}
	conn, ok := s.lookup(cluster)
	if !ok || reflect.DeepEqual(conn.MetricsConfig(), *cl.Metrics) {
		return
	}
	conn.SetMetricsConfig(*cl.Metrics)
}

// EndpointsService is bound to JS.
type EndpointsService struct {
	path   string
	store  secretstore.Store
	revs   *endpointcred.Registry
	lookup func(string) (EndpointConn, bool)
	// mu serialises endpoint actions, keychain calls included; it is taken
	// before the fleet file writer's lock, in that order only (C5 §6).
	mu sync.Mutex
}

func NewEndpointsService(path string, store secretstore.Store, revs *endpointcred.Registry, lookup func(string) (EndpointConn, bool)) *EndpointsService {
	return &EndpointsService{path: path, store: store, revs: revs, lookup: lookup}
}

// EndpointRowDTO is one configured endpoint and its credential's state. It
// carries no secret.
type EndpointRowDTO struct {
	Cluster  string `json:"cluster"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Tenant   string `json:"tenant,omitempty"`
	AuthType string `json:"authType,omitempty"`
	Username string `json:"username,omitempty"`
	State    string `json:"state"`
	Why      string `json:"why,omitempty"`
	Legacy   bool   `json:"legacy"`
	// StoredToo: a legacy token is in the file and a copy is in the
	// keychain - a Move whose file edit has not happened (C5 §6).
	StoredToo bool `json:"storedToo"`
	// Revision is the endpoint as the person sees it; every action sends it
	// back and is refused if fleet.yaml has moved since.
	Revision    string `json:"revision"`
	CanSet      bool   `json:"canSet"`
	CanRemove   bool   `json:"canRemove"`
	CanMove     bool   `json:"canMove"`
	CanTest     bool   `json:"canTest"`
	TestNote    string `json:"testNote,omitempty"`
	RemoveLabel string `json:"removeLabel,omitempty"`
}

// EndpointsStateDTO is every endpoint of every cluster, or why they could
// not be read.
type EndpointsStateDTO struct {
	Rows  []EndpointRowDTO `json:"rows"`
	Error string           `json:"error,omitempty"`
}

// EndpointActionDTO is an action's outcome, said in full.
type EndpointActionDTO struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

const removeLegacyLabel = "Remove the keychain copy - fleet.yaml still holds a token, and that is what Klyx sends"

// GetEndpoints reads fleet.yaml and every endpoint's credential state.
func (s *EndpointsService) GetEndpoints() EndpointsStateDTO {
	snap, err := config.ReadSnapshot(s.path)
	if err != nil {
		return EndpointsStateDTO{Rows: []EndpointRowDTO{}, Error: safehttp.NewRedactor().Text(err.Error())}
	}
	rows := []EndpointRowDTO{}
	for _, cl := range snap.Config.Clusters {
		for _, ne := range cl.Endpoints() {
			rows = append(rows, s.row(snap, cl, ne))
		}
	}
	return EndpointsStateDTO{Rows: rows}
}

func (s *EndpointsService) row(snap config.Snapshot, cl config.ClusterConfig, ne config.NamedEndpoint) EndpointRowDTO {
	legacy := ""
	if ne.Kind == config.EndpointMetrics && cl.Metrics != nil {
		legacy = cl.Metrics.Token
	}
	cred := endpointcred.Resolve(s.store, endpointcred.Input{Cluster: cl.Name, Kind: ne.Kind, Spec: ne.Spec, LegacyToken: legacy, Revoked: s.revs.Revoked(cl.Name, ne.Kind)})
	r := EndpointRowDTO{Cluster: cl.Name, Kind: ne.Kind, Source: endpointSource(ne.Spec), Tenant: ne.Spec.Tenant,
		State: string(cred.State), Why: cred.Why, Legacy: legacy != "", StoredToo: cred.StoredToo,
		Revision: snap.EndpointRevision(cl.Name, ne.Kind)}
	if a := ne.Spec.Auth; a != nil {
		r.AuthType, r.Username = a.Type, a.Username
	}
	direct := ne.Spec.Endpoint != "" && config.EndpointURLProblem(ne.Spec.Endpoint) == ""
	r.CanSet = direct && ne.Spec.Auth != nil && legacy == ""
	r.CanMove = ne.Kind == config.EndpointMetrics && legacy != "" && direct
	if s.store != nil {
		if _, err := s.store.Get(endpointcred.Account(cl.Name, ne.Kind)); err == nil {
			r.CanRemove = true
		}
	}
	if r.CanRemove && legacy != "" {
		r.RemoveLabel = removeLegacyLabel
	}
	if ne.Kind == config.EndpointMetrics || ne.Kind == config.EndpointAlertmanager {
		r.CanTest = true
	} else {
		r.TestNote = "not implemented until S8: a successful GET of / is not a reader's availability"
	}
	return r
}

// endpointSource is where the endpoint is, without any credentials.
func endpointSource(e config.EndpointConfig) string {
	switch {
	case e.Endpoint != "":
		u, err := url.Parse(e.Endpoint)
		if err != nil {
			return "an endpoint URL that does not parse"
		}
		u.User = nil
		return strings.TrimRight(u.String(), "/")
	case e.ServiceRef != nil:
		return "Service " + e.ServiceRef.Namespace + "/" + e.ServiceRef.Name + ":" + e.ServiceRef.Port + " (through the API server)"
	}
	return "discovered in the cluster (through the API server)"
}

// reviewed takes the action lock's snapshot and checks the endpoint is the
// one the person reviewed.
func (s *EndpointsService) reviewed(cluster, kind, revision string) (config.Snapshot, config.NamedEndpoint, *config.ClusterConfig, error) {
	snap, err := config.ReadSnapshot(s.path)
	if err != nil {
		return config.Snapshot{}, config.NamedEndpoint{}, nil, err
	}
	ne, cl, ok := snap.Endpoint(cluster, kind)
	if !ok {
		return snap, ne, nil, fmt.Errorf("cluster %q has no %s endpoint in fleet.yaml", cluster, kind)
	}
	if snap.EndpointRevision(cluster, kind) != revision {
		return snap, ne, cl, config.ErrEndpointChanged
	}
	return snap, ne, cl, nil
}

func fail(red safehttp.Redactor, err error) EndpointActionDTO {
	return EndpointActionDTO{Message: red.Text(err.Error())}
}

// SetCredential stores a secret for the endpoint the person reviewed, bound
// to that endpoint's origin, and moves its revision so every client uses
// it (C5 §3, §6, §7).
func (s *EndpointsService) SetCredential(cluster, kind, revision, secret string) EndpointActionDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	red := safehttp.NewRedactor([]string{secret})
	_, ne, cl, err := s.reviewed(cluster, kind, revision)
	if err != nil {
		return fail(red, err)
	}
	switch {
	case s.store == nil:
		return EndpointActionDTO{Message: "keychain unavailable: no credential store"}
	case secret == "":
		return EndpointActionDTO{Message: "enter the credential to store"}
	case ne.Spec.Endpoint == "":
		return EndpointActionDTO{Message: "credentials are not sent to a proxied or discovered endpoint; nothing was stored"}
	case ne.Spec.Auth == nil:
		return EndpointActionDTO{Message: "this endpoint has no auth in fleet.yaml; add auth: {type: bearer} or {type: basic, username: ...} first"}
	case kind == config.EndpointMetrics && cl.Metrics != nil && cl.Metrics.Token != "":
		return EndpointActionDTO{Message: "fleet.yaml still holds a plaintext token for this endpoint; move it to the keychain first"}
	}
	if p := config.EndpointURLProblem(ne.Spec.Endpoint); p != "" {
		return EndpointActionDTO{Message: p}
	}
	origin, err := safehttp.OriginOf(ne.Spec.Endpoint)
	if err != nil {
		return fail(red, err)
	}
	v, err := endpointcred.Record{V: 1, Origin: origin, Type: ne.Spec.Auth.Type, Secret: secret}.Encode()
	if err != nil {
		return fail(red, err)
	}
	install := func() error { return s.store.Set(endpointcred.Account(cluster, kind), v) }
	// a live metrics connection takes the credential and the reviewed
	// configuration as one step, so no reader pairs the new credential
	// with the configuration it replaces (Codex's revision 1 review)
	if conn, ok := s.lookup(cluster); ok && kind == config.EndpointMetrics && cl.Metrics != nil {
		if err := conn.ChangeMetricsCredential(*cl.Metrics, install); err != nil {
			return fail(red, err)
		}
		return EndpointActionDTO{OK: true, Message: "Stored in the keychain for " + origin + "."}
	}
	if conn, ok := s.lookup(cluster); ok && kind == config.EndpointAlertmanager && cl.Alertmanager != nil {
		if err := conn.ChangeAlertmanagerCredential(cl.Alertmanager, install); err != nil {
			return fail(red, err)
		}
		return EndpointActionDTO{OK: true, Message: "Stored in the keychain for " + origin + "."}
	}
	if err := install(); err != nil {
		return fail(red, err)
	}
	s.revs.Bump(cluster, kind, true)
	return EndpointActionDTO{OK: true, Message: "Stored in the keychain for " + origin + "."}
}

// RemoveCredential stops the endpoint's keychain credential from being used
// - at once, in memory - and then deletes it from the keychain (C5 §7).
func (s *EndpointsService) RemoveCredential(cluster, kind, revision string) EndpointActionDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	red := safehttp.NewRedactor()
	_, _, cl, err := s.reviewed(cluster, kind, revision)
	if err != nil {
		return fail(red, err)
	}
	if s.store == nil {
		return EndpointActionDTO{Message: "keychain unavailable: no credential store"}
	}
	s.revs.Revoke(cluster, kind)
	if err := s.store.Delete(endpointcred.Account(cluster, kind)); err != nil {
		return EndpointActionDTO{Message: "Removed from use; the keychain still holds it - " + red.Text(err.Error())}
	}
	if kind == config.EndpointMetrics && cl.Metrics != nil && cl.Metrics.Token != "" {
		return EndpointActionDTO{OK: true, Message: "Removed the keychain copy. fleet.yaml still holds a token, and that is what Klyx sends."}
	}
	return EndpointActionDTO{OK: true, Message: "Removed from use and from the keychain."}
}

// MoveToKeychain moves the legacy plaintext metrics token into the keychain
// against one snapshot (C5 §6): the keychain write first, then the file
// edit, which happens only if fleet.yaml still holds that snapshot.
func (s *EndpointsService) MoveToKeychain(cluster, revision string) EndpointActionDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ne, cl, err := s.reviewed(cluster, config.EndpointMetrics, revision)
	if cl == nil || cl.Metrics == nil || cl.Metrics.Token == "" {
		if err != nil {
			return fail(safehttp.NewRedactor(), err)
		}
		return EndpointActionDTO{Message: "fleet.yaml holds no plaintext metrics token for this cluster"}
	}
	token := cl.Metrics.Token
	red := safehttp.NewRedactor(safehttp.Bearer(token))
	if err != nil {
		return fail(red, err)
	}
	if s.store == nil {
		return EndpointActionDTO{Message: "keychain unavailable: no credential store; nothing changed"}
	}
	if p := config.EndpointURLProblem(ne.Spec.Endpoint); p != "" || ne.Spec.Endpoint == "" {
		return EndpointActionDTO{Message: "the token can only move for a direct endpoint URL without credentials in it; nothing changed"}
	}
	origin, err := safehttp.OriginOf(ne.Spec.Endpoint)
	if err != nil {
		return fail(red, err)
	}
	v, err := endpointcred.Record{V: 1, Origin: origin, Type: "bearer", Secret: token}.Encode()
	if err != nil {
		return fail(red, err)
	}
	if err := s.store.Set(endpointcred.Account(cluster, config.EndpointMetrics), v); err != nil {
		return EndpointActionDTO{Message: "Nothing changed: " + red.Text(err.Error())}
	}
	next, err := config.MoveMetricsTokenToAuth(snap, cluster)
	if err != nil {
		why := red.Text(err.Error())
		if errors.Is(err, config.ErrFileChangedSinceSnapshot) {
			why = "fleet.yaml changed meanwhile and still holds a token"
		}
		return EndpointActionDTO{Message: "The token is stored in the keychain for " + origin + "; " + why + " - review it and move again."}
	}
	for _, c := range next.Clusters {
		if c.Name == cluster && c.Metrics != nil {
			if conn, ok := s.lookup(cluster); ok {
				conn.SetMetricsConfig(*c.Metrics)
			}
		}
	}
	s.revs.Bump(cluster, config.EndpointMetrics, true)
	return EndpointActionDTO{OK: true, Message: "Moved to the keychain for " + origin + "; fleet.yaml now says auth: {type: bearer}."}
}

// TestEndpoint reads the endpoint the person reviewed once. Metrics only:
// Alertmanager and Loki have no reader until S8.
func (s *EndpointsService) TestEndpoint(cluster, kind, revision string) EndpointActionDTO {
	if kind != config.EndpointMetrics && kind != config.EndpointAlertmanager {
		return EndpointActionDTO{Message: "Test is not implemented until S8."}
	}
	s.mu.Lock()
	_, _, cl, err := s.reviewed(cluster, kind, revision)
	if err == nil {
		// the test reads the endpoint as reviewed, never the connection's
		// older configuration
		if kind == config.EndpointMetrics {
			s.syncMetrics(cluster, cl)
		} else {
			s.syncAlertmanager(cluster, cl)
		}
	}
	s.mu.Unlock()
	if err != nil {
		return fail(safehttp.NewRedactor(), err)
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return EndpointActionDTO{Message: "cluster not connected"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
	defer cancel()
	if kind == config.EndpointAlertmanager {
		// a status read, separate from the alerts snapshot: it never moves
		// the snapshot's freshness or result (S8a §4)
		st, src, err := conn.AlertmanagerStatus(ctx)
		if err != nil {
			return EndpointActionDTO{Message: "Unavailable: " + safehttp.NewRedactor().Text(err.Error())}
		}
		// the alerts snapshot reads again on its own, with its own outcome
		conn.RefreshAlerts()
		return EndpointActionDTO{OK: true, Message: "Available: " + amStatusText(st) + " at " + src + ", answered."}
	}
	_, capb := conn.ClusterMetrics(ctx, true)
	if !capb.Available {
		return EndpointActionDTO{Message: "Unavailable: " + capb.Reason}
	}
	return EndpointActionDTO{OK: true, Message: "Available: " + capb.Source + " answered."}
}

// amStatusText is an Alertmanager status as Settings' Test reports it.
func amStatusText(st alertmanager.Status) string {
	cluster := "cluster status not reported"
	switch st.ClusterStatus {
	case "disabled":
		cluster = "clustering disabled"
	case "":
	default:
		cluster = fmt.Sprintf("cluster %s with %d peers", st.ClusterStatus, st.Peers)
	}
	return "Alertmanager " + st.Version + ", " + cluster
}
