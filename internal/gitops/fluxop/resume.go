package fluxop

import (
	"time"

	"github.com/moomora/klyx/internal/gitops/resume"
)

// The resume tracker is shared (internal/gitops/resume): S1c's
// ArtifactGenerators use the same evidence protocol. These names keep S1a's
// API.
type Resume = resume.Resume

const (
	Current    = resume.Current
	Awaiting   = resume.Awaiting
	Evaluating = resume.Evaluating
	Unproven   = resume.Unproven
)

const (
	MaxBaselines = resume.MaxBaselines
	MaxLost      = resume.MaxLost
)

// Tracker is the shared tracker, observing the operator's projections.
type Tracker struct{ *resume.Tracker }

// NewTracker returns an empty tracker.
func NewTracker() *Tracker { return &Tracker{resume.NewTracker()} }

// Observe classifies one operator object's state, captured at at (S1a §4):
// SkippedDueToSchedule and ReconciliationDisabled are never evidence, and a
// provider's changed lastExportedRevision is.
func (t *Tracker) Observe(at time.Time, o *Object, c Common, exported string) Resume {
	fp, has := o.Fingerprint()
	return t.Tracker.Observe(at, resume.Sample{Kind: c.Kind, UID: c.UID, Disabled: c.Disabled, Fingerprint: fp, HasReady: has,
		NotEvidence: c.Reason == ReasonSkippedSchedule || c.Reason == ReasonDisabled,
		InProgress:  c.Ready == Reconciling || (c.Ready == Unknown && c.Reason == ReasonProgressing),
		Exported:    exported})
}
