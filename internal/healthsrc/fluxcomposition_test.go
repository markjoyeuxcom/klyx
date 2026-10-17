package healthsrc

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/gitops/composition"
	"github.com/moomora/klyx/internal/gitops/notify"
	"github.com/moomora/klyx/internal/gitops/resume"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
)

func compObj(apiVersion, kind, ns, name string, gen int64, labels, ann map[string]any, spec, status map[string]any) *unstructured.Unstructured {
	md := map[string]any{"name": name, "namespace": ns, "uid": "uid-" + kind + "-" + name, "generation": gen}
	if labels != nil {
		md["labels"] = labels
	}
	if ann != nil {
		md["annotations"] = ann
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": md, "spec": spec, "status": status}}
}

func compReady(status, reason string, gen int64) []any {
	return []any{map[string]any{"type": "Ready", "status": status, "reason": reason, "message": "m", "lastTransitionTime": "2026-10-17T09:00:00Z", "observedGeneration": gen}}
}

const (
	agAPI = composition.GeneratorGroup + "/" + composition.GeneratorVersion
	eaAPI = composition.ArtifactGroup + "/" + composition.ArtifactVersion
	rcAPI = notify.Group + "/" + notify.ReceiverVersion
)

func compositionFixture() []runtime.Object {
	art := func(rev string) map[string]any {
		return map[string]any{"artifact": map[string]any{"revision": rev, "digest": "sha256:ab", "url": "http://source-watcher.flux-system.svc/x.tgz",
			"lastUpdateTime": "2026-10-17T08:00:00Z", "size": int64(1024)}, "conditions": compReady("True", "Succeeded", 1)}
	}
	return []runtime.Object{
		compObj(agAPI, "ArtifactGenerator", "flux-system", "apps", 1, nil, nil,
			map[string]any{"sources": []any{map[string]any{"alias": "fleet", "kind": "GitRepository", "name": "fleet"}},
				"artifacts": []any{map[string]any{"name": "apps-a", "copy": []any{"@fleet/apps/** -> ./"}}}},
			map[string]any{"inventory": []any{map[string]any{"name": "apps-a", "namespace": "flux-system"}, map[string]any{"name": "apps-gone"}},
				"conditions": compReady("True", "Succeeded", 1)}),
		compObj(agAPI, "ArtifactGenerator", "flux-system", "paused", 1, nil, map[string]any{composition.AnnReconcile: "Disabled"},
			map[string]any{}, map[string]any{"conditions": compReady("True", "ReconciliationDisabled", 1)}),
		compObj(eaAPI, "ExternalArtifact", "flux-system", "apps-a", 1, map[string]any{composition.LabelGenerator: "uid-ArtifactGenerator-apps"}, nil,
			map[string]any{"sourceRef": map[string]any{"apiVersion": agAPI, "kind": "ArtifactGenerator", "name": "apps"}}, art("latest@sha256:ab")),
		compObj(eaAPI, "ExternalArtifact", "flux-system", "orphan", 1, map[string]any{composition.LabelGenerator: "uid-missing"}, nil,
			map[string]any{}, art("latest@sha256:cd")),
		compObj(eaAPI, "ExternalArtifact", "flux-system", "empty", 1, nil, nil,
			map[string]any{"sourceRef": map[string]any{"apiVersion": "ci.example/v1", "kind": "Pipeline", "name": "build"}}, map[string]any{}),
		compObj(rcAPI, "Receiver", "flux-system", "gh", 1, nil, nil,
			map[string]any{"type": "github", "secretRef": map[string]any{"name": "token"}, "resources": []any{map[string]any{"kind": "GitRepository", "name": "fleet"}}},
			map[string]any{"webhookPath": "/hook/PATHneedle3B", "conditions": compReady("True", "Succeeded", 1)}),
		compObj(rcAPI, "Receiver", "flux-system", "broken", 1, nil, nil,
			map[string]any{"type": "generic", "secretRef": map[string]any{"name": "missing"}},
			map[string]any{"conditions": compReady("False", notify.ReasonTokenNF, 1)}),
	}
}

func compServed(_ context.Context, group, _, _ string) (observe.Probe, error) {
	switch group {
	case composition.GeneratorGroup:
		return observe.Probe{Served: true, Version: composition.GeneratorVersion}, nil
	case composition.ArtifactGroup:
		return observe.Probe{Served: true, Version: composition.ArtifactVersion}, nil
	case notify.Group:
		return observe.Probe{Served: true, Version: notify.ReceiverVersion}, nil
	}
	return observe.Probe{}, nil
}

func startComposition(t *testing.T, objs ...runtime.Object) (*sink, *observe.Observer) {
	t.Helper()
	lists := map[schema.GroupVersionResource]string{
		{Group: composition.GeneratorGroup, Version: composition.GeneratorVersion, Resource: KindArtifactGenerators}: "ArtifactGeneratorList",
		{Group: composition.ArtifactGroup, Version: composition.ArtifactVersion, Resource: KindExternalArtifacts}:    "ExternalArtifactList",
		{Group: notify.Group, Version: notify.ReceiverVersion, Resource: KindReceivers}:                              "ReceiverList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objs...)
	kinds, parts := FluxComposition(NativeDeps{Cluster: "c1", Dyn: dyn, Served: compServed})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: []observe.DomainSpec{{ID: TaskFlux, Parts: parts}}, Throttle: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	o.SubscribeRows(TaskFlux, "composition", observe.Owner{Kind: "lens", ID: "t"})
	o.SubscribeRows(TaskFlux, "receivers", observe.Owner{Kind: "lens", ID: "t"})
	return s, o
}

// Coverage S1c §3 and §4 through the observer: generators and artifacts in
// one part with their joins from the same capture, Receivers in their own.
func TestFluxCompositionParts(t *testing.T) {
	s, o := startComposition(t, compositionFixture()...)
	waitFor(t, "composition and receivers counted", func() bool {
		c, ok1 := nativePart(s, TaskFlux, "composition")
		r, ok2 := nativePart(s, TaskFlux, "receivers")
		return ok1 && ok2 && c.Coverage == observe.Live && r.Coverage == observe.Live && c.Native != nil && r.Native != nil &&
			c.Native.Total == 5 && c.Native.Included == 1 && r.Native.Total == 2 && r.Native.Included == 1
	})
	rows, ok := o.RowsNow(TaskFlux, "composition")
	if !ok {
		t.Fatal("no composition rows")
	}
	by := map[string]Row[CompositionRow]{}
	for _, row := range rows.Rows.(RowSet[CompositionRow]).Rows {
		if row.Data.Generator != nil {
			by["ag/"+row.Data.Generator.Name] = row
		} else {
			by["ea/"+row.Data.Artifact.Name] = row
		}
	}
	apps := by["ag/apps"].Data
	if apps.GeneratedState != "found" || len(apps.Generated) != 2 || !apps.Generated[0].Found || apps.Generated[0].Label != "Ready" ||
		apps.Generated[1].Found || apps.Generated[1].Namespace != "flux-system" {
		t.Fatalf("the generator's artifacts: %+v", apps)
	}
	if by["ag/paused"].Eval.Label != composition.LabelSuspended {
		t.Fatalf("suspended by its annotation: %+v", by["ag/paused"].Eval)
	}
	if p := by["ea/apps-a"].Data.Producer; p.State != composition.ProducerVerified || p.Name != "apps" || p.Label != "Ready" || p.GeneratorFailing {
		t.Fatalf("a verified producer: %+v", p)
	}
	if p := by["ea/orphan"].Data.Producer; p.State != composition.ProducerGone || p.Label != "" {
		t.Fatalf("a complete read finds no recorded producer: %+v", p)
	}
	empty := by["ea/empty"]
	if p := empty.Data.Producer; p.State != composition.ProducerDeclared || p.Kind != "Pipeline" || p.Label != "" {
		t.Fatalf("another kind is declared, its health not read: %+v", p)
	}
	if empty.Eval.Label != composition.LabelNoArtifact || empty.Eval.Role != health.Fault {
		t.Fatalf("no artifact is a fault: %+v", empty.Eval)
	}

	rrows, ok := o.RowsNow(TaskFlux, "receivers")
	if !ok {
		t.Fatal("no receiver rows")
	}
	for _, row := range rrows.Rows.(RowSet[ReceiverRow]).Rows {
		switch row.Data.Name {
		case "gh":
			if row.Eval.Role != health.OK || row.Data.Meaning != "configuration accepted, token read" {
				t.Fatalf("a ready receiver: %+v", row)
			}
		case "broken":
			if row.Eval.Role != health.Fault || !strings.Contains(row.Eval.Reason, notify.TokenUnavailable) {
				t.Fatalf("token unavailable: %+v", row.Eval)
			}
		}
		if strings.Contains(strings.ToLower(row.Data.Name+row.Eval.Reason), "pathneedle") {
			t.Fatal("the webhook path never reaches a row")
		}
	}
}

// S1c §3: each kind at its one served version; another is a named gap.
func TestFluxCompositionVersions(t *testing.T) {
	served := func(_ context.Context, _, _, _ string) (observe.Probe, error) {
		return observe.Probe{Served: true, Version: "v9"}, nil
	}
	kinds, _ := FluxComposition(NativeDeps{Cluster: "c1", Served: served})
	if len(kinds) != 3 {
		t.Fatalf("three watches: %d", len(kinds))
	}
	for _, k := range kinds {
		if _, err := k.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported version v9 for "+k.Name) {
			t.Errorf("%s: %v", k.Name, err)
		}
	}
	budget := 0
	for _, k := range kinds {
		budget += k.Budget
	}
	if budget != 1<<20 {
		t.Fatalf("1 MiB per cluster together: %d", budget)
	}
}

// S1c §4: a resumed generator whose Ready did not move is awaiting
// evaluation and not counted; a capped generators read leaves a producer
// "incomplete", never a health.
func TestFluxCompositionResumeAndCoverage(t *testing.T) {
	project := composition.Project(composition.KindGenerator)
	projectA := composition.Project(composition.KindArtifact)
	gen := func(disabled bool) any {
		var ann map[string]any
		if disabled {
			ann = map[string]any{composition.AnnReconcile: "disabled"}
		}
		v, err := project(compObj(agAPI, "ArtifactGenerator", "flux-system", "apps", 1, nil, ann, map[string]any{},
			map[string]any{"conditions": compReady("True", "Succeeded", 1)}))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	art, err := projectA(compObj(eaAPI, "ExternalArtifact", "flux-system", "a", 1, map[string]any{composition.LabelGenerator: "uid-other"}, nil,
		map[string]any{}, map[string]any{"artifact": map[string]any{"revision": "r", "digest": "d"}}))
	if err != nil {
		t.Fatal(err)
	}
	eval := evalComposition("c1", resume.NewTracker())
	capture := func(at time.Time, g any, complete bool) observe.PartResult {
		return eval(observe.Capture{Now: at, Rows: true, RoleCap: 10,
			Objects:  map[string][]any{KindArtifactGenerators: {g}, KindExternalArtifacts: {art}},
			Complete: map[string]bool{KindArtifactGenerators: complete, KindExternalArtifacts: true}})
	}
	t0 := time.Now()
	capture(t0, gen(true), true)
	res := capture(t0.Add(time.Minute), gen(false), false)
	var g, a Row[CompositionRow]
	for _, row := range res.Rows.(RowSet[CompositionRow]).Rows {
		if row.Data.Generator != nil {
			g = row
		} else {
			a = row
		}
	}
	if g.Eval.Label != composition.LabelAwaiting || g.Eval.Included || g.Data.Resume != ResumeAwaiting || !strings.Contains(g.Eval.Reason, "nothing drifts") {
		t.Fatalf("resumed without evidence: %+v", g)
	}
	if p := a.Data.Producer; p.State != composition.ProducerIncomplete || p.Label != "" {
		t.Fatalf("a capped generators read: %+v", p)
	}
}
