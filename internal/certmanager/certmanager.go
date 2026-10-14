// Package certmanager parses cert-manager CRDs (read as unstructured) into
// vocabulary-correct issuance state. No cert-manager Go API dependency: tolerant
// of version drift, the same approach as internal/gitops/flux.
package certmanager

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/kmeta"
)

type Kind string

const (
	CertificateKind        Kind = "Certificate"
	CertificateRequestKind Kind = "CertificateRequest"
	OrderKind              Kind = "Order"
	ChallengeKind          Kind = "Challenge"
	IssuerKind             Kind = "Issuer"
	ClusterIssuerKind      Kind = "ClusterIssuer"
)

type ReadyState string

const (
	Ready   ReadyState = "Ready"
	Issuing ReadyState = "Issuing"
	Failed  ReadyState = "Failed"
	Unknown ReadyState = "Unknown"
)

// Certificate is a cert-manager Certificate's issuance state.
type Certificate struct {
	UID         string // the object's identity: a same-name recreation is another certificate
	Namespace   string
	Name        string
	Ready       ReadyState
	Reason      string
	Message     string
	IssuerKind  string // Issuer | ClusterIssuer
	IssuerName  string
	SecretName  string
	DNSNames    []string
	NotBefore   time.Time
	NotAfter    time.Time
	RenewalTime time.Time
	// Stale reports that the spec changed and the controller has not observed
	// it yet. cert-manager stamps observedGeneration on the Ready condition
	// (not at status top level), so staleness compares metadata.generation
	// against that. While stale, Ready reflects the PREVIOUS spec - the UI
	// must present the row as transitioning, never as confidently green.
	Stale              bool
	Generation         int64
	ObservedGeneration int64
	// ReadySince is the Ready condition's lastTransitionTime and
	// IssuingChanged the Issuing condition's, zero when absent: the times
	// that can date the rule's role (The Line §8.1).
	ReadySince     time.Time
	IssuingChanged time.Time
	// Writes date the latest spec and status writes (managedFields): whether
	// those times still date the role (kmeta.Undisturbed).
	Writes kmeta.Writes
}

// ParseCertificate extracts a Certificate's issuance state from a watched CR.
func ParseCertificate(u *unstructured.Unstructured) Certificate {
	c := Certificate{UID: string(u.GetUID()), Namespace: u.GetNamespace(), Name: u.GetName()}
	c.Ready, c.Reason, c.Message = readyFromConditions(u)
	c.Stale, c.Generation, c.ObservedGeneration = staleness(u)
	c.IssuerName, _, _ = unstructured.NestedString(u.Object, "spec", "issuerRef", "name")
	c.IssuerKind, _, _ = unstructured.NestedString(u.Object, "spec", "issuerRef", "kind")
	if c.IssuerKind == "" {
		c.IssuerKind = string(IssuerKind) // cert-manager defaults an unset issuerRef.kind to Issuer
	}
	c.SecretName, _, _ = unstructured.NestedString(u.Object, "spec", "secretName")
	c.DNSNames, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "dnsNames")
	c.NotBefore = parseTime(u, "status", "notBefore")
	c.NotAfter = parseTime(u, "status", "notAfter")
	c.RenewalTime = parseTime(u, "status", "renewalTime")
	c.ReadySince = conditionSince(u, "Ready")
	c.IssuingChanged = conditionSince(u, "Issuing")
	c.Writes = kmeta.WritesOf(u)
	return c
}

// Issuer is an Issuer or ClusterIssuer's Ready state. Namespace is empty for a
// ClusterIssuer (cluster-scoped).
type Issuer struct {
	UID       string
	Kind      Kind
	Namespace string
	Name      string
	Ready     ReadyState
	Reason    string
	Message   string
	// ReadySince is the Ready condition's lastTransitionTime, zero when absent.
	ReadySince time.Time
}

// ParseIssuer extracts an Issuer/ClusterIssuer's Ready state from a watched CR.
func ParseIssuer(u *unstructured.Unstructured) Issuer {
	i := Issuer{UID: string(u.GetUID()), Kind: Kind(u.GetKind()), Namespace: u.GetNamespace(), Name: u.GetName()}
	i.Ready, i.Reason, i.Message = readyFromConditions(u)
	i.ReadySince = conditionSince(u, "Ready")
	return i
}

// staleness compares metadata.generation with the Ready condition's
// observedGeneration (cert-manager stamps it per condition, not at status top
// level). found=false (no Ready condition, or one without observedGeneration)
// reads as not-stale rather than guessing.
func staleness(u *unstructured.Unstructured) (stale bool, gen, observed int64) {
	gen = u.GetGeneration()
	if gen == 0 {
		return false, gen, 0
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t != "Ready" {
			continue
		}
		switch og := cm["observedGeneration"].(type) {
		case int64:
			return gen > og, gen, og
		case float64:
			return gen > int64(og), gen, int64(og)
		}
	}
	return false, gen, 0
}

// readyFromConditions derives the issuance state, the headline reason, and the
// message from status.conditions. cert-manager carries a Ready condition and,
// for objects mid-issuance, an Issuing condition. Issuing takes precedence (it
// is the "actively working on it" signal, like Flux's Reconciling) but the Ready
// condition's reason is preferred for the headline because it names the failure
// (DoesNotExist, Pending, ...). Shared by Certificate/Issuer/chain-node parsing.
func readyFromConditions(u *unstructured.Unstructured) (ReadyState, string, string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	state := Unknown
	issuing := false
	haveReady := false
	var reason, message string
	var issuingReason, issuingMsg string
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		ctype, _ := cm["type"].(string)
		cstatus, _ := cm["status"].(string)
		switch ctype {
		case "Ready":
			haveReady = true
			switch cstatus {
			case "True":
				state = Ready
			case "False":
				state = Failed
			}
			reason, _ = cm["reason"].(string)
			message, _ = cm["message"].(string)
		case "Issuing":
			if cstatus == "True" {
				issuing = true
			}
			issuingReason, _ = cm["reason"].(string)
			issuingMsg, _ = cm["message"].(string)
		}
	}
	if issuing {
		state = Issuing
		if !haveReady {
			reason, message = issuingReason, issuingMsg
		}
	}
	return state, reason, message
}

// conditionSince is the named condition's lastTransitionTime, zero when the
// condition or its time is absent or invalid. The last match wins, as in
// readyFromConditions.
func conditionSince(u *unstructured.Unstructured, ctype string) time.Time {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	var since time.Time
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t != ctype {
			continue
		}
		since = time.Time{}
		if lt, ok := cm["lastTransitionTime"].(string); ok {
			if ts, err := time.Parse(time.RFC3339, lt); err == nil {
				since = ts
			}
		}
	}
	return since
}

// parseTime reads an RFC3339 timestamp at the given path, zero on missing/invalid.
func parseTime(u *unstructured.Unstructured, fields ...string) time.Time {
	s, _, _ := unstructured.NestedString(u.Object, fields...)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
