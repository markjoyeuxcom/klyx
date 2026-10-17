package healthsrc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/observe"
)

func gitopsFixture() []runtime.Object {
	argoApp := func(name, sync, health string) *unstructured.Unstructured {
		u := cr("argoproj.io/v1alpha1", "Application", "argocd", name, map[string]any{"project": "default",
			"source": map[string]any{"repoURL": "https://git.example/shop", "path": "deploy"}, "destination": map[string]any{"namespace": "shop", "server": "https://kubernetes.default.svc"}}, nil, nil)
		u.Object["status"] = map[string]any{"sync": map[string]any{"status": sync}, "health": map[string]any{"status": health}}
		return u
	}
	return []runtime.Object{
		cr("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "apps", map[string]any{"path": "./apps", "sourceRef": map[string]any{"kind": "GitRepository", "name": "shop"}},
			[]any{cond("Ready", "False", "BuildFailed")}, nil),
		cr("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "infra", map[string]any{"path": "./infra", "sourceRef": map[string]any{"kind": "GitRepository", "name": "shop"}},
			[]any{cond("Ready", "True", "ReconciliationSucceeded")}, nil),
		// Suspended and failed: an intended pause, never attention.
		cr("helm.toolkit.fluxcd.io/v2", "HelmRelease", "shop", "redis", map[string]any{"suspend": true, "chart": map[string]any{"spec": map[string]any{"chart": "redis"}}},
			[]any{cond("Ready", "False", "UpgradeFailed")}, nil),
		// A failing source nothing reconciles from yet: it must light its part.
		cr("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "unused", map[string]any{"url": "https://git.example/unused"},
			[]any{cond("Ready", "False", "GitOperationFailed")}, nil),
		cr("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "shop", map[string]any{"url": "https://git.example/shop"},
			[]any{cond("Ready", "True", "Succeeded")}, nil),
		argoApp("web", "OutOfSync", "Healthy"),
		argoApp("api", "Synced", "Healthy"),
		cr("argoproj.io/v1alpha1", "ApplicationSet", "argocd", "tenants", map[string]any{"generators": []any{map[string]any{"list": map[string]any{}}}},
			[]any{map[string]any{"type": "ErrorOccurred", "status": "True", "message": "rendering failed"}}, nil),
	}
}

func gitopsListKinds() map[schema.GroupVersionResource]string {
	m := map[schema.GroupVersionResource]string{
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: "KustomizationList",
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}:        "HelmReleaseList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:             "ApplicationList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}:          "ApplicationSetList",
		// S1c: served beside the sources at v1
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "externalartifacts"}: "ExternalArtifactList",
	}
	for _, r := range []string{"gitrepositories", "ocirepositories", "buckets", "helmrepositories", "helmcharts"} {
		m[schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: r}] = "SourceList"
	}
	return m
}

func TestGitOpsTasksOnFixtures(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gitopsListKinds(), gitopsFixture()...)
	versions := map[string]string{"kustomize.toolkit.fluxcd.io": "v1", "helm.toolkit.fluxcd.io": "v2", "source.toolkit.fluxcd.io": "v1", "argoproj.io": "v1alpha1"}
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		if v, ok := versions[group]; ok {
			return observe.Probe{Served: true, Version: v}, nil
		}
		return observe.Probe{Served: false}, nil
	}
	kinds, domains := GitOps(NativeDeps{Cluster: "c1", Dyn: dyn, Served: served})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, Throttle: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-o.Done() }()
	o.Start(ctx)

	expect := func(taskID, partID string, total, included int, check func(observe.Part) bool) {
		t.Helper()
		waitFor(t, taskID+"/"+partID, func() bool {
			p, ok := nativePart(s, taskID, partID)
			return ok && p.Coverage == observe.Live && p.Native != nil && p.Native.Total == total && p.Native.Included == included && (check == nil || check(p))
		})
	}
	expect(TaskFlux, "reconciliations", 3, 1, func(p observe.Part) bool {
		return p.Native.Roles.Fault == 1 && p.Native.Roles.Paused == 1 && p.Top[0].Name == "apps" && p.Top[0].GVR == "kustomize.toolkit.fluxcd.io/v1/kustomizations"
	})
	expect(TaskFlux, "sources", 2, 1, func(p observe.Part) bool { return p.Top[0].Name == "unused" })
	expect(TaskArgo, "applications", 2, 1, func(p observe.Part) bool {
		return p.Top[0].Name == "web" && p.Top[0].Role == "degraded" && p.Top[0].Label == "OutOfSync" && p.Native.Word == "out of sync or unhealthy"
	})
	expect(TaskArgo, "applicationsets", 1, 1, func(p observe.Part) bool { return p.Top[0].Name == "tenants" })
}

func TestGitOpsProjectionParity(t *testing.T) {
	for _, obj := range gitopsFixture() {
		raw := obj.(*unstructured.Unstructured)
		pv, err := ProjectUnstructured(raw)
		if err != nil {
			t.Fatal(err)
		}
		proj := pv.(*unstructured.Unstructured)
		var a, b any
		switch raw.GetKind() {
		case "Kustomization":
			a, b = flux.ParseKustomization(raw), flux.ParseKustomization(proj)
		case "HelmRelease":
			a, b = flux.ParseHelmRelease(raw), flux.ParseHelmRelease(proj)
		case "GitRepository":
			a, b = flux.ParseSource(raw), flux.ParseSource(proj)
		case "Application":
			a, b = argo.Parse(raw), argo.Parse(proj)
		case "ApplicationSet":
			a, b = argo.ParseApplicationSet(raw), argo.ParseApplicationSet(proj)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s %s parses differently after projection:\nraw  %+v\nproj %+v", raw.GetKind(), raw.GetName(), a, b)
		}
	}
}
