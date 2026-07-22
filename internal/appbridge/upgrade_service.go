package appbridge

import (
	"context"

	"github.com/moomora/klyx/internal/fleet"
)

// SkewFindingDTO mirrors upgrade.SkewFinding.
type SkewFindingDTO struct {
	Node    string `json:"node"`
	Version string `json:"version"`
	Delta   int    `json:"delta"`
	Level   string `json:"level"`
	Detail  string `json:"detail"`
}

// DeprecatedUseDTO mirrors fleet.DeprecatedUse.
type DeprecatedUseDTO struct {
	Group          string `json:"group"`
	Version        string `json:"version"`
	Resource       string `json:"resource"`
	Kind           string `json:"kind"`
	RemovedInMinor int    `json:"removedInMinor"`
	ReplacedBy     string `json:"replacedBy"`
	Count          int    `json:"count"`
	CountKnown     bool   `json:"countKnown"`
}

// UpgradeReportDTO mirrors fleet.UpgradeReport plus a fold-in error.
type UpgradeReportDTO struct {
	ServerVersion string             `json:"serverVersion"`
	ServerMinor   int                `json:"serverMinor"`
	EOLDate       string             `json:"eolDate"`
	Skew          []SkewFindingDTO   `json:"skew"`
	Deprecated    []DeprecatedUseDTO `json:"deprecated"`
	Error         string             `json:"error,omitempty"`
}

// UpgradeConn is the per-cluster surface the upgrade lens needs.
type UpgradeConn interface {
	UpgradeReadiness(ctx context.Context) (fleet.UpgradeReport, error)
}

// UpgradeService is bound to JS: the upgrade-readiness lens's backend.
// Read-only throughout.
type UpgradeService struct {
	lookup func(string) (UpgradeConn, bool)
}

func NewUpgradeService(lookup func(string) (UpgradeConn, bool)) *UpgradeService {
	return &UpgradeService{lookup: lookup}
}

// GetUpgradeReadiness returns one cluster's upgrade report. Errors fold into
// the DTO; slices are never nil.
func (s *UpgradeService) GetUpgradeReadiness(cluster string) UpgradeReportDTO {
	out := UpgradeReportDTO{Skew: []SkewFindingDTO{}, Deprecated: []DeprecatedUseDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	report, err := conn.UpgradeReadiness(ctx)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.ServerVersion = report.ServerVersion
	out.ServerMinor = report.ServerMinor
	out.EOLDate = report.EOLDate
	for _, sk := range report.Skew {
		out.Skew = append(out.Skew, SkewFindingDTO{Node: sk.Node, Version: sk.Version, Delta: sk.Delta, Level: sk.Level, Detail: sk.Detail})
	}
	for _, d := range report.Deprecated {
		out.Deprecated = append(out.Deprecated, DeprecatedUseDTO{
			Group: d.Group, Version: d.Version, Resource: d.Resource, Kind: d.Kind,
			RemovedInMinor: d.RemovedInMinor, ReplacedBy: d.ReplacedBy,
			Count: d.Count, CountKnown: d.CountKnown,
		})
	}
	return out
}
