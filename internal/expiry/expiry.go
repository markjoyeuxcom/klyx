// Package expiry holds the pure half of the fleet expiry-horizon lens:
// parsing certificate lifetimes out of PEM material. The fleet layer decides
// WHERE to look (TLS secrets, webhook CA bundles, cert-manager statuses);
// nothing here talks to a cluster.
package expiry

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"
)

// LeafInfo is the first certificate of a PEM bundle - the leaf in every sane
// bundle ordering, and for a CA bundle the CA itself.
type LeafInfo struct {
	Subject   string
	Issuer    string
	NotAfter  time.Time
	NotBefore time.Time
}

// ParseLeaf extracts the first CERTIFICATE block of a PEM bundle. Non-PEM or
// certificate-free input errors - the caller skips the item rather than
// inventing an expiry.
func ParseLeaf(pemBytes []byte) (LeafInfo, error) {
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return LeafInfo{}, fmt.Errorf("no CERTIFICATE block in PEM data")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return LeafInfo{}, fmt.Errorf("parse certificate: %w", err)
		}
		return LeafInfo{
			Subject:   cert.Subject.String(),
			Issuer:    cert.Issuer.String(),
			NotAfter:  cert.NotAfter,
			NotBefore: cert.NotBefore,
		}, nil
	}
}

// Item is one expiring thing, wherever it came from.
type Item struct {
	Source    string // "tls-secret" | "webhook-ca" | "cert-manager"
	Namespace string // "" for cluster-scoped sources
	Name      string
	Detail    string // webhook name, secret key, cert-manager condition detail
	Subject   string
	NotAfter  time.Time
}

// DaysLeft is the whole-day countdown (negative = already expired).
func (i Item) DaysLeft(now time.Time) int {
	return int(i.NotAfter.Sub(now).Hours() / 24)
}
