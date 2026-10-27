package appbridge

// Settings S3a in S2's fleet plans (§3.6): the host check, and a change
// that would move a running connection's alerts.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/secretstore"
)

func servesGroup(id, host, group string) config.SourceConfig {
	return config.SourceConfig{ID: id, Kind: config.SourceKindAlertmanager, Host: host, Discover: true, MemberLabel: "cluster", Serves: []config.ServeConfig{{Group: group}}}
}

// S3a-2: a placement that moves where a cluster's alerts come from is
// listed, and its commit puts the running fleet's subscriptions in force; a
// change under which the installations would not resolve is refused.
func TestAPlacementThatMovesTheAlertSourceIsListedAndResubscribed(t *testing.T) {
	r := newFleetRig(t, true)
	r.mu.Lock()
	r.sources = []config.SourceConfig{servesGroup("lgtm-we", "prd", "web")}
	r.mu.Unlock()
	d := devDraft()
	d.Group = "web"
	p := r.cs.PlanClusterEdit("dev", d)
	if p.Refused != "" || !hasChange(p, "fleet.alerts", "discovery in the cluster", "the installation lgtm-we") {
		t.Fatalf("a group the installation serves: %+v", p)
	}
	if res := r.apply(p, ""); !res.OK {
		t.Fatal(res.Error)
	}
	r.mu.Lock()
	runs := r.resubscribed
	r.mu.Unlock()
	if len(runs) == 0 {
		t.Fatal("the commit put no subscription in force")
	}
	group := ""
	for _, c := range runs[len(runs)-1] {
		if c.Name == "dev" {
			group = c.Group
		}
	}
	if group != "web" {
		t.Fatalf("resubscribed with dev's group %q", group)
	}
	// its own block keeps it whatever its group: nothing about its alerts moves
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    alertmanager: {endpoint: https://am.example}\n")
	SetRunningEndpoint(r.cs, "dev", config.EndpointAlertmanager, nil, &config.EndpointConfig{Endpoint: "https://am.example"})
	d.Group = "other"
	p = r.cs.PlanClusterEdit("dev", d)
	if p.Refused != "" {
		t.Fatalf("its own block wins: %s", p.Refused)
	}
	for _, c := range p.Changes {
		if c.ID == "fleet.alerts" {
			t.Fatalf("its own block's alerts moved: %+v", c)
		}
	}
}

func TestAChangeTheInstallationsCannotResolveIsRefused(t *testing.T) {
	r := newFleetRig(t, true)
	r.mu.Lock()
	r.sources = []config.SourceConfig{servesGroup("a", "prd", "web"), servesGroup("b", "prd", "web")}
	r.mu.Unlock()
	d := devDraft()
	d.Group = "web"
	if p := r.cs.PlanClusterEdit("dev", d); !strings.Contains(p.Refused, "the alert installations do not resolve") {
		t.Fatalf("two installations serve web: %+v", p)
	}
}

func TestAHostIsNotRemoved(t *testing.T) {
	r := newFleetRig(t, true)
	r.mu.Lock()
	r.sources = []config.SourceConfig{servesGroup("lgtm-we", "dev", "web")}
	r.mu.Unlock()
	if p := r.cs.PlanClusterRemove("dev", nil); !strings.Contains(p.Refused, "dev hosts lgtm-we: remove those first") {
		t.Fatalf("an active installation's host: %+v", p)
	}
	// an installation in the file, not yet in force, holds its host too
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\nsources:\n  - id: file-am\n    kind: alertmanager\n    host: prd\n    discover: true\n")
	if p := r.cs.PlanClusterRemove("prd", nil); !strings.Contains(p.Refused, "prd hosts file-am") {
		t.Fatalf("a file installation's host: %+v", p)
	}
	active := func() []config.SourceConfig {
		return []config.SourceConfig{servesGroup("b-am", "prd", "x"), servesGroup("file-am", "prd", "x")}
	}
	if got := SourceHosts(r.path, active)("prd"); !reflect.DeepEqual(got, []string{"b-am", "file-am"}) {
		t.Fatalf("active and file hosts, once each, sorted: %v", got)
	}
}

// Observability's row says a subscription or off, never "discovered".
func TestEndpointRowsSaySubscriptionsAndOff(t *testing.T) {
	if s := endpointSource(config.EndpointAlertmanager, config.EndpointConfig{Off: true}); !strings.HasPrefix(s, "off in fleet.yaml") {
		t.Errorf("off: %q", s)
	}
	if s := endpointSource(config.EndpointAlertmanager, config.EndpointConfig{Source: "lgtm-we"}); !strings.Contains(s, "subscribed to the installation lgtm-we") || strings.Contains(s, "discovered") {
		t.Errorf("subscribed: %q", s)
	}
}

// Codex's review of S3a-1, R3: only a legacy reader that filters nothing is
// called unscoped; off reads nothing, and a subscription's scope is its
// installation's.
func TestOnlyALegacyUnfilteredRowIsUnscoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	doc := "clusters:\n" +
		"  - name: off\n    context: off\n    alertmanager: {off: true}\n" +
		"  - name: sub\n    context: sub\n    alertmanager: {source: am}\n" +
		"  - name: legacy\n    context: legacy\n    alertmanager: {endpoint: https://am.example}\n" +
		"sources:\n  - id: am\n    kind: alertmanager\n    host: legacy\n    discover: true\n    memberLabel: cluster\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewEndpointsService(path, &secretstore.Memory{}, endpointcred.NewRegistry(), func(string) (EndpointConn, bool) { return nil, false })
	unscoped := map[string]bool{}
	for _, r := range svc.GetEndpoints().Rows {
		unscoped[r.Cluster] = r.Unscoped
	}
	if unscoped["off"] || unscoped["sub"] || !unscoped["legacy"] {
		t.Fatalf("unscoped: %v", unscoped)
	}
}

// Codex's re-review 2 of S3a-1, R2-1: a plan's refusals name a long
// installation id by its length alone, as the loader's diagnostics do,
// while two distinct ids of one length still differ. In S3a-2 a source
// move is a change line, under the same rule.
func TestPlanRefusalsWithholdLongInstallationIDs(t *testing.T) {
	const canary = "s3a1canary012345678901234567890123456789"
	withheld := "an installation whose id has 40 characters"
	t.Run("an active host", func(t *testing.T) {
		r := newFleetRig(t, true)
		r.mu.Lock()
		r.sources = []config.SourceConfig{servesGroup(canary, "dev", "web"), servesGroup("lgtm-we", "dev", "web")}
		r.mu.Unlock()
		p := r.cs.PlanClusterRemove("dev", nil)
		if strings.Contains(p.Refused, canary) || !strings.Contains(p.Refused, "dev hosts lgtm-we, "+withheld+": remove those first") {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("a host in the file", func(t *testing.T) {
		r := newFleetRig(t, true)
		r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\nsources:\n  - id: "+canary+"\n    kind: alertmanager\n    host: prd\n    discover: true\n")
		p := r.cs.PlanClusterRemove("prd", nil)
		if strings.Contains(p.Refused, canary) || !strings.Contains(p.Refused, "prd hosts "+withheld) {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("a source move", func(t *testing.T) {
		r := newFleetRig(t, true)
		r.mu.Lock()
		r.sources = []config.SourceConfig{servesGroup(canary, "prd", "web")}
		r.mu.Unlock()
		d := devDraft()
		d.Group = "web"
		p := r.cs.PlanClusterEdit("dev", d)
		if b, _ := json.Marshal(p); strings.Contains(string(b), canary) || !hasChange(p, "fleet.alerts", "discovery in the cluster", withheld) {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("two long ids of one length", func(t *testing.T) {
		r := newFleetRig(t, true)
		const a, b = "sourceaaaaaaaaaaaaaaaaaaaaaaaa", "sourcebbbbbbbbbbbbbbbbbbbbbbbb"
		first := devDraft()
		first.Group = "old"
		if out := r.apply(r.cs.PlanClusterEdit("dev", first), ""); !out.OK {
			t.Fatalf("fixture: %+v", out)
		}
		r.mu.Lock()
		r.sources = []config.SourceConfig{servesGroup(a, "prd", "old"), servesGroup(b, "prd", "web")}
		r.mu.Unlock()
		d := devDraft()
		d.Group = "web"
		if p := r.cs.PlanClusterEdit("dev", d); !hasChange(p, "fleet.alerts", "an installation whose id has 30 characters", "an installation whose id has 30 characters") {
			t.Fatalf("distinct installations are one source: %+v", p)
		}
	})
}
