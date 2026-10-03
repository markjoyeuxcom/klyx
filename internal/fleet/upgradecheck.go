package fleet

import (
	"context"
	"fmt"

	"github.com/moomora/klyx/internal/upgrade"
)

// DeprecatedUse is one deprecated API the cluster still SERVES, with the
// number of objects readable through it. Honesty note carried into the UI:
// the API server converts on read, so Count is "objects reachable via this
// API", not "objects created with it" - the operational meaning is "clients
// and manifests still using this version break at the removal minor".
type DeprecatedUse struct {
	Group          string
	Version        string
	Resource       string
	Kind           string
	RemovedInMinor int
	ReplacedBy     string
	Count          int
	CountKnown     bool // false when the count list failed (RBAC etc.)
	CountCapped    bool // the count hit the list cap: Count is a lower bound ("500+")
}

// UpgradeReport is one cluster's upgrade-readiness snapshot.
type UpgradeReport struct {
	ServerVersion string
	ServerMinor   int
	EOLDate       string // "2026-02-28" upstream end-of-support, "" when unknown
	Skew          []upgrade.SkewFinding
	Deprecated    []DeprecatedUse // only the SERVED deprecated APIs; empty = clean
	// Warnings name inputs the report could not read (e.g. the node list for
	// kubelet skew): an absent section must never read as "clean".
	Warnings []string
}

// UpgradeReadiness answers "can I upgrade this cluster": control-plane
// version + upstream EOL runway, kubelet skew findings, and which
// deprecated/removed APIs the cluster still serves (with reachable-object
// counts). Read-only, on-demand; discovery goes through the TTL cache.
func (c *ClusterConn) UpgradeReadiness(ctx context.Context) (UpgradeReport, error) {
	vi, err := c.typed.Discovery().ServerVersion()
	if err != nil || vi == nil {
		return UpgradeReport{}, fmt.Errorf("server version: %w", err)
	}
	report := UpgradeReport{ServerVersion: vi.GitVersion}
	if minor, ok := upgrade.ParseMinor(vi.GitVersion); ok {
		report.ServerMinor = minor
		if eol, ok := upgrade.EOLDate(minor); ok {
			report.EOLDate = eol.Format("2006-01-02")
		}
	}

	nodes, err := c.ListNodes(ctx)
	if err != nil {
		// No skew findings is not "no skew": say the input was missing.
		report.Warnings = append(report.Warnings, "kubelet skew unknown - the node list could not be read: "+err.Error())
	} else {
		vers := make(map[string]string, len(nodes))
		for _, n := range nodes {
			vers[n.Name] = n.Version
		}
		report.Skew = upgrade.SkewFindings(vi.GitVersion, vers)
	}

	disc := c.discovery()
	served := map[string]bool{}
	if groups, gerr := disc.ServerGroups(); gerr == nil && groups != nil {
		for _, g := range groups.Groups {
			for _, v := range g.Versions {
				served[v.GroupVersion] = true
			}
		}
	} else if gerr != nil {
		return UpgradeReport{}, fmt.Errorf("discovery: %w", gerr)
	}

	for _, api := range upgrade.Table {
		if !served[api.Group+"/"+api.Version] {
			continue
		}
		use := DeprecatedUse{
			Group: api.Group, Version: api.Version, Resource: api.Resource, Kind: api.Kind,
			RemovedInMinor: api.RemovedInMinor, ReplacedBy: api.ReplacedBy,
		}
		if c.meta != nil {
			if n, capped, cerr := c.CountResource(ctx, api.Group, api.Version, api.Resource); cerr == nil {
				use.Count, use.CountKnown, use.CountCapped = n, true, capped
			}
		}
		report.Deprecated = append(report.Deprecated, use)
	}
	return report, nil
}
