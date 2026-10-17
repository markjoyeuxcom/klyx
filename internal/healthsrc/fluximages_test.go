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

	"github.com/moomora/klyx/internal/gitops/imageauto"
	"github.com/moomora/klyx/internal/observe"
)

func imageGVR(version, res string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: imageauto.Group, Version: version, Resource: res}
}

func imgObj(version, kind, ns, name string, gen int64, spec, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": imageauto.Group + "/" + version, "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": "uid-" + kind + "-" + name, "generation": gen}, "spec": spec, "status": status}}
	return u
}

func imgReady(status, reason, msg string, gen int64) []any {
	return []any{map[string]any{"type": "Ready", "status": status, "reason": reason, "message": msg, "lastTransitionTime": "2026-10-17T09:00:00Z", "observedGeneration": gen}}
}

func startImages(t *testing.T, version string, objs ...runtime.Object) (*sink, *observe.Observer) {
	t.Helper()
	lists := map[schema.GroupVersionResource]string{
		imageGVR(version, KindImageRepositories): "ImageRepositoryList",
		imageGVR(version, KindImagePolicies):     "ImagePolicyList",
		imageGVR(version, KindImageAutomations):  "ImageUpdateAutomationList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objs...)
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		return observe.Probe{Served: group == imageauto.Group, Version: version}, nil
	}
	kinds, parts := FluxImages(NativeDeps{Cluster: "c1", Dyn: dyn, Served: served})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: []observe.DomainSpec{{ID: TaskFlux, Parts: parts}}, Throttle: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	o.SubscribeRows(TaskFlux, "images", observe.Owner{Kind: "lens", ID: "t"})
	return s, o
}

// Coverage S1b §3 and §4 through the observer: the parity fixtures' shapes,
// counted in one part, with each policy's repository and each
// automation's policies joined from the same capture.
func TestFluxImagesPartOnFixtures(t *testing.T) {
	s, o := startImages(t, imageauto.Version,
		imgObj("v1", "ImageRepository", "flux-system", "app", 1, map[string]any{"image": "ghcr.io/org/app", "interval": "5m"},
			map[string]any{"observedGeneration": int64(1), "lastScanResult": map[string]any{"tagCount": int64(3), "scanTime": time.Now().UTC().Format(time.RFC3339)}, "conditions": imgReady("True", "Succeeded", "ok", 1)}),
		imgObj("v1", "ImageRepository", "flux-system", "private", 1, map[string]any{"image": "ghcr.io/org/private", "interval": "5m"},
			map[string]any{"observedGeneration": int64(1), "conditions": imgReady("False", "AuthenticationFailed", "401 Unauthorized", 1)}),
		imgObj("v1", "ImagePolicy", "apps", "app", 1, map[string]any{"imageRepositoryRef": map[string]any{"name": "app", "namespace": "flux-system"}},
			map[string]any{"latestRef": map[string]any{"name": "ghcr.io/org/app", "tag": "1.2.0"}, "observedGeneration": int64(1), "conditions": imgReady("True", "Succeeded", "ok", 1)}),
		imgObj("v1", "ImagePolicy", "apps", "stuck", 1, map[string]any{"imageRepositoryRef": map[string]any{"name": "missing", "namespace": "flux-system"}},
			map[string]any{"latestRef": map[string]any{"name": "ghcr.io/org/stuck", "tag": "0.9.0"}, "observedGeneration": int64(1), "conditions": imgReady("False", "DependencyNotReady", "waiting", 1)}),
		imgObj("v1", "ImageUpdateAutomation", "apps", "apps", 1, map[string]any{"interval": "10m", "sourceRef": map[string]any{"kind": "GitRepository", "name": "fleet"}},
			map[string]any{"observedGeneration": int64(1), "lastAutomationRunTime": time.Now().UTC().Format(time.RFC3339), "lastPushCommit": "abc1234def",
				"observedPolicies": map[string]any{"app": map[string]any{"name": "ghcr.io/org/app", "tag": "1.2.0"}}, "conditions": imgReady("True", "Succeeded", "repository up-to-date", 1)}),
		imgObj("v1", "ImageUpdateAutomation", "apps", "paused", 1, map[string]any{"interval": "10m", "suspend": true}, map[string]any{"conditions": imgReady("True", "Succeeded", "repository up-to-date", 1)}),
	)
	waitFor(t, "images counted", func() bool {
		p, ok := nativePart(s, TaskFlux, "images")
		return ok && p.Coverage == observe.Live && p.Native != nil && p.Native.Total == 6 && p.Native.Included == 2
	})
	r, ok := o.RowsNow(TaskFlux, "images")
	if !ok {
		t.Fatal("no rows")
	}
	byName := map[string]Row[ImageRow]{}
	for _, row := range r.Rows.(RowSet[ImageRow]).Rows {
		switch {
		case row.Data.Repository != nil:
			byName["repo/"+row.Data.Repository.Name] = row
		case row.Data.Policy != nil:
			byName["policy/"+row.Data.Policy.Name] = row
		case row.Data.Automation != nil:
			byName["auto/"+row.Data.Automation.Name] = row
		}
	}
	if l := byName["policy/app"].Data.RepositoryLink; l == nil || l.State != imageauto.JoinFound || l.Label != "Ready" {
		t.Fatalf("the policy's repository block: %+v", l)
	}
	stuck := byName["policy/stuck"]
	if stuck.Data.BlockedBy != "flux-system/missing" || stuck.Data.RepositoryLink.State != imageauto.JoinNotFound || stuck.Eval.Label != "Failed" {
		t.Fatalf("a blocked policy keeps its own verdict: %+v", stuck)
	}
	auto := byName["auto/apps"].Data
	if auto.UsesState != imageauto.JoinFound || len(auto.Uses) != 2 {
		t.Fatalf("the automation's policies: %+v", auto)
	}
	for _, u := range auto.Uses {
		if (u.Name == "app") != (u.State == imageauto.UseUsed) {
			t.Fatalf("used or pending: %+v", u)
		}
	}
	if byName["auto/paused"].Eval.Label != imageauto.LabelSuspended || byName["repo/private"].Eval.Label != "Failed" {
		t.Fatalf("suspended and failing: %+v %+v", byName["auto/paused"].Eval, byName["repo/private"].Eval)
	}
	if len(byName["repo/app"].Data.Policies) != 1 {
		t.Fatalf("the repository's policies: %+v", byName["repo/app"].Data.Policies)
	}
}

// S1b §1: v1beta2 is read; any other served version is a named gap.
func TestFluxImagesVersions(t *testing.T) {
	s, _ := startImages(t, imageauto.VersionOld,
		imgObj("v1beta2", "ImagePolicy", "apps", "old", 1, map[string]any{"imageRepositoryRef": map[string]any{"name": "app"}},
			map[string]any{"latestImage": "registry.example:5000/team/app:1.2", "conditions": imgReady("True", "Succeeded", "ok", 1)}))
	waitFor(t, "a v1beta2 policy read", func() bool {
		p, ok := nativePart(s, TaskFlux, "images")
		return ok && p.Coverage == observe.Live && p.Native != nil && p.Native.Total == 1
	})
	served := func(_ context.Context, _, _, _ string) (observe.Probe, error) {
		return observe.Probe{Served: true, Version: "v1beta1"}, nil
	}
	kinds, _ := FluxImages(NativeDeps{Cluster: "c1", Served: served})
	for _, k := range kinds {
		if _, err := k.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported image automation version v1beta1") {
			t.Errorf("%s: %v", k.Name, err)
		}
	}
}
