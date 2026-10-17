package appbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/fluxop"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
)

const opNeedle = "OPDTOneedle6Z"

// Coverage S1a §2 and §6: the inspector's detail is read from the
// sanitised object; history digests and unknown metadata never cross; the
// inventory is parsed from ids.
func TestFluxOpDetailCarriesNoPayload(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSet",
		"metadata": map[string]any{"namespace": "team", "name": "rs", "uid": "u"},
		"spec": map[string]any{"resourcesTemplate": opNeedle, "inputs": []any{map[string]any{"k": opNeedle}},
			"dependsOn": []any{map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": "cfg", "readyExpr": "status.ready == true"}}},
		"status": map[string]any{
			"history": []any{map[string]any{"digest": "sha256:" + opNeedle, "lastReconciled": "2026-10-17T10:00:00Z", "lastReconciledStatus": "ReconciliationSucceeded",
				"totalReconciliations": int64(4), "metadata": map[string]any{"inputs": "2", "resources": "5", "other": opNeedle}}},
			"inventory": map[string]any{"entries": []any{map[string]any{"id": "team_web_apps_Deployment", "v": "v1"}, map[string]any{"id": "_team__Namespace", "v": "v1"}}},
		}}}
	d := fluxOpDetail(u)
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), opNeedle) {
		t.Fatalf("crossed: %s", b)
	}
	if len(d.History) != 1 || d.History[0].Metadata["inputs"] != "2" || d.History[0].Metadata["other"] != "" || d.History[0].TotalReconciliations != 4 {
		t.Errorf("history: %+v", d.History)
	}
	if d.InventoryTotal != 2 || d.Inventory[0].Kind != "Deployment" || d.Inventory[0].Group != "apps" || d.Inventory[1].Kind != "Namespace" || d.Inventory[1].Name != "team" {
		t.Errorf("inventory: %+v", d.Inventory)
	}
	if d.ReadyExprs["0"] != "status.ready == true" {
		t.Errorf("readyExprs: %v", d.ReadyExprs)
	}
}

// The rows converters carry the capture's evaluation and the parse, and no
// exported input value (S1a §6).
func TestFluxOpRowsConvert(t *testing.T) {
	u := &unstructured.Unstructured{}
	_ = u.UnmarshalJSON([]byte(`{"apiVersion":"fluxcd.controlplane.io/v1","kind":"ResourceSetInputProvider","metadata":{"name":"p","namespace":"team","uid":"u1","generation":1},
		"spec":{"type":"GitHubBranch","url":"https://github.com/org/repo","schedule":[{"cron":"0 * * * *"}]},
		"status":{"conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded","message":"ok","lastTransitionTime":"2026-10-17T10:00:00Z","observedGeneration":1}],
		"exportedInputs":[{"id":"` + opNeedle + `"}],"nextSchedule":{"cron":"0 * * * *","when":"2026-10-17T11:00:00Z"}}}`))
	v, err := fluxop.Project(fluxop.KindProvider)(u)
	if err != nil {
		t.Fatal(err)
	}
	p := fluxop.ParseProvider(v.(*fluxop.Object))
	rows := healthsrc.RowSet[healthsrc.FluxSetRow]{Rows: []healthsrc.Row[healthsrc.FluxSetRow]{{
		Ref: healthsrc.RowRef{UID: "u1", GVR: "fluxcd.controlplane.io/v1/resourcesetinputproviders", Namespace: "team", Name: "p", Generation: 1},
		Eval: health.NativeEval{Role: health.Unknown, Label: fluxop.LabelSchedule}, Data: healthsrc.FluxSetRow{Provider: &p, Resume: healthsrc.ResumeAwaiting}}}}
	out, err := rowsConverters[healthsrc.TaskFlux+"/resourcesets"](rows, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), opNeedle) {
		t.Fatalf("crossed: %s", b)
	}
	dtos := out.([]FluxSetRowDTO)
	pd := dtos[0].Provider
	if pd == nil || pd.ExportedInputCount != 1 || pd.Resume != "awaiting" || pd.Rule.Label != fluxop.LabelSchedule || pd.UID != "u1" ||
		pd.NextSchedule == nil || pd.NextSchedule.When == nil || len(pd.Schedule) != 1 {
		t.Fatalf("provider DTO: %+v", pd)
	}
}
