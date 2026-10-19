package appbridge

import (
	"github.com/moomora/klyx/internal/alertmanager"
	"reflect"
	"testing"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/hubble"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/netpol"
	"github.com/moomora/klyx/internal/rbac"
	"github.com/moomora/klyx/internal/upgrade"
	"github.com/moomora/klyx/internal/workloads"
)

// mirrorPairs are pure-type -> DTO pairs that are intentional 1:1 field-name
// mirrors (the DTO just adds json tags). A field added to the pure type but not
// the DTO silently drops at the Wails bridge - the documented gotcha that let
// crd.Fact.Pct render as 0 until PR #23. This test is that tripwire: every
// exported field on the pure type must have a same-named exported field on the
// DTO, so `go test ./internal/appbridge/...` fails instead of shipping an empty
// field to the webview.
//
// Scope (honest): it does NOT cover DTOs that rename/transform fields (e.g.
// CertificateDTO's NotBeforeUnix computed from a time.Time) - those are excluded
// deliberately - nor the remap-COPY half of the gotcha (a present-but-unassigned
// DTO field), which is covered by per-feature service tests such as
// TestGetInstanceDetailCarriesMeterPct.
var mirrorPairs = []struct {
	name string
	pure reflect.Type
	dto  reflect.Type
}{
	{"Fact", reflect.TypeOf(crd.Fact{}), reflect.TypeOf(FactDTO{})},
	{"RelatedRef", reflect.TypeOf(crd.RelatedRef{}), reflect.TypeOf(RelatedRefDTO{})},
	{"Condition", reflect.TypeOf(crd.Condition{}), reflect.TypeOf(ConditionDTO{})},
	{"ChainNode", reflect.TypeOf(certmanager.ChainNode{}), reflect.TypeOf(ChainNodeDTO{})},
	{"Issuer", reflect.TypeOf(certmanager.Issuer{}), reflect.TypeOf(IssuerDTO{})},
	{"ReloadTargets", reflect.TypeOf(workloads.ReloadTargets{}), reflect.TypeOf(ReloadTargetsDTO{})},
	{"Owner", reflect.TypeOf(workloads.Owner{}), reflect.TypeOf(OwnerDTO{})},
	{"GitOpsOwnership", reflect.TypeOf(fleet.GitOpsOwnership{}), reflect.TypeOf(GitOpsOwnershipDTO{})},
	{"AdhocResult", reflect.TypeOf(fleet.AdhocResult{}), reflect.TypeOf(AdhocResultDTO{})},
	{"AdhocObject", reflect.TypeOf(fleet.AdhocObject{}), reflect.TypeOf(AdhocObjectDTO{})},
	{"CompareContainer", reflect.TypeOf(fleet.CompareContainer{}), reflect.TypeOf(CompareContainerDTO{})},
	{"WorkloadCompare", reflect.TypeOf(fleet.WorkloadCompareInfo{}), reflect.TypeOf(WorkloadCompareDTO{})},
	{"ReasonRate", reflect.TypeOf(hubble.ReasonRate{}), reflect.TypeOf(ReasonRateDTO{})},
	{"HubbleStats", reflect.TypeOf(hubble.FlowStats{}), reflect.TypeOf(HubbleStatsDTO{})},
	{"AccessVerdict", reflect.TypeOf(rbac.Verdict{}), reflect.TypeOf(AccessVerdictDTO{})},
	{"AccessGrant", reflect.TypeOf(rbac.Grant{}), reflect.TypeOf(AccessGrantDTO{})},
	{"AccessEnumeration", reflect.TypeOf(rbac.Enumeration{}), reflect.TypeOf(AccessWhoCanDTO{})},
	{"NodeUsage", reflect.TypeOf(workloads.NodeUsage{}), reflect.TypeOf(NodeUsageDTO{})},
	{"RolloutRevision", reflect.TypeOf(workloads.RolloutRevision{}), reflect.TypeOf(RolloutRevisionDTO{})},
	{"PromotionUnit", reflect.TypeOf(fleet.PromotionUnit{}), reflect.TypeOf(PromotionUnitDTO{})},
	{"PromotionSource", reflect.TypeOf(fleet.PromotionSource{}), reflect.TypeOf(PromotionSourceDTO{})},
	{"SkewFinding", reflect.TypeOf(upgrade.SkewFinding{}), reflect.TypeOf(SkewFindingDTO{})},
	{"NetpolPolicyRef", reflect.TypeOf(netpol.PolicyRef{}), reflect.TypeOf(NetpolRefDTO{})},
	{"DeprecatedUse", reflect.TypeOf(fleet.DeprecatedUse{}), reflect.TypeOf(DeprecatedUseDTO{})},
	{"NodeSummary", reflect.TypeOf(workloads.NodeSummary{}), reflect.TypeOf(NodeSummaryDTO{})},
	{"HeadroomResult", reflect.TypeOf(workloads.HeadroomResult{}), reflect.TypeOf(HeadroomResultDTO{})},
	{"HeadroomPodRef", reflect.TypeOf(workloads.PodRef{}), reflect.TypeOf(HeadroomPodRefDTO{})},
	{"PDBBlocker", reflect.TypeOf(fleet.PDBBlocker{}), reflect.TypeOf(PDBBlockerDTO{})},
	{"KarpenterPool", reflect.TypeOf(karpenter.NodePool{}), reflect.TypeOf(KarpenterPoolDTO{})},
	{"KarpenterClaim", reflect.TypeOf(karpenter.NodeClaim{}), reflect.TypeOf(KarpenterClaimDTO{})},
	{"ContainerSummary", reflect.TypeOf(workloads.ContainerSummary{}), reflect.TypeOf(ContainerSummaryDTO{})},
	{"ContainerPort", reflect.TypeOf(workloads.ContainerPort{}), reflect.TypeOf(ContainerPortDTO{})},
	// coverage S8a: the straight mirrors among the Alerts lens's DTOs
	{"AlertPair", reflect.TypeOf(alertmanager.Pair{}), reflect.TypeOf(AMPairDTO{})},
	{"AlertMute", reflect.TypeOf(alertmanager.DisplayMute{}), reflect.TypeOf(AMMuteDTO{})},
}

func TestDTOFieldParity(t *testing.T) {
	for _, p := range mirrorPairs {
		dtoFields := make(map[string]bool, p.dto.NumField())
		for i := 0; i < p.dto.NumField(); i++ {
			if f := p.dto.Field(i); f.IsExported() {
				dtoFields[f.Name] = true
			}
		}
		for i := 0; i < p.pure.NumField(); i++ {
			f := p.pure.Field(i)
			if !f.IsExported() {
				continue
			}
			if !dtoFields[f.Name] {
				t.Errorf("%s: pure field %q has no matching field on %s - it silently drops at the Wails bridge; add it to the DTO and its *_service.go remap",
					p.name, f.Name, p.dto.Name())
			}
		}
	}
}
