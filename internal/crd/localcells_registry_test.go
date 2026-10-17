package crd

import (
	"strings"
	"testing"
)

// Every registered payload kind's list and Summary cells are evaluated
// locally over the sanitised object, so its resource must be listed: a kind
// missing here would take the server's cells - its Ready message among
// them - unread by the sanitiser (found in S1b: ImageUpdateAutomation).
func TestPayloadKindsListTheirResources(t *testing.T) {
	for k := range payloadKinds {
		group, kind, _ := strings.Cut(k, "/")
		plural := strings.ToLower(kind) + "s"
		if !IsPayloadResource(group, plural) {
			t.Errorf("%s is a payload kind, but %s/%s takes the server's cells", k, group, plural)
		}
	}
}

// The message-sensitive kinds' cells are evaluated locally too: a server
// cell carries the raw Ready message the kind's rule withholds. MessageRule
// is keyed by kind name; each name's served resource is listed here.
func TestMessageKindsListTheirResources(t *testing.T) {
	resources := map[string]string{
		"ImageUpdateAutomation": "image.toolkit.fluxcd.io/imageupdateautomations",
		"Receiver":              "notification.toolkit.fluxcd.io/receivers",
		"Alert":                 "notification.toolkit.fluxcd.io/alerts",
		"Provider":              "notification.toolkit.fluxcd.io/providers",
		"ArtifactGenerator":     "source.extensions.fluxcd.io/artifactgenerators",
		"ExternalArtifact":      "source.toolkit.fluxcd.io/externalartifacts",
		"Kustomization":         "kustomize.toolkit.fluxcd.io/kustomizations",
		"HelmRelease":           "helm.toolkit.fluxcd.io/helmreleases",
	}
	for kind := range messageRules {
		r, ok := resources[kind]
		if !ok {
			t.Errorf("%s has a message rule but no known resource", kind)
			continue
		}
		group, plural, _ := strings.Cut(r, "/")
		if !IsPayloadResource(group, plural) {
			t.Errorf("%s: %s takes the server's cells", kind, r)
		}
	}
}
