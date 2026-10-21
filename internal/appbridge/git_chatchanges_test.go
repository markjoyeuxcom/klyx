package appbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitcli"
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
	if err != nil || out.RenderErr == "" || out.Old != nil || out.New != nil || out.FilesTotal != 1 {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestChatRendersWaitForTheAdmission(t *testing.T) {
	for i := 0; i < chatRendersAtOnce; i++ {
		chatRenderAdmission <- struct{}{}
	}
	defer func() {
		for i := 0; i < chatRendersAtOnce; i++ {
			<-chatRenderAdmission
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := (&GitService{}).chatRender(ctx, nil, "", gitcli.Rev{}, "", map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("a third render waits for a slot within its deadline: %v", err)
	}
}
