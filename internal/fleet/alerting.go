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
	"github.com/moomora/klyx/internal/bounds"
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
	amMaxBytes       = bounds.AlertsBytes
	amHTTPTimeout    = 10 * time.Second
	amDiscoveryTTL   = 5 * time.Minute
	amMaxAlerts      = bounds.AlertsPerRead
	amMaxAppearances = bounds.AlertsAppearances
	amMaxInFlight    = 4
	// amMaxProofEndpoints bounds the governing Service's proof: one Pod GET
	// per endpoint, at most every amDiscoveryTTL.
	amMaxProofEndpoints = 16
)

var errAMTooLarge = fmt.Errorf("the Alertmanager response exceeded %d MiB; Klyx stopped reading it", amMaxBytes>>20)

var amLimits = endpointhttp.Limits{MaxBytes: amMaxBytes, TooLarge: errAMTooLarge}

// ErrNoAlertmanager: no Alertmanager is configured, and discovery completed
// and found none. Only this reads as "not installed" (S8a §4): it is the
// Observer's ErrSourceAbsent.
var ErrNoAlertmanager = fmt.Errorf("%w: no Alertmanager configured or found", observe.ErrSourceAbsent)

// ErrAlertsOff: the cluster's own block says off (Settings S3a §2.2): no
// alerts are read, and it reads as absent, never as a failure.
var ErrAlertsOff = fmt.Errorf("%w: alerts are off for this cluster in fleet.yaml", observe.ErrSourceAbsent)

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
	// buckets read a subscription's installation (Settings S3a §3.2); set
	// before Start, never after
	buckets *alertBuckets
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

// withAlertBuckets gives the connection the fleet's bucket readers, for a
// subscription (Settings S3a §3.2). Call it before Start.
func (c *ClusterConn) withAlertBuckets(b *alertBuckets) { c.am.buckets = b }

// WithAlertmanager sets the configured Alertmanager (nil: none, so
// discovery). Call it in the factory, before Start.
func (c *ClusterConn) WithAlertmanager(cfg *config.EndpointConfig) *ClusterConn {
	c.am.cfg = cloneEndpoint(cfg)
	c.am.configured.Store(configuredAM(cfg))
	return c
}

// configuredAM is the Alerting capability's configured half: a block that
// is not off (Settings S3a §2.2).
func configuredAM(cfg *config.EndpointConfig) bool { return cfg != nil && !cfg.Off }

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
	c.am.configured.Store(configuredAM(cfg))
	c.am.disc, c.am.found = nil, nil
	if c.creds.Revisions != nil {
		c.creds.Revisions.Bump(c.name, config.EndpointAlertmanager, false)
	}
	// a cluster no longer subscribed leaves its bucket: it would never
	// reach a join again to leave it (Settings S3a §13)
	if c.am.buckets != nil && (cfg == nil || cfg.Source == "") {
		c.am.buckets.leave(c.ID())
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
		c.am.configured.Store(configuredAM(cfg))
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
	case cfg.Off:
		return amOffIdentity, true
	case cfg.Source != "" && c.am.buckets != nil:
		key, _, _, _ := c.am.buckets.memberKey(c.name)
		return key, true
	case cfg.Source != "":
		return amSubscribedIdentity(cfg.Source), true
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

// amOffIdentity and amSubscribedIdentity are the identities of a block that
// says off, and of a subscription to an installation (Settings S3a §2.2).
const amOffIdentity = "off"

func amSubscribedIdentity(source string) string { return "source|" + source }

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
	get      func(ctx context.Context, path string, q url.Values) (int, []byte, error)
	redactor safehttp.Redactor
	match    map[string]string
	tenant   string
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
	// Settings S3a §2.2: off reads nothing, and a subscription is read
	// through its installation - never by discovery in this cluster. This
	// version does not read through installations yet, so a subscribed
	// cluster's alerts are unreadable, said as such
	switch {
	case cfg != nil && cfg.Off:
		return amSource{identity: amOffIdentity}, ErrAlertsOff
	case cfg != nil && cfg.Source != "":
		return amSource{identity: amSubscribedIdentity(cfg.Source)},
			fmt.Errorf("this cluster's alerts come from the installation %s, and this version of Klyx does not read through installations yet", cfg.Source)
	}
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
			redactor: d.Redactor(), match: match, tenant: tenant, rev: rev}, nil
	}
	if cfg != nil && cfg.ServiceRef != nil {
		svc := serviceOf(cfg.ServiceRef)
		return c.proxySourceLocked(svc, amIdentity("service", serviceAt(svc), tenant, match), "Service "+serviceAt(svc), tenant, match, rev)
	}
	d := c.discoverAlertmanagerLocked(ctx)
	switch {
	case d.err != nil && c.am.found == nil:
		return amSource{identity: c.am.identity}, fmt.Errorf("Alertmanager discovery could not be read: %w", d.err)
	case d.err != nil:
		// the installation behind the found Service is unproven now: the
		// read fails under the found source's identity, so its last list is
		// kept, stale, and never read again as live through that Service
		f := c.am.found
		return amSource{identity: amIdentity("discovered", serviceAt(f.svc)+"|"+f.resource, tenant, match)},
			fmt.Errorf("Alertmanager discovery could not be re-read, so %s is not read through %s: %w", f.resource, f.via, d.err)
	case d.ambiguous != "":
		return amSource{identity: "ambiguous|" + d.ambiguous}, errors.New(d.ambiguous)
	case d.none:
		return amSource{identity: "none"}, ErrNoAlertmanager
	}
	return c.proxySourceLocked(d.svc, amIdentity("discovered", serviceAt(d.svc)+"|"+d.resource, tenant, match),
		"discovered "+d.resource+" through "+d.via, tenant, match, rev)
}

func (c *ClusterConn) proxySourceLocked(svc endpointhttp.Service, identity, display, tenant string, match map[string]string, rev uint64) (amSource, error) {
	rc, err := c.amRESTLocked()
	if err != nil {
		return amSource{identity: identity}, fmt.Errorf("the service-proxy client could not be built: %w", err)
	}
	p := endpointhttp.NewProxy(rc, svc, tenant, amLimits)
	return amSource{identity: identity, display: display, get: p.Get, match: match, tenant: tenant, rev: rev}, nil
}

// legacySentence is the scope sentence of a cluster's own block or
// discovery (Settings S3a §3.5): where, in this cluster or at its URL, the
// tenant, and the filter - or that nothing is filtered.
func legacySentence(src amSource) string {
	where := "from " + src.display
	if strings.HasPrefix(src.identity, "service|") {
		where = "from the " + src.display + " on this cluster"
	}
	parts := []string{where}
	if src.tenant != "" {
		parts = append(parts, "tenant "+src.tenant)
	}
	if len(src.match) == 0 {
		parts = append(parts, "unfiltered: everything there is read as this cluster's")
	} else {
		words := make([]string, 0, len(src.match))
		for k, v := range alertmanager.DisplayMatch(src.match, src.redactor) {
			words = append(words, k+"="+v)
		}
		sort.Strings(words)
		parts = append(parts, "filtered by "+strings.Join(words, ", "))
	}
	return src.redactor.Text(strings.Join(parts, ", "))
}

// legacyLineage is a legacy read's provenance (Settings S3a §3.4): its
// mode, the source identity it was keyed to, and the credential revision.
func legacyLineage(src amSource) alertmanager.Lineage {
	mode := config.AMDiscovered
	switch {
	case strings.HasPrefix(src.identity, "endpoint|"):
		mode = config.AMDirect
	case strings.HasPrefix(src.identity, "service|"):
		mode = config.AMService
	}
	return alertmanager.Lineage{Mode: mode, Identity: src.identity, Tenant: src.tenant, CredRev: src.rev}
}

// amProxyREST is the connection's guarded service-proxy client with the
// Alertmanager limits, for an installation it hosts (Settings S3a §3.1).
func (c *ClusterConn) amProxyREST() (rest.Interface, error) {
	c.am.mu.Lock()
	defer c.am.mu.Unlock()
	return c.amRESTLocked()
}

// publishedUsability is the connection's usability incarnation as
// published, and whether it is usable now: an incarnation still pending
// publication is not yet in force (Settings S3a §3.4, S2 §12).
func (c *ClusterConn) publishedUsability() (uint64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	inc := c.usableInc
	if c.pending && inc > 0 {
		inc--
	}
	return inc, c.state.Usable() && !c.pending
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
	// bounded by the read's context: a discovery request never outlives the
	// snapshot's deadline or a re-key while am.mu and readMu are held
	rl, err := c.probeDiscovery().ServerResourcesForGroupVersionCtx(ctx, amResourceGVR.GroupVersion().String())
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

// amRoutePrefix is the path the operator serves the instance under. The
// Prometheus Operator (v0.94.1, pkg/alertmanager/statefulset.go) always
// passes --web.route-prefix: spec.routePrefix, else "/". externalUrl is the
// URL Alertmanager advertises, never where it serves.
func amRoutePrefix(am *unstructured.Unstructured) string {
	p, _, _ := unstructured.NestedString(am.Object, "spec", "routePrefix")
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
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
			ref := ep.TargetRef
			if ref == nil || ref.Kind != "Pod" || ref.Name == "" || ref.UID == "" || (ref.Namespace != "" && ref.Namespace != ns) {
				return "an endpoint has no Pod targetRef with a UID in " + ns
			}
			if pods++; pods > amMaxProofEndpoints {
				return fmt.Sprintf("it has more than %d endpoints to prove", amMaxProofEndpoints)
			}
			pod, err := c.typed.CoreV1().Pods(ns).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				return "a Pod behind it could not be read"
			}
			// the endpoint's address belongs to the Pod its targetRef names:
			// a same-name replacement proves nothing about that address
			if pod.UID != ref.UID {
				return "an endpoint names a Pod that has since been replaced"
			}
			owner := metav1.GetControllerOf(pod)
			if owner == nil || owner.Kind != "StatefulSet" {
				return "a Pod behind it is not owned by a StatefulSet"
			}
			if stsUID != "" {
				if string(owner.UID) != stsUID {
					return "its Pods belong to more than one StatefulSet"
				}
				continue // that StatefulSet is already proven
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
	r, _, err := c.ReadAlertsAt(ctx, key)
	return r, err
}

// ReadAlertsAt is ReadAlerts with the time the alerts were acquired, zero
// for the time the read lands: a subscribed member's share keeps its
// bucket read's time (Settings S3a §3.2).
func (c *ClusterConn) ReadAlertsAt(ctx context.Context, key string) (alertmanager.Read, time.Time, error) {
	c.am.mu.Lock()
	subscribed := c.am.cfg != nil && c.am.cfg.Source != "" && c.am.buckets != nil
	c.am.mu.Unlock()
	if subscribed {
		return c.readSubscribed(ctx, key)
	}
	r, err := c.readOwn(ctx, key)
	return r, time.Time{}, err
}

// readSubscribed is a subscribed member's read (S3a §3.2): its key is its
// lineage, and a generation keyed otherwise re-keys - leaving its bucket -
// before the member joins its new one. No fleet-wide slot is held while it
// waits: the bucket's read holds one.
func (c *ClusterConn) readSubscribed(ctx context.Context, key string) (alertmanager.Read, time.Time, error) {
	c.am.readMu.Lock()
	defer c.am.readMu.Unlock()
	b := c.am.buckets
	c.am.mu.Lock()
	id, sub, access, ok := b.memberKey(c.name)
	c.am.identity = id
	if id != key {
		if c.am.rekey != nil {
			c.am.rekey(id)
		}
		c.am.mu.Unlock()
		b.leave(c.ID())
		return alertmanager.Read{Identity: id}, time.Time{}, ErrSourceChanged
	}
	c.am.resolved.Store(true)
	c.am.mu.Unlock()
	if !ok {
		return alertmanager.Read{Identity: id}, time.Time{}, errors.New("this cluster's subscription is not in force; Klyx reads it once it is applied")
	}
	r, at, err := b.read(ctx, c.ID(), sub, key, access)
	if errors.Is(err, errSubscriptionMoved) {
		// re-keyed to the subscription now in force, as at the start of a
		// read (Codex's review of S3a-2, R1)
		c.am.mu.Lock()
		id, _, _, _ := b.memberKey(c.name)
		c.am.identity = id
		if c.am.rekey != nil {
			c.am.rekey(id)
		}
		c.am.mu.Unlock()
		b.leave(c.ID())
		return alertmanager.Read{Identity: id}, time.Time{}, ErrSourceChanged
	}
	return r, at, err
}

// readOwn is the read of the cluster's own block, or discovery (S8a).
func (c *ClusterConn) readOwn(ctx context.Context, key string) (alertmanager.Read, error) {
	c.am.readMu.Lock()
	defer c.am.readMu.Unlock()
	// the fleet-wide slot covers the whole read, discovery's GETs included
	select {
	case amReads <- struct{}{}:
		defer func() { <-amReads }()
	case <-ctx.Done():
		return alertmanager.Read{}, ctx.Err()
	}
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
	c.am.resolved.Store(!errors.Is(err, ErrNoAlertmanager) && !errors.Is(err, ErrAlertsOff))
	c.am.mu.Unlock()
	if err != nil {
		return alertmanager.Read{Identity: src.identity}, err
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
	out := alertmanager.Read{Identity: src.identity, Source: legacySentence(src), Match: alertmanager.DisplayMatch(src.match, src.redactor), Lineage: legacyLineage(src)}
	if len(reduced) > amMaxAlerts {
		reduced, out.Truncated = reduced[:amMaxAlerts], true
	}
	out.Alerts = make([]alertmanager.Item, 0, len(reduced))
	for _, r := range reduced {
		out.Alerts = append(out.Alerts, alertmanager.Item{Subject: alertmanager.SubjectOf(r.Labels), Counted: r.Counted(),
			Key: alertmanager.AlertKey(src.identity, r.Fingerprint), Display: alertmanager.Display(r, src.redactor)})
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
	case e.Off:
		at = amOffIdentity
	case e.Source != "":
		at = amSubscribedIdentity(e.Source) + "|scope=" + e.Scope
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
