package crd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/urlscrub"
)

const notifNeedle = "NOTIFneedle9Z"

func sanitisedJSON(t *testing.T, group, kind string, o map[string]any) (string, Sanitized) {
	t.Helper()
	s := SanitizeEmbedded(group, kind, o)
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s.Object); err != nil {
		t.Fatal(err)
	}
	return b.String(), s
}

// Coverage S1c §6 Stage 0: the registry entries and message rules close the
// exposure Klyx already had, on the sanitiser every detail, chat and
// Explain read passes through.
func TestSanitizeNotificationKinds(t *testing.T) {
	recv := map[string]any{"apiVersion": "notification.toolkit.fluxcd.io/v1", "kind": "Receiver",
		"metadata": map[string]any{"name": "gh", "namespace": "flux-system"},
		"spec":     map[string]any{"type": "generic", "secretRef": map[string]any{"name": "token"}},
		"status": map[string]any{"webhookPath": "/hook/" + notifNeedle, "conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "Succeeded", "message": "Receiver initialized for path: /hook/" + notifNeedle}}}}
	b, s := sanitisedJSON(t, "notification.toolkit.fluxcd.io", "Receiver", recv)
	if strings.Contains(b, notifNeedle) || !strings.Contains(b, ReceiverPathPrefix+WithheldValue) || s.Hold() == "" {
		t.Fatalf("receiver: %s %+v", b, s)
	}

	prov := map[string]any{"apiVersion": "notification.toolkit.fluxcd.io/v1beta3", "kind": "Provider",
		"metadata": map[string]any{"name": "slack", "namespace": "flux-system"},
		"spec": map[string]any{"type": "slack", "address": "https://hooks.slack.com/services/T0/B0/" + notifNeedle,
			"proxy": "http://user:" + notifNeedle + "@proxy.example:3128"}}
	if b, s := sanitisedJSON(t, "notification.toolkit.fluxcd.io", "Provider", prov); strings.Contains(b, notifNeedle) || s.Hold() != HoldPayload {
		t.Fatalf("provider: %s %+v", b, s)
	}

	ea := map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "ExternalArtifact",
		"metadata": map[string]any{"name": "app", "namespace": "flux-system"},
		"status": map[string]any{"artifact": map[string]any{"revision": "latest@sha256:abc",
			"url":      "https://store.example/t/" + notifNeedle + "/a.tgz?sig=" + notifNeedle,
			"metadata": map[string]any{"org.opencontainers.image.revision": "main@sha1:abc", "note": notifNeedle}}}}
	b, s = sanitisedJSON(t, "source.toolkit.fluxcd.io", "ExternalArtifact", ea)
	if strings.Contains(b, notifNeedle) || !strings.Contains(b, "https://store.example") || s.Hold() != HoldPayload {
		t.Fatalf("external artifact: %s %+v", b, s)
	}

	// a Kustomization first seen on a GitRepository, with an older
	// generation's ArtifactFailed condition quoting a signed URL
	ks := map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system", "generation": int64(3)},
		"spec":     map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "fleet"}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "ArtifactFailed", "observedGeneration": int64(2),
			"message": "failed to download artifact from https://store.example/a.tgz?sig=" + notifNeedle + ": status 403"}}}}
	b, s = sanitisedJSON(t, "kustomize.toolkit.fluxcd.io", "Kustomization", ks)
	if strings.Contains(b, notifNeedle) || !strings.Contains(b, "https://store.example "+urlscrub.OriginsRest) || s.Hold() != HoldMessages {
		t.Fatalf("residual kustomization message: %s %+v", b, s)
	}
	// a Kustomization with no URL beyond an origin is released
	clean := map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "a"},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded", "message": "Applied revision: main@sha1:abc"}}}}
	if _, s := sanitisedJSON(t, "kustomize.toolkit.fluxcd.io", "Kustomization", clean); s.Hold() != "" {
		t.Fatalf("a clean Kustomization: %+v", s)
	}
	// a generator's SourceFetchFailed condition holds Describe
	ag := map[string]any{"apiVersion": "source.extensions.fluxcd.io/v1beta1", "kind": "ArtifactGenerator", "metadata": map[string]any{"name": "g"},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "SourceFetchFailed",
			"message": "fetch https://store.example/x?sig=" + notifNeedle}}}}
	if b, s := sanitisedJSON(t, "source.extensions.fluxcd.io", "ArtifactGenerator", ag); strings.Contains(b, notifNeedle) || s.Hold() != HoldMessages {
		t.Fatalf("generator: %s %+v", b, s)
	}
	for _, k := range []string{"Receiver", "Alert", "Provider", "ArtifactGenerator", "ExternalArtifact", "Kustomization", "HelmRelease"} {
		if !MessageSensitive(k) {
			t.Errorf("%s is message-sensitive", k)
		}
	}
}

func TestNotificationMessages(t *testing.T) {
	if out, _ := NotificationMessage("NotificationDispatchFailed", "failed to send to https://hooks.slack.com/services/T/B/"+notifNeedle+": 404 body "+notifNeedle); out != NotificationWithheld {
		t.Fatalf("a dispatch failure is withheld whole: %q", out)
	}
	if out, _ := NotificationMessage("InvalidConfig", "invalid inclusion regex ^app-("); out != "invalid inclusion regex ^app-(" {
		t.Fatalf("InvalidConfig keeps its text: %q", out)
	}
	if out, _ := NotificationMessage("InvalidConfig", "bad https://u:"+notifNeedle+"@h/x"); strings.Contains(out, notifNeedle) {
		t.Fatalf("InvalidConfig passes the URL rule: %q", out)
	}
	if out, _ := ReceiverMessage("TokenNotFound", "unable to read token from secret flux-system/token: "+notifNeedle); out != ReceiverWithheld {
		t.Fatalf("anything else withheld: %q", out)
	}
}
