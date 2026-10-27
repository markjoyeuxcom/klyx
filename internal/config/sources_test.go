package config

import (
	"reflect"
	"strings"
	"testing"
)

// Settings S3a §2: installations, subscriptions, resolution and scopes.

// fleetWith is a fleet of three clusters - prd and dev in group we, lab
// ungrouped - with blocks added to each and a sources block.
func fleetWith(prd, dev, lab, sources string) string {
	y := "clusters:\n" +
		"  - name: prd\n    context: prd\n    group: we\n" + prd +
		"  - name: dev\n    context: dev\n    group: we\n" + dev +
		"  - name: lab\n    context: lab\n" + lab
	if sources != "" {
		y += "sources:\n" + sources
	}
	return y
}

const lgtm = "  - id: lgtm-we\n    kind: alertmanager\n    host: prd\n    serviceRef: {namespace: monitoring, name: am, port: web}\n    memberLabel: cluster\n    serves: [{group: we}]\n"

func TestSourcesLoadErrors(t *testing.T) {
	src := func(body string) string { return fleetWith("", "", "", body) }
	for _, tc := range []struct{ doc, want string }{
		{src(lgtm), ""},
		{src(lgtm + lgtm), `duplicate source id "lgtm-we"`},
		{src("  - id: LGTM\n    kind: alertmanager\n    host: prd\n    discover: true\n"), "sources[0]: id must be a DNS label"},
		{src("  - id: logs\n    kind: loki\n    host: prd\n    discover: true\n"), "sources.logs: kind loki is not built yet"},
		{src("  - id: m\n    kind: metrics\n    host: prd\n    discover: true\n"), "kind metrics is not built yet"},
		{src("  - id: x\n    host: prd\n    discover: true\n"), "sources.x: kind is required"},
		{src("  - id: x\n    kind: prometheus\n    host: prd\n    discover: true\n"), "kind must be alertmanager"},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n"), "give exactly one of serviceRef, discover: true or endpoint"},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n    discover: true\n    endpoint: https://am.example\n"), "give exactly one of"},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n    endpoint: https://am.example\n"), "host is not allowed beside it"},
		{src("  - id: x\n    kind: alertmanager\n    discover: true\n"), "host is required"},
		{src("  - id: x\n    kind: alertmanager\n    host: elsewhere\n    discover: true\n"), `sources.x.host: "elsewhere" is not a cluster of the fleet`},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n    discover: true\n    auth: {type: bearer}\n"), "auth and tlsSkipVerify are for an endpoint only"},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n    discover: true\n    tlsSkipVerify: true\n"), "auth and tlsSkipVerify are for an endpoint only"},
		{src("  - id: x\n    kind: alertmanager\n    endpoint: https://u:p@am.example\n"), "sources.x.endpoint: the endpoint URL carries credentials"},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n    serviceRef: {namespace: m, port: web}\n"), "sources.x.serviceRef requires namespace, name, and port"},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n    discover: true\n    memberLabel: \"a-b\"\n"), "sources.x.memberLabel"},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n    discover: true\n    serves: [{group: we, cluster: lab}]\n"), "sources.x.serves[0]: give exactly one of group or cluster"},
		{src("  - id: x\n    kind: alertmanager\n    host: prd\n    discover: true\n    name: \"a\\nb\"\n"), "sources.x.name must not contain control characters"},
		{src("  - id: x\n    kind: alertmanager\n    endpoint: https://am.example\n    tenant: \"a\\nb\"\n"), "tenant must not contain control characters"},
	} {
		_, err := loadYAML(t, tc.doc)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%q: %v, want %q", tc.want, err, tc.want)
		}
	}
}

func TestSubscriptionBlockErrors(t *testing.T) {
	for _, tc := range []struct{ lab, want string }{
		{"    alertmanager: {off: true}\n", ""},
		{"    alertmanager: {off: true, tenant: t}\n", `cluster "lab": alertmanager.off allows nothing beside it`},
		{"    alertmanager: {source: lgtm-we, endpoint: https://am.example}\n", "alertmanager.source reaches its installation"},
		{"    alertmanager: {source: lgtm-we, serviceRef: {namespace: m, name: am, port: web}}\n", "alertmanager.source reaches its installation"},
		{"    alertmanager: {scope: tenant}\n", "alertmanager.scope is allowed only with source"},
		{"    alertmanager: {source: lgtm-we, scope: cluster}\n", "alertmanager.scope must be tenant"},
		{"    alertmanager: {source: lgtm-we, scope: tenant, match: {team: a}}\n", "a match is not allowed beside it"},
		{"    alertmanager: {source: nowhere}\n", `alertmanager.source "nowhere" names no installation`},
		{"    loki: {source: lgtm-we}\n", `cluster "lab": loki.source is not built yet`},
		{"    loki: {scope: tenant}\n", "loki.scope is not built yet"},
		{"    loki: {off: true}\n", "loki.off is not built yet"},
	} {
		_, err := loadYAML(t, fleetWith("", "", tc.lab, lgtm))
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%q: %v, want %q", tc.lab, err, tc.want)
		}
	}
}

func resolve(t *testing.T, doc string) map[string]AMResolution {
	t.Helper()
	c, err := loadYAML(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ResolveAlertmanager(c.Sources, c.Clusters)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// §2.2's two tables: the steps, and a cluster's own block in each mode.
func TestResolutionModes(t *testing.T) {
	byName := "  - id: lab-am\n    kind: alertmanager\n    host: lab\n    discover: true\n    memberLabel: cluster\n    serves: [{cluster: dev}]\n"
	r := resolve(t, fleetWith("", "", "", lgtm+byName))
	for name, want := range map[string]AMResolution{
		"prd": {Mode: AMSubscribed, Via: ViaByGroup, Source: "lgtm-we", Host: "prd", Match: map[string]string{"cluster": "prd"}},
		"dev": {Mode: AMSubscribed, Via: ViaByName, Source: "lab-am", Host: "lab", Match: map[string]string{"cluster": "dev"}}, // by name over by group
		"lab": {Mode: AMDiscovered, Via: ViaDiscovery},
	} {
		if !reflect.DeepEqual(r[name], want) {
			t.Errorf("%s: %+v, want %+v", name, r[name], want)
		}
	}
	for block, want := range map[string]AMResolution{
		"    alertmanager: {source: lgtm-we, tenant: stg}\n":                         {Mode: AMSubscribed, Via: ViaOwnBlock, Source: "lgtm-we", Host: "prd", Tenant: "stg", Match: map[string]string{"cluster": "dev"}},
		"    alertmanager: {off: true}\n":                                            {Mode: AMOff, Via: ViaOwnBlock, Shadowed: []string{"lgtm-we"}},
		"    alertmanager: {endpoint: https://am.example}\n":                         {Mode: AMDirect, Via: ViaOwnBlock, Shadowed: []string{"lgtm-we"}},
		"    alertmanager:\n      serviceRef: {namespace: m, name: am, port: web}\n": {Mode: AMService, Via: ViaOwnBlock, Shadowed: []string{"lgtm-we"}},
		"    alertmanager: {tenant: t, match: {cluster: dev}}\n":                     {Mode: AMDiscovered, Via: ViaOwnBlock, Shadowed: []string{"lgtm-we"}},
		"    alertmanager: {}\n":                                                     {Mode: AMDiscovered, Via: ViaOwnBlock, Shadowed: []string{"lgtm-we"}},
	} {
		got := resolve(t, fleetWith("", block, "", lgtm))["dev"]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %+v, want %+v", block, got, want)
		}
	}
}

func TestResolutionConflicts(t *testing.T) {
	other := "  - id: other\n    kind: alertmanager\n    host: lab\n    discover: true\n    memberLabel: cluster\n    serves: [{group: we}]\n"
	if _, err := loadYAML(t, fleetWith("", "", "", lgtm+other)); err == nil || !strings.Contains(err.Error(), `group "we" is served by both "lgtm-we" and "other"`) {
		t.Errorf("two by group: %v", err)
	}
	named := func(id string) string {
		return "  - id: " + id + "\n    kind: alertmanager\n    host: lab\n    discover: true\n    memberLabel: cluster\n    serves: [{cluster: lab}]\n"
	}
	if _, err := loadYAML(t, fleetWith("", "", "", named("a")+named("b"))); err == nil || !strings.Contains(err.Error(), `cluster "lab" is served by name by both "a" and "b"`) {
		t.Errorf("two by name: %v", err)
	}
}

// §2.3: the effective scope, explicit for every subscription, and
// subscriptions sharing a tenant provably disjoint.
func TestScopes(t *testing.T) {
	r := resolve(t, fleetWith("", "    alertmanager: {source: lgtm-we, match: {cluster: dev-we, team: core}}\n", "", lgtm))
	if want := map[string]string{"cluster": "dev-we", "team": "core"}; !reflect.DeepEqual(r["dev"].Match, want) {
		t.Errorf("an override replaces the member label's value and adds a key: %+v", r["dev"].Match)
	}
	r = resolve(t, fleetWith("", "    alertmanager: {source: lgtm-we, tenant: dev-only, scope: tenant}\n", "", lgtm))
	if d := r["dev"]; !d.ScopeTenant || d.Match != nil || d.Tenant != "dev-only" {
		t.Errorf("scope: tenant alone: %+v", d)
	}
	bare := "  - id: solo\n    kind: alertmanager\n    endpoint: https://am.example\n"
	r = resolve(t, fleetWith("", "", "    alertmanager: {source: solo, match: {namespace: payments}}\n", bare))
	if want := map[string]string{"namespace": "payments"}; !reflect.DeepEqual(r["lab"].Match, want) {
		t.Errorf("a lone subscription without a member label takes its match: %+v", r["lab"])
	}
	for _, tc := range []struct{ prd, dev, lab, sources, want string }{
		{"", "", "    alertmanager: {source: solo}\n", bare, `the installation "solo" has no memberLabel, so its subscription needs a scope`},
		{"", "", "", strings.Replace(bare, "    endpoint", "    serves: [{cluster: lab}]\n    endpoint", 1), `the installation "solo" has no memberLabel`},
		{"    alertmanager: {source: lgtm-we, match: {cluster: same}}\n", "    alertmanager: {source: lgtm-we, match: {cluster: same}}\n", "", lgtm, `clusters "prd" and "dev" share the default tenant of "lgtm-we", both with "cluster" set to "same"`},
		{"", "    alertmanager: {source: lgtm-we, scope: tenant}\n", "", lgtm, `share the default tenant of "lgtm-we", so neither can take scope: tenant`},
		{"    alertmanager: {source: solo, match: {a: x}}\n", "    alertmanager: {source: solo, match: {a: y}}\n", "", bare, `clusters "prd" and "dev" share the default tenant of "solo", which has no memberLabel`},
	} {
		if _, err := loadYAML(t, fleetWith(tc.prd, tc.dev, tc.lab, tc.sources)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v", tc.want, err)
		}
	}
	// different tenants need no member label between them
	resolve(t, fleetWith("    alertmanager: {source: solo, tenant: a, match: {x: \"1\"}}\n", "    alertmanager: {source: solo, tenant: b, scope: tenant}\n", "", bare))
}

func TestSourceWarnings(t *testing.T) {
	c, err := loadYAML(t, fleetWith("", "", "", strings.Replace(lgtm, "serves: [{group: we}]", "serves: [{group: ne}, {cluster: gone}]", 1)))
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(c.Warnings(), "\n")
	if !strings.Contains(w, `source "lgtm-we" serves group "ne", which no cluster has`) || !strings.Contains(w, `serves cluster "gone", which is not in fleet.yaml`) || strings.Contains(w, "unknown top-level key") {
		t.Errorf("warnings: %s", w)
	}
}

// The block a connection uses: its own, else by name, else by group, else
// none - never discovery when an installation serves the cluster.
func TestConnectionAlertmanager(t *testing.T) {
	c, err := loadYAML(t, fleetWith("", "    alertmanager: {off: true}\n", "", lgtm+"  - id: lab-am\n    kind: alertmanager\n    host: lab\n    discover: true\n    memberLabel: cluster\n    serves: [{cluster: prd}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*EndpointConfig{}
	for _, cl := range c.Clusters {
		got[cl.Name] = ConnectionAlertmanager(c.Sources, cl)
	}
	if got["prd"] == nil || got["prd"].Source != "lab-am" || got["dev"] == nil || !got["dev"].Off || got["lab"] != nil {
		t.Errorf("connection blocks: prd=%+v dev=%+v lab=%+v", got["prd"], got["dev"], got["lab"])
	}
	if h := HostedBy(c.Sources, "prd"); !reflect.DeepEqual(h, []string{"lgtm-we"}) {
		t.Errorf("hosted by prd: %v", h)
	}
}

// An unknown key under sources is a load error with its place, and sources
// is no longer an unknown top-level key.
func TestSourcesDecodeStrictly(t *testing.T) {
	if _, err := loadYAML(t, fleetWith("", "", "", lgtm+"    membreLabel: cluster\n")); err == nil || !strings.Contains(err.Error(), "membreLabel") {
		t.Errorf("unknown key: %v", err)
	}
}

// Codex's review of S3a-1 (2026-10-27), R1 and R2: keys are judged as
// written, and no new diagnostic echoes a long value.
func TestKeysAreJudgedAsWritten(t *testing.T) {
	for _, tc := range []struct{ lab, want string }{
		{"    alertmanager: {source: \"\"}\n", `alertmanager.source names an installation in sources: it cannot be empty`},
		{"    alertmanager: {off: false}\n", `alertmanager.off is true, or left out`},
		{"    alertmanager: {off: true, tenant: \"\"}\n", `alertmanager.off allows nothing beside it`},
		{"    alertmanager: {source: lgtm-we, tlsSkipVerify: false}\n", `alertmanager.source reaches its installation`},
		{"    alertmanager: {scope: \"\"}\n", `alertmanager.scope is allowed only with source`},
		{"    alertmanager: {source: lgtm-we, scope: \"\"}\n", `alertmanager.scope must be tenant`},
		{"    alertmanager: {source: lgtm-we, scope: tenant, match: {}}\n", `a match is not allowed beside it`},
		{"    loki: {source: \"\"}\n", `loki.source is not built yet`},
		{"    loki: {scope: \"\"}\n", `loki.scope is not built yet`},
		{"    loki: {off: false}\n", `loki.off is not built yet`},
	} {
		if _, err := loadYAML(t, fleetWith("", "", tc.lab, lgtm)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.lab, err, tc.want)
		}
	}
	// the legacy modes, as written, still load
	for _, lab := range []string{"    alertmanager: {}\n", "    alertmanager: {tenant: \"\"}\n", "    loki: {endpoint: https://loki.example}\n"} {
		if _, err := loadYAML(t, fleetWith("", "", lab, lgtm)); err != nil {
			t.Errorf("%q: %v", lab, err)
		}
	}
}

func TestNewDiagnosticsWithholdLongValues(t *testing.T) {
	const canary = "C5-S3A1-CANARY-0123456789abcdef0123456789"
	for _, doc := range []string{
		fleetWith("", "", "    alertmanager: {source: "+canary+"}\n", lgtm),
		fleetWith("", "", "", "  - id: x\n    kind: alertmanager\n    host: "+canary+"\n    discover: true\n"),
		fleetWith("    alertmanager: {source: lgtm-we, match: {cluster: "+canary+"}}\n", "    alertmanager: {source: lgtm-we, match: {cluster: "+canary+"}}\n", "", lgtm),
		fleetWith("    alertmanager: {source: solo, tenant: "+canary+", match: {a: x}}\n", "    alertmanager: {source: solo, tenant: "+canary+", match: {a: y}}\n", "", "  - id: solo\n    kind: alertmanager\n    endpoint: https://am.example\n"),
	} {
		_, err := loadYAML(t, doc)
		if err == nil || strings.Contains(err.Error(), canary) || strings.Contains(DisplayError(err), canary) {
			t.Errorf("a long value echoed: %v", err)
		}
	}
	c, err := loadYAML(t, fleetWith("", "", "", strings.Replace(lgtm, "serves: [{group: we}]", "serves: [{group: we}, {cluster: "+canary+"}, {group: "+canary+"}]", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(c.Warnings(), "\n"); strings.Contains(w, canary) || !strings.Contains(w, "a value of 41 characters") {
		t.Errorf("warnings: %s", w)
	}
}

// Codex's re-review of S3a-1 (2026-10-27), RR1 and RR2: keys inherited
// through a YAML merge are written too, and identities and label names are
// withheld like values.
func TestMergedKeysAreWrittenToo(t *testing.T) {
	for _, tc := range []struct{ name, inherited, block string }{
		{"empty-source", `source: ""`, "alertmanager: {<<: *defaults}"},
		{"loki-source", `source: ""`, "loki: {<<: *defaults}"},
		{"loki-scope", `scope: ""`, "loki: {<<: *defaults}"},
		{"loki-off", `off: false`, "loki: {<<: *defaults}"},
		{"am-false-off", `off: false`, "alertmanager: {<<: *defaults}"},
		{"source-with-empty-endpoint", `endpoint: ""`, "alertmanager: {<<: *defaults, source: lgtm-we}"},
		{"off-with-empty-tenant", `tenant: ""`, "alertmanager: {<<: *defaults, off: true}"},
		{"a merge sequence", `source: ""`, "alertmanager: {<<: [*other, *defaults]}"},
		{"a nested merge", `<<: *inner`, "alertmanager: {<<: *defaults}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "inner: &inner {source: \"\"}\nother: &other {tenant: t}\ndefaults: &defaults {" + tc.inherited + "}\n" + fleetWith("", "    "+tc.block+"\n", "", lgtm)
			if cfg, err := loadYAML(t, doc); err == nil {
				r, re := ResolveAlertmanager(cfg.Sources, cfg.Clusters)
				t.Fatalf("a merged forbidden key loaded: resolution=%+v error=%v", r["dev"], re)
			}
		})
	}
	// valid merges and aliases still load, as they did
	for _, block := range []string{"alertmanager: {<<: *ok}", "alertmanager: *ok"} {
		doc := "ok: &ok {source: lgtm-we, tenant: t}\n" + fleetWith("", "    "+block+"\n", "", lgtm)
		if _, err := loadYAML(t, doc); err != nil {
			t.Errorf("%q: %v", block, err)
		}
	}
}

func TestIdentitiesAndLabelNamesAreWithheldToo(t *testing.T) {
	const canary = "s3a1canary012345678901234567890123456789"
	renamed := strings.ReplaceAll(lgtm, "lgtm-we", canary)
	cases := map[string]string{
		"duplicate-id":      fleetWith("", "", "", renamed+renamed),
		"id-in-place":       fleetWith("", "", "", strings.Replace(renamed, "host: prd", "host: absent", 1)),
		"id-in-warning":     fleetWith("", "", "", strings.Replace(renamed, "group: we", "group: absent", 1)),
		"id-in-scope-error": fleetWith("", "", "    alertmanager: {source: "+canary+"}\n", "  - id: "+canary+"\n    kind: alertmanager\n    endpoint: https://am.example\n"),
		"member-label-name": fleetWith("    alertmanager: {source: lgtm-we, match: {"+canary+": same}}\n", "    alertmanager: {source: lgtm-we, match: {"+canary+": same}}\n", "", strings.Replace(lgtm, "memberLabel: cluster", "memberLabel: "+canary, 1)),
		"group-conflict":    strings.ReplaceAll(fleetWith("", "", "", lgtm+strings.ReplaceAll(lgtm, "lgtm-we", "other-am")), "group: we", "group: "+canary),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadYAML(t, doc)
			if err != nil {
				if strings.Contains(err.Error(), canary) || strings.Contains(DisplayError(err), canary) {
					t.Fatalf("an identity or label name echoed whole: %v", err)
				}
				return
			}
			if strings.Contains(strings.Join(cfg.Warnings(), "\n"), canary) {
				t.Fatal("an identity echoed whole in a warning")
			}
		})
	}
}
