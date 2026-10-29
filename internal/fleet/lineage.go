package fleet

import (
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/sources"
)

// lineageNoun is what a kind's reads read, in a refusal's words.
func lineageNoun(kind string) string {
	switch kind {
	case config.EndpointLoki:
		return "logs"
	case config.EndpointMetrics:
		return "metrics"
	}
	return "alerts"
}

// LineageProblem is why data read under lineage l, for cluster, may no
// longer be sent (Settings S3a §4.3, S3b §4): the terms the read was made
// under moved since. "" while they hold. A lineage is checked against its
// own recorded terms, never looked up again; its kind says which terms.
func (r *Registry) LineageProblem(cluster string, l sources.Lineage) string {
	switch l.Mode {
	case "", config.AMOff:
		return ""
	case config.AMSubscribed:
		return r.subscribedProblem(cluster, l)
	}
	c, ok := r.Conn(cluster)
	cl, is := c.(*ClusterConn)
	if !ok || !is {
		return cluster + " is not connected"
	}
	switch l.Kind {
	case config.EndpointLoki:
		return cl.lokiLineageProblem(l)
	case config.EndpointMetrics:
		return cl.metricsLineageProblem(l)
	}
	// a legacy alerts read: the member's own source identity and credential
	if cl.AlertsIdentity() != l.Identity {
		return cluster + "'s alert source changed since its alerts were read"
	}
	if cl.creds.Revisions != nil && cl.creds.Revisions.Revision(cl.name, config.EndpointAlertmanager) != l.CredRev {
		return cluster + "'s Alertmanager credential changed since its alerts were read"
	}
	return ""
}

// subscribedProblem checks a subscribed read's terms, of its kind: the
// installation and the member's subscription at their revisions, the
// host's pinned connection current, usable and at its access generation,
// or a direct installation's access generation and credential revision.
func (r *Registry) subscribedProblem(cluster string, l sources.Lineage) string {
	kind := l.Kind
	if kind == "" {
		kind = config.EndpointAlertmanager
	}
	noun := lineageNoun(kind)
	a := r.AlertSources()
	r.mu.RLock()
	b := r.alerts
	r.mu.RUnlock()
	if a == nil || b == nil {
		return l.Installation + " is not in force"
	}
	src, rev, ok := a.Installation(l.Installation)
	if !ok || rev != l.InstRev {
		return l.Installation + " changed since its " + noun + " were read"
	}
	if sub, ok := a.SubscriptionOf(kind, cluster); !ok || sub.SubRev != l.SubRev || sub.Source.ID != l.Installation {
		return cluster + "'s subscription to " + l.Installation + " changed since its " + noun + " were read"
	}
	if src.Host != "" {
		// the host's pinned connection, still its current one and usable now,
		// at the same access generation: all three (§4.3)
		host, ok := b.host(src.Host)
		if !ok || uint64(host.ID()) != l.HostConn {
			return l.Installation + "'s host " + src.Host + " was reconnected or removed since its " + noun + " were read"
		}
		if _, usable := host.publishedUsability(); !usable {
			return l.Installation + "'s host " + src.Host + " is not connected"
		}
	}
	if b.access(src) != l.Access {
		return l.Installation + "'s access changed since its " + noun + " were read"
	}
	if src.Endpoint != "" && b.creds.Revisions != nil && b.creds.Revisions.Revision(endpointcred.SourceAccount(src.ID), kind) != l.CredRev {
		return l.Installation + "'s credential changed since its " + noun + " were read"
	}
	return ""
}

// metricsLineageProblem checks a read of the cluster's own metrics block
// (S3b §4): its source identity - the endpoint, the Service or the
// discovered one - its tenant and match, and the block's revision, which
// its credential and its configuration move.
func (c *ClusterConn) metricsLineageProblem(l sources.Lineage) string {
	if c.creds.Revisions != nil && c.creds.Revisions.Revision(c.name, config.EndpointMetrics) != l.CredRev {
		return c.name + "'s metrics source or its credential changed since its metrics were read"
	}
	c.metricsMu.Lock()
	cur := c.metricsState.lineage
	c.metricsMu.Unlock()
	if cur.Identity != l.Identity || cur.Tenant != l.Tenant || cur.Match != l.Match {
		return c.name + "'s metrics source changed since its metrics were read"
	}
	return ""
}

// lokiLineageProblem checks a read of the cluster's own Loki block (S3b
// §4): the reader it resolves to now is the same source at the same
// revision.
func (c *ClusterConn) lokiLineageProblem(l sources.Lineage) string {
	r, err := c.Loki()
	if err != nil {
		return c.name + "'s log history source changed since its logs were read"
	}
	if r.lineage.Identity != l.Identity || r.lineage.Tenant != l.Tenant || r.lineage.Match != l.Match || r.rev != l.CredRev {
		return c.name + "'s log history source or its credential changed since its logs were read"
	}
	return ""
}
