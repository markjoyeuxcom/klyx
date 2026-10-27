package appbridge

// Codex's review of S2c (2026-10-27): its backend probes, R5 and R6, as
// regression tests.

import (
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/config"
)

// R6: the contexts offered are those of the kubeconfig the file's entry
// names, not the one the cluster runs with.
func TestTheEditorsContextsComeFromTheFileEntry(t *testing.T) {
	r := newFleetRig(t, false)
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    kubeconfig: /tmp/other-kubeconfig\n")
	got := ""
	r.cs.kubeContexts = func(path string) ([]string, error) { got = path; return []string{"context-from-that-file"}, nil }
	e := r.cs.GetClusterEntry("dev")
	if !strings.Contains(e.Draft.Kubeconfig, "other-kubeconfig") || got != e.Draft.Kubeconfig || len(e.Contexts) != 1 {
		t.Fatalf("draft names %q but offers contexts from %q: %+v", e.Draft.Kubeconfig, got, e)
	}
}

// R5: while a held reconnect would loosen protection, the editor and the
// row say the published protection as now and the attempt's as pending.
func TestPendingProtectionIsNeverCalledCurrent(t *testing.T) {
	r := newFleetRig(t, false)
	d := devDraft()
	d.Protected = true
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
		t.Fatal(res.Error)
	}
	if e := r.cs.GetClusterEntry("dev"); !e.ProtectedNow || e.ProtectedBy == "" || e.PendingProtected != nil {
		t.Fatalf("settled protection: %+v", e)
	}
	d.Protected = false
	d.Context = "dev-2"
	if res := r.apply(r.cs.PlanClusterEdit("dev", d), "dev"); !res.OK {
		t.Fatal(res.Error)
	}
	if !r.cs.IsProtected("dev") {
		t.Fatal("fixture: the held attempt published early")
	}
	e := r.cs.GetClusterEntry("dev")
	if !e.ProtectedNow || e.ProtectedBy != "" || e.PendingProtected == nil || *e.PendingProtected || e.PendingProtectedBy != "" {
		t.Fatalf("pending protection called current: %+v", e)
	}
	for _, c := range r.cs.GetFleetConfig().Clusters {
		if c.Name == "dev" && (!c.Protected || c.ProtectedInferred || c.ProtectedBy != "" || c.PendingProtected == nil || *c.PendingProtected) {
			t.Fatalf("the row: %+v", c)
		}
	}
	connectNow(t, r, "dev")
	if e := r.cs.GetClusterEntry("dev"); e.ProtectedNow || e.PendingProtected != nil {
		t.Fatalf("after the attempt connected: %+v", e)
	}
}

// R8: the editor knows which endpoints the cluster configures, to link each
// to Observability.
func TestTheEditorListsTheClustersEndpoints(t *testing.T) {
	r := newFleetRig(t, false)
	if e := r.cs.GetClusterEntry("dev"); e.Endpoints == nil || len(e.Endpoints) != 0 {
		t.Fatalf("dev configures none: %+v", e.Endpoints)
	}
	SetRunningEndpoint(r.cs, "dev", config.EndpointAlertmanager, nil, &config.EndpointConfig{Endpoint: "https://am.example"})
	if e := r.cs.GetClusterEntry("dev"); len(e.Endpoints) != 1 || e.Endpoints[0] != config.EndpointAlertmanager {
		t.Fatalf("dev runs an Alertmanager endpoint: %+v", e.Endpoints)
	}
}
