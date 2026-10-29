package appbridge

import (
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3a §7, S3b §7: the migration offer, for every kind. Today's
// per-cluster blocks keep their behaviour; nothing is migrated on its own.
// When one direct URL, the same origin and path, is on several clusters'
// blocks of a kind, Settings offers "Make a shared installation": a
// proposal, which the person reviews and edits (the member label above
// all) and applies as an ordinary plan. It says each cluster's scope
// before and after, names what the person must choose, and never copies a
// secret: the installation's credential is stored once, by the person,
// and a legacy metrics token is never carried over.

// ScopeChangeDTO is a cluster's scope before and after a proposal:
// broadened, narrowed, the same, or not yet valid.
type ScopeChangeDTO struct {
	Cluster string `json:"cluster"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Change  string `json:"change"`
}

// MigrationOfferDTO is one "Make a shared installation" proposal.
type MigrationOfferDTO struct {
	Kind      string           `json:"kind"`
	Where     string           `json:"where"`
	Clusters  []string         `json:"clusters"`
	Draft     SourcesDraftDTO  `json:"draft"`
	Scopes    []ScopeChangeDTO `json:"scopes"`
	Conflicts []string         `json:"conflicts"`
	// Unresolved are the installation's fields the conflicts leave to the
	// person, auth and tlsSkipVerify: the draft holds no value for them,
	// and the page asks for one before it plans (Codex's review of S3a-4b,
	// the S3a-4c checkpoint: no default winner)
	Unresolved []string `json:"unresolved"`
	Choices    []string `json:"choices"`
	// Credential says the secret is stored once, by the person; the
	// clusters' old records stay until they are removed
	Credential string `json:"credential,omitempty"`
}

// MigrationOffers are the offers the running fleet's legacy blocks of a
// kind make (none is Alertmanager's).
func (s *SourcesService) MigrationOffers(kind string) []MigrationOfferDTO {
	out := []MigrationOfferDTO{}
	kind, err := kindOf(kind)
	if err != nil {
		return out
	}
	running := s.cs.fleetClusters()
	var active []config.SourceConfig
	if a := s.d.Active(); a != nil {
		active = a.Sources()
	}
	taken := map[string]bool{}
	for _, src := range active {
		taken[src.ID] = true
	}
	type group struct {
		where    string
		endpoint string
		clusters []config.ClusterConfig
	}
	byKey := map[string]*group{}
	var order []string
	for _, cl := range running {
		b := cl.Block(kind)
		if b == nil || b.Source != "" || b.Off || b.Endpoint == "" || config.EndpointURLProblem(b.Endpoint) != "" {
			continue
		}
		u, err := url.Parse(strings.TrimRight(b.Endpoint, "/"))
		if err != nil {
			continue
		}
		origin, err := safehttp.CanonicalOrigin(u)
		if err != nil {
			continue
		}
		// the origin canonical, the path as written: escaped, never decoded,
		// so /a%2Fb and /a/b are two (Codex's review of S3a-4b, R2)
		k := origin + u.EscapedPath()
		g := byKey[k]
		if g == nil {
			g = &group{where: k, endpoint: b.Endpoint}
			byKey[k] = g
			order = append(order, k)
		}
		g.clusters = append(g.clusters, cl)
	}
	for _, k := range order {
		g := byKey[k]
		if len(g.clusters) < 2 {
			continue
		}
		out = append(out, s.offerFor(kind, g.where, g.endpoint, g.clusters, active, running, taken))
	}
	return out
}

// offerIDs are each kind's offered installation ids' prefix.
var offerIDs = map[string]string{config.EndpointAlertmanager: "am-", config.EndpointLoki: "logs-", config.EndpointMetrics: "metrics-"}

// authOf is a member's auth as its block reads it: a legacy metrics token
// without an explicit auth is bearer auth (S3b §7), whose shape, never
// the token, an offer carries.
func authOf(kind string, m config.ClusterConfig) *config.EndpointAuth {
	b := m.Block(kind)
	if b.Auth == nil && kind == config.EndpointMetrics && m.Metrics.Token != "" {
		return &config.EndpointAuth{Type: "bearer"}
	}
	return b.Auth
}

// offerFor is one offer: an installation of a kind at the shared
// endpoint, each cluster subscribed with its tenant and match carried
// over.
func (s *SourcesService) offerFor(kind, where, endpoint string, members []config.ClusterConfig, active []config.SourceConfig, running []config.ClusterConfig, taken map[string]bool) MigrationOfferDTO {
	o := MigrationOfferDTO{Kind: kind, Where: where, Scopes: []ScopeChangeDTO{}, Conflicts: []string{}, Unresolved: []string{}, Choices: []string{}}
	host := where
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() != "" {
		host = strings.SplitN(u.Hostname(), ".", 2)[0]
	}
	src := SourceDraftDTO{ID: freeID(offerIDs[kind]+host, taken), Endpoint: endpoint}
	taken[src.ID] = true

	// what is equal on every member is the installation's; auth and the
	// certificate check that differ are named, with no default winner
	first := members[0].Block(kind)
	firstAuth := authOf(kind, members[0])
	sameAuth, sameTLS, sameTenant, tokens := true, true, true, false
	for _, m := range members {
		b := m.Block(kind)
		o.Clusters = append(o.Clusters, m.Name)
		sameAuth = sameAuth && reflect.DeepEqual(authOf(kind, m), firstAuth)
		sameTLS = sameTLS && b.TLSSkipVerify == first.TLSSkipVerify
		sameTenant = sameTenant && b.Tenant == first.Tenant
		tokens = tokens || kind == config.EndpointMetrics && m.Metrics.Token != ""
	}
	if sameAuth && firstAuth != nil {
		src.AuthType, src.AuthUsername = firstAuth.Type, firstAuth.Username
	}
	if !sameAuth {
		o.Unresolved = append(o.Unresolved, "auth")
		o.Conflicts = append(o.Conflicts, "The clusters authenticate differently ("+describeEach(members, func(m config.ClusterConfig) string {
			if a := authOf(kind, m); a != nil {
				return a.Type
			}
			return "none"
		})+"): choose the installation's auth.")
	}
	if sameTLS {
		src.TLSSkipVerify = first.TLSSkipVerify
	} else {
		o.Unresolved = append(o.Unresolved, "tlsSkipVerify")
		o.Conflicts = append(o.Conflicts, "The clusters check the certificate differently ("+describeEach(members, func(m config.ClusterConfig) string {
			return map[bool]string{true: "not verified", false: "verified"}[m.Block(kind).TLSSkipVerify]
		})+"): choose whether the installation verifies it.")
	}
	if sameTenant {
		src.Tenant = first.Tenant
	}
	var subs []SubscriptionEditDTO
	for _, m := range members {
		b := m.Block(kind)
		sub := &SubscriptionDTO{Source: src.ID, Match: copyTags(b.Match)}
		if !sameTenant {
			sub.Tenant = b.Tenant
		}
		subs = append(subs, SubscriptionEditDTO{Cluster: m.Name, Block: sub})
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Cluster < subs[j].Cluster })
	o.Draft = SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installAdd, Source: src}, Subscriptions: subs}
	if kind != config.EndpointAlertmanager {
		o.Draft.Kind = kind
	}
	if src.AuthType != "" || !sameAuth {
		o.Credential = "Store the installation's credential once, under " + shownInstallation(src.ID) + "; until then its reads fail as \"no credential stored for " + shownInstallation(src.ID) + "\". The clusters' own records stay until you remove them, and none is copied."
	}
	if tokens {
		o.Credential += " The clusters' metrics tokens are not carried over: the installation needs its own credential, and a cluster's token leaves fleet.yaml only with its block, when the subscription replaces it."
	}

	// the result's scopes, before and after: valid, or what to choose
	runSources := append(cloneSourceList(active), sourceOf(kind, src))
	runClusters := append([]config.ClusterConfig(nil), running...)
	for i := range runClusters {
		for _, se := range subs {
			if runClusters[i].Name == se.Cluster {
				runClusters[i].SetBlock(kind, blockOf(se.Block))
			}
		}
	}
	_, err := config.CheckSources(runSources, runClusters)
	var after map[string]config.AMResolution
	if err == nil {
		after, err = config.Resolve(kind, runSources, runClusters)
	}
	if err != nil {
		o.Choices = append(o.Choices, "As proposed, the subscriptions are not valid: "+config.DisplayError(err)+". Choose a member label for the installation, or a match for each cluster.")
	}
	for _, m := range members {
		b := m.Block(kind)
		sc := ScopeChangeDTO{Cluster: m.Name, Before: legacySentence(kind, config.AMDirect, b)}
		if err != nil {
			sc.After, sc.Change = "not valid yet", "a choice is needed"
		} else {
			r := after[m.Name]
			sc.After = scopeSentence(kind, r, sourceOf(kind, src), nil)
			sc.Change = scopeChange(b.Match, r)
		}
		o.Scopes = append(o.Scopes, sc)
	}
	return o
}

// scopeChange compares a legacy match with a subscription's effective
// scope: every key kept with its value and more is narrowed; fewer is
// broadened.
func scopeChange(before map[string]string, r config.AMResolution) string {
	if r.ScopeTenant {
		if len(before) == 0 {
			return "the same"
		}
		return "broadened"
	}
	after := r.Match
	contains := func(a, b map[string]string) bool { // a holds every pair of b
		for k, v := range b {
			if a[k] != v {
				return false
			}
		}
		return true
	}
	switch {
	case reflect.DeepEqual(normMatch(before), normMatch(after)):
		return "the same"
	case contains(after, before):
		return "narrowed"
	case contains(before, after):
		return "broadened"
	}
	return "changed"
}

func normMatch(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

// describeEach names each member's value: "dev bearer, stg none".
func describeEach(members []config.ClusterConfig, value func(config.ClusterConfig) string) string {
	parts := make([]string, 0, len(members))
	for _, m := range members {
		parts = append(parts, m.Name+" "+value(m))
	}
	return strings.Join(parts, ", ")
}
