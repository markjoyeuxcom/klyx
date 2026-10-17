package crd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/urlscrub"
)

func rawJSON(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return b.String()
}

func kustomizationWith(reason, msg string) map[string]any {
	return map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "apps"},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": reason, "message": msg}}}}
}

// Codex's Stage 0 review, finding 2: an ExternalArtifact's URL is any
// string, so a fetch failure that Origins finds no URL in, or whose URL
// follows an unrecognised wording, is withheld whole and holds Describe.
func TestFetchFailures(t *testing.T) {
	const n = "SIGNEDneedle2F"
	for _, raw := range []string{"https:/store.example/signed/" + n, "/signed/" + n} {
		req, err := http.NewRequest("GET", raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		cause := "http: no Host in request URL"
		if req.URL.Scheme == "" {
			cause = `unsupported protocol scheme ""`
		}
		msg := fmt.Errorf("failed to download archive: %w", &url.Error{Op: "Get", URL: req.URL.String(), Err: errors.New(cause)}).Error()
		s := SanitizeEmbedded("kustomize.toolkit.fluxcd.io", "Kustomization", kustomizationWith("ArtifactFailed", msg))
		b := rawJSON(s.Object)
		if strings.Contains(b, n) || s.Hold() != HoldMessages || !strings.Contains(b, "failed to download archive: "+urlscrub.OriginsRest) {
			t.Fatalf("%q: %s (hold %q)", raw, b, s.Hold())
		}
	}
	withheld := []struct{ kind, reason, msg string }{
		// no URL syntax at all, in a wording the pinned controllers do not write
		{"Kustomization", "ArtifactFailed", "failed to fetch " + n + ": unsupported protocol scheme"},
		// a URL candidate after an unrecognised prefix
		{"Kustomization", "ArtifactFailed", "fetching " + n + ` failed: Get "` + n + `": unsupported protocol scheme ""`},
		{"Kustomization", "ArtifactFailed", "failed to download archive: x\n" + n},
		{"HelmRelease", "ArtifactFailed", "Could not load chart: chart illegally contains " + n},
		{"ArtifactGenerator", "SourceFetchFailed", "fetch sources failed: " + n},
	}
	for _, c := range withheld {
		if out, changed := MessageRule(c.kind, c.reason, c.msg); out != FetchWithheld || !changed {
			t.Errorf("%s %s %q: %q", c.kind, c.reason, c.msg, out)
		}
	}
	kept := []struct{ kind, reason, msg, want string }{
		{"Kustomization", "ArtifactFailed", "kustomization path not found: stat /tmp/kustomization-1/apps: no such file or directory", ""},
		{"Kustomization", "ArtifactFailed", "Source artifact not found, retrying in 30s", ""},
		{"Kustomization", "ArtifactFailed", "failed to verify archive: computed digest doesn't match provided 'sha256:ab'", ""},
		{"Kustomization", "ArtifactFailed", "failed to download archive from https://store.example/a?sig=" + n + " (status: 403 Forbidden)",
			"failed to download archive from https://store.example " + urlscrub.OriginsRest},
		{"HelmRelease", "ArtifactFailed", "Source not ready: artifact not found. Retrying in 30s", ""},
		{"HelmRelease", "ArtifactFailed", "Could not load chart: GET http://source-controller.flux-system.svc/c.tgz giving up after 1 attempt(s)",
			"Could not load chart: GET http://source-controller.flux-system.svc " + urlscrub.OriginsRest},
		{"HelmRelease", "ArtifactFailed", "failed to download chart from 'https://store.example/c?sig=" + n + "' (status: 500)",
			"failed to download chart from 'https://store.example " + urlscrub.OriginsRest},
		{"ArtifactGenerator", "SourceFetchFailed", `fetch sources failed: failed to download archive: Get "/signed/` + n + `": unsupported protocol scheme ""`,
			"fetch sources failed: failed to download archive: " + urlscrub.OriginsRest},
		{"ArtifactGenerator", "SourceFetchFailed", "get sources failed: source 'GitRepository/flux-system/fleet' is not ready", ""},
		// another reason passes Origins only
		{"Kustomization", "BuildFailed", "kustomize build failed: accumulating resources from 'https://github.com/org/repo//base?ref=" + n + "'",
			"kustomize build failed: accumulating resources from 'https://github.com " + urlscrub.OriginsRest},
	}
	for _, c := range kept {
		want := c.want
		if want == "" {
			want = c.msg
		}
		if out, _ := MessageRule(c.kind, c.reason, c.msg); out != want {
			t.Errorf("%s %s %q: got %q, want %q", c.kind, c.reason, c.msg, out, want)
		}
	}
}

// Codex's Stage 0 review, finding 1: an Event's own text passes the rule of
// the object it is about, in either API; the change blocks an edit and
// holds Describe, never as a payload.
func TestEventObjects(t *testing.T) {
	const n = "PATHneedle1E"
	for _, c := range []struct{ group, subject, field string }{{"", "involvedObject", "message"}, {"events.k8s.io", "regarding", "note"}} {
		ev := map[string]any{"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "r.1", "namespace": "flux-system"},
			c.subject: map[string]any{"kind": "Receiver", "name": "gh"}, "reason": "Succeeded", c.field: "Receiver initialized for path: /hook/" + n}
		s := SanitizeEmbedded(c.group, "Event", ev)
		b, _ := json.Marshal(s.Object)
		if strings.Contains(string(b), n) || s.EditEligible || s.Hold() != HoldMessages || s.Payload || s.DescribeMode != DescribeWithheld {
			t.Fatalf("%s: %s %+v", c.group, b, s)
		}
		if strings.Contains(string(ev[c.field].(string)), "withheld") {
			t.Fatal("the input is never mutated")
		}
		cols, ok := BuiltinPayloadColumns(c.group, "events", "v1")
		if !ok || !IsPayloadResource(c.group, "events") {
			t.Fatalf("%s: events are listed locally", c.group)
		}
		cells := strings.Join(LocalCells(cols, s.Object), "|")
		if strings.Contains(cells, n) || !strings.Contains(cells, "Receiver|gh|Receiver initialized for path: "+WithheldValue) {
			t.Fatalf("%s cells: %s", c.group, cells)
		}
	}
	// a related object's rule applies too; a present subject without a
	// usable kind, or no subject, withholds the text (Codex's Stage 0
	// review 2, finding 2)
	recv := "Receiver initialized for path: /hook/" + n
	for name, ev := range map[string]map[string]any{
		"related": {"kind": "Event", "involvedObject": map[string]any{"kind": "Pod", "name": "p"},
			"related": map[string]any{"kind": "Provider", "name": "slack"}, "message": "posted to https://hooks.example/T/" + n},
		"a kind of another type": {"kind": "Event", "involvedObject": map[string]any{"kind": int64(3)}, "message": recv},
		"no kind":                {"kind": "Event", "involvedObject": map[string]any{"name": "gh"}, "message": recv},
		"an empty kind":          {"kind": "Event", "involvedObject": map[string]any{"kind": "", "name": "gh"}, "message": recv},
		"an empty related kind":  {"kind": "Event", "involvedObject": map[string]any{"kind": "Pod"}, "related": map[string]any{"name": "x"}, "message": recv},
		"no subject":             {"kind": "Event", "message": recv},
		"note, no subject":       {"kind": "Event", "related": map[string]any{"kind": "Pod"}, "note": recv},
	} {
		if s := SanitizeEmbedded("", "Event", ev); strings.Contains(fmt.Sprint(s.Object), n) || s.EditEligible || s.Hold() != HoldMessages {
			t.Fatalf("%s: withheld: %+v", name, s)
		}
	}
	// an absent or null related reference is harmless
	nullRelated := map[string]any{"kind": "Event", "involvedObject": map[string]any{"kind": "Pod", "name": "p"}, "related": nil, "message": "Pulled"}
	if s := SanitizeEmbedded("", "Event", nullRelated); s.Hold() != "" || !s.EditEligible {
		t.Fatalf("a null related reference: %+v", s)
	}
	for _, c := range []struct {
		subjects []EventSubject
		want     string
	}{
		{nil, EventWithheld},
		{[]EventSubject{{}}, EventWithheld},
		{[]EventSubject{{Kind: "Pod"}, {}}, EventWithheld},
		{[]EventSubject{{Kind: "Pod"}, {Kind: "Provider"}}, NotificationWithheld},
		{[]EventSubject{{Kind: "Pod"}}, recv},
	} {
		if out, _ := EventMessage("Succeeded", recv, c.subjects); out != c.want {
			t.Errorf("%+v: got %q, want %q", c.subjects, out, c.want)
		}
	}
	// an Event about another kind is untouched and editable
	pod := map[string]any{"kind": "Event", "involvedObject": map[string]any{"kind": "Pod", "name": "p"}, "reason": "BackOff", "message": "Back-off restarting failed container"}
	if s := SanitizeEmbedded("", "Event", pod); !s.EditEligible || s.Hold() != "" || s.Object["message"] != pod["message"] {
		t.Fatalf("a pod event: %+v", s)
	}
	if !MessageBracketed("", "Event") || !MessageBracketed("events.k8s.io", "Event") || MessageBracketed("", "Pod") || MessageBracketed("other.io", "Event") {
		t.Fatal("Describe brackets Events in either API")
	}
}
