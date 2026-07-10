package appbridge

import (
	"reflect"
	"testing"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/crd"
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
