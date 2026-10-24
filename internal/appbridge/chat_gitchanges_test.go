package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/gitcli"
)

// AI A1 §3.2 and §8: git_changes over a fake Git - what it refuses to
// compare, the render's qualifications, its fallback, its checks and its
// bounds - and the turn's guard before Git is read.

const (
	shaApplied   = "1111111111111111111111111111111111111111"
	shaAttempted = "2222222222222222222222222222222222222222"
)

type fakeGit struct {
	reqs []gitChangesReq
	out  gitChangesOut
	err  error
}

func (g *fakeGit) changes(_ context.Context, req gitChangesReq) (gitChangesOut, error) {
	g.reqs = append(g.reqs, req)
	out := g.out
	// as GitService does: a comparison runs on the renders, which are not
	// returned
	if g.err == nil && out.RenderErr == "" && req.Compare != nil {
		req.Compare(out.Old, out.New)
		out.Old, out.New = nil, nil
	}
	return out, g.err
}

func changesChat(mod func(ks map[string]any)) (*fakeChatConn, *fakeGit) {
	ks := map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system", "uid": "uid-ks", "generation": int64(4)},
		"spec": map[string]any{"path": "./clusters/prd", "sourceRef": map[string]any{"kind": "GitRepository", "name": "fleet"},
			"postBuild": map[string]any{"substitute": map[string]any{"x": "NeedleSubstitute"}}},
		"status": map[string]any{"observedGeneration": int64(4), "lastAppliedRevision": "main@sha1:" + shaApplied, "lastAttemptedRevision": "main@sha1:" + shaAttempted}}
	if mod != nil {
		mod(ks)
	}
	f := newFakeChat()
	f.flux = map[string]*unstructured.Unstructured{
		"Kustomization|flux-system|apps": uobj(ks),
		"GitRepository|flux-system|fleet": uobj(map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
			"metadata": map[string]any{"name": "fleet", "namespace": "flux-system"},
			"spec":     map[string]any{"url": "https://bob:NeedleURLToken@github.com/acme/fleet.git"}}),
	}
	return f, &fakeGit{}
}

func runChanges(t *testing.T, f *fakeChatConn, g chatGit, args string) (chatEnvelope, ChatRead, string) {
	t.Helper()
	tools := newChatTools(f, fixedNow)
	tools.git = g
	res := tools.as("r3").Run(context.Background(), toolChanges, json.RawMessage(args))
	if len(res.JSON) > chatChangesResultMax {
		t.Fatalf("%d bytes", len(res.JSON))
	}
	var env chatEnvelope
	if err := json.Unmarshal(res.JSON, &env); err != nil {
		t.Fatalf("%v %s", err, res.JSON)
	}
	return env, res.Read, string(res.JSON)
}

const changesArgs = `{"kind":"Kustomization","namespace":"flux-system","name":"apps"}`

func TestChangesSaysWhatItCannotCompareAndReadsNothingMore(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mod        func(ks map[string]any)
	}{
		{"no attempted revision", "records no attempted Git revision", func(ks map[string]any) {
			delete(ks["status"].(map[string]any), "lastAttemptedRevision")
		}},
		{"the same commit, no history", "are the same commit (1111111), and Flux's history records no other revision", func(ks map[string]any) {
			ks["status"].(map[string]any)["lastAttemptedRevision"] = "main@sha1:" + shaApplied
		}},
		{"the same commit, history of it only", "are the same commit (2222222)", func(ks map[string]any) {
			ks["status"].(map[string]any)["lastAppliedRevision"] = "main@sha1:" + shaAttempted
			ks["status"].(map[string]any)["history"] = []any{
				map[string]any{"lastReconciledStatus": "HealthCheckFailed", "metadata": map[string]any{"revision": "main@sha1:" + shaAttempted}},
				map[string]any{"lastReconciledStatus": "ReconciliationSucceeded", "metadata": map[string]any{"revision": "main@sha1:" + shaAttempted}},
			}
		}},
		{"not a Git source", "is a OCIRepository, not a GitRepository", func(ks map[string]any) {
			ks["spec"].(map[string]any)["sourceRef"] = map[string]any{"kind": "OCIRepository", "name": "fleet"}
		}},
	} {
		f, g := changesChat(tc.mod)
		env, read, _ := runChanges(t, f, g, changesArgs)
		if read.Outcome != "completed" || !strings.Contains(env.Note, tc.want) || len(g.reqs) != 0 || env.Data != nil || env.ReadID != "r3" {
			t.Fatalf("%s: %+v %+v", tc.name, env, read)
		}
	}
	f, _ := changesChat(nil)
	env, read, _ := runChanges(t, f, nil, changesArgs)
	if read.Outcome != "failed" || !strings.Contains(env.Errors[0], "not available") {
		t.Fatalf("no Git in the session is a failed read: %+v", env)
	}
	f, g := changesChat(nil)
	g.err = errChatGitNotApproved
	env, read, out := runChanges(t, f, g, changesArgs)
	if read.Outcome != "failed" || !strings.Contains(env.Errors[0], "not approved") || strings.Contains(out, "NeedleURLToken") {
		t.Fatalf("%+v", env)
	}
	if len(g.reqs) != 1 || g.reqs[0].Applied != shaApplied || g.reqs[0].Attempted != shaAttempted || !strings.Contains(g.reqs[0].RepoURL, "github.com/acme/fleet") {
		t.Fatalf("Git is asked with the full SHAs and the source's URL, in process: %+v", g.reqs)
	}
}

func TestChangesRefusesBeforeAnyRead(t *testing.T) {
	f, g := changesChat(nil)
	for _, args := range []string{
		`{"kind":"HelmRelease","namespace":"flux-system","name":"apps"}`,
		`{"kind":"Kustomization","name":"apps"}`,
		`{"kind":"Kustomization","namespace":"flux-system","name":"apps","path":"x"}`,
	} {
		if _, read, _ := runChanges(t, f, g, args); read.Outcome != "failed" {
			t.Fatalf("%s: %+v", args, read)
		}
	}
	if f.fluxCalls != 0 || len(g.reqs) != 0 {
		t.Fatal("a refused call reads nothing")
	}
}

func TestChangesComparesTheRendersAndChecksEachObject(t *testing.T) {
	f, g := changesChat(nil)
	committed := time.Date(2026, 10, 21, 11, 58, 0, 0, time.UTC)
	g.out = gitChangesOut{
		Commits:      []gitcli.Commit{{SHA: shaAttempted, Author: "Ada", Time: committed, Committed: committed, Subject: "bump web, token ghp_abcdefghijklmnopqrstuvwxyz0123456789"}},
		CommitsTotal: 1, Ancestor: true,
		Files: []gitcli.NameStatus{{Status: "M", Path: "clusters/prd/kustomization.yaml"}}, FilesTotal: 1,
		Old: []*unstructured.Unstructured{deploy("web", "ghcr.io/acme/web:1.0", "NeedleEnvOld"), deploy("gone", "x:1", "a")},
		New: []*unstructured.Unstructured{deploy("web", "ghcr.io/acme/web:1.1", "NeedleEnvNew")},
	}
	env, read, out := runChanges(t, f, g, changesArgs)
	for _, n := range []string{"NeedleEnvOld", "NeedleEnvNew", "NeedleSubstitute", "NeedleURLToken", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"} {
		if strings.Contains(out, n) {
			t.Fatalf("%q crossed: %s", n, out)
		}
	}
	b, _ := json.Marshal(env.Data)
	var d chatChangesData
	_ = json.Unmarshal(b, &d)
	if d.Render.State != "compared" || d.ObjectsTotal != 2 || len(d.Commits) != 1 || d.Commits[0].Author != "Ada" || d.Files[0] != "M clusters/prd/kustomization.yaml" ||
		d.Path != "./clusters/prd" || !d.Ancestor || len(d.Render.Qualifications) != 2 || d.Render.Qualifications[0] != historicalSpecUnknown ||
		!strings.Contains(d.Render.Qualifications[1], "postBuild substitution is not applied") {
		t.Fatalf("%s", b)
	}
	if len(env.Checks) != 2 || read.Outcome != "completed" || !env.Complete {
		t.Fatalf("%+v %+v", env.Checks, read)
	}
	gone, web := env.Checks[0], env.Checks[1]
	if gone.Statement != "the source render of revision 2222222 removes Deployment shop/gone (by name), against 1111111 (a source render under the current spec, the historical spec unknown; partial)" ||
		gone.Class != checkDiff || gone.Subject.Identity != identityName || gone.Subject.UID != "" || gone.Coverage != "partial" || gone.ID != "r3.c1" {
		t.Fatalf("%+v", gone)
	}
	if web.Statement != "the source render of revision 2222222 changes Deployment shop/web (by name), against 1111111: spec.template.spec.containers[web].image; spec.template.spec.containers[web].env[LOG_LEVEL], changed (not shown) (a source render under the current spec, the historical spec unknown; partial)" ||
		web.Observed != "2026-10-21T11:58:00Z" {
		t.Fatalf("%+v", web)
	}
}

func TestChangesFallsBackWhenTheRenderIsNotEstablished(t *testing.T) {
	f, g := changesChat(nil)
	g.out = gitChangesOut{Files: []gitcli.NameStatus{{Status: "M", Path: "clusters/prd/a.yaml"}}, FilesTotal: 1,
		RenderErr: "the render of 2222222: the confined build passed its time bound and was stopped"}
	env, read, _ := runChanges(t, f, g, changesArgs)
	b, _ := json.Marshal(env.Data)
	var d chatChangesData
	_ = json.Unmarshal(b, &d)
	if d.Render.State != "not established" || !strings.Contains(d.Render.Reason, "time bound") || len(d.Objects) != 0 || len(env.Checks) != 0 ||
		env.Complete || len(d.Files) != 1 || read.Outcome != "completed" {
		t.Fatalf("a failed render keeps the files and commits and claims no change: %s %+v", b, env)
	}
}

// AI A1 implementation review, finding 10: checks count toward the
// result's bound. Long legal field names and annotation keys on many
// objects make long diff checks; the result stays within 32 KiB, and the
// ledger holds the checks the model was sent.
func TestChangesBoundsItsChecks(t *testing.T) {
	for name, build := range map[string]func(n int) (*unstructured.Unstructured, *unstructured.Unstructured){
		"custom resources with long field names": func(n int) (*unstructured.Unstructured, *unstructured.Unstructured) {
			a, b := map[string]any{}, map[string]any{}
			for j := 0; j < 8; j++ {
				k := fmt.Sprintf("field%d", j) + strings.Repeat("x", 490)
				a[k], b[k] = "old", "new"
			}
			old := object("example.test/v1", "Widget", strings.Repeat("n", 63), fmt.Sprintf("obj%02d", n)+strings.Repeat("a", 240), map[string]any{"spec": a})
			return old, object("example.test/v1", "Widget", strings.Repeat("n", 63), old.GetName(), map[string]any{"spec": b})
		},
		"deployments with long annotation keys": func(n int) (*unstructured.Unstructured, *unstructured.Unstructured) {
			name := fmt.Sprintf("obj-%02d-", n) + strings.Repeat("a", 240)
			old, next := deploy(name, "web:v1", "old"), deploy(name, "web:v1", "new")
			annA, annB := map[string]string{}, map[string]string{}
			for j := 0; j < 5; j++ {
				k := fmt.Sprintf("example.com/k%d-", j) + strings.Repeat("z", 240)
				annA[k], annB[k] = "old", "new"
			}
			old.SetAnnotations(annA)
			next.SetAnnotations(annB)
			return old, next
		},
	} {
		f, g := changesChat(nil)
		for n := 0; n < 12; n++ {
			old, next := build(n)
			g.out.Old, g.out.New = append(g.out.Old, old), append(g.out.New, next)
		}
		env, read, raw := runChanges(t, f, g, changesArgs) // fails over 32 KiB
		if len(env.Checks) == 0 || len(read.Checks) != len(env.Checks) || read.Checks[0].ID != env.Checks[0].ID {
			t.Fatalf("%s: the ledger holds the checks sent: %d %d", name, len(read.Checks), len(env.Checks))
		}
		for _, c := range env.Checks {
			if len(c.Statement) > chatCheckStatementMax+len("…") {
				t.Fatalf("%s: %d bytes", name, len(c.Statement))
			}
		}
		if len(raw) > chatChangesResultMax {
			t.Fatalf("%s: %d bytes", name, len(raw))
		}
	}
}

func TestChangesStaysWithinItsBound(t *testing.T) {
	f, g := changesChat(nil)
	for i := 0; i < 200; i++ {
		g.out.Files = append(g.out.Files, gitcli.NameStatus{Status: "M", Path: fmt.Sprintf("clusters/prd/%s-%03d.yaml", strings.Repeat("f", 150), i)})
	}
	g.out.FilesTotal = 450
	for i := 0; i < 20; i++ {
		g.out.Commits = append(g.out.Commits, gitcli.Commit{SHA: fmt.Sprintf("%040d", i), Author: "Ada", Subject: strings.Repeat("s", 190)})
	}
	g.out.CommitsTotal = 35
	for i := 0; i < 30; i++ {
		n := fmt.Sprintf("app-%02d", i)
		g.out.Old = append(g.out.Old, deploy(n, "x:1", "a"))
		g.out.New = append(g.out.New, deploy(n, "x:2", "b"))
	}
	env, read, out := runChanges(t, f, g, changesArgs)
	b, _ := json.Marshal(env.Data)
	var d chatChangesData
	_ = json.Unmarshal(b, &d)
	if len(out) > chatChangesResultMax || len(out) <= chatResultMax || !env.Truncated || read.Outcome != "truncated" || d.FilesTotal != 450 || d.CommitsTotal != 35 ||
		d.ObjectsTotal != 30 || len(d.Objects) != renderObjectsMax || len(env.Checks) != chatChecksMax || env.Complete {
		t.Fatalf("the files go first, the objects stay: %d bytes, %d files, %d commits, %d objects", len(out), len(d.Files), len(d.Commits), len(d.Objects))
	}
}

func TestChangesReadsGitOnlyBehindTheTurnsGuard(t *testing.T) {
	g := &fakeGit{out: gitChangesOut{Old: []*unstructured.Unstructured{deploy("web", "x:1", "a")}, New: []*unstructured.Unstructured{deploy("web", "x:2", "a")}}}
	f := newToolsFixture(t, calls(ai.ToolCall{ID: "c1", Name: toolChanges, Args: json.RawMessage(changesArgs)}), answer("one"),
		calls(ai.ToolCall{ID: "c2", Name: toolChanges, Args: json.RawMessage(changesArgs)}), answer("two"))
	conn, _ := changesChat(nil)
	conn.summary = f.conn.summary
	f.conn, f.cur = conn, conn
	SetChatGit(f.svc, nil) // a nil service never replaces the read
	f.svc.git = g
	st := f.svc.Start("x")
	st = f.svc.Ask("x", st.ConversationID, "what changed?", nil)
	if len(g.reqs) != 1 || st.Exchanges[0].Reads[0].Tool != toolChanges || st.Exchanges[0].Reads[0].Outcome != "completed" {
		t.Fatalf("%+v", st.Exchanges[0].Reads)
	}
	// the connection is lost after the GitRepository read: Git is never asked
	conn.fluxHook = func(kind string) {
		if kind == "GitRepository" {
			f.mu.Lock()
			f.usable = false
			f.mu.Unlock()
		}
	}
	_ = f.svc.Ask("x", st.ConversationID, "and now?", nil)
	if len(g.reqs) != 1 {
		t.Fatalf("Git was read after the guard failed: %d", len(g.reqs))
	}
}

// The A1 lab's case: a crashing revision whose first reconcile passed its
// health check is recorded as applied, so applied equals attempted; Flux's
// history names the revision before it that reconciled successfully.
func TestChangesFallsBackToFluxsHistoryWhenAppliedEqualsAttempted(t *testing.T) {
	const older = "3333333333333333333333333333333333333333"
	f, g := changesChat(func(ks map[string]any) {
		st := ks["status"].(map[string]any)
		st["lastAppliedRevision"] = "main@sha1:" + shaAttempted
		st["history"] = []any{
			map[string]any{"lastReconciledStatus": "HealthCheckFailed", "metadata": map[string]any{"revision": "main@sha1:" + shaAttempted}},
			map[string]any{"lastReconciledStatus": "ReconciliationSucceeded", "metadata": map[string]any{"revision": "main@sha1:" + shaAttempted}},
			map[string]any{"lastReconciledStatus": "HealthCheckFailed", "metadata": map[string]any{"revision": "main@sha1:" + shaApplied}},
			map[string]any{"lastReconciledStatus": "ReconciliationSucceeded", "metadata": map[string]any{"revision": "main@sha1:" + older}},
		}
	})
	g.out = gitChangesOut{Old: []*unstructured.Unstructured{deploy("web", "x:1", "a")}, New: []*unstructured.Unstructured{deploy("web", "x:1", "b")}}
	env, _, _ := runChanges(t, f, g, changesArgs)
	if len(g.reqs) != 1 || g.reqs[0].Applied != older || g.reqs[0].Attempted != shaAttempted {
		t.Fatalf("the newest other revision that reconciled successfully, never a failed one: %+v", g.reqs)
	}
	b, _ := json.Marshal(env.Data)
	var d chatChangesData
	_ = json.Unmarshal(b, &d)
	if d.BaseFrom != "status.history: the newest other revision Flux reconciled successfully" || d.Applied != older {
		t.Fatalf("%s", b)
	}
}
