package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/endpointhttp"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/safehttp"
)

// Coverage S8a: the Alertmanager endpoint on a connection - its
// configuration, its resolution (endpoint, serviceRef, or Prometheus
// Operator discovery with its proof of one installation), the source
// identity that partitions evidence, the per-cluster and fleet-wide read
// bounds, the credential path, and the read itself.

const (
	amMaxBytes       = 8 << 20
	amHTTPTimeout    = 10 * time.Second
	amDiscoveryTTL   = 5 * time.Minute
	amMaxAlerts      = 5000
	amMaxAppearances = 20000
	amMaxInFlight    = 4
)

var errAMTooLarge = fmt.Errorf("the Alertmanager response exceeded %d MiB; Klyx stopped reading it", amMaxBytes>>20)

var amLimits = endpointhttp.Limits{MaxBytes: amMaxBytes, TooLarge: errAMTooLarge}

// ErrNoAlertmanager: no Alertmanager is configured, and discovery completed
// and found none. Only this reads as "not installed" (S8a §4): it is the
// Observer's ErrSourceAbsent.
var ErrNoAlertmanager = fmt.Errorf("%w: no Alertmanager configured or found", observe.ErrSourceAbsent)

// ErrSourceChanged: the read's generation was keyed to another source; the
// re-key that follows discards it (S8a §4).
var ErrSourceChanged = errors.New("the alert source changed during the read; it is read again for the new source")

// amReads bounds alert reads in flight fleet-wide (S8a §8).
var amReads = make(chan struct{}, amMaxInFlight)

// alertmanagerState is a connection's Alertmanager endpoint. mu guards the
// configuration, discovery and identity; readMu makes one read at a time per
// cluster, across re-keys (the old generation's read finishes before the new
// one's starts).
type alertmanagerState struct {
	readMu   sync.Mutex
	mu       sync.Mutex
	cfg      *config.EndpointConfig
	disc     *amDiscovery // the last discovery outcome
	found    *amDiscovery // the last discovery that found one installation
	rest     rest.Interface
	restErr  error
	identity string
	rekey    func(key string) // re-keys the alerts snapshot (set with the health Observer)
	client   func(policy safehttp.Policy) *http.Client
	// configured and resolved make the Alerting capability (S8a §4):
	// atomics, so the capability set reads them under c.mu without am.mu
	// (a read holds am.mu while discovery takes c.mu).
	configured atomic.Bool
	resolved   atomic.Bool // the last keyed read resolved an Alertmanager
}

// amDiscovery is one discovery outcome.
type amDiscovery struct {
	at        time.Time
	err       error  // could not be read
	none      bool   // read complete, nothing found
	ambiguous string // read complete, no single installation proven
	svc       endpointhttp.Service
	resource  string // namespace/name of the Alertmanager resource
	via       string // how the Service was chosen
}

// WithAlertmanager sets the configured Alertmanager (nil: none, so
// discovery). Call it in the factory, before Start.
func (c *ClusterConn) WithAlertmanager(cfg *config.EndpointConfig) *ClusterConn {
	c.am.cfg = cloneEndpoint(cfg)
	c.am.configured.Store(cfg != nil)
	return c
}

// alertsPresent is the Alerting capability: an Alertmanager configured, or
// the last keyed read resolved one.
func (c *ClusterConn) alertsPresent() bool {
	return c.am.configured.Load() || c.am.resolved.Load()
}

// AlertmanagerConfig is the configured Alertmanager as this connection
// uses it (nil when none).
func (c *ClusterConn) AlertmanagerConfig() *config.EndpointConfig {
	c.am.mu.Lock()
	defer c.am.mu.Unlock()
	return cloneEndpoint(c.am.cfg)
}

// SetAlertmanagerConfig adopts a reviewed configuration (S8a §4, C5 §7):
// the discovery cache goes, the revision moves, and the source identity is
// recomputed - a different one re-keys the alerts snapshot - all under the
// state's lock.
func (c *ClusterConn) SetAlertmanagerConfig(cfg *config.EndpointConfig) {
	c.am.mu.Lock()
	defer c.am.mu.Unlock()
	c.am.cfg = cloneEndpoint(cfg)
	c.am.configured.Store(cfg != nil)
	c.am.disc, c.am.found = nil, nil
	if c.creds.Revisions != nil {
		c.creds.Revisions.Bump(c.name, config.EndpointAlertmanager, false)
	}
	c.reidentifyLocked()
}

// ChangeAlertmanagerCredential installs a credential and the configuration
// it was reviewed with as one step for this connection's reads (C5 §7).
func (c *ClusterConn) ChangeAlertmanagerCredential(cfg *config.EndpointConfig, install func() error) error {
	c.am.mu.Lock()
	defer c.am.mu.Unlock()
	if err := install(); err != nil {
		return err
	}
	if !sameEndpoint(c.am.cfg, cfg) {
		c.am.cfg = cloneEndpoint(cfg)
		c.am.configured.Store(cfg != nil)
		c.am.disc, c.am.found = nil, nil
	}
	if c.creds.Revisions != nil {
		c.creds.Revisions.Bump(c.name, config.EndpointAlertmanager, true)
	}
	c.reidentifyLocked()
	return nil
}

// reidentifyLocked recomputes the identity from the configuration and the
// last discovery, without the network; a change re-keys the snapshot.
// Caller holds am.mu.
func (c *ClusterConn) reidentifyLocked() {
	id := amPendingIdentity
	if src, ok := c.staticSourceLocked(); ok {
		id = src
	}
	c.setIdentityLocked(id)
}

// amPendingIdentity is the identity before a configuration that depends on
// discovery has been resolved: a new, unread collection.
const amPendingIdentity = "pending"

func (c *ClusterConn) setIdentityLocked(id string) {
	if id == c.am.identity {
		return
	}
	c.am.identity = id
	if c.am.rekey != nil {
		c.am.rekey(id)
	}
}

// staticSourceLocked is the identity a configured endpoint or serviceRef
// has, with no network; false when it depends on discovery.
func (c *ClusterConn) staticSourceLocked() (string, bool) {
	cfg := c.am.cfg
	if cfg == nil {
		return "", false
	}
	switch {
	case cfg.Endpoint != "":
		at, err := endpointAt(cfg.Endpoint)
		if err != nil {
			return "endpoint|invalid", true
		}
		return amIdentity("endpoint", at, cfg.Tenant, cfg.Match), true
	case cfg.ServiceRef != nil:
		return amIdentity("service", serviceAt(serviceOf(cfg.ServiceRef)), cfg.Tenant, cfg.Match), true
	}
	return "", false
}

// amIdentity is a source identity: the resolution, the effective location,
// the tenant and the sorted match set (S8a §4).
func amIdentity(how, at, tenant string, match map[string]string) string {
	return how + "|" + at + "|tenant=" + tenant + "|match=" + strings.Join(alertmanager.FilterParams(match), ",")
}

// endpointAt is an endpoint's canonical origin plus its path.
func endpointAt(endpoint string) (string, error) {
	if p := config.EndpointURLProblem(endpoint); p != "" {
		return "", errors.New(p)
	}
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil {
		return "", errors.New("the endpoint URL does not parse")
	}
	origin, err := safehttp.CanonicalOrigin(u)
	if err != nil {
		return "", err
	}
	return origin + u.Path, nil
}

func serviceOf(sr *config.MetricsServiceRef) endpointhttp.Service {
	scheme := sr.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return endpointhttp.Service{Namespace: sr.Namespace, Name: sr.Name, Port: sr.Port, Scheme: scheme, Path: sr.Path}
}

func serviceAt(s endpointhttp.Service) string {
	at := s.Namespace + "/" + s.Name + ":" + s.Port
	if p := strings.Trim(s.Path, "/"); p != "" {
		at += "/" + p
	}
	return at
}

// amSource is a resolved source for one read.
type amSource struct {
	identity string
	display  string
	note     string
	get      func(ctx context.Context, path string, q url.Values) (int, []byte, error)
	redactor safehttp.Redactor
	match    map[string]string
	rev      uint64
}

// resolveAlertmanager resolves the source a read uses now, under am.mu.
func (c *ClusterConn) resolveAlertmanager(ctx context.Context) (amSource, error) {
	c.am.mu.Lock()
	defer c.am.mu.Unlock()
	return c.resolveAlertmanagerLocked(ctx)
}

// resolveAlertmanagerLocked is resolveAlertmanager; caller holds am.mu.
func (c *ClusterConn) resolveAlertmanagerLocked(ctx context.Context) (amSource, error) {
	cfg := c.am.cfg
	var tenant string
	var match map[string]string
	if cfg != nil {
		tenant, match = cfg.Tenant, cfg.Match
	}
	rev := c.creds.Revisions.Revision(c.name, config.EndpointAlertmanager)
	if cfg != nil && cfg.Endpoint != "" {
		at, err := endpointAt(cfg.Endpoint)
		if err != nil {
			return amSource{identity: "endpoint|invalid"}, err
		}
		auth := c.endpointAuth(config.EndpointAlertmanager, *cfg, "", rev)
		if auth.Unusable != "" {
			return amSource{identity: amIdentity("endpoint", at, tenant, match)}, errors.New(auth.Unusable)
		}
		client := c.amClient(endpointhttp.DirectPolicy(amLimits, amHTTPTimeout, cfg.TLSSkipVerify))
		d := endpointhttp.NewDirect(strings.TrimRight(cfg.Endpoint, "/"), auth, client, amLimits)
		return amSource{identity: amIdentity("endpoint", at, tenant, match), display: at, get: d.Get,
			redactor: d.Redactor(), match: match, rev: rev}, nil
	}
	if cfg != nil && cfg.ServiceRef != nil {
		svc := serviceOf(cfg.ServiceRef)
		return c.proxySourceLocked(svc, amIdentity("service", serviceAt(svc), tenant, match), "Service "+serviceAt(svc), tenant, match, rev, "")
	}
	d := c.discoverAlertmanagerLocked(ctx)
	note := ""
	switch {
	case d.err != nil && c.am.found == nil:
		return amSource{identity: c.am.identity}, fmt.Errorf("Alertmanager discovery could not be read: %w", d.err)
	case d.err != nil:
		note = "discovery could not be re-read: " + d.err.Error()
		d = c.am.found
	case d.ambiguous != "":
		return amSource{identity: "ambiguous|" + d.ambiguous}, errors.New(d.ambiguous)
	case d.none:
		return amSource{identity: "none"}, ErrNoAlertmanager
	}
	return c.proxySourceLocked(d.svc, amIdentity("discovered", serviceAt(d.svc)+"|"+d.resource, tenant, match),
		"discovered "+d.resource+" through "+d.via, tenant, match, rev, note)
}

func (c *ClusterConn) proxySourceLocked(svc endpointhttp.Service, identity, display, tenant string, match map[string]string, rev uint64, note string) (amSource, error) {
	rc, err := c.amRESTLocked()
	if err != nil {
		return amSource{identity: identity}, fmt.Errorf("the service-proxy client could not be built: %w", err)
	}
	p := endpointhttp.NewProxy(rc, svc, tenant, amLimits)
	return amSource{identity: identity, display: display, note: note, get: p.Get, match: match, rev: rev}, nil
}

// amRESTLocked is the guarded service-proxy client with the Alertmanager
// limits (C5 §4, S8a §8); fake-client connections use their clientset's.
func (c *ClusterConn) amRESTLocked() (rest.Interface, error) {
	if c.restConfig == nil {
		// a fake clientset's client is a typed nil: a failed read, never a
		// panic in the snapshot's read
		rc := c.typed.CoreV1().RESTClient()
		if rc == nil || reflect.ValueOf(rc).IsNil() {
			return nil, errors.New("this connection has no service-proxy client")
		}
		return rc, nil
	}
	if c.am.rest == nil && c.am.restErr == nil {
		c.am.rest, c.am.restErr = endpointhttp.NewGuardedProxyREST(c.restConfig, amLimits)
	}
	return c.am.rest, c.am.restErr
}

func (c *ClusterConn) amClient(p safehttp.Policy) *http.Client {
	if c.am.client != nil {
		return c.am.client(p)
	}
	return safehttp.NewClient(p)
}

// endpointAuth is an external endpoint's credential and tenant as resolved
// now, gated on revision rev (C5 §3, §7).
func (c *ClusterConn) endpointAuth(kind string, spec config.EndpointConfig, legacy string, rev uint64) endpointhttp.Auth {
	revs := c.creds.Revisions
	cred := endpointcred.Resolve(c.creds.Store, endpointcred.Input{Cluster: c.name, Kind: kind,
		Spec: spec, LegacyToken: legacy, Revoked: revs.Revoked(c.name, kind)})
	auth := endpointhttp.Auth{Tenant: spec.Tenant}
	if !cred.Usable() {
		auth.Unusable = cred.Why
		if auth.Unusable == "" {
			auth.Unusable = "the credential is " + string(cred.State)
		}
		return auth
	}
	auth.Header, auth.Redactor = cred.Header(), cred.Redactor()
	auth.Live = revs.Live(c.name, kind, rev)
	return auth
}

var amResourceGVR = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "alertmanagers"}

// discoverAlertmanagerLocked is the cached discovery (S8a §2): five
// minutes for an outcome read complete, re-run at once after a failure.
// Caller holds am.mu.
func (c *ClusterConn) discoverAlertmanagerLocked(ctx context.Context) *amDiscovery {
	now := c.clk.Now()
	if d := c.am.disc; d != nil && d.err == nil && now.Sub(d.at) < amDiscoveryTTL {
		return d
	}
	d := c.discoverAlertmanager(ctx)
	d.at = now
	c.am.disc = d
	if d.err == nil && !d.none && d.ambiguous == "" {
		c.am.found = d
	}
	if d.none || d.ambiguous != "" {
		c.am.found = nil
	}
	return d
}

// discoverAlertmanager finds the one Alertmanager installation through the
// Prometheus Operator's resources, proven (S8a §2):
//  1. the resource not served is "none"; any other discovery error is a
//     failure, never "none";
//  2. exactly one Alertmanager resource, else none or ambiguity;
//  3. a Service pinning that instance (selector alertmanager=<name>), its
//     port named web or http-web;
//  4. else the governing alertmanager-operated, only when its EndpointSlices
//     hold pods owned by UID by one StatefulSet that the resource owns.
func (c *ClusterConn) discoverAlertmanager(ctx context.Context) *amDiscovery {
	rl, err := c.discovery().ServerResourcesForGroupVersion(amResourceGVR.GroupVersion().String())
	switch {
	case apierrors.IsNotFound(err):
		return &amDiscovery{none: true}
	case err != nil:
		return &amDiscovery{err: err}
	}
	served := false
	for _, r := range rl.APIResources {
		if r.Name == amResourceGVR.Resource {
			served = true
		}
	}
	if !served {
		return &amDiscovery{none: true}
	}
	list, err := c.dyn.Resource(amResourceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return &amDiscovery{err: err}
	}
	if list.GetContinue() != "" {
		return &amDiscovery{err: errors.New("the Alertmanager list was not complete")}
	}
	switch len(list.Items) {
	case 0:
		return &amDiscovery{none: true}
	case 1:
	default:
		return &amDiscovery{ambiguous: fmt.Sprintf("%d Alertmanagers found; set alertmanager.serviceRef", len(list.Items))}
	}
	am := list.Items[0]
	ns, name := am.GetNamespace(), am.GetName()
	resource := ns + "/" + name
	path := amRoutePrefix(&am)
	svcs, err := c.typed.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return &amDiscovery{err: err}
	}
	if svcs.Continue != "" {
		return &amDiscovery{err: errors.New("the Service list was not complete")}
	}
	sort.Slice(svcs.Items, func(i, j int) bool { return svcs.Items[i].Name < svcs.Items[j].Name })
	for _, s := range svcs.Items {
		if s.Spec.Selector["alertmanager"] != name {
			continue
		}
		for _, p := range s.Spec.Ports {
			if p.Name == "web" || p.Name == "http-web" {
				return &amDiscovery{svc: endpointhttp.Service{Namespace: ns, Name: s.Name, Port: p.Name, Scheme: "http", Path: path},
					resource: resource, via: "Service " + s.Name}
			}
		}
	}
	for _, s := range svcs.Items {
		if s.Name != "alertmanager-operated" {
			continue
		}
		if why := c.proveGoverning(ctx, ns, am.GetUID()); why != "" {
			return &amDiscovery{ambiguous: "the governing Service alertmanager-operated could not be proven to front only " + resource + " (" + why + "); set alertmanager.serviceRef"}
		}
		return &amDiscovery{svc: endpointhttp.Service{Namespace: ns, Name: s.Name, Port: "web", Scheme: "http", Path: path},
			resource: resource, via: "Service alertmanager-operated (proven by ownership)"}
	}
	return &amDiscovery{ambiguous: "no Service for " + resource + " was found; set alertmanager.serviceRef"}
}

// amRoutePrefix is the path the operator serves the instance under: its
// routePrefix, else its externalUrl's path.
func amRoutePrefix(am *unstructured.Unstructured) string {
	if p, _, _ := unstructured.NestedString(am.Object, "spec", "routePrefix"); p != "" && p != "/" {
		return p
	}
	if raw, _, _ := unstructured.NestedString(am.Object, "spec", "externalUrl"); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Path != "" && u.Path != "/" {
			return u.Path
		}
	}
	return ""
}

// proveGoverning proves the governing Service fronts only the resource's
// pods: every endpoint has a Pod targetRef, every such Pod is owned by UID
// by one StatefulSet, and that StatefulSet is owned by UID by the resource.
// "" when proven; otherwise why not.
func (c *ClusterConn) proveGoverning(ctx context.Context, ns string, amUID types.UID) string {
	slices, err := c.typed.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=alertmanager-operated"})
	if err != nil {
		return "its EndpointSlices could not be read"
	}
	if slices.Continue != "" {
		return "its EndpointSlices were not read complete"
	}
	stsUID := ""
	pods := 0
	for _, sl := range slices.Items {
		for _, ep := range sl.Endpoints {
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" || ep.TargetRef.Name == "" {
				return "an endpoint has no Pod targetRef"
			}
			pod, err := c.typed.CoreV1().Pods(ns).Get(ctx, ep.TargetRef.Name, metav1.GetOptions{})
			if err != nil {
				return "a Pod behind it could not be read"
			}
			owner := metav1.GetControllerOf(pod)
			if owner == nil || owner.Kind != "StatefulSet" {
				return "a Pod behind it is not owned by a StatefulSet"
			}
			if stsUID != "" && string(owner.UID) != stsUID {
				return "its Pods belong to more than one StatefulSet"
			}
			stsUID = string(owner.UID)
			sts, err := c.typed.AppsV1().StatefulSets(ns).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil || string(sts.UID) != stsUID {
				return "the StatefulSet behind it could not be read"
			}
			amOwner := metav1.GetControllerOf(sts)
			if amOwner == nil || amOwner.Kind != "Alertmanager" || amOwner.UID != amUID {
				return "its StatefulSet is not owned by the Alertmanager resource"
			}
			pods++
		}
	}
	if pods == 0 {
		return "it has no endpoints to prove"
	}
	return ""
}

// ReadAlerts is the alerts snapshot's read (S8a §3, §4) for the generation
// keyed key: one read per cluster at a time, at most four fleet-wide; the
// source resolved now, and refused when it is not the key's (the re-key
// that follows discards this generation); /alerts then /alerts/groups with
// match as filter; the credential fence; Reduce, the match re-check, the
// entry cap and the display values.
func (c *ClusterConn) ReadAlerts(ctx context.Context, key string) (alertmanager.Read, error) {
	c.am.readMu.Lock()
	defer c.am.readMu.Unlock()
	// the resolution, the identity it gives and any re-key are one step
	// against configuration changes, which take the same lock
	c.am.mu.Lock()
	src, err := c.resolveAlertmanagerLocked(ctx)
	c.am.identity = src.identity
	if src.identity != key {
		// whether or not the identity changed: a generation keyed otherwise
		// (an Observer restarted with the empty key) re-keys to it
		if c.am.rekey != nil {
			c.am.rekey(src.identity)
		}
		c.am.mu.Unlock()
		return alertmanager.Read{Identity: src.identity}, ErrSourceChanged
	}
	// the read's own outcome for its key is the capability's evidence
	c.am.resolved.Store(!errors.Is(err, ErrNoAlertmanager))
	c.am.mu.Unlock()
	if err != nil {
		return alertmanager.Read{Identity: src.identity}, err
	}
	select {
	case amReads <- struct{}{}:
		defer func() { <-amReads }()
	case <-ctx.Done():
		return alertmanager.Read{Identity: src.identity}, ctx.Err()
	}
	filter := alertmanager.FilterParams(src.match)
	q := url.Values{"active": {"true"}, "silenced": {"true"}, "inhibited": {"true"}, "unprocessed": {"true"}, "filter": filter}
	alerts, err := amGet(ctx, src, "/api/v2/alerts", q, alertmanager.ParseAlerts)
	if err != nil {
		return alertmanager.Read{Identity: src.identity}, err
	}
	gq := url.Values{"active": {"true"}, "muted": {"true"}, "silenced": {"false"}, "inhibited": {"false"}, "filter": filter}
	groups, err := amGet(ctx, src, "/api/v2/alerts/groups", gq, alertmanager.ParseGroups)
	if err != nil {
		return alertmanager.Read{Identity: src.identity}, err
	}
	appearances := 0
	for _, g := range groups {
		appearances += len(g.Alerts)
	}
	if appearances > amMaxAppearances {
		return alertmanager.Read{Identity: src.identity}, fmt.Errorf("the alert groups held more than %d appearances; mutes would be incomplete", amMaxAppearances)
	}
	if c.creds.Revisions.Revision(c.name, config.EndpointAlertmanager) != src.rev {
		return alertmanager.Read{Identity: src.identity}, errors.New("the endpoint's credential changed during the read; it is read again next time")
	}
	kept := alerts[:0]
	for _, a := range alerts {
		if alertmanager.Matches(a.Labels, src.match) {
			kept = append(kept, a)
		}
	}
	reduced := alertmanager.Reduce(kept, groups)
	out := alertmanager.Read{Identity: src.identity, Source: src.redactor.Text(src.display), Match: src.match, Note: src.redactor.Text(src.note), ReadAt: c.clk.Now()}
	if len(reduced) > amMaxAlerts {
		reduced, out.Truncated = reduced[:amMaxAlerts], true
	}
	out.Alerts = make([]alertmanager.Item, 0, len(reduced))
	for _, r := range reduced {
		out.Alerts = append(out.Alerts, alertmanager.Item{Subject: alertmanager.SubjectOf(r.Labels), Counted: r.Counted(), Display: alertmanager.Display(r, src.redactor)})
	}
	return out, nil
}

// amGet reads one path and parses it; a non-2xx status is a failed read.
func amGet[T any](ctx context.Context, src amSource, path string, q url.Values, parse func([]byte) (T, error)) (T, error) {
	var zero T
	code, body, err := src.get(ctx, path, q)
	if err != nil {
		return zero, src.redactor.Err(err, errAMTooLarge, endpointhttp.ErrCredentialChanged, context.Canceled, context.DeadlineExceeded)
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

// AlertmanagerStatus reads GET /api/v2/status through the source as
// configured now, for Settings' Test (S8a §4): it is not the alerts
// snapshot's read and never touches it.
func (c *ClusterConn) AlertmanagerStatus(ctx context.Context) (alertmanager.Status, string, error) {
	src, err := c.resolveAlertmanager(ctx)
	if err != nil {
		return alertmanager.Status{}, src.display, err
	}
	st, err := amGet(ctx, src, "/api/v2/status", nil, alertmanager.ParseStatus)
	if err != nil {
		return alertmanager.Status{}, src.redactor.Text(src.display), err
	}
	st.Version, st.ClusterStatus = alertmanager.Clip(src.redactor.Text(st.Version), alertmanager.MaxLabelValue), alertmanager.Clip(src.redactor.Text(st.ClusterStatus), alertmanager.MaxLabelValue)
	return st, src.redactor.Text(src.display), nil
}

// AlertsIdentity is the current source identity.
func (c *ClusterConn) AlertsIdentity() string {
	c.am.mu.Lock()
	defer c.am.mu.Unlock()
	return c.am.identity
}

// setAlertsRekey sets the re-key hook (the health Observer's).
func (c *ClusterConn) setAlertsRekey(f func(key string)) {
	c.am.mu.Lock()
	defer c.am.mu.Unlock()
	c.am.rekey = f
}

func cloneEndpoint(e *config.EndpointConfig) *config.EndpointConfig {
	if e == nil {
		return nil
	}
	cp := *e
	if e.ServiceRef != nil {
		sr := *e.ServiceRef
		cp.ServiceRef = &sr
	}
	if e.Auth != nil {
		a := *e.Auth
		cp.Auth = &a
	}
	if e.Match != nil {
		cp.Match = make(map[string]string, len(e.Match))
		for k, v := range e.Match {
			cp.Match[k] = v
		}
	}
	return &cp
}

func sameEndpoint(a, b *config.EndpointConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	return amIdentityOf(a) == amIdentityOf(b) && a.TLSSkipVerify == b.TLSSkipVerify &&
		((a.Auth == nil && b.Auth == nil) || (a.Auth != nil && b.Auth != nil && *a.Auth == *b.Auth))
}

func amIdentityOf(e *config.EndpointConfig) string {
	at := ""
	switch {
	case e.Endpoint != "":
		at, _ = endpointAt(e.Endpoint)
	case e.ServiceRef != nil:
		at = serviceAt(serviceOf(e.ServiceRef))
	}
	return amIdentity("cfg", at, e.Tenant, e.Match)
}

// RefreshAlerts asks the alerts snapshot for a fresh read of its own (after
// Settings' Test): it never carries the Test's result.
func (c *ClusterConn) RefreshAlerts() {
	c.mu.RLock()
	o := c.health
	c.mu.RUnlock()
	if o != nil {
		o.Refresh(healthsrc.SnapAlerts)
	}
}
