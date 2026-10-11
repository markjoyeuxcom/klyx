package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/helmcli"
)

// HelmReleaseDTO is the JSON projection of a single Helm release.
type HelmReleaseDTO struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Chart       string `json:"chart"`
	AppVersion  string `json:"appVersion"`
	Status      string `json:"status"`
	Revision    int    `json:"revision"`
	UpdatedUnix int64  `json:"updatedUnix"`
	// Rule is the relocated lens rule at read time (1c addendum §2); Then is
	// the verdict it takes when a pending release crosses Klyx's deadline.
	Rule RuleDTO      `json:"rule"`
	Then *RuleStepDTO `json:"then,omitempty"`
}

func toHelmReleaseDTO(r helmcli.Release, now time.Time) HelmReleaseDTO {
	rule, then := ruleStep(func(t time.Time) health.NativeEval { return helmcli.ReleaseRule(r, t) }, now)
	return HelmReleaseDTO{
		Name:        r.Name,
		Namespace:   r.Namespace,
		Chart:       r.Chart,
		AppVersion:  r.AppVersion,
		Status:      r.Status,
		Revision:    r.Revision,
		UpdatedUnix: r.UpdatedUnix,
		Rule:        rule,
		Then:        then,
	}
}

// HelmReleasesResultDTO is the response from ListHelmReleases.
// Available is false when the helm binary cannot be resolved or the cluster has
// no kubeContext; in that case Message explains why and Releases is nil.
type HelmReleasesResultDTO struct {
	Available bool             `json:"available"`
	Message   string           `json:"message,omitempty"`
	Releases  []HelmReleaseDTO `json:"releases"`
	// Error marks a FAILED read (cluster not connected, helm list error) as
	// distinct from "helm not found": both are Available=false, but only the
	// latter is a definitive absence. The tape must show a failure as
	// unreadable, never as "no helm" (review 2026-10-03 #10).
	Error string `json:"error,omitempty"`
}

// HelmHistoryEntryDTO is one row in a release's rollout history.
type HelmHistoryEntryDTO struct {
	Revision    int    `json:"revision"`
	Status      string `json:"status"`
	Chart       string `json:"chart"`
	AppVersion  string `json:"appVersion"`
	Description string `json:"description"`
	UpdatedUnix int64  `json:"updatedUnix"`
}

// HelmHistoryResultDTO is the response from GetHelmHistory.
type HelmHistoryResultDTO struct {
	History []HelmHistoryEntryDTO `json:"history"`
	Error   string                `json:"error,omitempty"`
}

// HelmValuesResultDTO is the response from GetHelmValues. Values is MASKED YAML:
// secret-shaped leaves are redacted before it leaves the backend, upholding the
// invariant that secret values never leak via helm values. MaskedPaths lists the
// dotted paths that were redacted; the frontend reveals an individual value on
// demand via RevealHelmValue.
type HelmValuesResultDTO struct {
	Values      string   `json:"values"` // masked YAML string; "" means no user values set
	MaskedPaths []string `json:"maskedPaths"`
	Error       string   `json:"error,omitempty"`
}
