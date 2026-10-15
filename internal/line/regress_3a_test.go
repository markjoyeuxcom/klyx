package line

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
)

// Regressions from Codex's review of slice 3a (577724f): Argo pairing,
// chart identity, changed charts, the tie-break's scope, Argo attempts and
// suspension, and Helm source failures.

func TestRegress3aArgoRepeatedSourceIdentity(t *testing.T) {
	main, release := gitSource("refs/heads/main"), gitSource("refs/heads/release")
	sources := []argo.Source{main, release}
	a := argoApp("multi", sources, deployed(sources, sha('a'), sha('b')), func(a *argo.App) {
		a.Compared = argo.Revisions{Sources: sources, Revisions: []string{sha('c'), sha('d')}}
		a.Operation = argo.Operation{Phase: "Running", Sync: argo.Revisions{Sources: sources, Revisions: []string{sha('e'), sha('f')}}}
	})
	us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = []Object[argo.App]{a} }))
	if len(us) != 2 {
		t.Fatalf("units=%d", len(us))
	}
	if us[1].Applied.SHA != sha('b') || us[1].Compared.SHA != sha('d') || us[1].Attempted.SHA != sha('f') {
		t.Fatalf("second source was paired to first: applied=%s compared=%s attempted=%s", us[1].Applied.SHA, us[1].Compared.SHA, us[1].Attempted.SHA)
	}
}

func TestRegress3aReferenceTieIgnoresOtherKeys(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	a := station("a", "dev", fluxOn(sha('a')))
	b := station("b", "prd", fluxOn(sha('b')))
	a.Sources[0].Data.ArtifactUpdated = now.Add(-time.Hour)
	b.Sources[0].Data.ArtifactUpdated = now
	unrelated := gitSrc("other", "https://github.com/acme/other", "branch", "main", "main@sha1:"+sha('a'))
	unrelated.Data.ArtifactUpdated = now.Add(time.Hour)
	a.Sources = append(a.Sources, unrelated)
	a.Kustomizations = append(a.Kustomizations, ks("other", "main@sha1:"+sha('a'), func(r *flux.Resource) { r.SourceName = "other" }))
	l := build(a, b)
	if l.R != sha('b') {
		t.Fatalf("unselected repository changed R: got %s want %s", l.R, sha('b'))
	}
}

func reviewHelm(r flux.Resource, s Object[flux.Source]) StationInput {
	return station("c", "dev", func(in *StationInput) {
		in.Sources = []Object[flux.Source]{s}
		in.HelmReleases = []Object[flux.Resource]{{UID: "hr", Data: r}}
	})
}
func TestRegress3aHelmSourceFailure(t *testing.T) {
	since := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	s := Object[flux.Source]{UID: "repo", FailSince: &since, FailBasis: "observed", Data: flux.Source{Kind: flux.HelmRepositoryKind, Namespace: "shop", Name: "repo", URL: "https://charts.example", Ready: flux.Failed}}
	r := flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "shop", Name: "web", Ready: flux.Ready, Chart: "web", SourceKind: "HelmRepository", SourceNamespace: "shop", SourceName: "repo", History: []flux.HelmHistory{{ChartName: "web", ChartVersion: "1.0.0", Status: "deployed"}}}
	us, _ := Units(reviewHelm(r, s))
	if len(us) != 1 || !us[0].Failing || us[0].FailSince == nil || !us[0].FailSince.Equal(since) {
		t.Fatalf("bound source failure lost: %+v", us)
	}
}
func TestRegress3aArgoSuspendedExcludesFailure(t *testing.T) {
	src := gitSource("refs/heads/main")
	a := argoApp("paused", []argo.Source{src}, deployed([]argo.Source{src}, sha('a')), func(a *argo.App) {
		a.HealthStatus = "Suspended"
		a.Operation = argo.Operation{Phase: "Failed", Sync: argo.Revisions{Sources: []argo.Source{src}, Revisions: []string{sha('b')}}}
	})
	us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = []Object[argo.App]{a} }))
	if !us[0].Suspended || us[0].Failing {
		t.Fatalf("suspended app remains failing: %+v", us[0])
	}
}
func TestRegress3aArgoSuccessfulOperationIsNotAnAttempt(t *testing.T) {
	src := gitSource("refs/heads/main")
	a := argoApp("error", []argo.Source{src}, deployed([]argo.Source{src}, sha('b')), func(a *argo.App) {
		a.Conditions = []argo.Condition{{Type: "ComparisonError", Message: "read failed"}}
		a.Operation = argo.Operation{Phase: "Succeeded", Sync: argo.Revisions{Sources: []argo.Source{src}, Revisions: []string{sha('b')}}}
	})
	us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = []Object[argo.App]{a} }))
	if us[0].Attempted != nil {
		t.Fatalf("successful old operation became attempted: phase=%s attempted=%+v", us[0].AttemptPhase, us[0].Attempted)
	}
}
func TestRegress3aHelmChartHasNoRepositoryIdentity(t *testing.T) {
	chart := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmChart", "metadata": map[string]any{"namespace": "shop", "name": "web"}, "spec": map[string]any{"chart": "web", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}}}}
	source := Object[flux.Source]{Data: flux.ParseSource(chart)}
	r := flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "shop", Name: "web", SourceKind: "HelmChart", SourceNamespace: "shop", SourceName: "web", History: []flux.HelmHistory{{ChartName: "web", ChartVersion: "1.0.0", Status: "deployed"}}}
	us, _ := Units(reviewHelm(r, source))
	if len(us) > 0 && us[0].Key.Repo == "" {
		t.Fatalf("chartRef compares under an empty repository: %+v", us[0].Key)
	}
}
func TestRegress3aHelmChangedChartDoesNotClaimOldVersion(t *testing.T) {
	source := Object[flux.Source]{Data: flux.Source{Kind: flux.HelmRepositoryKind, Namespace: "shop", Name: "repo", URL: "https://charts.example"}}
	r := flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "shop", Name: "cache", Chart: "new-chart", SourceKind: "HelmRepository", SourceNamespace: "shop", SourceName: "repo", History: []flux.HelmHistory{{ChartName: "old-chart", ChartVersion: "1.0.0", Status: "deployed"}}}
	us, _ := Units(reviewHelm(r, source))
	if len(us) > 0 && us[0].Key.RefName == "new-chart" && us[0].Applied != nil {
		t.Fatalf("old-chart deployment attributed to new-chart: %+v", us[0])
	}
}

// Regressions from Codex's review of 599091d: a contested historical
// source, and a chart path that is not a chart name.

func TestRegress3aFixArgoContestedHistoricalSource(t *testing.T) {
	old := gitSource("refs/heads/release")
	a, b := gitSource("refs/heads/main"), gitSource("refs/heads/develop")
	for _, sources := range [][]argo.Source{{a, b}, {b, a}} {
		app := argoApp("edited", sources, deployed([]argo.Source{old}, sha('a')), func(app *argo.App) {
			app.Compared = argo.Revisions{Sources: []argo.Source{old}, Revisions: []string{sha('b')}}
			app.Operation = argo.Operation{Phase: "Failed", Sync: argo.Revisions{Sources: []argo.Source{old}, Revisions: []string{sha('c')}}}
		})
		us, _ := Units(station("c", "dev", func(in *StationInput) { in.Apps = []Object[argo.App]{app} }))
		if len(us) != 2 {
			t.Fatalf("units=%d", len(us))
		}
		for _, u := range us {
			if u.Applied != nil || u.Compared != nil || u.Attempted != nil {
				t.Errorf("source %s was arbitrarily assigned the one old release source: applied=%+v compared=%+v attempted=%+v", u.Key.RefName, u.Applied, u.Compared, u.Attempted)
			}
		}
	}
}

func TestRegress3aFixHelmPathIsNotChartName(t *testing.T) {
	for _, kind := range []flux.Kind{flux.GitRepositoryKind, flux.BucketKind} {
		for _, path := range []string{"./charts/web", "./charts/web-1.1.0.tgz"} {
			t.Run(string(kind)+"/"+path, func(t *testing.T) {
				s := Object[flux.Source]{Data: flux.Source{Kind: kind, Namespace: "shop", Name: "repo", URL: "https://example.com/charts", Ready: flux.Ready}}
				r := flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "shop", Name: "web", Chart: path, SourceKind: string(kind), SourceNamespace: "shop", SourceName: "repo", Generation: 1, ObservedGeneration: 1,
					History: []flux.HelmHistory{{Version: 2, ChartName: "web", ChartVersion: "1.1.0", Status: "failed"}, {Version: 1, ChartName: "web", ChartVersion: "1.0.0", Status: "deployed"}}}
				us, _ := Units(reviewHelm(r, s))
				if len(us) != 1 {
					t.Fatalf("units=%d", len(us))
				}
				u := us[0]
				if u.SourceChanged || u.Applied == nil || u.Attempted == nil {
					t.Fatalf("chart path mistaken for changed metadata.name: key=%+v changed=%v applied=%+v attempted=%+v", u.Key, u.SourceChanged, u.Applied, u.Attempted)
				}
			})
		}
	}
}

// Regression from Codex's review of 89f8b8f: a newest history record does
// not prove an edited path-sourced chart.

func TestRegress3aFix2HistoryDoesNotProveCurrentChartPath(t *testing.T) {
	for _, kind := range []flux.Kind{flux.GitRepositoryKind, flux.BucketKind} {
		for _, observed := range []int64{1, 2} {
			name := string(kind)
			if observed == 1 {
				name += "/pending"
			} else {
				name += "/terminal-failure"
			}
			t.Run(name, func(t *testing.T) {
				source := Object[flux.Source]{Data: flux.Source{Kind: kind, Namespace: "shop", Name: "repo", URL: "https://example.com/charts", Ready: flux.Ready}}
				changed := flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "shop", Name: "release", Chart: "./charts/new", SourceKind: string(kind), SourceNamespace: "shop", SourceName: "repo", Generation: 2, ObservedGeneration: observed, Stale: observed < 2, Ready: flux.Failed,
					History: []flux.HelmHistory{{Version: 1, ChartName: "old", ChartVersion: "1.0.0", Status: "deployed"}}}
				before := changed
				before.Chart = "./charts/old"
				before.Generation = 1
				before.ObservedGeneration = 1
				before.Stale = false
				before.Ready = flux.Ready
				oldUnits, _ := Units(reviewHelm(before, source))
				if len(oldUnits) != 1 || oldUnits[0].Applied == nil {
					t.Fatal("precondition: old path had a deployed release")
				}
				staleUnits, _ := Units(reviewHelm(changed, source))
				if len(staleUnits) != 1 {
					t.Fatalf("units=%d", len(staleUnits))
				}
				if staleUnits[0].Applied != nil {
					t.Errorf("old deployment assigned to edited path %s: %+v", staleUnits[0].Key.RefName, staleUnits[0].Applied)
				}
				stale := reviewHelm(changed, source)
				stale.Cluster = "stale"
				stale.Env = "dev"
				good := changed
				good.Generation = 1
				good.ObservedGeneration = 1
				good.Stale = false
				good.Ready = flux.Ready
				good.History = []flux.HelmHistory{{Version: 1, ChartName: "new", ChartVersion: "1.0.0", Status: "deployed"}}
				current := reviewHelm(good, source)
				current.Cluster = "good"
				current.Env = "prd"
				key := staleUnits[0].Key
				l := Build(Input{Stations: []StationInput{stale, current}, Read: allOK(stale, current), Selector: &key})
				got := stateOf(l, "stale")
				if got.State != StateAppliedUnknown || l.Applied != 1 {
					t.Errorf("unapplied new chart counted as deployed: state=%q headline=%d/%d R=%s", got.State, l.Applied, l.Compared, l.R)
				}
			})
		}
	}
}
