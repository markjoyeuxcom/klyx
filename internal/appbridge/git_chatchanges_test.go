package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/readfence"
)

// AI A1 §3.2: the Git half of git_changes over a real repository and the
// confined build (this test binary is the child, as for the cache diff).

const historyBase = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          image: ghcr.io/acme/web:%s\n          env:\n            - name: LOG_LEVEL\n              value: %s\n"

func TestGitChangesListsAndRendersBothRevisions(t *testing.T) {
	store, shas := newHistoryFixture(t,
		map[string]string{
			"clusters/prd/kustomization.yaml": "resources: [../../apps/base]\n",
			"apps/base/kustomization.yaml":    "resources: [deployment.yaml]\n",
			"apps/base/deployment.yaml":       fmt.Sprintf(historyBase, "1.0", "NeedleOld"),
		},
		// a base change outside the unit's path, and a change inside it
		map[string]string{
			"apps/base/deployment.yaml":       fmt.Sprintf(historyBase, "1.1", "NeedleNew"),
			"clusters/prd/kustomization.yaml": "resources: [../../apps/base]\ncommonLabels:\n  tier: web\n",
		},
		map[string]string{"other/readme.md": "unrelated\n"},
	)
	svc := newDiffService(t, store)
	spec := map[string]any{"path": "./clusters/prd", "targetNamespace": "shop", "commonMetadata": map[string]any{"labels": map[string]any{"team": "a"}}}
	out, err := svc.changes(context.Background(), gitChangesReq{RepoURL: "https://github.com/acme/fleet.git", Applied: shas[0], Attempted: shas[2], Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ensured) != 2 || store.ensured[0] != shas[0] || store.ensured[1] != shas[2] {
		t.Fatalf("both revisions are ensured in the cache: %v", store.ensured)
	}
	if out.CommitsTotal != 1 || len(out.Commits) != 1 || out.Commits[0].SHA != shas[1] || out.Commits[0].Subject != "fleet 1" || !out.Ancestor {
		t.Fatalf("only the commit touching the unit's path: %+v", out.Commits)
	}
	if out.FilesTotal != 1 || out.Files[0].Path != "clusters/prd/kustomization.yaml" {
		t.Fatalf("only the files under the path: %+v", out.Files)
	}
	if out.RenderErr != "" || len(out.Old) != 1 || len(out.New) != 1 {
		t.Fatalf("%q %d %d", out.RenderErr, len(out.Old), len(out.New))
	}
	n := out.New[0]
	if n.GetNamespace() != "shop" || n.GetLabels()["tier"] != "web" || n.GetLabels()["team"] != "a" || out.Old[0].GetLabels()["tier"] != "" {
		t.Fatalf("both renders under the current spec's overlay and commonMetadata: %v %v", n.GetLabels(), out.Old[0].GetLabels())
	}
	// the base change outside the path shows in the comparison
	cs, total := compareRenders(out.Old, out.New, testResolve, &projector{})
	// (commonLabels also writes the selector and the pod template's labels,
	// which chat's projection does not hold)
	if total != 1 || len(cs[0].Visible) != 3 || cs[0].Visible[0].Path != "metadata.labels.tier" || cs[0].Visible[1].Path != "spec.selector" ||
		cs[0].Visible[2].Path != "spec.template.spec.containers[web].image" ||
		hiddenPaths(&cs[0]) != "spec.template.metadata added (not shown); spec.template.spec.containers[web].env[LOG_LEVEL] changed (not shown)" {
		t.Fatalf("%+v", cs)
	}

	if _, err := svc.changes(context.Background(), gitChangesReq{RepoURL: "https://github.com/acme/other.git", Applied: shas[0], Attempted: shas[2], Spec: spec}); !errors.Is(err, errChatGitNotApproved) {
		t.Fatalf("an unapproved repository is never read: %v", err)
	}

	// a build that fails falls back: the commits and files stand
	bad := map[string]any{"path": "./clusters/prd", "patches": []any{map[string]any{"patch": "not: [valid"}}}
	out, err = svc.changes(context.Background(), gitChangesReq{RepoURL: "https://github.com/acme/fleet.git", Applied: shas[0], Attempted: shas[2], Spec: bad})
	if err != nil || out.RenderErr != "the render of "+shas[0][:7]+": kustomize reported an error; its text is not sent, since it can quote the repository's values" ||
		out.Old != nil || out.New != nil || out.FilesTotal != 1 {
		t.Fatalf("%v %+v", err, out)
	}
}

// AI A1 implementation review, finding 3: each Git step checks the read
// fence, so one that changes after the first fetch, or once the second
// render reaches its build, stops the rest; the control reads on.
func TestGitChangesStopsAtItsFence(t *testing.T) {
	store, shas := newHistoryFixture(t,
		map[string]string{"clusters/prd/kustomization.yaml": "resources: [deployment.yaml]\n", "clusters/prd/deployment.yaml": fmt.Sprintf(historyBase, "1.0", "a")},
		map[string]string{"clusters/prd/deployment.yaml": fmt.Sprintf(historyBase, "1.1", "b")},
	)
	svc := newDiffService(t, store)
	req := gitChangesReq{RepoURL: "https://github.com/acme/fleet.git", Applied: shas[0], Attempted: shas[1], Spec: map[string]any{"path": "./clusters/prd"}}
	changed := errors.New("the fence changed")
	ctx := readfence.With(context.Background(), func() error {
		if len(store.ensured) >= 1 {
			return changed
		}
		return nil
	})
	if _, err := svc.changes(ctx, req); !errors.Is(err, changed) || len(store.ensured) != 1 {
		t.Fatalf("the second fetch is not made: %v %v", err, store.ensured)
	}
	var builds atomic.Int32
	exe := childExe
	childExe = func() (string, error) { builds.Add(1); return exe() }
	t.Cleanup(func() { childExe = exe })
	ctx = readfence.With(context.Background(), func() error {
		if builds.Load() >= 2 {
			return changed
		}
		return nil
	})
	if out, err := svc.changes(ctx, req); !errors.Is(err, changed) || builds.Load() != 2 || out.RenderErr != "" || out.New != nil {
		t.Fatalf("the second build is not run, and no render failure is claimed: %v %d %+v", err, builds.Load(), out)
	}
	if out, err := svc.changes(context.Background(), req); err != nil || len(out.Old) != 1 || len(out.New) != 1 {
		t.Fatalf("the control: %v %+v", err, out)
	}
	// every step is fenced: two fetches, four history reads, and each
	// render's inputs, files and build; a fence failing at any one of them
	// stops the comparison there
	var calls atomic.Int32
	count := readfence.With(context.Background(), func() error { calls.Add(1); return nil })
	if _, err := svc.changes(count, req); err != nil || calls.Load() != 12 {
		t.Fatalf("12 fenced steps: %d %v", calls.Load(), err)
	}
	for n := int32(1); n <= 12; n++ {
		var calls atomic.Int32
		ctx := readfence.With(context.Background(), func() error {
			if calls.Add(1) == n {
				return changed
			}
			return nil
		})
		if out, err := svc.changes(ctx, req); !errors.Is(err, changed) || out.RenderErr != "" {
			t.Fatalf("a fence failing at step %d: %v %+v", n, err, out)
		}
	}
}

// AI A1 implementation review, P1: a failed render's own text never reaches
// the model. kustomize quotes an invalid patch whole, an ordinary Helm value
// included, which no scrubber recognises; the tool sends the failure's
// fixed class.
func TestAFailedRenderSendsOnlyItsClass(t *testing.T) {
	const needle = "PrivateHelmValueNeedle"
	release := "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: web\nspec:\n  values:\n    featureMode: " + needle + "\n"
	bad := "resources: [release.yaml]\npatches:\n- patch: |-\n    - op: add\n      path: /spec/values\n      value: {featureMode: " + needle + "}\n    - invalid\n"
	store, shas := newHistoryFixture(t,
		map[string]string{"clusters/prd/kustomization.yaml": "resources: [release.yaml]\n", "clusters/prd/release.yaml": release},
		map[string]string{"clusters/prd/kustomization.yaml": bad},
	)
	svc := newDiffService(t, store)
	f, _ := changesChat(func(ks map[string]any) {
		st := ks["status"].(map[string]any)
		st["lastAppliedRevision"], st["lastAttemptedRevision"] = "main@sha1:"+shas[0], "main@sha1:"+shas[1]
		delete(ks["spec"].(map[string]any), "postBuild")
	})
	env, read, raw := runChanges(t, f, svc, changesArgs)
	b, _ := json.Marshal(env.Data)
	var d chatChangesData
	_ = json.Unmarshal(b, &d)
	if d.Render.State != "not established" ||
		d.Render.Reason != "the render of "+shas[1][:7]+": kustomize reported an error; its text is not sent, since it can quote the repository's values" ||
		d.FilesTotal != 1 || read.Outcome != "completed" {
		t.Fatalf("the build failed and the files stand: %s", b)
	}
	if strings.Contains(raw, needle) || strings.Contains(read.Detail, needle) || strings.Contains(raw, "invalid") {
		t.Fatalf("the renderer's text reached the model: %s", raw)
	}
}

// The comparison runs inside the admission, on both renders, and the
// renders are not returned: what it keeps is all that outlives them (the
// §8 load test). Through the tool, the comparison is the result's.
func TestAComparisonRunsInsideTheAdmission(t *testing.T) {
	store, shas := newHistoryFixture(t,
		map[string]string{"clusters/prd/kustomization.yaml": "resources: [deployment.yaml]\n", "clusters/prd/deployment.yaml": fmt.Sprintf(historyBase, "1.0", "a")},
		map[string]string{"clusters/prd/deployment.yaml": fmt.Sprintf(historyBase, "1.1", "b")},
	)
	svc := newDiffService(t, store)
	held, olds, news := -1, 0, 0
	out, err := svc.changes(context.Background(), gitChangesReq{RepoURL: "https://github.com/acme/fleet.git", Applied: shas[0], Attempted: shas[1],
		Spec: map[string]any{"path": "./clusters/prd"}, Compare: func(old, new []*unstructured.Unstructured) {
			held, olds, news = len(chatRenderAdmission), len(old), len(new)
		}})
	if err != nil || held != 1 || olds != 1 || news != 1 || out.Old != nil || out.New != nil || len(chatRenderAdmission) != 0 {
		t.Fatalf("compared inside the admission, the renders not returned: %v %d %d %d %+v", err, held, olds, news, out)
	}
	f, _ := changesChat(func(ks map[string]any) {
		st := ks["status"].(map[string]any)
		st["lastAppliedRevision"], st["lastAttemptedRevision"] = "main@sha1:"+shas[0], "main@sha1:"+shas[1]
	})
	env, _, _ := runChanges(t, f, svc, changesArgs)
	b, _ := json.Marshal(env.Data)
	var d chatChangesData
	_ = json.Unmarshal(b, &d)
	if d.Render.State != "compared" || d.ObjectsTotal != 1 || len(d.Objects) != 1 || d.Objects[0].Name != "web" {
		t.Fatalf("the tool's comparison is its result: %s", b)
	}
}

// One comparison at a time, fleet-wide, and another waits within its
// deadline.
func TestChatComparisonsWaitForTheAdmission(t *testing.T) {
	if cap(chatRenderAdmission) != 1 {
		t.Fatalf("one comparison at a time: %d", cap(chatRenderAdmission))
	}
	release, err := admitComparison(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := admitComparison(ctx); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("a second comparison waits within its deadline: %v", err)
	}
}
