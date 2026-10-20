package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/line"
)

// The bridge needle (coverage S0b §7): a plain marker no scrubber knows, in
// every payload leaf of varied types; the serialised DTO the frontend
// receives must not carry it anywhere.
const s0bNeedle = "BRIDGEneedle9K"

func s0bConn(t *testing.T, objs ...runtime.Object) *fleet.ClusterConn {
	t.Helper()
	kinds := map[schema.GroupVersionResource]string{
		{Group: "fluxcd.controlplane.io", Version: "v1", Resource: "resourcesets"}:      "ResourceSetList",
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}: "GitRepositoryList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:           "ApplicationList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objs...)
	return fleet.NewClusterConn("x", kubefake.NewSimpleClientset(), nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
}

func TestS0b_BridgeNeedle_Detail(t *testing.T) {
	rs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSet",
		"metadata": map[string]any{"name": "with-secret", "namespace": "team-a", "uid": "u1",
			"annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": `{"spec":{"inputs":[{"t":"` + s0bNeedle + `"}]}}`}},
		"spec": map[string]any{
			"inputs":            []any{map[string]any{"db_password": s0bNeedle, "port": int64(5432), "flag": true, "list": []any{s0bNeedle}}},
			"resources":         []any{map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "db"}, "stringData": map[string]any{"password": s0bNeedle}}},
			"resourcesTemplate": "kind: Secret\nstringData:\n  t: " + s0bNeedle,
		},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "BuildFailed",
			"message": "fetch https://bob:" + s0bNeedle + "@git.example.com/x failed"}}},
	}}
	gr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "platform", "namespace": "flux-system", "uid": "u2"},
		"spec":     map[string]any{"url": "https://x-access-token:" + s0bNeedle + "@github.com/org/repo", "interval": "1m"},
	}}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd", "uid": "u3"},
		"spec":     map[string]any{"source": map[string]any{"repoURL": "https://github.com/org/shop", "path": "deploy"}},
		"operation": map[string]any{"sync": map[string]any{"manifests": []any{"kind: Secret\ndata: " + s0bNeedle},
			"source": map[string]any{"helm": map[string]any{"values": s0bNeedle}}}},
		"status": map[string]any{"history": []any{map[string]any{"source": map[string]any{
			"repoURL": "https://github.com/org/shop", "helm": map[string]any{"parameters": []any{map[string]any{"name": "p", "value": s0bNeedle}}},
			"directory": map[string]any{"jsonnet": map[string]any{"extVars": []any{map[string]any{"name": "e", "value": s0bNeedle}}}}}}}},
	}}
	svc := NewCRDService(func(string) (CRDConn, bool) { return s0bConn(t, rs, gr, app), true })
	for _, c := range []struct{ group, version, plural, ns, name, mode string }{
		{"fluxcd.controlplane.io", "v1", "resourcesets", "team-a", "with-secret", "withheld"},
		{"source.toolkit.fluxcd.io", "v1", "gitrepositories", "flux-system", "platform", "url-scrubbed"},
		{"argoproj.io", "v1alpha1", "applications", "argocd", "shop", "withheld"},
	} {
		d := svc.GetInstanceDetail("x", c.group, c.version, c.plural, c.ns, c.name)
		if d.Error != "" {
			t.Fatalf("%s: %s", c.plural, d.Error)
		}
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), s0bNeedle) {
			t.Errorf("%s: the needle crossed the bridge:\n%s", c.plural, b)
		}
		if d.EditEligible || d.DescribeMode != c.mode {
			t.Errorf("%s: edit %v describe %q", c.plural, d.EditEligible, d.DescribeMode)
		}
	}
}

// Chat (coverage S0b §3): a registered surface never reaches a model, by the
// complete serialised tool result.
func TestS0b_BridgeNeedle_ChatGet(t *testing.T) {
	f := newFakeChat()
	rsGVR := schema.GroupVersionResource{Group: "fluxcd.controlplane.io", Version: "v1", Resource: "resourcesets"}
	f.kinds["resourceset"] = []fleet.ChatKind{{GVR: rsGVR, Kind: "ResourceSet", Namespaced: true}}
	f.objects[key(rsGVR, "team-a", "rs")] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSet",
		"metadata": map[string]any{"name": "rs", "namespace": "team-a", "uid": "u1"},
		"spec": map[string]any{"resourcesTemplate": "x: " + s0bNeedle, "inputs": []any{map[string]any{"v": s0bNeedle}},
			"resources": []any{map[string]any{"apiVersion": "v1", "kind": "Secret", "stringData": map[string]any{"p": s0bNeedle}}}},
	}}
	res := newChatTools(f, fixedNow).Run(context.Background(), toolGet, json.RawMessage(`{"kind":"resourceset","namespace":"team-a","name":"rs"}`))
	if strings.Contains(string(res.JSON), s0bNeedle) {
		t.Fatalf("the needle reached the chat result: %s", res.JSON)
	}
}

// AI Suggest-fix (coverage S0b §3): the full prompt bundle, after every
// cap and scrubber, carries no payload.
func TestS0b_BridgeNeedle_FixPrompt(t *testing.T) {
	rs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSet",
		"metadata": map[string]any{"name": "rs", "namespace": "team-a", "uid": "u1"},
		"spec":     map[string]any{"resourcesTemplate": "x: " + s0bNeedle, "inputs": []any{map[string]any{"region": s0bNeedle}}},
	}}
	conn := s0bConn(t, rs)
	bundle, root, err := buildResourceBundle(context.Background(), conn, "fluxcd.controlplane.io", "v1", "resourcesets", "team-a", "rs")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := redactManifestForAI(root.YAML, false, false)
	if err != nil {
		t.Fatal(err)
	}
	system, user := BuildFixPrompt(bundle, manifest)
	if strings.Contains(system+user, s0bNeedle) {
		t.Fatal("the needle reached the Suggest-fix prompt")
	}
}

// Codex's S0b implementation review, finding 2: the Line's Git sources and
// Argo projects never carry a credential URL across the bridge.
func TestS0bLineGitSourcesAndArgoProjects(t *testing.T) {
	const needle = "S0bLineNeedle7"
	url := "https://bob:" + needle + "@host.example/repo"
	noNeedle := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), needle) {
			t.Errorf("%s: credential crossed: %s", name, b)
		}
	}
	noNeedle("line git sources", gitSources(line.StationInput{Cluster: "c",
		Sources: []line.Object[flux.Source]{{Data: flux.Source{Kind: flux.GitRepositoryKind, URL: url}}}}))
	f := &fakeArgoConn{projects: []argo.Project{{SourceRepos: []string{url}, Destinations: []string{url + "/shop"}}}}
	res := NewArgoService(func(string) (ArgoConn, bool) { return f, true }).ListProjects("c")
	noNeedle("argo projects", res)
	if len(res.Projects) != 1 || res.Projects[0].SourceRepos[0] != "https://host.example/repo" {
		t.Fatalf("projects: %+v", res.Projects)
	}
}

// Codex's S0b implementation review, finding 9: a Secret's policy is read
// from its real data, so the editor is not offered and Describe is withheld.
func TestS0bSecretDetailPolicy(t *testing.T) {
	sec := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"namespace": "shop", "name": "s", "uid": "u"}, "data": map[string]any{"k": "YQ=="}}}
	d, err := s0bConn(t, sec).GetInstanceDetail(context.Background(), "", "v1", "secrets", "shop", "s")
	if err != nil {
		t.Fatal(err)
	}
	if d.EditEligible || d.DescribeMode != "withheld" || len(d.SecretKeys) != 1 {
		t.Fatalf("edit=%v mode=%s keys=%d", d.EditEligible, d.DescribeMode, len(d.SecretKeys))
	}
}

// Codex's S0b re-review, finding 5: every early refusal and error a
// describe returns passes the URL rule.
func TestS0bDescribeEarlyFailuresScrubbed(t *testing.T) {
	const needle = "EARLYneedle2W"
	e := errors.New("request failed https://bob:" + needle + "@host.example/repo")
	cases := map[string]func(f *fakeExecConn){
		"discovery": func(f *fakeExecConn) { f.kindErr = e },
		"snapshot":  func(f *fakeExecConn) { f.snaps = []fakeSnap{{err: e}} },
		"pin":       func(f *fakeExecConn) { f.pinnedErr = e },
		"argv":      func(f *fakeExecConn) { f.describeArgvErr = e },
		"command":   func(f *fakeExecConn) { f.kindName = ""; f.describeErr = e },
	}
	for name, set := range cases {
		f := &fakeExecConn{kubeContext: "kind-klyx", kindGroup: "kustomize.toolkit.fluxcd.io", kindName: "Kustomization",
			snaps:       []fakeSnap{{uid: "u", rv: "7"}, {uid: "u", rv: "7"}},
			describeCmd: func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "echo", "Name: apps") }}
		set(f)
		r := NewExecService(func(string) (ExecConn, bool) { return f, true }).Describe(context.Background(), "kind-klyx", "ks", "shop", "apps")
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), needle) {
			t.Errorf("%s: %s", name, b)
		}
		if !r.Scrubbed {
			t.Errorf("%s: a scrubbed error is labelled: %+v", name, r)
		}
	}
}

// Codex's S0b re-review, finding 1, through the bridge.
func TestS0bEmbeddedSecretOfUnexpectedTypeBridge(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSet",
		"metadata": map[string]any{"name": "bad-secret", "namespace": "shop", "uid": "u"},
		"spec":     map[string]any{"resources": []any{map[string]any{"apiVersion": "v1", "kind": "Secret", "data": "SECRETbridge8M"}}}}}
	d := NewCRDService(func(string) (CRDConn, bool) { return s0bConn(t, u), true }).GetInstanceDetail("c", "fluxcd.controlplane.io", "v1", "resourcesets", "shop", "bad-secret")
	b, _ := json.Marshal(d)
	if d.Error != "" || strings.Contains(string(b), "SECRETbridge8M") || d.EditEligible || d.DescribeMode != "withheld" {
		t.Fatalf("edit=%v mode=%s: %s", d.EditEligible, d.DescribeMode, b)
	}
}
