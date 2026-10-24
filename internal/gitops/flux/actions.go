package flux

import (
	"encoding/json"
	"time"
)

// ReconcileRequestedAtAnnotation is the annotation Flux watches to trigger an
// out-of-band reconcile (equivalent to `flux reconcile <kind> <name>`).
const ReconcileRequestedAtAnnotation = "reconcile.fluxcd.io/requestedAt"

// ReconcileToken is the requestedAt value a reconcile at now writes: Flux
// answers it in status.lastHandledReconcileAt.
func ReconcileToken(now time.Time) string { return now.Format(time.RFC3339Nano) }

// ReconcilePatch builds a JSON merge patch that stamps the reconcile annotation
// with now (RFC3339Nano). Applying it makes the Flux controller re-reconcile.
func ReconcilePatch(now time.Time) []byte {
	body := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				ReconcileRequestedAtAnnotation: ReconcileToken(now),
			},
		},
	}
	b, _ := json.Marshal(body) // single-key nested maps: deterministic, never errors
	return b
}

// SuspendPatch builds a JSON merge patch toggling spec.suspend.
func SuspendPatch(suspend bool) []byte {
	body := map[string]any{"spec": map[string]any{"suspend": suspend}}
	b, _ := json.Marshal(body)
	return b
}

// ResourceForKind maps a Flux Kind to its plural resource name for GVR
// construction. ok is false for kinds Klyx does not act on.
func ResourceForKind(k Kind) (string, bool) {
	switch k {
	case KustomizationKind:
		return "kustomizations", true
	case HelmReleaseKind:
		return "helmreleases", true
	case ImageRepositoryKind:
		return "imagerepositories", true
	case ImagePolicyKind:
		return "imagepolicies", true
	case ImageUpdateAutomationKind:
		return "imageupdateautomations", true
	case ReceiverKind:
		return "receivers", true
	case ProviderKind:
		return "providers", true
	case AlertKind:
		return "alerts", true
	default:
		return "", false
	}
}
