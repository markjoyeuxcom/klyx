package line

import "github.com/moomora/klyx/internal/gitops/flux"

// The applied-value types (The Line slice 8d, slice 8 note §5): a unit's
// applied value, typed by what the unit is bound to, never by the revision's
// shape.
const (
	AppliedGit      = "git"      // a Kustomization bound to a GitRepository: the commit
	AppliedArtifact = "artifact" // a Kustomization bound to an OCIRepository, a Bucket or an ExternalArtifact (S1c): the revision
	AppliedChart    = "chart"    // a HelmRelease: the newest deployed entry's chart name@version
)

// AppliedValue types a Flux unit's applied value as the line reads it.
// ok is false when the value is unknown: no applied revision yet, one that
// does not parse, an unsupported source kind, or no deployed Helm entry.
func AppliedValue(r flux.Resource) (kind, value string, ok bool) {
	switch r.Kind {
	case flux.KustomizationKind:
		switch r.SourceKind {
		case string(flux.GitRepositoryKind):
			if rev, ok := ParseGitRevision(r.Revision); ok {
				return AppliedGit, rev.SHA, true
			}
		case string(flux.OCIRepositoryKind), string(flux.BucketKind), string(flux.ExternalArtifactKind):
			if r.Revision != "" {
				return AppliedArtifact, r.Revision, true
			}
		}
	case flux.HelmReleaseKind:
		for _, h := range r.History { // newest first
			if h.Status == "deployed" {
				if h.ChartName != "" && h.ChartVersion != "" {
					return AppliedChart, h.ChartName + "@" + h.ChartVersion, true
				}
				break
			}
		}
	}
	return "", "", false
}
