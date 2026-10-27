package appbridge

// Settings S3a in S2's fleet plans (§3.6): the host check, and a change
// that would move a running connection's alerts.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/config"
)

func servesGroup(id, host, group string) config.SourceConfig {
	return config.SourceConfig{ID: id, Kind: config.SourceKindAlertmanager, Host: host, Discover: true, MemberLabel: "cluster", Serves: []config.ServeConfig{{Group: group}}}
}

func TestAPlacementThatMovesTheAlertSourceIsRefused(t *testing.T) {
	r := newFleetRig(t, true)
	r.mu.Lock()
	r.sources = []config.SourceConfig{servesGroup("lgtm-we", "prd", "web")}
	r.mu.Unlock()
	d := devDraft()
	d.Group = "web"
	p := r.cs.PlanClusterEdit("dev", d)
	if !strings.Contains(p.Refused, "this changes where dev's alerts come from (discovery in the cluster to the installation lgtm-we)") {
		t.Fatalf("a group the installation serves: %+v", p)
	}
	d.Group = "unserved"
	if p := r.cs.PlanClusterEdit("dev", d); p.Refused != "" {
		t.Fatalf("a group no installation serves: %s", p.Refused)
	}
	// a cluster with its own block keeps it whatever its group
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    alertmanager: {endpoint: https://am.example}\n")
	SetRunningEndpoint(r.cs, "dev", config.EndpointAlertmanager, nil, &config.EndpointConfig{Endpoint: "https://am.example"})
	d.Group = "web"
	if p := r.cs.PlanClusterEdit("dev", d); p.Refused != "" {
		t.Fatalf("its own block wins: %s", p.Refused)
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
	if got := SourceHosts(r.path, []config.SourceConfig{servesGroup("b-am", "prd", "x"), servesGroup("file-am", "prd", "x")})("prd"); !reflect.DeepEqual(got, []string{"b-am", "file-am"}) {
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
