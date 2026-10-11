// Package externalsecrets parses External Secrets Operator objects
// (ExternalSecret, SecretStore, ClusterSecretStore) into vocabulary-correct
// view models. Pure: unstructured in, models out - no client-go clients. The
// M10/M11 diagnosis-lens pattern applied to ESO.
package externalsecrets

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Kind string

const (
	ExternalSecretKind     Kind = "ExternalSecret"
	SecretStoreKind        Kind = "SecretStore"
	ClusterSecretStoreKind Kind = "ClusterSecretStore"
)

// SyncState is an ExternalSecret/store's aggregate state in ESO vocabulary.
type SyncState string

const (
	Synced  SyncState = "Synced"  // Ready=True (reason SecretSynced / Valid)
	Failed  SyncState = "Failed"  // Ready=False
	Unknown SyncState = "Unknown" // no Ready condition - never guessed
)

// ExternalSecret is one ExternalSecret's sync state.
type ExternalSecret struct {
	UID       string // the object's identity: a same-name recreation is another secret
	Namespace string
	Name      string
	State     SyncState
	Reason    string
	Message   string
	// StoreKind/StoreName bind the secret to its (Cluster)SecretStore - the
	// headline for a stuck secret, exactly as the bound issuer is in M11.
	// Kind defaults to SecretStore when spec.secretStoreRef.kind is unset.
	StoreKind Kind
	StoreName string
	// TargetSecret is the Kubernetes Secret ESO writes (spec.target.name,
	// defaulting to the ExternalSecret's own name when unset - ESO's rule).
	TargetSecret string
	// RefreshInterval is spec.refreshInterval (ESO defaults it to 1h; an
	// explicit "0" disables refresh). Zero here means refresh is disabled.
	RefreshInterval time.Duration
	// RefreshTime is status.refreshTime: the last successful sync. Zero when
	// the secret has never synced.
	RefreshTime time.Time
	// Stale is ESO-native staleness: refresh is enabled and the last
	// successful sync is more than 2x the interval ago - the controller has
	// stopped refreshing even though Ready may still describe the last good
	// sync. Distinct from Failed (which is a reported error).
	Stale bool
}

// Store is a SecretStore or ClusterSecretStore's validation state. Namespace
// is empty for a ClusterSecretStore (cluster-scoped).
type Store struct {
	UID       string
	Kind      Kind
	Namespace string
	Name      string
	State     SyncState
	Reason    string
	Message   string
	// Provider is the single key under spec.provider (azurekv, vault, aws,
	// gcpsm, kubernetes, ...) - display-only.
	Provider string
}

// esoDefaultRefreshInterval mirrors ESO's API default for
// spec.refreshInterval when the field is absent.
const esoDefaultRefreshInterval = time.Hour

// staleGraceFactor: a secret is stale when now-refreshTime exceeds this many
// intervals. x2 tolerates one missed cycle (controller restart, provider
// blip) without crying wolf.
const staleGraceFactor = 2

// ParseExternalSecret extracts an ExternalSecret's sync state. now drives the
// refresh-staleness computation (injected for determinism).
func ParseExternalSecret(u *unstructured.Unstructured, now time.Time) ExternalSecret {
	e := ExternalSecret{UID: string(u.GetUID()), Namespace: u.GetNamespace(), Name: u.GetName()}
	e.State, e.Reason, e.Message = readyFromConditions(u)

	e.StoreName, _, _ = unstructured.NestedString(u.Object, "spec", "secretStoreRef", "name")
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "secretStoreRef", "kind")
	e.StoreKind = Kind(kind)
	if e.StoreKind == "" {
		e.StoreKind = SecretStoreKind // ESO defaults an unset kind to SecretStore
	}

	e.TargetSecret, _, _ = unstructured.NestedString(u.Object, "spec", "target", "name")
	if e.TargetSecret == "" {
		e.TargetSecret = e.Name // ESO's default target name
	}

	e.RefreshInterval = parseRefreshInterval(u)
	e.RefreshTime = parseTime(u, "status", "refreshTime")
	// Stale only applies once a first sync happened: a never-synced secret is
	// already loud via State (Failed/Unknown), and its age is meaningless.
	if e.RefreshInterval > 0 && !e.RefreshTime.IsZero() {
		e.Stale = now.Sub(e.RefreshTime) > staleGraceFactor*e.RefreshInterval
	}
	return e
}

// parseRefreshInterval reads spec.refreshInterval. Absent -> ESO's 1h default;
// explicit "0" (or unparsable) -> 0, meaning refresh disabled / never stale.
func parseRefreshInterval(u *unstructured.Unstructured) time.Duration {
	s, found, _ := unstructured.NestedString(u.Object, "spec", "refreshInterval")
	if !found || s == "" {
		return esoDefaultRefreshInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// ParseStore extracts a SecretStore/ClusterSecretStore's validation state.
func ParseStore(u *unstructured.Unstructured) Store {
	s := Store{UID: string(u.GetUID()), Kind: Kind(u.GetKind()), Namespace: u.GetNamespace(), Name: u.GetName()}
	s.State, s.Reason, s.Message = readyFromConditions(u)
	if prov, ok, _ := unstructured.NestedMap(u.Object, "spec", "provider"); ok {
		for k := range prov {
			if s.Provider == "" || k < s.Provider {
				s.Provider = k // deterministic: smallest key (there is exactly one in practice)
			}
		}
	}
	return s
}

// readyFromConditions derives the aggregate state from the Ready condition.
// ESO conditions are positive-polarity only (Ready True = good); there is no
// transitional condition - refresh staleness covers "in-between" instead.
func readyFromConditions(u *unstructured.Unstructured) (SyncState, string, string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	state := Unknown
	var reason, message string
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t != "Ready" {
			continue
		}
		switch st, _ := cm["status"].(string); st {
		case "True":
			state = Synced
		case "False":
			state = Failed
		}
		reason, _ = cm["reason"].(string)
		message, _ = cm["message"].(string)
	}
	return state, reason, message
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
