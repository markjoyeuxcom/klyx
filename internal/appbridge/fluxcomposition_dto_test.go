package appbridge

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/composition"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/gitops/notify"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

const s1cNeedle = "S1cBRIDGEneedle"

func rawJSONOf(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func s1cObj(api, kind, ns, name, uid string, labels map[string]any, spec, status map[string]any) *unstructured.Unstructured {
	md := map[string]any{"name": name, "namespace": ns, "uid": uid, "generation": int64(1)}
	if labels != nil {
		md["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": api, "kind": kind, "metadata": md, "spec": spec, "status": status}}
}

func s1cReady(status, reason, msg string) []any {
	return []any{map[string]any{"type": "Ready", "status": status, "reason": reason, "message": msg, "lastTransitionTime": "2026-10-17T09:00:00Z", "observedGeneration": int64(1)}}
}

// evaluate runs one of the Flux task's S1c parts over projected objects.
func evaluate(t *testing.T, part string, objs map[string][]*unstructured.Unstructured) any {
	t.Helper()
	kinds, parts := healthsrc.FluxComposition(healthsrc.NativeDeps{Cluster: "c"})
	capture := observe.Capture{Now: time.Now(), Rows: true, RoleCap: 10, Objects: map[string][]any{}, Complete: map[string]bool{}}
	for _, k := range kinds {
		capture.Complete[k.Name] = true
		for _, u := range objs[k.Name] {
			v, err := k.Project(u)
			if err != nil {
				t.Fatal(err)
			}
			capture.Objects[k.Name] = append(capture.Objects[k.Name], v)
		}
	}
	for _, p := range parts {
		if p.ID == part {
			res := p.Evaluate(capture)
			out, err := rowsConverters[healthsrc.TaskFlux+"/"+part](res.Rows, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("no part %s", part)
	return nil
}

// Coverage S1c §6: the composition and receiver rows cross without a URL
// path, producer metadata, a webhook path or a Receiver's Ready message.
func TestS1cRowsCrossUnderTheRules(t *testing.T) {
	gen := s1cObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "flux-system", "apps", "uid-ag", nil,
		map[string]any{"sources": []any{map[string]any{"alias": "fleet", "kind": "GitRepository", "name": "fleet"}},
			"artifacts": []any{map[string]any{"name": "apps-a", "copy": []any{map[string]any{"from": "@fleet/apps/**", "to": "@artifact/"}}}}},
		map[string]any{"inventory": []any{map[string]any{"name": "apps-a", "namespace": "flux-system"}},
			"conditions": s1cReady("False", "SourceFetchFailed", `fetch sources failed: failed to download archive: Get "/signed/`+s1cNeedle+`": unsupported protocol scheme ""`)})
	art := s1cObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "flux-system", "apps-a", "uid-ea", map[string]any{"source.extensions.fluxcd.io/generator": "uid-ag"},
		map[string]any{"sourceRef": map[string]any{"apiVersion": "source.extensions.fluxcd.io/v1beta1", "kind": "ArtifactGenerator", "name": "apps"}},
		map[string]any{"artifact": map[string]any{"revision": "latest@sha256:ab", "digest": "sha256:ab", "url": "https://store.example/t/" + s1cNeedle + "/a.tgz?sig=" + s1cNeedle,
			"metadata": map[string]any{"org.opencontainers.image.revision": "main@sha1:abc", "note": s1cNeedle}}, "conditions": s1cReady("True", "Succeeded", "ok")})
	rows := evaluate(t, "composition", map[string][]*unstructured.Unstructured{healthsrc.KindArtifactGenerators: {gen}, healthsrc.KindExternalArtifacts: {art}}).([]CompositionRowDTO)
	b := rawJSONOf(t, rows)
	if strings.Contains(b, s1cNeedle) || len(rows) != 2 {
		t.Fatalf("composition rows: %s", b)
	}
	for _, r := range rows {
		if a := r.Artifact; a != nil {
			if a.Origin != "https://store.example" || a.OriginRevision != "main@sha1:abc" || a.Producer == nil || a.Producer.State != composition.ProducerVerified || !a.Producer.Failing {
				t.Fatalf("the artifact row: %+v %+v", a, a.Producer)
			}
		}
		if g := r.Generator; g != nil && (len(g.Sources) != 1 || len(g.Outputs) != 1 || g.Outputs[0].Copy[0] != "@fleet/apps/** → @artifact/" || len(g.Generated) != 1 || !g.Generated[0].Found) {
			t.Fatalf("the generator row: %+v", g)
		}
	}
	rcv := s1cObj("notification.toolkit.fluxcd.io/v1", "Receiver", "flux-system", "gh", "uid-r", nil,
		map[string]any{"type": "github", "secretRef": map[string]any{"name": "token"}, "events": []any{"push"}, "resources": []any{map[string]any{"kind": "GitRepository", "name": "fleet"}}},
		map[string]any{"webhookPath": "/hook/" + s1cNeedle, "conditions": s1cReady("True", "Succeeded", "Receiver initialized for path: /hook/"+s1cNeedle)})
	rr := evaluate(t, "receivers", map[string][]*unstructured.Unstructured{healthsrc.KindReceivers: {rcv}}).([]ReceiverDTO)
	if b := rawJSONOf(t, rr); strings.Contains(b, s1cNeedle) || len(rr) != 1 || rr[0].Meaning != "configuration accepted, token read" || rr[0].Resources[0].Name != "fleet" {
		t.Fatalf("receiver rows: %s", b)
	}
	// AI evidence names neither a URL, metadata, a webhook path nor a Secret
	for _, part := range []string{"composition", "receivers"} {
		a := rawJSONOf(t, aiEvidenceFields[healthsrc.TaskFlux+"/"+part])
		for _, k := range []string{`"url"`, `"metadata"`, `"webhookPath"`, `"secretRef"`} {
			if strings.Contains(a, k) {
				t.Errorf("%s evidence admits %s", part, k)
			}
		}
	}
}

// S1c §2-3: Providers and Alerts are facts, joined both ways, with capped
// reads said and dispatch evidence counted from the events' reasons.
func TestGetNotifications(t *testing.T) {
	prov := s1cObj("notification.toolkit.fluxcd.io/v1beta3", "Provider", "flux-system", "slack", "uid-p", nil,
		map[string]any{"type": "slack", "channel": "alerts", "address": "https://hooks.slack.com/services/T/B/" + s1cNeedle, "secretRef": map[string]any{"name": "slack-url"}, "suspend": true}, nil)
	alert := s1cObj("notification.toolkit.fluxcd.io/v1beta3", "Alert", "flux-system", "on-call", "uid-a", nil,
		map[string]any{"providerRef": map[string]any{"name": "slack"}, "eventSeverity": "error", "eventSources": []any{map[string]any{"kind": "Kustomization", "name": "*"}},
			"eventMetadata": map[string]any{"cluster": s1cNeedle}, "inclusionList": []any{"https://u:" + s1cNeedle + "@h/x"}}, nil)
	orphan := s1cObj("notification.toolkit.fluxcd.io/v1beta3", "Alert", "flux-system", "orphan", "uid-o", nil, map[string]any{"providerRef": map[string]any{"name": "gone"}}, nil)
	conn := &fakeGitOpsConn{notifications: notify.Read{
		Providers:    notify.List{Items: []*unstructured.Unstructured{prov}},
		Alerts:       notify.List{Items: []*unstructured.Unstructured{alert, orphan}, More: 3},
		AlertReasons: map[string][]string{"uid-a": {"NotificationDispatchFailed", "NotificationDispatchFailed", "Info"}, "uid-predecessor-of-orphan": {"NotificationDispatchFailed"}},
	}}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	d := svc.GetNotifications("c")
	if b := rawJSONOf(t, d); strings.Contains(b, s1cNeedle) {
		t.Fatalf("a credential crossed: %s", b)
	}
	if len(d.Providers) != 1 || !d.Providers[0].HasAddress || d.Providers[0].SecretRef != "slack-url" || len(d.Providers[0].Alerts) != 1 || d.Providers[0].AlertsState != notify.ProviderIncomplete {
		t.Fatalf("the provider: %+v", d.Providers)
	}
	if d.AlertsList.More != 3 {
		t.Fatalf("past the cap is said: %+v", d.AlertsList)
	}
	byName := map[string]AlertDTO{}
	for _, a := range d.Alerts {
		byName[a.Name] = a
	}
	if a := byName["on-call"]; a.ProviderState != notify.ProviderFound || !a.ProviderSuspended || a.Dispatch["NotificationDispatchFailed"] != 2 || a.MetadataKeys[0] != "cluster" {
		t.Fatalf("on-call: %+v", a)
	}
	if a := byName["orphan"]; a.ProviderState != notify.ProviderMissing {
		t.Fatalf("a missing provider in a complete read: %+v", a)
	}
	// a capped provider read never says "not found"
	conn.notifications.Providers.More = -1
	for _, a := range svc.GetNotifications("c").Alerts {
		if a.Name == "orphan" && a.ProviderState != notify.ProviderIncomplete {
			t.Fatalf("capped: %+v", a)
		}
	}
}

// S1c §2-3: the inspector's detail is for the shown object only, with a
// generator's sources and an artifact's consumers from the shared rows.
func TestGetCompositionDetail(t *testing.T) {
	gen := s1cObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "flux-system", "apps", "uid-ag", nil,
		map[string]any{"sources": []any{map[string]any{"alias": "fleet", "kind": "GitRepository", "name": "fleet"}, map[string]any{"alias": "x", "kind": "Bucket", "name": "gone"}}}, nil)
	art := s1cObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "flux-system", "apps-a", "uid-ea", nil, map[string]any{}, nil)
	ok := health.NativeEval{Role: health.OK, Label: "Ready"}
	sources := observe.Rows{Part: observe.Part{Coverage: observe.Live}, Rows: healthsrc.RowSet[flux.Source]{Rows: []healthsrc.Row[flux.Source]{
		{Eval: ok, Data: flux.Source{Kind: flux.GitRepositoryKind, Namespace: "flux-system", Name: "fleet"}}}}}
	recs := observe.Rows{Part: observe.Part{Coverage: observe.Live}, Rows: healthsrc.RowSet[flux.Resource]{Rows: []healthsrc.Row[flux.Resource]{
		{Eval: ok, Data: flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "apps", SourceKind: "ExternalArtifact", SourceNamespace: "flux-system", SourceName: "apps-a"}},
		{Eval: ok, Data: flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "other", SourceKind: "GitRepository", SourceNamespace: "flux-system", SourceName: "apps-a"}}}}}
	conn := &fakeGitOpsConn{byKind: map[string]*unstructured.Unstructured{"ArtifactGenerator": gen, "ExternalArtifact": art},
		rowsByPart: map[string]observe.Rows{"sources": sources, "reconciliations": recs}}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)

	d := svc.GetCompositionDetail("c", "ArtifactGenerator", "flux-system", "apps", "uid-ag")
	if d.Error != "" || len(d.Sources) != 2 || !d.Sources[0].Found || d.Sources[0].Label != "Ready" || d.Sources[1].State != notify.ProviderMissing {
		t.Fatalf("the generator's sources: %+v", d)
	}
	if d := svc.GetCompositionDetail("c", "ArtifactGenerator", "flux-system", "apps", "uid-old"); !d.Replaced || len(d.Sources) != 0 {
		t.Fatalf("a replacement reads nothing: %+v", d)
	}
	d = svc.GetCompositionDetail("c", "ExternalArtifact", "flux-system", "apps-a", "uid-ea")
	if len(d.Consumers) != 1 || d.Consumers[0].Name != "apps" || d.ConsumersState != notify.ProviderFound {
		t.Fatalf("the artifact's consumers: %+v", d)
	}
	// unread rows: a join never claims none
	conn.rowsByPart = map[string]observe.Rows{}
	if d := svc.GetCompositionDetail("c", "ExternalArtifact", "flux-system", "apps-a", "uid-ea"); d.ConsumersState != "not readable" {
		t.Fatalf("unread consumers: %+v", d)
	}
	if d := svc.GetCompositionDetail("c", "Provider", "flux-system", "x", "u"); d.Error == "" {
		t.Fatal("a Provider has no composition detail")
	}
}

// Codex's S1c implementation review, findings 1, 3, 4 and 5: the detail
// names the generation it read and only its own events; a stale source
// read certifies no health; a capped events read claims no absence.
func TestS1cReviewFences(t *testing.T) {
	gen := s1cObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "flux-system", "apps", "uid-ag", nil,
		map[string]any{"sources": []any{map[string]any{"alias": "fleet", "kind": "GitRepository", "name": "fleet"}}}, nil)
	gen.SetGeneration(2)
	ok := health.NativeEval{Role: health.OK, Label: "Ready"}
	stale := observe.Rows{Part: observe.Part{Coverage: observe.Stale}, Rows: healthsrc.RowSet[flux.Source]{Rows: []healthsrc.Row[flux.Source]{
		{Eval: ok, Data: flux.Source{Kind: flux.GitRepositoryKind, Namespace: "flux-system", Name: "fleet"}}}}}
	conn := &fakeGitOpsConn{byKind: map[string]*unstructured.Unstructured{"ArtifactGenerator": gen}, rowsByPart: map[string]observe.Rows{"sources": stale},
		events: []workloads.EventSummary{{Reason: "Mine", UID: "uid-ag", Kind: "ArtifactGenerator", Name: "apps"},
			{Reason: "Predecessor", UID: "uid-old", Kind: "ArtifactGenerator", Name: "apps"}, {Reason: "Nobody", Kind: "ArtifactGenerator", Name: "apps"}}}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	d := svc.GetCompositionDetail("c", "ArtifactGenerator", "flux-system", "apps", "uid-ag")
	if d.Generation != 2 {
		t.Fatalf("the generation read: %+v", d)
	}
	if len(d.Events) != 1 || d.Events[0].Reason != "Mine" {
		t.Fatalf("only its own events: %+v", d.Events)
	}
	if s := d.Sources[0]; s.Found || s.Label != "" || s.State != "not readable" || d.SourcesState != "not readable" {
		t.Fatalf("a stale read certifies nothing: %+v", d.Sources)
	}
	// a stale reconciliations read lists no consumer
	conn.byKind["ExternalArtifact"] = s1cObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "flux-system", "apps-a", "uid-ea", nil, map[string]any{}, nil)
	conn.rowsByPart["reconciliations"] = observe.Rows{Part: observe.Part{Coverage: observe.Stale}, Rows: healthsrc.RowSet[flux.Resource]{Rows: []healthsrc.Row[flux.Resource]{
		{Eval: ok, Data: flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "apps", SourceKind: "ExternalArtifact", SourceNamespace: "flux-system", SourceName: "apps-a"}}}}}
	if d := svc.GetCompositionDetail("c", "ExternalArtifact", "flux-system", "apps-a", "uid-ea"); len(d.Consumers) != 0 || d.ConsumersState != "not readable" {
		t.Fatalf("stale consumers: %+v", d)
	}

	alert := s1cObj("notification.toolkit.fluxcd.io/v1beta3", "Alert", "flux-system", "on-call", "uid-a", nil, map[string]any{"providerRef": map[string]any{"name": "slack"}}, nil)
	conn.notifications = notify.Read{Alerts: notify.List{Items: []*unstructured.Unstructured{alert}}, AlertReasons: map[string][]string{}, EventsTruncated: true}
	a := svc.GetNotifications("c").Alerts[0]
	if !a.DispatchPartial || strings.Contains(a.DispatchSummary, "no dispatch problems") {
		t.Fatalf("a capped read claims no absence: %+v", a)
	}
	conn.notifications.AlertReasons = map[string][]string{"uid-a": {"NotificationDispatchFailed"}}
	if a := svc.GetNotifications("c").Alerts[0]; !a.DispatchPartial || !strings.Contains(a.DispatchSummary, "at least these") {
		t.Fatalf("a capped count is a lower bound: %+v", a)
	}
}

// Finding 7: a generator's owner reaches its row, for its verbs' line.
func TestGeneratorOwner(t *testing.T) {
	gen := s1cObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "flux-system", "apps", "uid-ag",
		map[string]any{"kustomize.toolkit.fluxcd.io/name": "platform", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"},
		map[string]any{"sources": []any{map[string]any{"alias": "fleet", "kind": "GitRepository", "name": "fleet"}}}, nil)
	rows := evaluate(t, "composition", map[string][]*unstructured.Unstructured{healthsrc.KindArtifactGenerators: {gen}}).([]CompositionRowDTO)
	if o := rows[0].Generator.Owner; o == nil || o.Kind != "Kustomization" || o.Name != "platform" || o.Namespace != "flux-system" {
		t.Fatalf("the owner: %+v", rows[0].Generator)
	}
}
