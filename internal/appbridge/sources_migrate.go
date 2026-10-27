package appbridge

import (
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3a §7: the migration offer. Today's per-cluster blocks keep
// their behaviour; nothing is migrated on its own. When one direct URL,
// the same origin and path, is on several clusters, Settings offers
// "Make a shared installation": a proposal, which the person reviews and
// edits (the member label above all) and applies as an ordinary plan. It
// says each cluster's scope before and after, names what the person must
// choose, and never copies a secret: the installation's credential is
// stored once, by the person.

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
	Where     string           `json:"where"`
	Clusters  []string         `json:"clusters"`
	Draft     SourcesDraftDTO  `json:"draft"`
	Scopes    []ScopeChangeDTO `json:"scopes"`
	Conflicts []string         `json:"conflicts"`
	Choices   []string         `json:"choices"`
	// Credential says the secret is stored once, by the person; the
	// clusters' old records stay until they are removed
	Credential string `json:"credential,omitempty"`
}

// MigrationOffers are the offers the running fleet's legacy blocks make.
func (s *SourcesService) MigrationOffers() []MigrationOfferDTO {
	out := []MigrationOfferDTO{}
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
		b := cl.Alertmanager
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
		k := origin + u.Path // the origin canonical, the path as written
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
		out = append(out, s.offerFor(g.where, g.endpoint, g.clusters, active, running, taken))
	}
	return out
}

// offerFor is one offer: an installation at the shared endpoint, each
// cluster subscribed with its tenant and match carried over.
func (s *SourcesService) offerFor(where, endpoint string, members []config.ClusterConfig, active []config.SourceConfig, running []config.ClusterConfig, taken map[string]bool) MigrationOfferDTO {
	o := MigrationOfferDTO{Where: where, Scopes: []ScopeChangeDTO{}, Conflicts: []string{}, Choices: []string{}}
	host := where
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() != "" {
		host = strings.SplitN(u.Hostname(), ".", 2)[0]
	}
	src := SourceDraftDTO{ID: freeID("am-"+host, taken), Endpoint: endpoint}
	taken[src.ID] = true

	// what is equal on every member is the installation's; auth and the
	// certificate check that differ are named, with no default winner
	first := members[0].Alertmanager
	sameAuth, sameTLS, sameTenant := true, true, true
	for _, m := range members {
		b := m.Alertmanager
		o.Clusters = append(o.Clusters, m.Name)
		sameAuth = sameAuth && reflect.DeepEqual(b.Auth, first.Auth)
		sameTLS = sameTLS && b.TLSSkipVerify == first.TLSSkipVerify
		sameTenant = sameTenant && b.Tenant == first.Tenant
	}
	if sameAuth && first.Auth != nil {
		src.AuthType, src.AuthUsername = first.Auth.Type, first.Auth.Username
	}
	if !sameAuth {
		o.Conflicts = append(o.Conflicts, "The clusters authenticate differently ("+describeEach(members, func(b *config.EndpointConfig) string {
			if b.Auth == nil {
				return "none"
			}
			return b.Auth.Type
		})+"): choose the installation's auth.")
	}
	if sameTLS {
		src.TLSSkipVerify = first.TLSSkipVerify
	} else {
		o.Conflicts = append(o.Conflicts, "The clusters check the certificate differently ("+describeEach(members, func(b *config.EndpointConfig) string {
			return map[bool]string{true: "not verified", false: "verified"}[b.TLSSkipVerify]
		})+"): choose whether the installation verifies it.")
	}
	if sameTenant {
		src.Tenant = first.Tenant
	}
	var subs []SubscriptionEditDTO
	for _, m := range members {
		b := m.Alertmanager
		sub := &SubscriptionDTO{Source: src.ID, Match: copyTags(b.Match)}
		if !sameTenant {
			sub.Tenant = b.Tenant
		}
		subs = append(subs, SubscriptionEditDTO{Cluster: m.Name, Block: sub})
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Cluster < subs[j].Cluster })
	o.Draft = SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installAdd, Source: src}, Subscriptions: subs}
	if src.AuthType != "" || !sameAuth {
		o.Credential = "Store the installation's credential once, under " + shownInstallation(src.ID) + "; until then its reads fail as \"no credential stored for " + shownInstallation(src.ID) + "\". The clusters' own records stay until you remove them, and none is copied."
	}

	// the result's scopes, before and after: valid, or what to choose
	runSources := append(cloneSourceList(active), sourceOf(src))
	runClusters := append([]config.ClusterConfig(nil), running...)
	for i := range runClusters {
		for _, se := range subs {
			if runClusters[i].Name == se.Cluster {
				runClusters[i].Alertmanager = blockOf(se.Block)
			}
		}
	}
	after, err := config.CheckSources(runSources, runClusters)
	if err != nil {
		o.Choices = append(o.Choices, "As proposed, the subscriptions are not valid: "+config.DisplayError(err)+". Choose a member label for the installation, or a match for each cluster.")
	}
	for _, m := range members {
		b := m.Alertmanager
		sc := ScopeChangeDTO{Cluster: m.Name, Before: legacyScope(b)}
		if err != nil {
			sc.After, sc.Change = "not valid yet", "a choice is needed"
		} else {
			r := after[m.Name]
			sc.After = scopeSentence(r, sourceOf(src))
			sc.Change = scopeChange(b.Match, r)
		}
		o.Scopes = append(o.Scopes, sc)
	}
	return o
}

// legacyScope is a legacy direct block's scope, in words.
func legacyScope(b *config.EndpointConfig) string {
	parts := []string{"its own endpoint"}
	if b.Tenant != "" {
		parts = append(parts, "tenant "+b.Tenant)
	}
	if len(b.Match) == 0 {
		return strings.Join(append(parts, "unfiltered: everything there is read as its own"), ", ")
	}
	words := make([]string, 0, len(b.Match))
	for k, v := range b.Match {
		words = append(words, k+"="+v)
	}
	sort.Strings(words)
	return strings.Join(append(parts, "filtered by "+strings.Join(words, ", ")), ", ")
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
func describeEach(members []config.ClusterConfig, value func(*config.EndpointConfig) string) string {
	parts := make([]string, 0, len(members))
	for _, m := range members {
		parts = append(parts, m.Name+" "+value(m.Alertmanager))
	}
	return strings.Join(parts, ", ")
}
