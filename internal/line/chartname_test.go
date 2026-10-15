package line

import (
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
)

// Flux chart units keyed by their recorded chart name (Codex's verdict on
// 9c51f11, variant B): every compared fact is one Flux's history records,
// nothing is attached to the current spec or source, and the key discloses
// that no repository is recorded.

func recorded(name, version, status string) flux.HelmHistory {
	return flux.HelmHistory{ChartName: name, ChartVersion: version, Status: status}
}

func fluxRelease(hist ...flux.HelmHistory) flux.Resource {
	return flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "shop", Name: "web", Chart: "./charts/web",
		SourceKind: "GitRepository", SourceNamespace: "shop", SourceName: "repo", Ready: flux.Ready, History: hist}
}

func gitSourceAt(url string) Object[flux.Source] {
	return Object[flux.Source]{Data: flux.Source{Generation: 1, ObservedGeneration: 1, Kind: "GitRepository", Namespace: "shop", Name: "repo", URL: url, Ready: flux.Ready}}
}

func oneUnit(t *testing.T, r flux.Resource, s Object[flux.Source]) Unit {
	t.Helper()
	us, ex := Units(reviewHelm(r, s))
	if len(us) != 1 {
		t.Fatalf("units %d, exclusions %+v", len(us), ex)
	}
	return us[0]
}

// The recorded name-key and version survive a path edit, a source edit or
// recreation, and a suspension: history changes only with Helm actions.
func TestRecordedChartSurvivesSpecSourceAndSuspension(t *testing.T) {
	want := Key{Kind: KeyChartName, RefName: "old"}
	check := func(name string, u Unit) {
		t.Helper()
		if u.Key != want || u.Applied == nil || u.Applied.Version != "1.0.0" {
			t.Errorf("%s: %+v applied %+v", name, u.Key, u.Applied)
		}
	}
	// Real parsers: a path edit pending (generation 2), and a suspended one.
	check("path edit", oneUnit(t, regress3528HR(2, 1, "./charts/new", false), regress3528Source("https://example.com/repo", 1)))
	check("suspended edit", oneUnit(t, regress3528HR(2, 2, "./charts/new", true), regress3528Source("https://example.com/repo", 1)))
	// A source URL edit and a recreated source: the key claims no
	// repository, so neither moves the record.
	before := oneUnit(t, regress3528HR(1, 1, "./charts/web", false), regress3528Source("https://example.com/old", 1))
	after := oneUnit(t, regress3528HR(1, 1, "./charts/web", false), regress3528Source("https://example.com/new", 2))
	check("before", before)
	check("source edited", after)
	if before.Key != after.Key || after.Key.Repo != "" {
		t.Fatalf("no repository attribution: %+v / %+v", before.Key, after.Key)
	}
}

func TestRecordedChartHistoryRules(t *testing.T) {
	src := gitSourceAt("https://example.com/repo")
	t.Run("failed only: the key from the newest record, applied unknown, a historical attempt", func(t *testing.T) {
		u := oneUnit(t, fluxRelease(recorded("web", "1.1.0", "failed")), src)
		if u.Key.RefName != "web" || u.Applied != nil || u.Attempted == nil || u.Attempted.Version != "1.1.0" {
			t.Fatalf("%+v", u)
		}
	})
	t.Run("no history: chart identity unknown", func(t *testing.T) {
		if us, ex := Units(reviewHelm(fluxRelease(), src)); len(us) != 0 || ex[0].Reason != ExcludedNoChartID {
			t.Fatalf("%+v %+v", us, ex)
		}
	})
	t.Run("a nameless newest deployment never falls back to an older named one", func(t *testing.T) {
		r := fluxRelease(recorded("", "2.0.0", "deployed"), recorded("web", "1.0.0", "deployed"))
		if us, ex := Units(reviewHelm(r, src)); len(us) != 0 || ex[0].Reason != ExcludedNoChartID {
			t.Fatalf("%+v %+v", us, ex)
		}
	})
	t.Run("a versionless deployment: applied unknown", func(t *testing.T) {
		if u := oneUnit(t, fluxRelease(recorded("web", "", "deployed")), src); u.Applied != nil {
			t.Fatalf("%+v", u.Applied)
		}
	})
	t.Run("a failed attempt of another chart is not this key's attempt", func(t *testing.T) {
		u := oneUnit(t, fluxRelease(recorded("api", "3.0.0", "failed"), recorded("web", "1.0.0", "deployed")), src)
		if u.Key.RefName != "web" || u.Applied.Version != "1.0.0" || u.Attempted != nil {
			t.Fatalf("%+v", u)
		}
	})
	t.Run("a missing or unread source: still extracted, failing from the release alone", func(t *testing.T) {
		r := fluxRelease(recorded("web", "1.0.0", "deployed"))
		r.Ready = flux.Failed
		in := station("c", "dev", func(in *StationInput) { in.HelmReleases = []Object[flux.Resource]{{UID: "hr", Data: r}} })
		us, _ := Units(in)
		if len(us) != 1 || !us[0].Failing || us[0].Applied.Version != "1.0.0" {
			t.Fatalf("%+v", us)
		}
	})
	t.Run("a configured chart name that differs from the record is current context", func(t *testing.T) {
		r := fluxRelease(recorded("web", "1.0.0", "deployed"))
		r.SourceKind, r.Chart = "HelmRepository", "web2"
		hr := Object[flux.Source]{Data: flux.Source{Generation: 1, ObservedGeneration: 1, Kind: "HelmRepository", Namespace: "shop", Name: "repo", URL: "https://charts.example"}}
		u := oneUnit(t, r, hr)
		if u.Key.RefName != "web" || !u.ConfiguredChartDiffers || u.SourceChanged {
			t.Fatalf("%+v", u)
		}
	})
}

func TestRecordedChartKeyNeverMixesAndDisclosesItself(t *testing.T) {
	fluxKey := Key{Kind: KeyChartName, RefName: "web"}
	argoKey := Key{Kind: KeyChart, Repo: "charts.example", RefName: "web"}
	if fluxKey == argoKey || !strings.Contains(fluxKey.String(), "by recorded name; repository not recorded") {
		t.Fatalf("keys: %q / %q", fluxKey, argoKey)
	}
	// An Argo chart source and a Flux release of the same chart on one
	// station are two units on two keys.
	in := station("c", "dev", func(in *StationInput) {
		in.Sources = []Object[flux.Source]{gitSourceAt("https://example.com/repo")}
		in.HelmReleases = []Object[flux.Resource]{{UID: "hr", Data: fluxRelease(recorded("web", "1.0.0", "deployed"))}}
		src := argo.Source{RepoURL: "https://charts.example", Chart: "web", TargetRevision: "1.*"}
		in.Apps = []Object[argo.App]{argoApp("web", []argo.Source{src}, deployed([]argo.Source{src}, "1.0.0"))}
	})
	us, _ := Units(in)
	if len(us) != 2 || us[0].Key == us[1].Key {
		t.Fatalf("%+v", us)
	}
}

// R's tie-break for a recorded-name key is the version string's lexical
// order, disclosed - never the current source's fetch time. Held is never
// inferred from a historical attempt plus a current failure.
func TestRecordedChartTieBreakAndNoHeld(t *testing.T) {
	stationAt := func(cluster, env, version string, fetched time.Time, mod ...func(*flux.Resource)) StationInput {
		r := fluxRelease(recorded("web", version, "deployed"))
		for _, m := range mod {
			m(&r)
		}
		s := gitSourceAt("https://example.com/repo")
		s.Data.ArtifactUpdated = fetched
		in := reviewHelm(r, s)
		in.Cluster, in.Env = cluster, env
		return in
	}
	key := Key{Kind: KeyChartName, RefName: "web"}
	t0 := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	for _, newer := range []string{"a", "b"} { // whichever source fetched last, R is the same
		fa, fb := t0, t0.Add(time.Hour)
		if newer == "a" {
			fa, fb = fb, fa
		}
		in := Input{Stations: []StationInput{stationAt("a", "dev", "1.9.0", fa), stationAt("b", "prd", "1.10.0", fb)}, Selector: &key}
		in.Read = allOK(in.Stations...)
		l := Build(in)
		if l.R != "1.10.0" || !l.RTieBroken || l.RTieBy != TieByVersionString {
			t.Fatalf("newer %s: R %q broken %v by %q", newer, l.R, l.RTieBroken, l.RTieBy)
		}
	}
	// A failed historical attempt of R plus a current failure: never held.
	failing := func(r *flux.Resource) {
		r.Ready = flux.Failed
		r.History = []flux.HelmHistory{recorded("web", "2.0.0", "failed"), recorded("web", "1.0.0", "deployed")}
	}
	in := Input{Stations: []StationInput{stationAt("a", "dev", "2.0.0", t0), stationAt("b", "stg", "2.0.0", t0), stationAt("c", "prd", "1.0.0", t0, failing)}, Selector: &key}
	in.Read = allOK(in.Stations...)
	l := Build(in)
	if c := stateOf(l, "c"); l.R != "2.0.0" || c.Held || !c.Flags.Failing || c.State != StateDiffers {
		t.Fatalf("R %q, c %+v", l.R, c)
	}
}

// Codex's review of a9f7ef9: a recorded-name unit's comparison depends on its
// history alone - an unread current source is disclosed context, never a
// gate - and a historical attempt is never current syncing.
func TestRecordedChartSourceIsContextAndAttemptsAreHistory(t *testing.T) {
	key := Key{Kind: KeyChartName, RefName: "web"}
	in := func(cluster, env string) StationInput {
		s := station(cluster, env, func(in *StationInput) {
			in.Sources = []Object[flux.Source]{gitSourceAt("https://example.com/repo")}
			in.HelmReleases = []Object[flux.Resource]{{UID: "hr-" + cluster, Data: fluxRelease(recorded("web", "1.0.0", "deployed"))}}
		})
		unread := map[string]string{}
		for k, v := range allKindsOK {
			unread[k] = v
		}
		unread["gitrepositories"] = ReadError
		s.KindRead = unread
		return s
	}
	stations := []StationInput{in("a", "dev"), in("b", "prd")}
	read := map[string]string{}
	for i := range stations {
		cov := map[string]string{"kustomizations": "absent", "helmreleases": "live", "applications": "absent", "gitrepositories": "unreadable"}
		read[stations[i].Cluster] = ResolveRead(&stations[i], cov, Availability{Usable: true})
	}
	l := Build(Input{Stations: stations, Read: read, Selector: &key})
	a := stateOf(l, "a")
	if read["a"] != ReadOK || a.State != StateMatches || l.Compared != 2 || a.Units[0].ContextRead != ReadError || a.Units[0].Read != ReadOK {
		t.Fatalf("read %q, state %q, compared %d, unit %+v", read["a"], a.State, l.Compared, a.Units[0])
	}

	r := fluxRelease(recorded("web", "1.1.0", "failed"), recorded("web", "1.0.0", "deployed"))
	r.Suspended = true
	u := oneUnit(t, r, gitSourceAt("https://example.com/repo"))
	if u.Attempted == nil || u.Syncing || u.Failing {
		t.Fatalf("a suspended release keeps its historical attempt, never syncing: %+v", u)
	}
	r.Suspended = false
	if u := oneUnit(t, r, gitSourceAt("https://example.com/repo")); u.Syncing {
		t.Fatalf("a historical attempt is not current syncing: %+v", u)
	}
	r.Stale = true
	if u := oneUnit(t, r, gitSourceAt("https://example.com/repo")); !u.Syncing {
		t.Fatalf("an unobserved spec is: %+v", u)
	}
}
