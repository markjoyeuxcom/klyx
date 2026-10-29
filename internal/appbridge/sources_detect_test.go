package appbridge

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
)

// Settings S3a §6: Detect proposes one installation per cluster it proves
// one in, with its sample's bounds and what it could not show; it never
// infers a shared installation, and never says a missing member has no
// alerts.

func TestDetectProposesPerCluster(t *testing.T) {
	r := newSourcesRig(t, hostedOnPrd("am-prd", config.ServeConfig{Group: "nothing"}))
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var asked []string
	r.svc.d.Detect = func(_ context.Context, cluster string, names []string) fleet.DetectSample {
		asked = append(asked, cluster)
		if strings.Join(names, ",") != "prd,dev" {
			t.Errorf("the fleet's names: %v", names)
		}
		switch cluster {
		case "prd":
			return fleet.DetectSample{Cluster: "prd", Found: true, Namespace: "monitoring", Service: "am", Port: "web", Via: "Service am", At: at,
				Alerts: 12, LabelNames: []string{"alertname", "cluster"}, Members: map[string][]string{"cluster": {"dev", "prd"}}}
		default:
			return fleet.DetectSample{Cluster: "dev", Found: true, Namespace: "monitoring", Service: "am", Port: "web", Via: "Service am", At: at,
				Alerts: 500, Truncated: true, Members: map[string][]string{"cluster": {"dev"}, "k8s_cluster": {"dev"}}}
		}
	}
	got := r.svc.Detect(DetectScopeDTO{Cluster: "prd"})
	if len(asked) != 1 || len(got.Proposals) != 1 || got.Clusters[0].Outcome != "found" {
		t.Fatalf("one cluster: %+v", got)
	}
	p := got.Proposals[0]
	if p.Host != "prd" || p.Where != "Service monitoring/am:web in prd" || p.MemberLabel != "cluster" || strings.Join(p.MembersSeen, ",") != "dev,prd" ||
		p.SampledAt != "2026-10-27T12:00:00Z" || p.Sample != "12 alerts, at most 500 alerts and 1 MiB" {
		t.Fatalf("%+v", p)
	}
	if !slices.Contains(p.Notes, notSeenNote) {
		t.Fatalf("notes: %v", p.Notes)
	}
	d := p.Draft.Installation
	if d == nil || d.Op != installAdd || d.Source.ID != "am-prd-2" || d.Source.Host != "prd" || d.Source.ServiceRef.Name != "am" ||
		strings.Join(d.Source.ServesClusters, ",") != "dev,prd" {
		t.Fatalf("the draft: %+v", d)
	}
	// both clusters of the fleet, by group: two proposals, never one shared
	// installation for equal Service names
	asked = nil
	dd := devDraft()
	dd.Group = "platform"
	if res := r.apply(r.cs.PlanClusterEdit("dev", dd), ""); !res.OK {
		t.Fatal(res.Error)
	}
	got = r.svc.Detect(DetectScopeDTO{Group: "platform"})
	if len(asked) != 2 || len(got.Proposals) != 2 || got.Proposals[0].Draft.Installation.Source.ID == got.Proposals[1].Draft.Installation.Source.ID {
		t.Fatalf("two clusters: %+v", got)
	}
	dev := got.Proposals[1]
	if dev.MemberLabel != "" || !strings.Contains(strings.Join(dev.Notes, " "), "choose the member label") || !strings.Contains(dev.Sample, "cut at its bounds") {
		t.Fatalf("an ambiguous label and a cut sample: %+v", dev)
	}
	if got := r.svc.Detect(DetectScopeDTO{Group: "nothing"}); got.Error == "" {
		t.Fatal("an empty group")
	}
}

func TestDetectSaysWhatItCouldNotRead(t *testing.T) {
	r := newSourcesRig(t)
	r.svc.d.Detect = func(_ context.Context, cluster string, _ []string) fleet.DetectSample {
		if cluster == "prd" {
			return fleet.DetectSample{Cluster: "prd", Found: true, Namespace: "monitoring", Service: "am", Port: "web", Unread: "the sample was larger than 1 MiB, so it was not read"}
		}
		return fleet.DetectSample{Cluster: "dev", Ambiguous: "2 Alertmanagers found; set alertmanager.serviceRef"}
	}
	got := r.svc.Detect(DetectScopeDTO{Cluster: "prd"})
	if p := got.Proposals[0]; !strings.HasPrefix(p.Sample, "not read: the sample was larger than 1 MiB") || p.MemberLabel != "" || p.Draft.Installation.Source.ServesClusters[0] != "prd" {
		t.Fatalf("%+v", p)
	}
	if got := r.svc.Detect(DetectScopeDTO{Cluster: "dev"}); len(got.Proposals) != 0 || got.Clusters[0].Outcome != "ambiguous" {
		t.Fatalf("%+v", got)
	}
}

// Settings S3a §7: the migration offer, proposed and never applied on its
// own; no secret copied; scopes before and after; conflicts and choices
// named.
func TestMigrationOffers(t *testing.T) {
	r := newSourcesRig(t)
	legacy := func(cluster string, b *config.EndpointConfig) {
		SetRunningEndpoint(r.cs, cluster, config.EndpointAlertmanager, nil, b)
	}
	legacy("prd", &config.EndpointConfig{Endpoint: "https://AM.example:443/am/", Tenant: "ops", Auth: &config.EndpointAuth{Type: "bearer"}, Match: map[string]string{"cluster": "prd"}})
	if offers := r.svc.MigrationOffers(""); len(offers) != 0 {
		t.Fatalf("a URL on one cluster offered: %+v", offers)
	}
	legacy("dev", &config.EndpointConfig{Endpoint: "https://am.example/am", Tenant: "ops", Auth: &config.EndpointAuth{Type: "basic", Username: "klyx"}})
	before, revBefore := r.file(), r.active.Revision()
	offers := r.svc.MigrationOffers("")
	if len(offers) != 1 || strings.Join(offers[0].Clusters, ",") != "prd,dev" {
		t.Fatalf("%+v", offers)
	}
	o := offers[0]
	if r.file() != before || r.active.Revision() != revBefore {
		t.Fatal("an offer applied itself")
	}
	if len(o.Conflicts) != 1 || !strings.Contains(o.Conflicts[0], "prd bearer, dev basic") || strings.Join(o.Unresolved, ",") != "auth" {
		t.Fatalf("conflicts: %v %v", o.Conflicts, o.Unresolved)
	}
	if len(o.Choices) != 1 || o.Scopes[1].After != "not valid yet" || o.Scopes[1].Before != "from its own endpoint at https://am.example:443, tenant ops, unfiltered: everything there is read as this cluster's" {
		t.Fatalf("an unscoped block: %+v %+v", o.Choices, o.Scopes)
	}
	if o.Draft.Installation.Source.AuthType != "" || o.Draft.Installation.Source.Tenant != "ops" || !strings.Contains(o.Credential, "none is copied") {
		t.Fatalf("%+v %q", o.Draft.Installation.Source, o.Credential)
	}
	// the same auth, a match each, and a tenant each: valid with no member
	// label, the scopes the same, and the offer applies as an ordinary
	// plan, the old records kept
	legacy("dev", &config.EndpointConfig{Endpoint: "https://am.example/am", Tenant: "dev-t", Auth: &config.EndpointAuth{Type: "bearer"}, Match: map[string]string{"cluster": "dev"}})
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    alertmanager:\n      endpoint: https://am.example/am\n")
	if err := r.store.Set(endpointcred.Account("dev", config.EndpointAlertmanager), "a record"); err != nil {
		t.Fatal(err)
	}
	o = r.svc.MigrationOffers("")[0]
	if len(o.Conflicts) != 0 || len(o.Choices) != 0 || o.Scopes[0].Change != "the same" || o.Scopes[1].After != "from am-am at https://am.example:443, tenant dev-t, filtered by cluster=dev" ||
		o.Draft.Installation.Source.Tenant != "" {
		t.Fatalf("%+v", o)
	}
	p := r.svc.PlanSources(o.Draft)
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	if !strings.Contains(r.file(), "    alertmanager:\n      source: am-am\n      tenant: dev-t\n      match:\n        cluster: dev\n") {
		t.Fatalf("\n%s", r.file())
	}
	if _, err := r.store.Get(endpointcred.Account("dev", config.EndpointAlertmanager)); err != nil {
		t.Fatal("a cluster's record was removed")
	}
	if _, err := r.store.Get(endpointcred.SourceAccount("am-am")); err == nil {
		t.Fatal("a secret was copied to the installation")
	}
	// a Service shared by name is never offered
	legacy("prd", &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "m", Name: "am", Port: "web"}})
	legacy("dev", &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "m", Name: "am", Port: "web"}})
	if offers := r.svc.MigrationOffers(""); len(offers) != 0 {
		t.Fatalf("equal Service names offered: %+v", offers)
	}
}

// One tenant shared without a member label is a choice the offer names;
// the person's member label makes the same draft valid.
func TestAMigrationOfferNamesTheChoiceOnASharedTenant(t *testing.T) {
	r := newSourcesRig(t)
	for _, c := range []string{"prd", "dev"} {
		SetRunningEndpoint(r.cs, c, config.EndpointAlertmanager, nil, &config.EndpointConfig{Endpoint: "https://am.example/am", Tenant: "ops", Match: map[string]string{"cluster": c}})
	}
	o := r.svc.MigrationOffers("")[0]
	if len(o.Choices) != 1 || !strings.Contains(o.Choices[0], "memberLabel") || o.Scopes[0].After != "not valid yet" {
		t.Fatalf("%+v", o)
	}
	o.Draft.Installation.Source.MemberLabel = "cluster"
	if p := r.svc.PlanSources(o.Draft); p.Refused != "" {
		t.Fatalf("with a member label: %s", p.Refused)
	}
}

// Codex's review of S3a-4b, R2: the offer groups URLs by their path as
// written, escaped: /a%2Fb and /a/b are two endpoints a proxy may route
// apart, never one installation; the same path, with or without its
// trailing slash, is one.
func TestAMigrationOfferKeepsEscapedPathsApart(t *testing.T) {
	for _, tc := range []struct {
		name, prd, dev string
		offers         int
		where          string
	}{
		{"an escaped slash", "https://am.example/a%2Fb", "https://am.example/a/b", 0, ""},
		{"the same path", "https://am.example/a/b", "https://AM.example:443/a/b/", 1, "https://am.example:443/a/b"},
		{"the same escaped path", "https://am.example/a%2Fb", "https://am.example/a%2Fb", 1, "https://am.example:443/a%2Fb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSourcesRig(t)
			SetRunningEndpoint(r.cs, "prd", config.EndpointAlertmanager, nil, &config.EndpointConfig{Endpoint: tc.prd, Tenant: "prd", Match: map[string]string{"cluster": "prd"}})
			SetRunningEndpoint(r.cs, "dev", config.EndpointAlertmanager, nil, &config.EndpointConfig{Endpoint: tc.dev, Tenant: "dev", Match: map[string]string{"cluster": "dev"}})
			offers := r.svc.MigrationOffers("")
			if len(offers) != tc.offers || tc.offers == 1 && offers[0].Where != tc.where {
				t.Fatalf("%+v", offers)
			}
		})
	}
}

// Codex's review of S3a-4b, the S3a-4c checkpoint: the fields a conflict
// leaves to the person are named, for the page to ask; clusters that
// agree leave nothing unresolved.
func TestAnOffersConflictsAreLeftUnresolved(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prd, dev   bool
		unresolved string
	}{
		{"the certificate check differs", true, false, "tlsSkipVerify"},
		{"the clusters agree", false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSourcesRig(t)
			SetRunningEndpoint(r.cs, "prd", config.EndpointAlertmanager, nil, &config.EndpointConfig{Endpoint: "https://am.example", TLSSkipVerify: tc.prd, Match: map[string]string{"cluster": "prd"}})
			SetRunningEndpoint(r.cs, "dev", config.EndpointAlertmanager, nil, &config.EndpointConfig{Endpoint: "https://am.example", TLSSkipVerify: tc.dev, Match: map[string]string{"cluster": "dev"}})
			offers := r.svc.MigrationOffers("")
			if len(offers) != 1 || strings.Join(offers[0].Unresolved, ",") != tc.unresolved || len(offers[0].Conflicts) != len(offers[0].Unresolved) {
				t.Fatalf("%+v", offers)
			}
		})
	}
}
