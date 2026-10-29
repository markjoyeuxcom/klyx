package fleet

import (
	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
)

// LineageProblem is why alerts read under lineage l, for cluster, may no
// longer be sent (Settings S3a §4.3): the terms the read was made under
// moved since. "" while they hold. A lineage is checked against its own
// recorded terms, never looked up again.
func (r *Registry) LineageProblem(cluster string, l alertmanager.Lineage) string {
	switch l.Mode {
	case "", config.AMOff:
		return ""
	case config.AMSubscribed:
		return r.subscribedProblem(cluster, l)
	}
	// a legacy read: the member's own source identity and credential
	c, ok := r.Conn(cluster)
	cl, is := c.(*ClusterConn)
	if !ok || !is {
		return cluster + " is not connected"
	}
	if cl.AlertsIdentity() != l.Identity {
		return cluster + "'s alert source changed since its alerts were read"
	}
	if cl.creds.Revisions != nil && cl.creds.Revisions.Revision(cl.name, config.EndpointAlertmanager) != l.CredRev {
		return cluster + "'s Alertmanager credential changed since its alerts were read"
	}
	return ""
}

func (r *Registry) subscribedProblem(cluster string, l alertmanager.Lineage) string {
	a := r.AlertSources()
	r.mu.RLock()
	b := r.alerts
	r.mu.RUnlock()
	if a == nil || b == nil {
		return l.Installation + " is not in force"
	}
	src, rev, ok := a.Installation(l.Installation)
	if !ok || rev != l.InstRev {
		return l.Installation + " changed since its alerts were read"
	}
	if sub, ok := a.Subscription(cluster); !ok || sub.SubRev != l.SubRev || sub.Source.ID != l.Installation {
		return cluster + "'s subscription to " + l.Installation + " changed since its alerts were read"
	}
	if src.Host != "" {
		// the host's pinned connection, still its current one and usable now,
		// at the same access generation: all three (§4.3)
		host, ok := b.host(src.Host)
		if !ok || uint64(host.ID()) != l.HostConn {
			return l.Installation + "'s host " + src.Host + " was reconnected or removed since its alerts were read"
		}
		if _, usable := host.publishedUsability(); !usable {
			return l.Installation + "'s host " + src.Host + " is not connected"
		}
	}
	if b.access(src) != l.Access {
		return l.Installation + "'s access changed since its alerts were read"
	}
	if src.Endpoint != "" && b.creds.Revisions != nil && b.creds.Revisions.Revision("source/"+src.ID, config.EndpointAlertmanager) != l.CredRev {
		return l.Installation + "'s credential changed since its alerts were read"
	}
	return ""
}
