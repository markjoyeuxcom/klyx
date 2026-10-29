package appbridge

import (
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/secretstore"
)

// Settings S3a §5: Observability's plans, from what runs, with two
// candidates, applied as one transaction.

type sourcesRig struct {
	*fleetRig
	svc       *SourcesService
	active    *fleet.AlertSources
	store     *secretstore.Memory
	revs      *endpointcred.Registry
	published int
}

func newSourcesRig(t *testing.T, sources ...config.SourceConfig) *sourcesRig {
	t.Helper()
	r := &sourcesRig{fleetRig: newFleetRig(t, true), store: &secretstore.Memory{}, revs: endpointcred.NewRegistry()}
	if len(sources) > 0 {
		// in force as at a start: the file says them
		v, err := config.FileVersion(r.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.EditSourcesAt(r.path, v, config.SourcesEdit{Sources: &sources}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := fleet.NewAlertSources(sources, r.cs.fleetClusters())
	if err != nil {
		t.Fatal(err)
	}
	r.active = a
	r.svc = NewSourcesService(r.path, r.cs, SourcesDeps{
		Active: func() *fleet.AlertSources { return r.active },
		Publish: func(s []config.SourceConfig, run []config.ClusterConfig) error {
			r.published++
			_, err := r.active.Apply(s, run)
			return err
		},
		Work: func(moved map[string][]string) []PlanWorkDTO {
			var out []PlanWorkDTO
			for _, k := range observabilityKinds {
				for _, c := range moved[k] {
					out = append(out, PlanWorkDTO{ID: "conv:" + k + ":" + c, Kind: kindChat, Cluster: c, What: "a chat on " + c + " holding " + kindNames[k].noun})
				}
			}
			return out
		},
		Store: r.store, Revs: r.revs,
	})
	return r
}

func hostedOnPrd(id string, serves ...config.ServeConfig) config.SourceConfig {
	return config.SourceConfig{ID: id, Kind: config.SourceKindAlertmanager, Host: "prd",
		ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "am", Port: "web"}, MemberLabel: "cluster", Serves: serves}
}

func (r *sourcesRig) applySources(p PlanDTO) ApplyResultDTO {
	r.t.Helper()
	if p.Refused != "" {
		r.t.Fatalf("refused: %s", p.Refused)
	}
	return r.svc.ApplySources(p.Digest)
}

func change(p PlanDTO, id string) (PlanChangeDTO, bool) {
	for _, c := range p.Changes {
		if c.ID == id {
			return c, true
		}
	}
	return PlanChangeDTO{}, false
}

// An installation added with a cluster's subscription: the plan says the
// installation, the block, the member's alerts before and after, and what
// ends; Apply writes both blocks once and puts them in force.
func TestASourcesPlanAddsAnInstallationAndASubscription(t *testing.T) {
	r := newSourcesRig(t)
	src := sourceDraftOf(hostedOnPrd("lgtm-we", config.ServeConfig{Cluster: "prd"}))
	p := r.svc.PlanSources(SourcesDraftDTO{
		Installation:  &InstallationEditDTO{Op: installAdd, Source: src},
		Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: &SubscriptionDTO{Source: "lgtm-we"}}},
	})
	if p.Refused != "" || !p.Writes || !p.Activates || p.Section != "observability" {
		t.Fatalf("%+v", p)
	}
	if c, ok := change(p, "sources.lgtm-we"); !ok || c.From != "none" || c.To != "host prd; Service http monitoring/am:web; member label cluster; serves prd" {
		t.Fatalf("installation: %+v", p.Changes)
	}
	if c, ok := change(p, "clusters.dev.alertmanager"); !ok || c.To != "subscribed to lgtm-we" {
		t.Fatalf("block: %+v", p.Changes)
	}
	if c, ok := change(p, "alerts.dev"); !ok || c.From != "discovered in this cluster, unfiltered: everything there is read as this cluster's" || c.To != "from lgtm-we, hosted by prd, filtered by cluster=dev" {
		t.Fatalf("dev's alerts: %+v", p.Changes)
	}
	if c, ok := change(p, "alerts.prd"); !ok || c.To != "from lgtm-we, hosted by prd, filtered by cluster=prd" {
		t.Fatalf("prd, served by name: %+v", p.Changes)
	}
	ends := map[string]bool{}
	for _, w := range p.Ends {
		ends[w.ID] = true
	}
	if !ends["alerts:dev"] || !ends["alerts:prd"] || !ends["conv:alertmanager:dev"] || !ends["conv:alertmanager:prd"] {
		t.Fatalf("ends: %+v", p.Ends)
	}
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	file := r.file()
	if !strings.Contains(file, "    alertmanager:\n      source: lgtm-we\n") || !strings.Contains(file, "sources:\n  - id: lgtm-we\n") {
		t.Fatalf("written:\n%s", file)
	}
	if run, _ := r.cs.runningEntry("dev"); run.Alertmanager == nil || run.Alertmanager.Source != "lgtm-we" {
		t.Fatalf("dev's running block: %+v", run.Alertmanager)
	}
	for _, c := range []string{"dev", "prd"} {
		if sub, ok := r.active.Subscription(c); !ok || sub.Source.ID != "lgtm-we" {
			t.Fatalf("%s is not subscribed in force: %+v", c, sub)
		}
	}
	if r.published != 1 {
		t.Fatalf("published %d times", r.published)
	}
	if again := r.svc.ApplySources(p.Digest); again.OK {
		t.Fatal("a plan applied twice")
	}
}

// Apply refuses, with the new plan, when the file, the running fleet or the
// installations in force moved since the plan; nothing is written.
func TestASourcesPlanIsRefusedWhenAnythingMoved(t *testing.T) {
	draft := func() SourcesDraftDTO {
		return SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installAdd, Source: sourceDraftOf(hostedOnPrd("lgtm-we", config.ServeConfig{Cluster: "dev"}))}}
	}
	for name, move := range map[string]func(r *sourcesRig){
		"the file": func(r *sourcesRig) { r.handEdit("# the fleet\n", "# the fleet, edited\n") },
		"the running fleet": func(r *sourcesRig) {
			d := devDraft()
			d.Group = "web"
			if res := r.apply(r.cs.PlanClusterEdit("dev", d), ""); !res.OK {
				r.t.Fatal(res.Error)
			}
		},
		"the installations in force": func(r *sourcesRig) {
			if _, err := r.active.Apply([]config.SourceConfig{hostedOnPrd("other", config.ServeConfig{Cluster: "prd"})}, r.cs.fleetClusters()); err != nil {
				r.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSourcesRig(t)
			p := r.svc.PlanSources(draft())
			move(r)
			before := r.file()
			res := r.svc.ApplySources(p.Digest)
			if res.OK || res.Plan == nil || !strings.Contains(res.Error, "changed") {
				t.Fatalf("%+v", res)
			}
			if r.file() != before || r.published != 0 {
				t.Fatal("a moved plan wrote or published")
			}
		})
	}
}

// Two candidates (S3a §5): the file's unapplied group change and another
// cluster's subscription hand edit are neither adopted nor published; the
// disk candidate keeps them as the file has them.
func TestASourcesPlanAdoptsNothingElseFromTheFile(t *testing.T) {
	r := newSourcesRig(t, hostedOnPrd("lgtm-we", config.ServeConfig{Group: "web"}))
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    group: web\n")
	r.handEdit("    group: platform\n", "    group: platform\n    alertmanager: {off: true}\n")
	p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit,
		Source: sourceDraftOf(hostedOnPrd("lgtm-we", config.ServeConfig{Group: "web"}, config.ServeConfig{Cluster: "lab"}))}})
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	if _, ok := change(p, "alerts.dev"); ok {
		t.Fatalf("dev's file group was adopted: %+v", p.Changes)
	}
	if _, ok := change(p, "alerts.prd"); ok {
		t.Fatalf("prd's file block was adopted: %+v", p.Changes)
	}
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	if _, ok := r.active.Subscription("dev"); ok {
		t.Fatal("dev joined by its file group, which is not applied")
	}
	if run, _ := r.cs.runningEntry("prd"); run.Alertmanager != nil {
		t.Fatal("prd's file block was published")
	}
	file := r.file()
	if !strings.Contains(file, "    group: web\n") || !strings.Contains(file, "alertmanager: {off: true}") || !strings.Contains(file, "      - cluster: lab\n") {
		t.Fatalf("the disk candidate: \n%s", file)
	}
}

// A cluster in the file and not running is validated for its later
// connect, and never becomes a reader.
func TestAFileOnlyClusterIsValidatedNotRead(t *testing.T) {
	r := newSourcesRig(t, hostedOnPrd("lgtm-we", config.ServeConfig{Cluster: "dev"}))
	r.handEdit("clusters:\n", "clusters:\n  - name: lab\n    context: lab-ctx\n")
	if p := r.svc.PlanSources(SourcesDraftDTO{Subscriptions: []SubscriptionEditDTO{{Cluster: "lab", Block: &SubscriptionDTO{Source: "nope"}}}}); p.Refused == "" {
		t.Fatal("an invalid block for a file-only cluster was planned")
	}
	p := r.svc.PlanSources(SourcesDraftDTO{Subscriptions: []SubscriptionEditDTO{{Cluster: "lab", Block: &SubscriptionDTO{Source: "lgtm-we", Match: map[string]string{"cluster": "lab"}}}}})
	if c, ok := change(p, "clusters.lab.alertmanager"); !ok || !strings.Contains(c.Label, "not running") {
		t.Fatalf("%+v", p)
	}
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	if _, ok := r.active.Subscription("lab"); ok {
		t.Fatal("a file-only cluster became a reader")
	}
	if !strings.Contains(r.file(), "  - name: lab\n    context: lab-ctx\n    alertmanager:\n      source: lgtm-we\n") {
		t.Fatalf("\n%s", r.file())
	}
}

// One transaction: an installation's removal with its subscriber's block,
// written whole; a removal leaving a block that names it is refused; a
// block that cannot be built refuses the whole plan, writing nothing.
func TestAnInstallationsRemovalIsOneTransaction(t *testing.T) {
	r := newSourcesRig(t, hostedOnPrd("lgtm-we", config.ServeConfig{Cluster: "prd"}))
	if res := r.applySources(r.svc.PlanSources(SourcesDraftDTO{Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: &SubscriptionDTO{Source: "lgtm-we"}}}})); !res.OK {
		t.Fatal(res.Error)
	}
	remove := &InstallationEditDTO{Op: installRemove, Source: SourceDraftDTO{ID: "lgtm-we"}}
	if p := r.svc.PlanSources(SourcesDraftDTO{Installation: remove}); !strings.Contains(p.Refused, "do not resolve") {
		t.Fatalf("a block still names it: %+v", p)
	}
	p := r.svc.PlanSources(SourcesDraftDTO{Installation: remove, Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: nil}}})
	if c, ok := change(p, "alerts.prd"); !ok || c.To != "discovered in this cluster, unfiltered: everything there is read as this cluster's" {
		t.Fatalf("prd's alerts after the removal: %+v", p.Changes)
	}
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	if file := r.file(); strings.Contains(file, "sources:") || strings.Contains(file, "alertmanager") {
		t.Fatalf("\n%s", file)
	}
	// a block on the list dash's line cannot be built: nothing is written
	r.handEdit("  - name: dev\n    context: dev-ctx\n", "  - alertmanager: {off: true}\n    name: dev\n    context: dev-ctx\n")
	before := r.file()
	p = r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installAdd, Source: sourceDraftOf(hostedOnPrd("again", config.ServeConfig{Cluster: "prd"}))},
		Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: &SubscriptionDTO{Source: "again"}}}})
	if !strings.Contains(p.Refused, "by hand") || r.file() != before {
		t.Fatalf("%+v", p)
	}
}

// "Use the file's values" adopts the sources block and the subscription
// blocks, never a legacy block or anything of S2's; it writes nothing.
func TestUseTheFilesValuesForSources(t *testing.T) {
	r := newSourcesRig(t)
	if p := r.svc.PlanSourcesFromFile(""); !strings.Contains(p.Refused, "the ones in force") {
		t.Fatalf("nothing differs: %+v", p)
	}
	r.handEdit("    context: dev-ctx\n", "    context: dev-ctx\n    group: web\n    alertmanager:\n      source: lgtm-we\n")
	r.handEdit("    group: platform\n", "    group: platform\n    alertmanager:\n      endpoint: https://am.example\n")
	doc := r.file() + "sources:\n  - id: lgtm-we\n    kind: alertmanager\n    host: prd\n    discover: true\n    memberLabel: cluster\n"
	r.handEdit(r.file(), doc)
	p := r.svc.PlanSourcesFromFile("")
	if p.Refused != "" || p.Writes {
		t.Fatalf("%+v", p)
	}
	if c, ok := change(p, "clusters.prd.alertmanager"); ok {
		t.Fatalf("the file's legacy block is in the plan: %+v", c)
	}
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	if sub, ok := r.active.Subscription("dev"); !ok || sub.Source.ID != "lgtm-we" {
		t.Fatal("the file's subscription was not put in force")
	}
	if run, _ := r.cs.runningEntry("prd"); run.Alertmanager != nil {
		t.Fatal("a legacy block was adopted")
	}
	if run, _ := r.cs.runningEntry("dev"); run.Group != "" {
		t.Fatal("S2's group was adopted")
	}
}

// The state: the sentence, each installation and each cluster's row.
func TestTheSourcesState(t *testing.T) {
	r := newSourcesRig(t, hostedOnPrd("lgtm-we", config.ServeConfig{Cluster: "prd"}, config.ServeConfig{Group: "platform"}))
	SetRunningEndpoint(r.cs, "dev", config.EndpointAlertmanager, nil, &config.EndpointConfig{Off: true})
	if _, err := r.active.Apply(r.active.Sources(), r.cs.fleetClusters()); err != nil {
		t.Fatal(err)
	}
	st := r.svc.GetSources("")
	if st.Sentence != "Alerts: 1 cluster from 1 installation, 1 off" {
		t.Fatalf("%q", st.Sentence)
	}
	if len(st.Installations) != 1 || st.Installations[0].Where != "Service monitoring/am:web in prd" || st.Installations[0].Credential != "not needed: read through its host" ||
		strings.Join(st.Installations[0].Subscribers, ",") != "prd" || st.Installations[0].Revision == "" {
		t.Fatalf("%+v", st.Installations)
	}
	rows := map[string]SubscriptionRowDTO{}
	for _, s := range st.Subscriptions {
		rows[s.Cluster] = s
	}
	if rows["prd"].Via != config.ViaByName || rows["prd"].Sentence != "from lgtm-we, hosted by prd, filtered by cluster=prd" {
		t.Fatalf("prd: %+v", rows["prd"])
	}
	if rows["dev"].Mode != config.AMOff || rows["dev"].Block == nil || !rows["dev"].Block.Off {
		t.Fatalf("dev: %+v", rows["dev"])
	}
	if !st.FileDiffers {
		t.Fatal("the file has no sources, unlike what is in force")
	}
}

// An edit of an installation the file no longer has is refused: the file
// is never edited from what is in force alone.
func TestAnEditOfAnInstallationTheFileLostIsRefused(t *testing.T) {
	r := newSourcesRig(t, hostedOnPrd("lgtm-we", config.ServeConfig{Cluster: "prd"}))
	v, _ := config.FileVersion(r.path)
	none := []config.SourceConfig{}
	if _, err := config.EditSourcesAt(r.path, v, config.SourcesEdit{Sources: &none}); err != nil {
		t.Fatal(err)
	}
	before := r.file()
	p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: sourceDraftOf(hostedOnPrd("lgtm-we", config.ServeConfig{Cluster: "dev"}))}})
	if !strings.Contains(p.Refused, "fleet.yaml no longer has lgtm-we") || r.file() != before {
		t.Fatalf("%+v", p)
	}
}

// A legacy row says its scope sentence (S3a §3.5, §8), as fleet.yaml says
// its block: where, the tenant, and the filter or that nothing is filtered;
// an endpoint by its origin alone.
func TestALegacyRowSaysItsScopeSentence(t *testing.T) {
	const unfiltered = "unfiltered: everything there is read as this cluster's"
	for _, tc := range []struct {
		name, mode string
		block      *config.EndpointConfig
		want       string
	}{
		{"its own Service, filtered", config.AMService, &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "alertmanager-main", Port: "web"}, Match: map[string]string{"cluster": "dev", "env": "dev"}},
			"from the Service monitoring/alertmanager-main:web on this cluster, filtered by cluster=dev, env=dev"},
		{"its own endpoint, a tenant", config.AMDirect, &config.EndpointConfig{Endpoint: "https://am.example/prefix?token=never", Tenant: "ops"},
			"from its own endpoint at https://am.example:443, tenant ops, " + unfiltered},
		{"discovery, scoped by its block", config.AMDiscovered, &config.EndpointConfig{Tenant: "ops", Match: map[string]string{"cluster": "dev"}},
			"discovered in this cluster, tenant ops, filtered by cluster=dev"},
		{"discovery, no block", config.AMDiscovered, nil, "discovered in this cluster, " + unfiltered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopeSentence(config.EndpointAlertmanager, config.AMResolution{Mode: tc.mode}, config.SourceConfig{}, tc.block); got != tc.want {
				t.Fatalf("%q", got)
			}
		})
	}
	// and the page's row says it
	r := newSourcesRig(t)
	SetRunningEndpoint(r.cs, "dev", config.EndpointAlertmanager, nil, &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "alertmanager-main", Port: "web"}, Match: map[string]string{"cluster": "dev"}})
	seen := false
	for _, row := range r.svc.GetSources("").Subscriptions {
		if row.Cluster == "dev" {
			seen = true
			if row.Sentence != "from the Service monitoring/alertmanager-main:web on this cluster, filtered by cluster=dev" || !row.Legacy {
				t.Fatalf("dev's row: %+v", row)
			}
		}
	}
	if !seen {
		t.Fatal("no row for dev")
	}
}

// A plan's member sentence after the change reads the cluster's block as
// the plan leaves it: a subscription block with a match removed leaves
// discovery, unfiltered - never the old block's match.
func TestAPlansSentenceAfterReadsTheBlockItLeaves(t *testing.T) {
	r := newSourcesRig(t, hostedOnPrd("lgtm-we"))
	block := &SubscriptionDTO{Source: "lgtm-we", Match: map[string]string{"team": "platform"}}
	r.applySources(r.svc.PlanSources(SourcesDraftDTO{Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: block}}}))
	p := r.svc.PlanSources(SourcesDraftDTO{Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: nil}}})
	c, ok := change(p, "alerts.dev")
	if p.Refused != "" || !ok || c.From != "from lgtm-we, hosted by prd, filtered by cluster=dev, team=platform" || c.To != "discovered in this cluster, unfiltered: everything there is read as this cluster's" {
		t.Fatalf("%+v %+v", p.Refused, p.Changes)
	}
}
