package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/certmanager"
)

// CertificateDTO is the JSON projection of a cert-manager Certificate.
type CertificateDTO struct {
	Namespace  string   `json:"namespace"`
	Name       string   `json:"name"`
	Ready      string   `json:"ready"`
	Reason     string   `json:"reason"`
	Message    string   `json:"message"`
	IssuerKind string   `json:"issuerKind"`
	IssuerName string   `json:"issuerName"`
	SecretName string   `json:"secretName"`
	DNSNames   []string `json:"dnsNames"`
	// NotAfterUnix is the certificate's expiry as a unix timestamp; 0 when the
	// cert has not been issued yet (status.notAfter empty).
	NotAfterUnix int64 `json:"notAfterUnix"`
	// ExpiresInSeconds is now->notAfter, computed server-side for determinism.
	// Negative means already expired. Only meaningful when NotAfterUnix != 0.
	ExpiresInSeconds int64 `json:"expiresInSeconds"`
	// RenewalUnix is when cert-manager will renew (status.renewalTime); 0 unknown.
	// now >= renewalTime is the "in the renewal window" signal the UI flags amber.
	RenewalUnix int64 `json:"renewalUnix"`
}

func toCertificateDTO(c certmanager.Certificate, now time.Time) CertificateDTO {
	dto := CertificateDTO{
		Namespace:  c.Namespace,
		Name:       c.Name,
		Ready:      string(c.Ready),
		Reason:     c.Reason,
		Message:    c.Message,
		IssuerKind: c.IssuerKind,
		IssuerName: c.IssuerName,
		SecretName: c.SecretName,
		DNSNames:   c.DNSNames,
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
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ready     string `json:"ready"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
}

func toIssuerDTO(i certmanager.Issuer) IssuerDTO {
	return IssuerDTO{
		Kind:      string(i.Kind),
		Namespace: i.Namespace,
		Name:      i.Name,
		Ready:     string(i.Ready),
		Reason:    i.Reason,
		Message:   i.Message,
	}
}
