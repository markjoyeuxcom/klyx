package line

import (
	"testing"

	"github.com/moomora/klyx/internal/gitops/flux"
)

// The Line slice 8d: applied values typed by what the unit is bound to,
// never by a revision's shape.
func TestAppliedValue(t *testing.T) {
	sha := "a1b9e3c0d4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9"
	for name, c := range map[string]struct {
		r         flux.Resource
		kind, val string
		ok        bool
	}{
		"git":                {flux.Resource{Kind: flux.KustomizationKind, SourceKind: "GitRepository", Revision: "main@sha1:" + sha}, AppliedGit, sha, true},
		"git unparsable":     {flux.Resource{Kind: flux.KustomizationKind, SourceKind: "GitRepository", Revision: "main"}, "", "", false},
		"git none yet":       {flux.Resource{Kind: flux.KustomizationKind, SourceKind: "GitRepository"}, "", "", false},
		"oci hex-shaped":     {flux.Resource{Kind: flux.KustomizationKind, SourceKind: "OCIRepository", Revision: "sha256:" + sha + sha[:24]}, AppliedArtifact, "sha256:" + sha + sha[:24], true},
		"bucket":             {flux.Resource{Kind: flux.KustomizationKind, SourceKind: "Bucket", Revision: "sha256:abc"}, AppliedArtifact, "sha256:abc", true},
		"unsupported source": {flux.Resource{Kind: flux.KustomizationKind, SourceKind: "ExternalArtifact", Revision: "x"}, "", "", false},
		"helm deployed": {flux.Resource{Kind: flux.HelmReleaseKind, History: []flux.HelmHistory{
			{Status: "failed", ChartName: "podinfo", ChartVersion: "6.8.0"}, {Status: "deployed", ChartName: "podinfo", ChartVersion: "6.7.1"}}}, AppliedChart, "podinfo@6.7.1", true},
		"helm none deployed": {flux.Resource{Kind: flux.HelmReleaseKind, History: []flux.HelmHistory{{Status: "failed", ChartName: "podinfo", ChartVersion: "6.8.0"}}}, "", "", false},
	} {
		kind, val, ok := AppliedValue(c.r)
		if kind != c.kind || val != c.val || ok != c.ok {
			t.Errorf("%s: %q %q %v", name, kind, val, ok)
		}
	}
}
