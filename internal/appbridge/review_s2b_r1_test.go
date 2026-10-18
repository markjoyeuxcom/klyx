package appbridge

import (
	"encoding/json"
	"fmt"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/velero"
	"strings"
	"testing"
	"time"
)

func TestReviewS2bFinalRestoreDTOBound(t *testing.T) {
	mapping := map[string]any{}
	var inc, exc []any
	for i := 0; i < 100; i++ {
		n := fmt.Sprintf("%s%02d", strings.Repeat("n", 61), i)
		m := fmt.Sprintf("%s%02d", strings.Repeat("m", 61), i)
		mapping[n] = m
		inc = append(inc, n)
		exc = append(exc, m)
	}
	name := strings.Repeat("b", 253)
	u := restoreU("r1", name, map[string]any{
		"scheduleName":       strings.Repeat("s", 253),
		"includedNamespaces": inc, "excludedNamespaces": exc, "namespaceMapping": mapping,
		"existingResourcePolicy": "update", "itemOperationTimeout": "4h0m0s",
		"resourceModifier": map[string]any{"kind": "ConfigMap", "name": strings.Repeat("m", 253)},
	})
	r := velero.ProjectRestore(&u)
	if r.Size > velero.MaxObject {
		t.Fatalf("projection %d", r.Size)
	}
	b := &velero.Backup{Meta: velero.Meta{Namespace: "velero", Name: name, UID: "11111111-2222-3333-4444-555555555555"}, Phase: "Completed", Start: time.Now(), Completion: time.Now()}
	bk := veleroBackups{state: "live", byName: map[string]healthsrc.VeleroBackupRow{"velero/" + name: {Backup: b}}}
	dto := veleroRestoreDTO(r, bk)
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("projection=%d bridge DTO=%d", r.Size, len(encoded))
	if len(encoded) > velero.MaxObject {
		t.Fatalf("final Restore DTO %d > 4096; bounded only the abbreviated projection (%d)", len(encoded), r.Size)
	}
}
