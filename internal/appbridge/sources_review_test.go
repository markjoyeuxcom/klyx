package appbridge

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/readfence"
)

// Codex's review of S3a-4a: its probes, as regression tests.

// R2: a credential action's installation is resolved inside the fleet
// Apply lock: one that moved while the action waited for it changes
// neither the record nor its revision.
func TestACredentialActionChecksItsRevisionInsideTheFleetLock(t *testing.T) {
	for _, action := range []string{"store", "remove"} {
		for _, move := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/unchanged", true: "/moved at the lock"}[move], func(t *testing.T) {
				old := directSource("shared", "https://am.example/before")
				es, a, store, revs := sourceCredRig(t, old)
				shown := revisionOf(a, "shared")
				if res := es.SetSourceCredential("shared", shown, "INITIAL"); !res.OK {
					t.Fatal(res.Message)
				}
				account := endpointcred.SourceAccount("shared")
				initial, _ := store.Get(account)
				credRev := revs.Revision("source/shared", config.EndpointAlertmanager)
				es.fleetLock = func() func() {
					if move {
						next := old
						next.Endpoint = "https://am.example/after"
						if _, err := a.Apply([]config.SourceConfig{next}, []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}); err != nil {
							t.Fatal(err)
						}
					}
					return func() {}
				}
				var res EndpointActionDTO
				if action == "store" {
					res = es.SetSourceCredential("shared", shown, "REPLACEMENT")
				} else {
					res = es.RemoveSourceCredential("shared", shown)
				}
				if !move {
					if !res.OK {
						t.Fatalf("%+v", res)
					}
					return
				}
				after, err := store.Get(account)
				if res.OK || err != nil || after != initial || revs.Revision("source/shared", config.EndpointAlertmanager) != credRev || !strings.Contains(res.Message, "changed since it was shown") {
					t.Fatalf("a stale action took effect: %+v", res)
				}
			})
		}
	}
}

// R1: "Use the file's values" publishes nothing when the file changed
// after its Apply read it.
func TestFileAdoptionRechecksTheFileAtPublication(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "a late file edit"}[move], func(t *testing.T) {
			old := hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"})
			r := newSourcesRig(t, old)
			next := old
			next.Tenant = "ops"
			v, _ := config.FileVersion(r.path)
			if _, err := config.EditSourcesAt(r.path, v, config.SourcesEdit{Sources: &[]config.SourceConfig{next}}); err != nil {
				t.Fatal(err)
			}
			p := r.svc.PlanSourcesFromFile()
			if p.Refused != "" || p.Writes {
				t.Fatalf("%+v", p)
			}
			oldRev := r.active.Revision()
			prior := r.svc.d.AlertWork
			r.svc.d.AlertWork = func(clusters []string) []PlanWorkDTO {
				work := prior(clusters)
				if move {
					raw := r.file()
					if err := os.WriteFile(r.path, []byte(strings.Replace(raw, "tenant: ops", "tenant: later", 1)), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return work
			}
			res := r.svc.ApplySources(p.Digest)
			if !move {
				if !res.OK {
					t.Fatalf("%+v", res)
				}
				return
			}
			if res.OK || res.Plan == nil || r.published != 0 || r.active.Revision() != oldRev {
				t.Fatalf("a changed file was adopted: %+v, published %d", res, r.published)
			}
		})
	}
}

// R4: every material input of an installation that changes is said, with
// what it was and what it becomes.
func TestAPlanSaysEveryChangedInput(t *testing.T) {
	for field, want := range map[string][2]string{
		"endpoint":     {"https://am.example:443/before", "https://am.example:443/after"},
		"Service":      {"http monitoring/am:web, path /before", "https monitoring/am:web, path /after"},
		"auth":         {"basic as before", "basic as after"},
		"name":         {"before", "after"},
		"member label": {"cluster", "team"},
	} {
		t.Run(field, func(t *testing.T) {
			old := hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"})
			switch field {
			case "endpoint", "auth":
				old = directSource("shared", "https://am.example/before")
				old.Auth = &config.EndpointAuth{Type: "basic", Username: "before"}
			case "Service":
				old.ServiceRef.Path = "/before"
			case "name":
				old.Name = "before"
			}
			r := newSourcesRig(t, old)
			d := sourceDraftOf(old)
			switch field {
			case "endpoint":
				d.Endpoint = "https://am.example/after"
			case "auth":
				d.AuthUsername = "after"
			case "Service":
				d.ServiceRef.Path, d.ServiceRef.Scheme = "/after", "https"
			case "name":
				d.Name = "after"
			case "member label":
				d.MemberLabel = "team"
			}
			p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
			if p.Refused != "" {
				t.Fatal(p.Refused)
			}
			c, ok := change(p, "sources.shared."+strings.ReplaceAll(field, " ", "-"))
			if !ok || c.From != want[0] || c.To != want[1] || c.Label != "shared's "+field {
				t.Fatalf("%+v", p.Changes)
			}
		})
	}
}

// pendingExplain is a provider whose call waits, after its evidence was
// read, for the test to release it.
type pendingExplain struct{ entered, release chan struct{} }

func (p *pendingExplain) Complete(ctx context.Context, _ ai.Request) (ai.Result, error) {
	close(p.entered)
	select {
	case <-p.release:
	case <-ctx.Done():
		return ai.Result{}, ctx.Err()
	}
	if err := readfence.Check(ctx); err != nil {
		return ai.Result{}, err
	}
	return ai.Result{Text: "synthetic"}, nil
}

// subscriptionCheck is a lineage check by the subscription's revision in
// force.
func subscriptionCheck(t *testing.T, r *sourcesRig) {
	t.Helper()
	SetAlertLineageCheck(func(cluster string, l alertmanager.Lineage) string {
		if cur, ok := r.active.Subscription(cluster); !ok || cur.SubRev != l.SubRev {
			return "the subscription changed"
		}
		return ""
	})
	t.Cleanup(func() { alertLineageCheck.Store(nil) })
}

// R6: a running Explain holding the member's alerts is named in the plan's
// work, and is refused at its send after the Apply.
func TestAPlanNamesAnExplainHoldingItsAlerts(t *testing.T) {
	old := hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"})
	r := newSourcesRig(t, old)
	subscriptionCheck(t, r)
	sub, _ := r.active.Subscription("dev")
	lineage := alertmanager.Lineage{Mode: config.AMSubscribed, Installation: old.ID, SubRev: sub.SubRev, InstRev: sub.InstRev, Identity: "read-old"}
	chat := newToolsFixture(t)
	r.svc.d.AlertWork = chat.svc.AlertWork // the production wiring
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"/pods/ns/p": {Kind: "Pod", Namespace: "ns", Name: "p", UID: "p-uid"}}, rows: map[string]observe.Rows{
		healthsrc.TaskAlerts + "/alerts": {Part: observe.Part{ID: "alerts"}, Rows: healthsrc.AlertRows{Lineage: lineage, Alerts: []healthsrc.AlertRow{alertRow("Mine", "active", true, "p-uid", healthsrc.SubjectFound)}}},
	}}
	provider := &pendingExplain{entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewAIService(func(string) (AIConn, bool) { return conn, true }, &AIState{Provider: provider, ProviderID: ai.ProviderAnthropic, Origin: ai.DefaultAnthropicOrigin}, func(string) bool { return false }, nil)
	var once sync.Once
	release := func() { once.Do(func() { close(provider.release) }) }
	t.Cleanup(release)
	done := make(chan AIResultDTO, 1)
	go func() {
		done <- svc.Explain("dev", ExplainTargetDTO{Kind: "object", Version: "v1", Plural: "pods", Namespace: "ns", Name: "p", UID: "p-uid"})
	}()
	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Explain did not reach its send")
	}
	d := sourceDraftOf(old)
	d.Tenant = "ops"
	p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
	named := false
	for _, w := range p.Ends {
		named = named || w.Kind == "explain" && w.Cluster == "dev" && strings.HasPrefix(w.ID, "explain:")
	}
	if !named {
		t.Fatalf("the plan does not name the Explain: %+v", p.Ends)
	}
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	release()
	if res := <-done; res.OK || !strings.Contains(res.Error, "subscription changed") {
		t.Fatalf("the Explain was not refused: %+v", res)
	}
	if left := explainAlertWork(map[string]bool{"dev": true}); len(left) != 0 {
		t.Fatalf("a finished Explain is still named: %+v", left)
	}
}

// R6: a read made while an Apply moves the terms records its lineage only
// after the publication, and is then refused: no work holding the old
// terms starts unnamed, and the conversation goes on.
func TestNoAlertDependencyStartsDuringAnApply(t *testing.T) {
	old := hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"})
	r := newSourcesRig(t, old)
	subscriptionCheck(t, r)
	sub, _ := r.active.Subscription("dev")
	f := newToolsFixture(t, calls(alertsCall), answer("read during the apply"), answer("second"))
	conn := alertsLineageChat()
	row := conn.rows[healthsrc.TaskAlerts+"/alerts"]
	ar := row.Rows.(healthsrc.AlertRows)
	ar.Lineage = alertmanager.Lineage{Mode: config.AMSubscribed, Installation: old.ID, SubRev: sub.SubRev, InstRev: sub.InstRev, Identity: "read-old"}
	row.Rows = ar
	conn.rows[healthsrc.TaskAlerts+"/alerts"] = row
	f.useConn(conn)
	r.svc.d.AlertWork = f.svc.AlertWork
	d := sourceDraftOf(old)
	d.Tenant = "ops"
	p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	var st ChatStateDTO
	asked := make(chan struct{})
	r.svc.d.AlertWork = func(clusters []string) []PlanWorkDTO {
		work := f.svc.AlertWork(clusters)
		// a conversation starts and reads dev's alerts while the Apply holds
		// its final work list
		go func() {
			defer close(asked)
			st = f.svc.Start("dev")
			st = f.svc.Ask("dev", st.ConversationID, "is web alerting?", nil)
		}()
		time.Sleep(50 * time.Millisecond)
		return work
	}
	if res := r.svc.ApplySources(p.Digest); !res.OK {
		t.Fatal(res.Error)
	}
	<-asked
	if len(st.Exchanges) == 0 {
		t.Fatalf("the conversation did not run: %+v", st)
	}
	ex := st.Exchanges[0]
	if ex.Outcome != "answered" || ex.Reads[0].Outcome == "completed" || len(f.lineagesOf(st.ConversationID)) != 0 {
		t.Fatalf("a read under the old terms was recorded during the Apply: %+v %+v", ex, f.lineagesOf(st.ConversationID))
	}
	if st = f.svc.Ask("dev", st.ConversationID, "and now?", nil); st.Ended != "" {
		t.Fatalf("the conversation was ended: %q", st.Ended)
	}
}

// R7: installations reordered in the file are not a difference.
func TestReorderedInstallationsAreNotADifference(t *testing.T) {
	a := hostedOnPrd("alpha", config.ServeConfig{Cluster: "dev"})
	b := hostedOnPrd("beta", config.ServeConfig{Cluster: "prd"})
	r := newSourcesRig(t, a, b)
	v, _ := config.FileVersion(r.path)
	reordered := []config.SourceConfig{b, a}
	if _, err := config.EditSourcesAt(r.path, v, config.SourcesEdit{Sources: &reordered}); err != nil {
		t.Fatal(err)
	}
	if r.svc.GetSources().FileDiffers {
		t.Fatal("an order-only difference is offered")
	}
	if p := r.svc.PlanSourcesFromFile(); !strings.Contains(p.Refused, "the ones in force") {
		t.Fatalf("%+v", p)
	}
}

// Codex's re-review of S3a-4a, RR1: a change is decided from the inputs,
// never from their words, and what the words cannot show is named - a
// query or fragment withheld, or that only the writing changed - alone and
// beside another change.
func TestAChangeTheWordsCannotShowIsNamed(t *testing.T) {
	const at = "https://am.example:443/prefix"
	for _, tc := range []struct {
		name, from, to string
		edit           func(*SourceDraftDTO)
		id, was, now   string
	}{
		{name: "the query alone", from: "https://am.example/prefix?route=before", to: "https://am.example/prefix?route=after",
			id: "endpoint", was: at + " (with a query)", now: at + " (with a query): its query changed, and is not shown"},
		{name: "the query and the name", from: "https://am.example/prefix?route=before", to: "https://am.example/prefix?route=after",
			edit: func(d *SourceDraftDTO) { d.Name = "shared AM" },
			id:   "endpoint", was: at + " (with a query)", now: at + " (with a query): its query changed, and is not shown"},
		{name: "the query and the path", from: "https://am.example/prefix?route=before", to: "https://am.example/other?route=after",
			id: "endpoint", was: at + " (with a query)", now: "https://am.example:443/other (with a query): its query changed, and is not shown"},
		{name: "the fragment alone", from: "https://am.example/prefix#before", to: "https://am.example/prefix#after",
			id: "endpoint", was: at, now: at + ": its fragment changed, and is not shown"},
		{name: "the query and the fragment", from: "https://am.example/prefix?route=before#before", to: "https://am.example/prefix?route=after",
			id: "endpoint", was: at + " (with a query)", now: at + " (with a query): its query and fragment changed, and are not shown"},
		{name: "only the writing", from: "https://AM.example/prefix", to: "https://am.example:443/prefix",
			id: "endpoint", was: at, now: at + ": written differently, to the same origin and path"},
		{name: "a query added", from: "https://am.example/prefix", to: "https://am.example/prefix?route=after",
			id: "endpoint", was: at, now: at + " (with a query)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := directSource("shared", tc.from)
			r := newSourcesRig(t, old)
			d := sourceDraftOf(old)
			d.Endpoint = tc.to
			if tc.edit != nil {
				tc.edit(&d)
			}
			p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
			if p.Refused != "" {
				t.Fatalf("refused: %s", p.Refused)
			}
			c, ok := change(p, "sources.shared."+tc.id)
			if !ok || c.From != tc.was || c.To != tc.now {
				t.Fatalf("%+v", p.Changes)
			}
			for _, ch := range p.Changes {
				if strings.Contains(ch.From+ch.To, "route=") || strings.Contains(ch.From+ch.To, "before") || strings.Contains(ch.From+ch.To, "after") {
					t.Fatalf("a withheld part is shown: %+v", ch)
				}
			}
			if _, ok := change(p, "sources.shared.name"); ok != (tc.edit != nil) {
				t.Fatalf("the name: %+v", p.Changes)
			}
			if res := r.svc.ApplySources(p.Digest); !res.OK {
				t.Fatalf("apply: %+v", res)
			}
		})
	}
	t.Run("a Service scheme written out", func(t *testing.T) {
		old := hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"})
		old.ServiceRef.Scheme = ""
		r := newSourcesRig(t, old)
		d := sourceDraftOf(old)
		d.ServiceRef.Scheme = "http"
		p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
		c, ok := change(p, "sources.shared.Service")
		if p.Refused != "" || !ok || c.From == "" || c.To != c.From+": written differently, with the same effect" {
			t.Fatalf("%+v %+v", p.Refused, p.Changes)
		}
	})
}

// Codex's second re-review of S3a-4a, R2-1: what an installation serves is
// compared as typed selectors, never as their words, and a value that could
// read as the words around it is quoted - a group "blue, group green" is
// not the groups blue and green. Order alone changes nothing.
func TestSelectorsAndServicesAreComparedAsTheirInputs(t *testing.T) {
	for _, withName := range []bool{false, true} {
		t.Run(map[bool]string{false: "the selectors alone", true: "the selectors and the name"}[withName], func(t *testing.T) {
			old := hostedOnPrd("shared", config.ServeConfig{Group: "blue, group green"})
			r := newSourcesRig(t, old)
			d := sourceDraftOf(old)
			d.ServesGroups = []string{"blue", "green"}
			if withName {
				d.Name = "shared AM"
			}
			p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
			if p.Refused != "" {
				t.Fatalf("refused: %s", p.Refused)
			}
			c, ok := change(p, "sources.shared.serves")
			if !ok || c.From != `group "blue, group green"` || c.To != "group blue, group green" {
				t.Fatalf("%+v", p.Changes)
			}
			if _, ok := change(p, "sources.shared.name"); ok != withName {
				t.Fatalf("the name: %+v", p.Changes)
			}
			if res := r.svc.ApplySources(p.Digest); !res.OK {
				t.Fatalf("apply: %+v", res)
			}
			if got := r.active.Sources()[0].Serves; len(got) != 2 {
				t.Fatalf("in force: %+v", got)
			}
		})
	}
	t.Run("only their order", func(t *testing.T) {
		old := hostedOnPrd("shared", config.ServeConfig{Group: "blue"}, config.ServeConfig{Group: "green"})
		r := newSourcesRig(t, old)
		d := sourceDraftOf(old)
		d.ServesGroups = []string{"green", "blue"}
		p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
		if !strings.Contains(p.Refused, "changes nothing") {
			t.Fatalf("%+v %+v", p.Refused, p.Changes)
		}
	})
	t.Run("their order, beside the name", func(t *testing.T) {
		old := hostedOnPrd("shared", config.ServeConfig{Group: "blue"}, config.ServeConfig{Group: "green"})
		r := newSourcesRig(t, old)
		d := sourceDraftOf(old)
		d.ServesGroups = []string{"green", "blue"}
		d.Name = "shared AM"
		p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
		if p.Refused != "" || len(p.Changes) == 0 {
			t.Fatalf("%+v", p)
		}
		if _, ok := change(p, "sources.shared.serves"); ok {
			t.Fatalf("an order-only change of what it serves is named: %+v", p.Changes)
		}
	})
	t.Run("a group for a cluster of the same name", func(t *testing.T) {
		old := hostedOnPrd("shared", config.ServeConfig{Group: "dev"})
		r := newSourcesRig(t, old)
		d := sourceDraftOf(old)
		d.ServesGroups, d.ServesClusters = nil, []string{"dev"}
		p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
		c, ok := change(p, "sources.shared.serves")
		if p.Refused != "" || !ok || c.From != "group dev" || c.To != "dev" {
			t.Fatalf("%+v %+v", p.Refused, p.Changes)
		}
	})
	t.Run("a Service whose parts read alike", func(t *testing.T) {
		old := hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"})
		old.ServiceRef = &config.MetricsServiceRef{Namespace: "a/b", Name: "c", Port: "web"}
		r := newSourcesRig(t, old)
		d := sourceDraftOf(old)
		d.ServiceRef = &ServiceRefDTO{Namespace: "a", Name: "b/c", Port: "web"}
		p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
		c, ok := change(p, "sources.shared.Service")
		if p.Refused != "" || !ok || c.From != `http "a/b"/c:web` || c.To != `http a/"b/c":web` {
			t.Fatalf("%+v %+v", p.Refused, p.Changes)
		}
	})
}

// The same words with different inputs are never called the same effect,
// unless the inputs say so: a Service's scheme written out as its default.
func TestSameWordsAreSameEffectOnlyWhenTheInputsSaySo(t *testing.T) {
	svc := func(scheme, ns string) config.SourceConfig {
		return config.SourceConfig{ServiceRef: &config.MetricsServiceRef{Scheme: scheme, Namespace: ns, Name: "am", Port: "web"}}
	}
	for _, tc := range []struct {
		name, field string
		was, now    config.SourceConfig
		want        string
	}{
		{"a scheme written out", "Service", svc("", "monitoring"), svc("http", "monitoring"), ": written differently, with the same effect"},
		{"another Service", "Service", svc("", "monitoring"), svc("http", "other"), ": changed in a way these words cannot show"},
		{"a Service added", "Service", config.SourceConfig{}, svc("http", "monitoring"), ": changed in a way these words cannot show"},
		{"any other field", "serves", config.SourceConfig{}, config.SourceConfig{}, ": changed in a way these words cannot show"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unshownChange(tc.field, tc.was, tc.now, true); got != tc.want {
				t.Fatalf("%q", got)
			}
			if got := unshownChange(tc.field, tc.was, tc.now, false); got != "" {
				t.Fatalf("different words named again: %q", got)
			}
		})
	}
}

// Codex's third re-review of S3a-4a, R3-1: a value holding a joined
// string's delimiters never reads as several - what an installation serves,
// its Service and its auth are compared as encoded values.
func TestAValueHoldingDelimitersIsOneValue(t *testing.T) {
	for _, withName := range []bool{false, true} {
		t.Run(map[bool]string{false: "the selectors alone", true: "the selectors and the name"}[withName], func(t *testing.T) {
			old := hostedOnPrd("shared", config.ServeConfig{Group: "blue\x01group\x00green"})
			r := newSourcesRig(t, old)
			d := sourceDraftOf(old)
			d.ServesGroups = []string{"blue", "green"}
			if withName {
				d.Name = "shared AM"
			}
			p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
			if p.Refused != "" {
				t.Fatalf("refused: %s", p.Refused)
			}
			c, ok := change(p, "sources.shared.serves")
			if !ok || c.From != `group "blue\x01group\x00green"` || c.To != "group blue, group green" {
				t.Fatalf("%+v", p.Changes)
			}
			if res := r.svc.ApplySources(p.Digest); !res.OK {
				t.Fatalf("apply: %+v", res)
			}
		})
	}
	for _, tc := range []struct {
		name     string
		was, now config.SourceConfig
	}{
		{"a Service", config.SourceConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "a\x00b", Name: "c", Port: "web"}},
			config.SourceConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "a", Name: "b\x00c", Port: "web"}}},
		{"an auth", config.SourceConfig{Auth: &config.EndpointAuth{Type: "basic\x00x", Username: "y"}},
			config.SourceConfig{Auth: &config.EndpointAuth{Type: "basic", Username: "x\x00y"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := map[string]string{"a Service": "Service", "an auth": "auth"}[tc.name]
			if installationInputs(tc.was)[f] == installationInputs(tc.now)[f] {
				t.Fatalf("two different %s inputs compare equal", f)
			}
		})
	}
}
