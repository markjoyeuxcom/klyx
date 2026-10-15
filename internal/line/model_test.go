package line

import (
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
)

// The Line §12, "Line": comparison units, key selection, R, states, flags
// and held, as table-style tests over plain station inputs.

const repo = "https://github.com/acme/fleet"

func sha(c byte) string { return strings.Repeat(string(c), 40) }

func ks(name, rev string, mod ...func(*flux.Resource)) Object[flux.Resource] {
	r := flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: name, Ready: flux.Ready, Revision: rev,
		SourceKind: "GitRepository", SourceName: "fleet", SourceNamespace: "flux-system"}
	for _, m := range mod {
		m(&r)
	}
	return Object[flux.Resource]{UID: "uid-" + name, Data: r}
}

func gitSrc(name, url, refKind, refName, rev string) Object[flux.Source] {
	return Object[flux.Source]{UID: "uid-src-" + name, Data: flux.Source{Generation: 1, ObservedGeneration: 1, Kind: "GitRepository", Namespace: "flux-system", Name: name, Ready: flux.Ready,
		URL: url, RefKind: refKind, RefName: refName, Revision: rev}}
}

var allKindsOK = map[string]string{"kustomizations": ReadOK, "helmreleases": ReadOK, "applications": ReadOK,
	"gitrepositories": ReadOK, "helmrepositories": ReadOK, "ocirepositories": ReadOK, "buckets": ReadOK, "helmcharts": ReadOK}

func station(cluster, env string, mod ...func(*StationInput)) StationInput {
	in := StationInput{Station: Station{Cluster: cluster, Env: env}, KindRead: allKindsOK}
	for _, m := range mod {
		m(&in)
	}
	return in
}

// fluxOn: one Kustomization applied at rev from the fleet repo's main.
func fluxOn(rev string, mods ...func(*flux.Resource)) func(*StationInput) {
	return func(in *StationInput) {
		in.Sources = append(in.Sources, gitSrc("fleet", repo, "branch", "main", "main@sha1:"+rev))
		r := ""
		if rev != "" {
			r = "main@sha1:" + rev
		}
		in.Kustomizations = append(in.Kustomizations, ks("apps", r, mods...))
	}
}

func allOK(stations ...StationInput) map[string]string {
	m := map[string]string{}
	for _, s := range stations {
		m[s.Cluster] = ReadOK
	}
	return m
}

func build(stations ...StationInput) Line {
	return Build(Input{Stations: stations, Read: allOK(stations...)})
}

func stateOf(l Line, cluster string) StationLine {
	for _, sls := range [][]StationLine{l.Stations, l.Siding} {
		for _, s := range sls {
			if s.Cluster == cluster {
				return s
			}
		}
	}
	return StationLine{}
}

func states(l Line) string {
	var out []string
	for _, s := range l.Stations {
		out = append(out, s.Cluster+"="+s.State)
	}
	return strings.Join(out, " ")
}

// ---- units ------------------------------------------------------------------

func TestSameURLDifferentRefsAreTwoKeys(t *testing.T) {
	in := station("a", "dev", func(in *StationInput) {
		in.Sources = append(in.Sources, gitSrc("main", repo, "branch", "main", "main@sha1:"+sha('1')), gitSrc("rel", repo+".git", "tag", "v1", "v1@sha1:"+sha('2')))
		in.Kustomizations = append(in.Kustomizations,
			ks("x", "main@sha1:"+sha('1'), func(r *flux.Resource) { r.SourceName = "main" }),
			ks("y", "v1@sha1:"+sha('2'), func(r *flux.Resource) { r.SourceName = "rel" }))
	})
	us, _ := Units(in)
	if len(us) != 2 || us[0].Key == us[1].Key || us[0].Key.Repo != us[1].Key.Repo {
		t.Fatalf("units: %+v", us)
	}
}

func TestExclusionsAreCounted(t *testing.T) {
	in := station("a", "dev", func(in *StationInput) {
		in.Sources = append(in.Sources, gitSrc("fleet", repo, "branch", "main", "garbage"))
		in.Kustomizations = append(in.Kustomizations,
			ks("oci", "latest@sha256:"+strings.Repeat("a", 64), func(r *flux.Resource) { r.SourceKind = "OCIRepository" }),
			ks("bucket", "x", func(r *flux.Resource) { r.SourceKind = "Bucket" }),
			ks("orphan", "main@sha1:"+sha('1'), func(r *flux.Resource) { r.SourceName = "missing" }),
			ks("weird", "main/"+sha('1')))
	})
	us, ex := Units(in)
	var reasons []string
	for _, x := range ex {
		reasons = append(reasons, x.Name+":"+x.Reason)
	}
	if len(us) != 0 || strings.Join(reasons, " ") != "oci:oci source bucket:bucket source orphan:source not read weird:unparsable revision" {
		t.Fatalf("units %d, exclusions %v", len(us), reasons)
	}
	if ex[0].Digest == "" {
		t.Fatal("an OCI source's digest is shown")
	}
}

func TestHelmReleaseChartUnit(t *testing.T) {
	src := Object[flux.Source]{Data: flux.Source{Generation: 1, ObservedGeneration: 1, Kind: "HelmRepository", Namespace: "flux-system", Name: "bitnami", URL: "https://charts.bitnami.com/bitnami"}}
	hr := func(hist ...flux.HelmHistory) StationInput {
		return station("a", "dev", func(in *StationInput) {
			in.Sources = append(in.Sources, src)
			in.HelmReleases = append(in.HelmReleases, Object[flux.Resource]{UID: "u", Data: flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "shop", Name: "redis",
				Chart: "redis", SourceKind: "HelmRepository", SourceName: "bitnami", SourceNamespace: "flux-system", History: hist}})
		})
	}
	us, _ := Units(hr(flux.HelmHistory{Version: 3, ChartName: "redis", ChartVersion: "18.1.0", Status: "failed"},
		flux.HelmHistory{Version: 2, ChartName: "redis", ChartVersion: "18.0.0", ConfigDigest: "sha256:aa", Status: "deployed"}))
	u := us[0]
	if u.Key.Kind != KeyChartName || u.Key.Repo != "" || u.Key.RefName != "redis" || u.Applied.Version != "18.0.0" || u.Attempted.Version != "18.1.0" || u.AttemptPhase != "failed" {
		t.Fatalf("recorded name and versions, no repository: %+v", u)
	}
	if us, ex := Units(hr()); len(us) != 0 || len(ex) != 1 || ex[0].Reason != ExcludedNoChartID {
		t.Fatal("no history: chart identity unknown")
	}
}

func argoApp(name string, sources []argo.Source, hist []argo.HistoryEntry, mod ...func(*argo.App)) Object[argo.App] {
	a := argo.App{Namespace: "argocd", Name: name, Sources: sources, History: hist}
	for _, m := range mod {
		m(&a)
	}
	return Object[argo.App]{UID: "uid-" + name, Data: a}
}

func gitSource(target string) argo.Source {
	return argo.Source{RepoURL: repo, Path: "apps", TargetRevision: target}
}

func deployed(srcs []argo.Source, revs ...string) []argo.HistoryEntry {
	return []argo.HistoryEntry{{ID: 1, Revisions: argo.Revisions{Sources: srcs, Revisions: revs}}}
}

func TestArgoUnits(t *testing.T) {
	main := gitSource("refs/heads/main")
	t.Run("compared but unsynced over a deployed A", func(t *testing.T) {
		a := argoApp("web", []argo.Source{main}, deployed([]argo.Source{main}, sha('a')), func(a *argo.App) {
			a.Compared = argo.Revisions{Sources: []argo.Source{main}, Revisions: []string{sha('b')}}
		})
		us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = append(in.Apps, a) }))
		if us[0].Applied.SHA != sha('a') || us[0].Compared.SHA != sha('b') || us[0].Key.RefKind != "branch" {
			t.Fatalf("%+v", us[0])
		}
	})
	t.Run("a failed sync attempts B and fails", func(t *testing.T) {
		a := argoApp("web", []argo.Source{main}, deployed([]argo.Source{main}, sha('a')), func(a *argo.App) {
			a.Operation = argo.Operation{Phase: "Failed", Sync: argo.Revisions{Sources: []argo.Source{main}, Revisions: []string{sha('b')}}}
		})
		us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = append(in.Apps, a) }))
		if !us[0].Failing || us[0].Attempted.SHA != sha('b') || us[0].Syncing {
			t.Fatalf("%+v", us[0])
		}
	})
	t.Run("a source edited since its deployment", func(t *testing.T) {
		old := gitSource("refs/heads/release")
		a := argoApp("web", []argo.Source{main}, deployed([]argo.Source{old}, sha('a')))
		us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = append(in.Apps, a) }))
		if !us[0].SourceChanged {
			t.Fatalf("%+v", us[0])
		}
	})
	t.Run("a reorder since its deployment", func(t *testing.T) {
		other := argo.Source{RepoURL: "https://github.com/acme/other", Path: "x", TargetRevision: "refs/heads/main"}
		a := argoApp("web", []argo.Source{main, other}, deployed([]argo.Source{other, main}, sha('e'), sha('a')))
		us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = append(in.Apps, a) }))
		if len(us) != 2 || us[0].Applied.SHA != sha('a') || !us[0].SourceChanged || us[1].Applied.SHA != sha('e') {
			t.Fatalf("paired with the recorded sources, flagged: %+v", us)
		}
	})
	t.Run("a chart range is never a version", func(t *testing.T) {
		chart := argo.Source{RepoURL: "https://charts.example", Chart: "web", TargetRevision: "1.*"}
		a := argoApp("web", []argo.Source{chart}, deployed([]argo.Source{chart}, "1.4.2"))
		us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = append(in.Apps, a) }))
		if us[0].Key.Kind != KeyChart || us[0].Applied.Version != "1.4.2" || us[0].Compared != nil {
			t.Fatalf("%+v", us[0])
		}
	})
	t.Run("no history (revisionHistoryLimit 0) is applied unknown", func(t *testing.T) {
		a := argoApp("web", []argo.Source{main}, nil, func(a *argo.App) { a.SyncStatus = "Synced" })
		us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = append(in.Apps, a) }))
		if us[0].Applied != nil {
			t.Fatalf("%+v", us[0])
		}
	})
	t.Run("an unqualified Argo ref never shares a Flux branch key", func(t *testing.T) {
		a := argoApp("web", []argo.Source{gitSource("main")}, deployed([]argo.Source{gitSource("main")}, sha('a')))
		in := station("c", "dev", fluxOn(sha('a')), func(in *StationInput) { in.Apps = append(in.Apps, a) })
		us, _ := Units(in)
		if len(us) != 2 || us[0].Key == us[1].Key || us[1].Key.RefKind != RefUnqualified {
			t.Fatalf("%+v", us)
		}
	})
}

// ---- keys, R and states ------------------------------------------------------------

func TestKeyCountsEachStationOnceAndTiesLexically(t *testing.T) {
	two := func(in *StationInput) { // two units on one key: counted once
		fluxOn(sha('a'))(in)
		in.Kustomizations = append(in.Kustomizations, ks("infra", "main@sha1:"+sha('a')))
	}
	l := build(station("a", "dev", two), station("b", "prd", fluxOn(sha('a'))))
	if l.Key == nil || l.KeyStations != 2 || l.KeyHow != KeyByMostStations || l.Keys[0].Stations != 2 {
		t.Fatalf("key: %+v %+v", l.Key, l.Keys)
	}
	if got := stateOf(l, "a"); got.E != 2 || got.State != StateMatches {
		t.Fatalf("several units on one station: %+v", got)
	}
}

func TestNoSharedKeyComparesNothing(t *testing.T) {
	l := build(station("a", "dev", fluxOn(sha('a'))), station("b", "prd"))
	if l.Key != nil || stateOf(l, "a").State != StateNotCompared {
		t.Fatalf("%+v", l.Key)
	}
}

func TestOverrideAndSelector(t *testing.T) {
	other := Key{Kind: KeyGit, Repo: "github.com/acme/other", RefKind: "branch", RefName: "main"}
	l := Build(Input{Stations: []StationInput{station("a", "dev", fluxOn(sha('a'))), station("b", "prd", fluxOn(sha('a')))},
		Read: map[string]string{"a": ReadOK, "b": ReadOK}, Override: &other})
	if l.KeyHow != KeyByOverride || *l.Key != other || stateOf(l, "a").State != StateNotCompared {
		t.Fatalf("override: %+v %s", l.Key, states(l))
	}
	sel := Key{Kind: KeyGit, Repo: "github.com/acme/fleet", RefKind: "branch", RefName: "main"}
	l = Build(Input{Stations: []StationInput{station("a", "dev", fluxOn(sha('a'))), station("b", "prd", fluxOn(sha('a')))},
		Read: map[string]string{"a": ReadOK, "b": ReadOK}, Override: &other, Selector: &sel})
	if l.KeyHow != KeyBySelector || l.R != sha('a') {
		t.Fatalf("selector: %+v R %s", l.Key, l.R)
	}
}

func TestRMajorityTieBreakAndMixedNeverVotes(t *testing.T) {
	l := build(station("d", "dev", fluxOn(sha('b'))), station("s", "stg", fluxOn(sha('a'))), station("p", "prd", fluxOn(sha('a'))))
	if l.R != sha('a') || l.RStations != 2 || l.RTieBroken || states(l) != "d=differs s=matches p=matches" {
		t.Fatalf("majority: R %s %s", l.R, states(l))
	}
	if l.Applied != 2 || l.Compared != 3 {
		t.Fatalf("headline: %d of %d", l.Applied, l.Compared)
	}
	// A tie goes to the newest artifact, disclosed.
	older := func(t time.Time) func(*StationInput) {
		return func(in *StationInput) { in.Sources[0].Data.ArtifactUpdated = t }
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	l = build(station("d", "dev", fluxOn(sha('b')), older(now)), station("p", "prd", fluxOn(sha('a')), older(now.Add(-time.Hour))))
	if l.R != sha('b') || !l.RTieBroken {
		t.Fatalf("tie: R %s broken %v", l.R, l.RTieBroken)
	}
	// A mixed station never votes.
	mixed := func(in *StationInput) {
		fluxOn(sha('c'))(in)
		in.Kustomizations = append(in.Kustomizations, ks("infra", "main@sha1:"+sha('d')))
	}
	l = build(station("a", "dev", mixed), station("b", "stg", mixed), station("p", "prd", fluxOn(sha('a'))))
	if l.R != sha('a') || stateOf(l, "a").State != StateMixed {
		t.Fatalf("mixed: R %s %s", l.R, states(l))
	}
}

func TestStatePrecedence(t *testing.T) {
	unknown := fluxOn("") // no lastAppliedRevision: applied unknown
	knownPlusUnknown := func(in *StationInput) {
		fluxOn(sha('a'))(in)
		in.Kustomizations = append(in.Kustomizations, ks("infra", ""))
	}
	stations := []StationInput{
		station("chk", "dev", fluxOn(sha('a'))),
		station("none", "dev"),
		station("unk", "stg", unknown),
		station("mix", "stg", knownPlusUnknown),
		station("ok1", "prd", fluxOn(sha('a'))),
		station("ok2", "prd", fluxOn(sha('a'))),
	}
	read := allOK(stations...)
	read["chk"] = ReadStale
	l := Build(Input{Stations: stations, Read: read})
	want := map[string]string{"chk": StateChecking, "none": StateNotCompared, "unk": StateAppliedUnknown, "mix": StateMixed, "ok1": StateMatches, "ok2": StateMatches}
	for c, s := range want {
		if got := stateOf(l, c); got.State != s {
			t.Errorf("%s: %s, want %s", c, got.State, s)
		}
	}
	if l.Checking != 1 || l.NotCompared != 1 || l.Applied != 2 {
		t.Fatalf("headline: %+v", l)
	}
	// Every station mixed: no R, nothing lit.
	l = build(station("a", "dev", knownPlusUnknown), station("b", "prd", knownPlusUnknown))
	if l.R != "" || states(l) != "a=mixed b=mixed" {
		t.Fatalf("no R: %q %s", l.R, states(l))
	}
}

func TestPartialStaleAndFailedReadsNeverMatch(t *testing.T) {
	for _, r := range []string{ReadPartial, ReadStale, ReadError, ReadChecking} {
		l := Build(Input{Stations: []StationInput{station("a", "dev", fluxOn(sha('a'))), station("b", "prd", fluxOn(sha('a')))},
			Read: map[string]string{"a": r, "b": ReadOK}})
		if stateOf(l, "a").State != StateChecking {
			t.Errorf("%s read: %s", r, stateOf(l, "a").State)
		}
	}
}

func TestFlags(t *testing.T) {
	since := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	failing := fluxOn(sha('a'), func(r *flux.Resource) { r.Ready = flux.Failed })
	withSince := func(in *StationInput) {
		in.Kustomizations[0].FailSince, in.Kustomizations[0].FailBasis = &since, "rule"
	}
	suspended := fluxOn(sha('a'), func(r *flux.Resource) { r.Suspended = true; r.Ready = flux.Failed })
	stale := fluxOn(sha('a'), func(r *flux.Resource) { r.Stale = true })
	comparedB := func(in *StationInput) { in.Sources[0].Data.Revision = "main@sha1:" + sha('b') }
	l := build(station("f", "dev", failing, withSince), station("s", "stg", suspended, comparedB), station("g", "prd", stale))
	f, s, g := stateOf(l, "f"), stateOf(l, "s"), stateOf(l, "g")
	if !f.Flags.Failing || f.Flags.FailSince == nil || !f.Flags.FailSince.Equal(since) || f.Flags.FailBasis != "rule" {
		t.Fatalf("failing since: %+v", f.Flags)
	}
	if s.Flags.Failing || !s.Flags.Suspended || s.Flags.Compared != sha('b') || s.Flags.ComparedVs != sha('a') {
		t.Fatalf("suspended units never fail; compared vs applied: %+v", s.Flags)
	}
	if !g.Flags.Syncing {
		t.Fatalf("a stale generation is syncing: %+v", g.Flags)
	}
}

func TestHeldNeedsAttemptedRAndFailing(t *testing.T) {
	attemptedR := fluxOn(sha('b'), func(r *flux.Resource) { r.Ready = flux.Failed; r.LastAttemptedRevision = "main@sha1:" + sha('a') })
	unrelated := fluxOn(sha('b'), func(r *flux.Resource) { r.Ready = flux.Failed; r.LastAttemptedRevision = "main@sha1:" + sha('c') })
	l := build(station("h", "dev", attemptedR), station("u", "stg", unrelated), station("p1", "prd", fluxOn(sha('a'))), station("p2", "prd", fluxOn(sha('a'))))
	h, u := stateOf(l, "h"), stateOf(l, "u")
	if !h.Held || h.State != StateDiffers || h.HeldWhy != "attempted "+sha('a')+", failing" {
		t.Fatalf("held: %+v", h)
	}
	if u.Held || u.State != StateDiffers || !u.Flags.Failing {
		t.Fatalf("an unrelated failure stays differs: %+v", u)
	}
}

func TestChartValuesDiffer(t *testing.T) {
	chartStation := func(cluster, env, version, digest string) StationInput {
		return station(cluster, env, func(in *StationInput) {
			in.Sources = append(in.Sources, Object[flux.Source]{Data: flux.Source{Generation: 1, ObservedGeneration: 1, Kind: "HelmRepository", Namespace: "flux-system", Name: "repo", URL: "https://charts.example"}})
			in.HelmReleases = append(in.HelmReleases, Object[flux.Resource]{Data: flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "shop", Name: "web", Chart: "web",
				SourceKind: "HelmRepository", SourceName: "repo", SourceNamespace: "flux-system",
				History: []flux.HelmHistory{{ChartName: "web", ChartVersion: version, ConfigDigest: digest, Status: "deployed"}}}})
		})
	}
	in := Input{Stations: []StationInput{chartStation("a", "dev", "1.0.0", "sha256:x"), chartStation("b", "stg", "1.0.0", "sha256:x"), chartStation("c", "prd", "1.0.0", "sha256:y")}}
	in.Read = allOK(in.Stations...)
	us, _ := Units(in.Stations[0])
	sel := us[0].Key
	in.Selector = &sel
	l := Build(in)
	if l.R != "1.0.0" || stateOf(l, "c").State != StateMatches || !stateOf(l, "c").Flags.ValuesDiffer || stateOf(l, "a").Flags.ValuesDiffer {
		t.Fatalf("values differ: R %s %+v", l.R, stateOf(l, "c"))
	}
}

func TestSharedKeysAcrossNamespacesAndTools(t *testing.T) {
	// Two GitRepositories with the same URL and ref in different namespaces
	// share a key; a fully qualified Argo branch matches a Flux branch.
	in := station("a", "dev", func(in *StationInput) {
		in.Sources = append(in.Sources, gitSrc("fleet", repo, "branch", "main", "main@sha1:"+sha('a')))
		other := gitSrc("fleet", repo+".git", "name", "refs/heads/main", "main@sha1:"+sha('a'))
		other.Data.Namespace = "team-b"
		in.Sources = append(in.Sources, other)
		in.Kustomizations = append(in.Kustomizations, ks("x", "main@sha1:"+sha('a')),
			ks("y", "main@sha1:"+sha('a'), func(r *flux.Resource) { r.Namespace, r.SourceNamespace = "team-b", "team-b" }))
		main := gitSource("refs/heads/main")
		in.Apps = append(in.Apps, argoApp("web", []argo.Source{main}, deployed([]argo.Source{main}, sha('a'))))
	})
	us, _ := Units(in)
	if len(us) != 3 || us[0].Key != us[1].Key || us[0].Key != us[2].Key {
		t.Fatalf("one key: %v %v %v", us[0].Key, us[1].Key, us[2].Key)
	}
}

// A unit whose own kind or source kind is not read ok keeps its station
// checking (§3.3 rule 1; slice 3 note §2.4).
func TestUnitReadKeepsTheStationChecking(t *testing.T) {
	staleSources := map[string]string{}
	for k, v := range allKindsOK {
		staleSources[k] = v
	}
	staleSources["gitrepositories"] = ReadStale
	l := build(station("a", "dev", fluxOn(sha('a')), func(in *StationInput) { in.KindRead = staleSources }), station("b", "prd", fluxOn(sha('a'))))
	if got := stateOf(l, "a"); got.State != StateChecking || got.Units[0].Read != ReadStale {
		t.Fatalf("a stale source kind: %+v", got)
	}
	// An unrelated kind that is unreadable changes nothing.
	unrelated := map[string]string{}
	for k, v := range allKindsOK {
		unrelated[k] = v
	}
	unrelated["buckets"] = ReadError
	l = build(station("a", "dev", fluxOn(sha('a')), func(in *StationInput) { in.KindRead = unrelated }), station("b", "prd", fluxOn(sha('a'))))
	if got := stateOf(l, "a"); got.State != StateMatches {
		t.Fatalf("an unrelated unreadable kind: %+v", got.State)
	}
}

// Held evidence with Git history (parent §3.4, slice 6d): a Flux unit's
// GitRepository that is not ready, and a reported DependencyNotReady
// naming its dependency - the controller's own words, never inferred.
func TestHeldEvidenceFacts(t *testing.T) {
	notReady := func(in *StationInput) { in.Sources[0].Data.Ready = flux.Failed }
	blocked := func(r *flux.Resource) {
		r.Ready, r.Reason, r.Message = flux.Failed, "DependencyNotReady", "dependency 'flux-system/infra' is not ready"
	}
	us, _ := Units(station("a", "dev", fluxOn(sha('a')), notReady))
	if len(us) != 1 || !us[0].SourceNotReady || !us[0].Failing || us[0].BlockedBy != "" {
		t.Fatalf("source not ready: %+v", us)
	}
	us, _ = Units(station("a", "dev", fluxOn(sha('a'), blocked)))
	if len(us) != 1 || us[0].SourceNotReady || us[0].BlockedBy != "flux-system/infra" {
		t.Fatalf("dependency: %+v", us)
	}
	us, _ = Units(station("a", "dev", fluxOn(sha('a'), func(r *flux.Resource) { r.Reason, r.Message = "DependencyNotReady", "waiting" })))
	if us[0].BlockedBy != "a dependency" {
		t.Fatalf("an unnamed dependency: %+v", us[0])
	}
	// A suspended unit is never evidence.
	us, _ = Units(station("a", "dev", fluxOn(sha('a'), blocked, func(r *flux.Resource) { r.Suspended = true }), notReady))
	if us[0].SourceNotReady || us[0].BlockedBy != "" {
		t.Fatalf("suspended: %+v", us[0])
	}
	// Argo never reports either.
	us, _ = Units(station("b", "dev", func(in *StationInput) {
		in.Apps = append(in.Apps, argoApp("app", []argo.Source{gitSource("main")}, deployed([]argo.Source{gitSource("main")}, sha('b')),
			func(a *argo.App) {
				a.Conditions = []argo.Condition{{Type: "ComparisonError", Message: "repository not found"}}
			}))
	}))
	if len(us) != 1 || us[0].SourceNotReady || us[0].BlockedBy != "" {
		t.Fatalf("argo: %+v", us)
	}
}
