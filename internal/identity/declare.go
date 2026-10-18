package identity

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

// Declared is what a ServiceAccount declares now, per adapter (S5 §3.1).
type Declared struct {
	Namespace, Name, UID string
	// AWS IRSA.
	RoleARN     *Value `json:"roleArn,omitempty"`
	Audience    *Value `json:"audience,omitempty"` // nil: the default, sts.amazonaws.com
	STSRegional *Value `json:"stsRegional,omitempty"`
	TokenExpiry *Value `json:"tokenExpiry,omitempty"`
	// Azure.
	AzureClient *Value `json:"azureClient,omitempty"`
	AzureTenant *Value `json:"azureTenant,omitempty"`
	AzureExpiry *Value `json:"azureExpiry,omitempty"`
	// AzureExpiryOutOfRange: outside 3600-86400; the webhook rejects such pods.
	AzureExpiryOutOfRange bool `json:"azureExpiryOutOfRange,omitempty"`
	// GKE: the linked Google service account (impersonation).
	GKEServiceAccount *Value `json:"gkeServiceAccount,omitempty"`
	// Automount is the ServiceAccount's automountServiceAccountToken.
	Automount *bool `json:"automount,omitempty"`
}

func annValue(a map[string]string, key string, sh shape) *Value {
	s, ok := a[key]
	if !ok {
		return nil
	}
	if len(s) > MaxValue {
		v := Value{raw: s, Shown: "oversized value (not shown)", Unexpected: true}
		return &v
	}
	v := valueOf(s, sh)
	return &v
}

// DeclaredOf reads a ServiceAccount's current declarations.
func DeclaredOf(sa *corev1.ServiceAccount) Declared {
	a := sa.Annotations
	d := Declared{Namespace: sa.Namespace, Name: sa.Name, UID: string(sa.UID), Automount: sa.AutomountServiceAccountToken,
		RoleARN: annValue(a, AnnRoleARN, shapeARN), Audience: annValue(a, AnnAudience, shapeToken),
		STSRegional: annValue(a, AnnSTSRegional, shapeBool), TokenExpiry: annValue(a, AnnTokenExpiry, shapeInt),
		AzureClient: annValue(a, AnnAzureClient, shapeGUID), AzureTenant: annValue(a, AnnAzureTenant, shapeTenant),
		AzureExpiry: annValue(a, AnnAzureExpiry, shapeInt), GKEServiceAccount: annValue(a, AnnGKEServiceAcct, shapeEmail)}
	if d.AzureExpiry != nil {
		n, err := strconv.Atoi(d.AzureExpiry.raw)
		d.AzureExpiryOutOfRange = err != nil || n < 3600 || n > 86400
	}
	return d
}

// DeclaredAudience is the IRSA audience the ServiceAccount asks for: its
// annotation, else the default.
func (d Declared) DeclaredAudience() string {
	if d.Audience != nil {
		return d.Audience.raw
	}
	return AudienceSTS
}
