package appbridge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

const pushSHA = "abc1234def5678abc1234def5678abc1234def56"

func automationObj(uid string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "image.toolkit.fluxcd.io/v1", "kind": "ImageUpdateAutomation",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system", "uid": uid, "generation": int64(1)},
		"spec": map[string]any{"interval": "10m", "sourceRef": map[string]any{"kind": "GitRepository", "name": "fleet"},
			"git": map[string]any{"push": map[string]any{"branch": "auto"}}},
		"status": map[string]any{"lastPushCommit": pushSHA},
	}}
}

func gitRepoObj() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "fleet", "namespace": "flux-system", "uid": "g"},
		"spec":     map[string]any{"url": "https://bob:TOKENneedle@github.com/org/fleet", "ref": map[string]any{"branch": "main"}},
		"status":   map[string]any{"artifact": map[string]any{"revision": "main@sha1:" + pushSHA}},
	}}
}

func reconRows(live bool, ks ...flux.Resource) observe.Rows {
	cov := observe.Live
	if !live {
		cov = observe.Unreadable
	}
	rows := healthsrc.RowSet[flux.Resource]{}
	for _, k := range ks {
		rows.Rows = append(rows.Rows, healthsrc.Row[flux.Resource]{Data: k})
	}
	return observe.Rows{Part: observe.Part{Coverage: cov}, Rows: rows}
}

// Coverage S1b §2: the automation's push evidence, fenced by its UID.
func TestGetImageDetailPushEvidence(t *testing.T) {
	ks := func(name, rev string) flux.Resource {
		return flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: name, SourceKind: "GitRepository", SourceNamespace: "flux-system", SourceName: "fleet", Revision: rev}
	}
	conn := &fakeGitOpsConn{
		byKind: map[string]*unstructured.Unstructured{"ImageUpdateAutomation": automationObj("u-auto"), "GitRepository": gitRepoObj()},
		events: []workloads.EventSummary{
			{UID: "u-auto", Reason: "Succeeded", Message: "pushed commit 'abc1234' to branch 'auto'"},
			{UID: "u-other", Reason: "Succeeded", Message: "pushed commit 'abc1234' to branch 'main'"},
		},
		rows: reconRows(true, ks("apps", "main@sha1:"+pushSHA), ks("infra", "main@sha1:0000000"),
			flux.Resource{Kind: flux.KustomizationKind, Name: "other", SourceKind: "GitRepository", SourceNamespace: "flux-system", SourceName: "elsewhere"}),
		rowsOK: true,
	}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	d := svc.GetImageDetail("c", "ImageUpdateAutomation", "flux-system", "apps", "u-auto")
	if d.Error != "" || d.Push == nil || len(d.Events) != 1 {
		t.Fatalf("detail: %+v", d)
	}
	p := d.Push
	if p.LastPushCommit != pushSHA || p.SourceName != "fleet" || d.Generation != 1 {
		t.Fatalf("the evidence names what it describes: %+v gen %d", p, d.Generation)
	}
	if p.BranchState != "bound" || p.Branch != "auto" {
		t.Fatalf("the branch from the bound event only: %+v", p)
	}
	if p.SourceLine != "fetched" || p.SourceBranchNote != "pushed to auto; this source tracks main" {
		t.Fatalf("the source line: %+v", p)
	}
	if p.Target.Branch != "auto" || p.Target.CheckoutFrom != "source" || strings.Contains(p.Target.RepositoryURL, "TOKENneedle") {
		t.Fatalf("the push target as configured now: %+v", p.Target)
	}
	if p.AppliedState != "found" || len(p.Applied) != 2 || !p.Applied[0].Applied || p.Applied[1].Applied {
		t.Fatalf("applied: %+v", p.Applied)
	}

	// a replacement is refused
	if d := svc.GetImageDetail("c", "ImageUpdateAutomation", "flux-system", "apps", "u-old"); !d.Replaced || d.Push != nil || d.UID != "u-auto" {
		t.Fatalf("replaced: %+v", d)
	}
	// an unreadable GitRepository and an unreadable reconciliations read
	conn.byKindErr = map[string]error{"GitRepository": errors.New("forbidden")}
	conn.rows = reconRows(false)
	d = svc.GetImageDetail("c", "ImageUpdateAutomation", "flux-system", "apps", "u-auto")
	if d.Push.SourceState != "not readable" || d.Push.SourceLine != "missing" || d.Push.AppliedState != "not readable" || len(d.Push.Applied) != 0 {
		t.Fatalf("unreadable: %+v", d.Push)
	}
	if d := svc.GetImageDetail("c", "Kustomization", "flux-system", "apps", "u"); d.Error == "" {
		t.Fatal("not an image kind")
	}
}

// Coverage S1b §6: the author's email and the selector never reach a model,
// through chat's projection or Explain's allowlist; the template, its
// values and push option values are withheld by the sanitiser first.
func TestImageAutomationNeverReachesAModel(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "image.toolkit.fluxcd.io/v1", "kind": "ImageUpdateAutomation",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system", "uid": "u"},
		"spec": map[string]any{"policySelector": map[string]any{"matchLabels": map[string]any{"team": "SELECTORneedle"}},
			"git": map[string]any{"commit": map[string]any{"author": map[string]any{"name": "bot", "email": "EMAILneedle@example.com"},
				"messageTemplate": "TEMPLATEneedle", "messageTemplateValues": map[string]any{"k": "VALUEneedle"}},
				"push": map[string]any{"branch": "auto", "options": map[string]any{"ci.variable": "OPTIONneedle"}}}},
	}}
	gvr := schema.GroupVersionResource{Group: "image.toolkit.fluxcd.io", Version: "v1", Resource: "imageupdateautomations"}
	sanitised := &unstructured.Unstructured{Object: crd.SanitizeEmbedded(gvr.Group, u.GetKind(), u.Object).Object}
	b, _ := json.Marshal(chatProject(gvr, sanitised))
	for _, n := range []string{"SELECTORneedle", "EMAILneedle", "TEMPLATEneedle", "VALUEneedle", "OPTIONneedle"} {
		if strings.Contains(string(b), n) {
			t.Fatalf("%s reached chat: %s", n, b)
		}
	}
	if !strings.Contains(string(b), `"auto"`) {
		t.Fatalf("the rest is projected: %s", b)
	}
	a := aiEvidenceFields[healthsrc.TaskFlux+"/images"]["automation"]
	for _, f := range []string{"authorEmail", "selector", "authorName"} {
		if _, ok := a[f]; ok {
			t.Fatalf("%s is an AI fact", f)
		}
	}
}
