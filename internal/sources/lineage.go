// Package sources holds what every reader of an observability source
// shares (Settings S3b §3.2): a read's lineage, whatever the kind.
package sources

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Lineage is a read's provenance (Settings S3a §3.4, S3b §3.2): identities
// and counters only, never data, so it can travel with what the read
// informed and be checked before a later send. Every field is fixed at the
// read; it is never looked up again. It was Alertmanager's, which keeps
// every field it carried and leaves Kind and Match empty.
type Lineage struct {
	// Kind is the source's kind: loki or metrics; empty for Alertmanager,
	// whose reads carry what they always did
	Kind string `json:"kind,omitempty"`
	// Mode is how the cluster resolved: subscribed, direct, service or
	// discovered (legacy), or off
	Mode string `json:"mode"`
	// Identity is the source identity the read was keyed to
	Identity string `json:"identity"`
	// the installation and the member's subscription, when subscribed
	Installation string `json:"installation,omitempty"`
	InstRev      uint64 `json:"instRev,omitempty"`
	SubRev       uint64 `json:"subRev,omitempty"`
	// the host, its connection and its access generation, when hosted;
	// Access alone for a direct installation
	Host     string `json:"host,omitempty"`
	HostConn uint64 `json:"hostConn,omitempty"`
	Access   string `json:"access,omitempty"`
	Tenant   string `json:"tenant,omitempty"`
	// Match is the effective match as display (label=value, in label
	// order), when a Loki or metrics read had one
	Match string `json:"match,omitempty"`
	// CredRev is the endpoint credential's revision the read used
	CredRev uint64 `json:"credRev,omitempty"`
}

// Key is the lineage as an opaque, display-safe key: two reads share one
// only when every field does. A view keys what it shows by it (§3.2).
func (l Lineage) Key() string {
	if l == (Lineage{}) {
		return ""
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%q|%q|%q|%q|%d|%d|%q|%d|%q|%q|%q|%d",
		l.Kind, l.Mode, l.Identity, l.Installation, l.InstRev, l.SubRev, l.Host, l.HostConn, l.Access, l.Tenant, l.Match, l.CredRev))
	return hex.EncodeToString(sum[:8])
}
