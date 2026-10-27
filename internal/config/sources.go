package config

import (
	"fmt"
	"regexp"
	"sort"
)

// Settings S3a §2: observability installations and the clusters'
// subscriptions to them. An installation is what can be reached, written
// once under sources; a subscription is a cluster's use of one for a kind,
// implied for every cluster it serves and written under the cluster when it
// differs. S3a builds Alertmanager; Loki and metrics follow in S3b.

// SourceKindAlertmanager is the only installation kind S3a reads.
const SourceKindAlertmanager = "alertmanager"

// ScopeTenant is a subscription's scope: everything in its tenant is the
// member's (S3a §2.3).
const ScopeTenant = "tenant"

// SourceConfig is one installation (S3a §2.1).
type SourceConfig struct {
	ID   string `yaml:"id"`
	Kind string `yaml:"kind"`
	Name string `yaml:"name"` // display only
	// Host is the fleet cluster whose API server reaches a serviceRef or a
	// discovered installation; an endpoint has none
	Host          string             `yaml:"host"`
	ServiceRef    *MetricsServiceRef `yaml:"serviceRef"`
	Discover      bool               `yaml:"discover"`
	Endpoint      string             `yaml:"endpoint"`
	TLSSkipVerify bool               `yaml:"tlsSkipVerify"`
	Auth          *EndpointAuth      `yaml:"auth"` // endpoint only: the secret is in the keychain as source/<id>
	Tenant        string             `yaml:"tenant"`
	// MemberLabel makes a subscription's default scope: this label equal
	// to the member's name
	MemberLabel string        `yaml:"memberLabel"`
	Serves      []ServeConfig `yaml:"serves"`
}

// ServeConfig is one entry of serves: a group, or a cluster by name.
type ServeConfig struct {
	Group   string `yaml:"group"`
	Cluster string `yaml:"cluster"`
}

// sourceID is an installation id: a DNS label, since the keychain account
// and the lineage name it.
var sourceID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validateSources checks the sources block alone and against the fleet's
// cluster names (S3a §2.1, §2.2): every refusal names its place.
func (c *Config) validateSources() error {
	clusters := map[string]bool{}
	for _, cl := range c.Clusters {
		clusters[cl.Name] = true
	}
	ids := map[string]bool{}
	for i, s := range c.Sources {
		place := fmt.Sprintf("sources[%d]", i)
		if !sourceID.MatchString(s.ID) {
			return fmt.Errorf("%s: id must be a DNS label (lowercase letters, digits and -), up to 63 characters", place)
		}
		if ids[s.ID] {
			return fmt.Errorf("duplicate source id %s", describeValue(s.ID))
		}
		ids[s.ID] = true
		// named by its id when that is short enough to show, else by its
		// index: a long id may be a pasted value (Codex's re-review of
		// S3a-1, RR2)
		if len([]rune(s.ID)) < 25 {
			place = "sources." + s.ID
		}
		switch s.Kind {
		case SourceKindAlertmanager:
		case EndpointLoki, EndpointMetrics:
			return fmt.Errorf("%s: kind %s is not built yet: this version reads Alertmanager installations only", place, s.Kind)
		case "":
			return fmt.Errorf("%s: kind is required: alertmanager", place)
		default:
			return fmt.Errorf("%s: kind must be alertmanager", place)
		}
		if hasControl(s.Name) {
			return fmt.Errorf("%s.name must not contain control characters", place)
		}
		locators := 0
		for _, set := range []bool{s.ServiceRef != nil, s.Discover, s.Endpoint != ""} {
			if set {
				locators++
			}
		}
		if locators != 1 {
			return fmt.Errorf("%s: give exactly one of serviceRef, discover: true or endpoint", place)
		}
		switch {
		case s.Endpoint != "" && s.Host != "":
			return fmt.Errorf("%s: an endpoint is reached directly, so host is not allowed beside it", place)
		case s.Endpoint == "" && s.Host == "":
			return fmt.Errorf("%s: a serviceRef or discover: true is reached through a cluster's API server, so host is required", place)
		case s.Host != "" && !clusters[s.Host]:
			return fmt.Errorf("%s.host: %s is not a cluster of the fleet", place, describeValue(s.Host))
		case s.Endpoint == "" && (s.Auth != nil || s.TLSSkipVerify):
			return fmt.Errorf("%s: auth and tlsSkipVerify are for an endpoint only; a host's API server authenticates the rest", place)
		}
		if p := EndpointURLProblem(s.Endpoint); p != "" {
			return fmt.Errorf("%s.endpoint: %s", place, p)
		}
		if err := validateEndpoint(place, EndpointConfig{ServiceRef: s.ServiceRef, Auth: s.Auth, Tenant: s.Tenant}, false); err != nil {
			return err
		}
		if s.MemberLabel != "" && !labelName.MatchString(s.MemberLabel) {
			return fmt.Errorf("%s.memberLabel: %s is not a label name", place, describeKey(s.MemberLabel))
		}
		for j, sv := range s.Serves {
			if (sv.Group == "") == (sv.Cluster == "") {
				return fmt.Errorf("%s.serves[%d]: give exactly one of group or cluster", place, j)
			}
		}
	}
	return nil
}

// validateSubscription checks a cluster's own Alertmanager block for the
// subscription fields (S3a §2.1), and refuses them on Loki until S3b. Keys
// are judged as written, never as decoded: a key written empty, or false,
// is refused rather than read as left out, so a malformed subscription
// never falls through to discovery (Codex's review of S3a-1, R1).
func validateSubscription(cluster string, kind string, e *EndpointConfig) error {
	if e == nil {
		return nil
	}
	has := func(key string, set bool) bool { return set || e.wrote(key) }
	if kind == EndpointLoki {
		for _, k := range []struct {
			key string
			set bool
		}{{"source", e.Source != ""}, {"scope", e.Scope != ""}, {"off", e.Off}} {
			if has(k.key, k.set) {
				return fmt.Errorf("cluster %s: loki.%s is not built yet: this version subscribes Alertmanager only", describeValue(cluster), k.key)
			}
		}
		return nil
	}
	source := has("source", e.Source != "")
	beside := []struct {
		key string
		set bool
	}{{"source", e.Source != ""}, {"scope", e.Scope != ""}, {"endpoint", e.Endpoint != ""}, {"serviceRef", e.ServiceRef != nil}, {"auth", e.Auth != nil},
		{"tlsSkipVerify", e.TLSSkipVerify}, {"tenant", e.Tenant != ""}, {"match", len(e.Match) > 0}}
	besideOff, locators := false, false
	for _, k := range beside {
		if has(k.key, k.set) {
			besideOff = true
			switch k.key {
			case "endpoint", "serviceRef", "auth", "tlsSkipVerify":
				locators = true
			}
		}
	}
	switch {
	case e.wrote("off") && !e.Off:
		return fmt.Errorf("cluster %s: alertmanager.off is true, or left out", describeValue(cluster))
	case e.Off && besideOff:
		return fmt.Errorf("cluster %s: alertmanager.off allows nothing beside it", describeValue(cluster))
	case source && e.Source == "":
		return fmt.Errorf("cluster %s: alertmanager.source names an installation in sources: it cannot be empty", describeValue(cluster))
	case source && locators:
		return fmt.Errorf("cluster %s: alertmanager.source reaches its installation, so endpoint, serviceRef, auth and tlsSkipVerify are not allowed beside it", describeValue(cluster))
	case has("scope", e.Scope != "") && !source:
		return fmt.Errorf("cluster %s: alertmanager.scope is allowed only with source", describeValue(cluster))
	case has("scope", e.Scope != "") && e.Scope != ScopeTenant:
		return fmt.Errorf("cluster %s: alertmanager.scope must be tenant", describeValue(cluster))
	case e.Scope == ScopeTenant && has("match", len(e.Match) > 0):
		return fmt.Errorf("cluster %s: alertmanager.scope: tenant takes everything in the tenant, so a match is not allowed beside it", describeValue(cluster))
	}
	return nil
}

// describeValue names a value the file holds in a diagnostic, as
// describeKey names a key: quoted, or by its length alone at 25
// characters or more, which may be a pasted credential (slice 0; Codex's
// review of S3a-1, R2).
func describeValue(v string) string {
	if n := len([]rune(v)); n >= 25 {
		return fmt.Sprintf("a value of %d characters", n)
	}
	return fmt.Sprintf("%q", v)
}

// Alertmanager resolution modes (S3a §2.2).
const (
	AMSubscribed = "subscribed" // through an installation
	AMOff        = "off"        // off in fleet.yaml
	AMDirect     = "direct"     // legacy: its own endpoint
	AMService    = "service"    // legacy: its own serviceRef
	AMDiscovered = "discovered" // legacy: discovery in the cluster
)

// How a cluster's resolution was reached (S3a §2.2).
const (
	ViaOwnBlock  = "its own block"
	ViaByName    = "by name"
	ViaByGroup   = "by group"
	ViaDiscovery = "discovery"
)

// AMResolution is one cluster's Alertmanager as its configuration
// resolves it (S3a §2.2, §2.3).
type AMResolution struct {
	Mode string
	Via  string
	// Source is the installation, when subscribed; Host is its host, ""
	// for an endpoint
	Source string
	Host   string
	// Tenant and Match are the effective scope, when subscribed; Match is
	// empty for scope: tenant
	Tenant      string
	Match       map[string]string
	ScopeTenant bool
	// Shadowed are installations that would have served the cluster, but
	// its own block wins (S3a §2.2): they create no reader for it
	Shadowed []string
}

// ResolveAlertmanager resolves every cluster's Alertmanager (S3a §2.2):
// its own block, an installation serving it by name, one serving its
// group, or discovery in itself; then each subscription's effective scope,
// and that subscriptions sharing a tenant are provably disjoint (§2.3).
// Every refusal names what it names. The configuration's sources must
// already be valid (validateSources).
func ResolveAlertmanager(sources []SourceConfig, clusters []ClusterConfig) (map[string]AMResolution, error) {
	byID := map[string]SourceConfig{}
	byName := map[string][]string{}
	byGroup := map[string][]string{}
	for _, s := range sources {
		byID[s.ID] = s
		for _, sv := range s.Serves {
			if sv.Cluster != "" {
				byName[sv.Cluster] = appendOnce(byName[sv.Cluster], s.ID)
			} else {
				byGroup[sv.Group] = appendOnce(byGroup[sv.Group], s.ID)
			}
		}
	}
	out := make(map[string]AMResolution, len(clusters))
	for _, cl := range clusters {
		named, grouped := byName[cl.Name], []string(nil)
		if cl.Group != "" {
			grouped = byGroup[cl.Group]
		}
		switch {
		case len(named) > 1:
			return nil, fmt.Errorf("cluster %s is served by name by both %s and %s: one installation serves a cluster at each step", describeValue(cl.Name), describeValue(named[0]), describeValue(named[1]))
		case len(grouped) > 1:
			return nil, fmt.Errorf("cluster %s's group %s is served by both %s and %s: one installation serves a group", describeValue(cl.Name), describeValue(cl.Group), describeValue(grouped[0]), describeValue(grouped[1]))
		}
		implied, via := "", ""
		if len(named) == 1 {
			implied, via = named[0], ViaByName
		} else if len(grouped) == 1 {
			implied, via = grouped[0], ViaByGroup
		}
		r := AMResolution{}
		own := cl.Alertmanager
		switch {
		case own == nil && implied == "":
			r = AMResolution{Mode: AMDiscovered, Via: ViaDiscovery}
		case own == nil:
			r = AMResolution{Mode: AMSubscribed, Via: via, Source: implied}
		case own.Off:
			r = AMResolution{Mode: AMOff, Via: ViaOwnBlock}
		case own.Source != "":
			s, ok := byID[own.Source]
			if !ok {
				return nil, fmt.Errorf("cluster %s: alertmanager.source %s names no installation in sources", describeValue(cl.Name), describeValue(own.Source))
			}
			if s.Kind != SourceKindAlertmanager {
				return nil, fmt.Errorf("cluster %s: alertmanager.source %s is not an Alertmanager installation", describeValue(cl.Name), describeValue(own.Source))
			}
			r = AMResolution{Mode: AMSubscribed, Via: ViaOwnBlock, Source: own.Source}
		case own.Endpoint != "":
			r = AMResolution{Mode: AMDirect, Via: ViaOwnBlock}
		case own.ServiceRef != nil:
			r = AMResolution{Mode: AMService, Via: ViaOwnBlock}
		default:
			r = AMResolution{Mode: AMDiscovered, Via: ViaOwnBlock}
		}
		if own != nil && implied != "" && implied != r.Source {
			r.Shadowed = []string{implied}
		}
		if r.Mode == AMSubscribed {
			s := byID[r.Source]
			r.Host = s.Host
			if err := scopeOf(&r, s, cl.Name, own); err != nil {
				return nil, err
			}
		}
		out[cl.Name] = r
	}
	if err := disjoint(byID, clusters, out); err != nil {
		return nil, err
	}
	return out, nil
}

// scopeOf fills a subscription's effective tenant and match (S3a §2.3):
// the member label equal to the member's name, extended by the block's
// match, or everything in the tenant. A subscription with neither is
// refused: nothing reads "everything there" by default.
func scopeOf(r *AMResolution, s SourceConfig, cluster string, own *EndpointConfig) error {
	r.Tenant = s.Tenant
	if own != nil && own.Tenant != "" {
		r.Tenant = own.Tenant
	}
	if own != nil && own.Scope == ScopeTenant {
		r.ScopeTenant = true
		return nil
	}
	m := map[string]string{}
	if s.MemberLabel != "" {
		m[s.MemberLabel] = cluster
	}
	if own != nil {
		for k, v := range own.Match {
			m[k] = v
		}
	}
	if len(m) == 0 {
		return fmt.Errorf("cluster %s: the installation %s has no memberLabel, so its subscription needs a scope: give the cluster's alertmanager block that source with a match, or scope: tenant", describeValue(cluster), describeValue(s.ID))
	}
	r.Match = m
	return nil
}

// disjoint refuses subscriptions of one installation that share an
// effective tenant without provably disjoint scopes (S3a §2.3): each must
// carry the member label, with distinct values, and scope: tenant shares
// its tenant with none.
func disjoint(byID map[string]SourceConfig, clusters []ClusterConfig, res map[string]AMResolution) error {
	type key struct{ source, tenant string }
	members := map[key][]string{}
	var order []key
	for _, cl := range clusters {
		r := res[cl.Name]
		if r.Mode != AMSubscribed {
			continue
		}
		k := key{r.Source, r.Tenant}
		if _, seen := members[k]; !seen {
			order = append(order, k)
		}
		members[k] = append(members[k], cl.Name)
	}
	for _, k := range order {
		ms := members[k]
		if len(ms) < 2 {
			continue
		}
		tenant := "the default tenant"
		if k.tenant != "" {
			tenant = "tenant " + describeValue(k.tenant)
		}
		label := byID[k.source].MemberLabel
		values := map[string]string{}
		for _, m := range ms {
			r := res[m]
			if r.ScopeTenant {
				other := ms[0]
				if other == m {
					other = ms[1]
				}
				return fmt.Errorf("clusters %s and %s share %s of %s, so neither can take scope: tenant", describeValue(m), describeValue(other), tenant, describeValue(k.source))
			}
			if label == "" {
				return fmt.Errorf("clusters %s and %s share %s of %s, which has no memberLabel: give each its own tenant, or the installation a memberLabel", describeValue(ms[0]), describeValue(ms[1]), tenant, describeValue(k.source))
			}
			v := r.Match[label]
			if prev, dup := values[v]; dup {
				return fmt.Errorf("clusters %s and %s share %s of %s, both with %s set to %s, so their alerts cannot be told apart", describeValue(prev), describeValue(m), tenant, describeValue(k.source), describeKey(label), describeValue(v))
			}
			values[v] = m
		}
	}
	return nil
}

func appendOnce(list []string, id string) []string {
	for _, x := range list {
		if x == id {
			return list
		}
	}
	return append(list, id)
}

// ConnectionAlertmanager is the Alertmanager block a cluster's connection
// uses (S3a §2.2): its own block, or a subscription an installation of
// sources implies for it, by name then by group; nil for discovery. It
// never falls through to discovery when an installation serves the
// cluster.
func ConnectionAlertmanager(sources []SourceConfig, cc ClusterConfig) *EndpointConfig {
	if cc.Alertmanager != nil {
		return cc.Alertmanager
	}
	var grouped string
	for _, s := range sources {
		for _, sv := range s.Serves {
			switch {
			case sv.Cluster != "" && sv.Cluster == cc.Name:
				return &EndpointConfig{Source: s.ID}
			case sv.Group != "" && sv.Group == cc.Group && grouped == "":
				grouped = s.ID
			}
		}
	}
	if grouped != "" {
		return &EndpointConfig{Source: grouped}
	}
	return nil
}

// HostedBy are the installations naming cluster as their host, sorted.
func HostedBy(sources []SourceConfig, cluster string) []string {
	var out []string
	for _, s := range sources {
		if s.Host == cluster {
			out = append(out, s.ID)
		}
	}
	sort.Strings(out)
	return out
}

// sourceWarnings are the sources block's non-fatal problems: a serves
// entry naming a cluster or group the fleet does not have yet.
func (c *Config) sourceWarnings() []string {
	names, groups := map[string]bool{}, map[string]bool{}
	for _, cl := range c.Clusters {
		names[cl.Name] = true
		if cl.Group != "" {
			groups[cl.Group] = true
		}
	}
	var w []string
	for _, s := range c.Sources {
		for _, sv := range s.Serves {
			switch {
			case sv.Cluster != "" && !names[sv.Cluster]:
				w = append(w, fmt.Sprintf("source %s serves cluster %s, which is not in fleet.yaml", describeValue(s.ID), describeValue(sv.Cluster)))
			case sv.Group != "" && !groups[sv.Group]:
				w = append(w, fmt.Sprintf("source %s serves group %s, which no cluster has", describeValue(s.ID), describeValue(sv.Group)))
			}
		}
	}
	return w
}
