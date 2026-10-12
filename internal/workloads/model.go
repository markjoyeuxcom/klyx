// Package workloads turns Kubernetes workload objects (Deploy/STS/DS) plus their
// pods into a health-ranked, triage-sorted view. Pure of client-go clients: it
// operates on API structs and is fixture-testable.
package workloads

import (
	"time"

	"github.com/moomora/klyx/internal/health"
	"k8s.io/apimachinery/pkg/types"
)

// HealthRank is the triage ordering; lower value sorts nearer the top (worse).
type HealthRank int

const (
	Unhealthy HealthRank = iota // ready==0 (desired>0), or an active hard failure
	Degraded                    // ready<desired, rolling out / benign, no hard failure
	Restarts                    // ready==desired but containers restarted / recovered OOM (info)
	Healthy                     // ready==desired, no restarts; incl. desired==0 "Scaled to 0"
)

// String is the pinned lowercase API value (no title-case, no UI wording).
func (r HealthRank) String() string {
	switch r {
	case Unhealthy:
		return "unhealthy"
	case Degraded:
		return "degraded"
	case Restarts:
		return "restarts"
	default:
		return "healthy"
	}
}

type Owner struct {
	Kind, Namespace, Name string // "Kustomization" / "HelmRelease"
}

type Pod struct {
	Name       string
	UID        types.UID // object identity: a verb on it writes only this pod
	Ready      bool
	Restarts   int
	Reason     string // worst container/pod reason, "" if running clean
	Node       string
	AgeSeconds int       // age at the assembly's now
	Created    time.Time // creation time: the anchor a live view ticks the age from
}

type Workload struct {
	Kind, Namespace, Name              string
	UID                                types.UID // object identity: a same-name recreation is a different workload
	Desired, Ready, Available, Updated int
	Restarts                           int
	Reason                             string // single human-facing status string
	Rank                               HealthRank
	GitOps                             *Owner
	// Paused: Deployment spec.paused - an INTENDED operator state (rollouts
	// held), not an error; mirrors the Suspended treatment on the GitOps side.
	// Always false for StatefulSet/DaemonSet (the API has no paused there).
	Paused    bool
	Pods      []Pod
	Resources WorkloadResources
	// Generation vs ObservedGeneration: when spec changed but the controller
	// has not observed it yet, status (and Rank) reflects the PREVIOUS spec.
	// The UI renders such rows as transitioning ("dim = not yet realized"),
	// the same staleness language as Flux resources and Gateways.
	Generation         int64
	ObservedGeneration int64
	// Reloader declares the ConfigMaps/Secrets this workload reloads on via
	// Stakater Reloader annotations; nil when it carries none. Forward intent
	// only - it does not prove the Reloader operator is installed.
	Reloader *ReloadTargets
	// Health is the shared verdict (internal/health), computed from pods
	// attributed by owner reference. Rank/Reason are legacy and stay only
	// until every consumer has migrated.
	Health health.Verdict
}
