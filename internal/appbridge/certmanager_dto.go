package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/certmanager"
)

// CertificateDTO is the JSON projection of a cert-manager Certificate.
type CertificateDTO struct {
	// UID is the object's identity (1c slice 3 §2a): rows key and select by
	// it, so a same-name recreation is a different row.
	UID        string   `json:"uid,omitempty"`
	Namespace  string   `json:"namespace"`
	Name       string   `json:"name"`
	Ready      string   `json:"ready"`
	Reason     string   `json:"reason"`
	Message    string   `json:"message"`
	IssuerKind string   `json:"issuerKind"`
	IssuerName string   `json:"issuerName"`
	SecretName string   `json:"secretName"`
	DNSNames   []string `json:"dnsNames"`
	// NotBeforeUnix is the certificate's start time as a unix timestamp; 0 when
	// the cert has not been issued yet (status.notBefore empty).
	NotBeforeUnix int64 `json:"notBeforeUnix"`
	// NotAfterUnix is the certificate's expiry as a unix timestamp; 0 when the
	// cert has not been issued yet (status.notAfter empty).
	NotAfterUnix int64 `json:"notAfterUnix"`
	// ExpiresInSeconds is now->notAfter, computed server-side for determinism.
	// Negative means already expired. Only meaningful when NotAfterUnix != 0.
	ExpiresInSeconds int64 `json:"expiresInSeconds"`
	// RenewalUnix is when cert-manager will renew (status.renewalTime); 0 unknown.
	// now >= renewalTime is the "in the renewal window" signal the UI flags amber.
	RenewalUnix int64 `json:"renewalUnix"`
	// Stale: the spec changed and the controller has not observed it yet -
	// Ready reflects the previous spec; the row renders as transitioning.
	Stale              bool  `json:"stale"`
	Generation         int64 `json:"generation"`
	ObservedGeneration int64 `json:"observedGeneration"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

func toCertificateDTO(c certmanager.Certificate, now time.Time) CertificateDTO {
	dto := CertificateDTO{
		Rule:       toRuleDTO(certmanager.CertificateRule(c, now)),
		Namespace:  c.Namespace,
		Name:       c.Name,
		Ready:      string(c.Ready),
		Reason:     c.Reason,
		Message:    c.Message,
		IssuerKind: c.IssuerKind,
		IssuerName: c.IssuerName,
		SecretName: c.SecretName,
		DNSNames:   []string{},
	}
	dto.Stale = c.Stale
	dto.Generation = c.Generation
	dto.ObservedGeneration = c.ObservedGeneration
	if c.DNSNames != nil {
		dto.DNSNames = c.DNSNames
	}
	if !c.NotBefore.IsZero() {
		dto.NotBeforeUnix = c.NotBefore.Unix()
	}
	if !c.NotAfter.IsZero() {
		dto.NotAfterUnix = c.NotAfter.Unix()
		dto.ExpiresInSeconds = int64(c.NotAfter.Sub(now).Seconds())
	}
	if !c.RenewalTime.IsZero() {
		dto.RenewalUnix = c.RenewalTime.Unix()
	}
	return dto
}

// IssuerDTO is the JSON projection of an Issuer/ClusterIssuer's Ready state.
type IssuerDTO struct {
	UID        string `json:"uid,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Ready      string `json:"ready"`
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

func toIssuerDTO(i certmanager.Issuer) IssuerDTO {
	return IssuerDTO{
		Rule:      toRuleDTO(certmanager.IssuerRule(i)),
		Kind:      string(i.Kind),
		Namespace: i.Namespace,
		Name:      i.Name,
		Ready:     string(i.Ready),
		Reason:    i.Reason,
		Message:   i.Message,
	}
}

// ChainNodeDTO is one object in a certificate's issuance chain
// (CertificateRequest / Order / Challenge) plus the objects it owns.
type ChainNodeDTO struct {
	Kind      string         `json:"kind"`
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Ready     string         `json:"ready"`
	Reason    string         `json:"reason"`
	Message   string         `json:"message"`
	Children  []ChainNodeDTO `json:"children"`
}

// CertChainDTO is a Certificate's resolved issuance chain: the bound issuer's
// health (the headline), the CertificateRequest -> Order -> Challenge tree, and
// the deepest stuck node (failingLink). Warnings carries soft resolution errors.
type CertChainDTO struct {
	Certificate CertificateDTO `json:"certificate"`
	Issuer      *IssuerDTO     `json:"issuer"`
	Requests    []ChainNodeDTO `json:"requests"`
	FailingLink *ChainNodeDTO  `json:"failingLink"`
	Warnings    []string       `json:"warnings"`
}

func toChainNodeDTO(n certmanager.ChainNode) ChainNodeDTO {
	dto := ChainNodeDTO{
		Kind:      string(n.Kind),
		Namespace: n.Namespace,
		Name:      n.Name,
		Ready:     string(n.Ready),
		Reason:    n.Reason,
		Message:   n.Message,
		Children:  []ChainNodeDTO{},
	}
	for _, ch := range n.Children {
		dto.Children = append(dto.Children, toChainNodeDTO(ch))
	}
	return dto
}

func toChainDTO(c certmanager.Chain, now time.Time) CertChainDTO {
	dto := CertChainDTO{
		Certificate: toCertificateDTO(c.Certificate, now),
		Requests:    []ChainNodeDTO{},
		Warnings:    []string{},
	}
	if c.Warnings != nil {
		dto.Warnings = c.Warnings
	}
	if c.Issuer != nil {
		i := toIssuerDTO(*c.Issuer)
		dto.Issuer = &i
	}
	for _, r := range c.Requests {
		dto.Requests = append(dto.Requests, toChainNodeDTO(r))
	}
	if c.FailingLink != nil {
		fl := toChainNodeDTO(*c.FailingLink)
		dto.FailingLink = &fl
	}
	return dto
}
