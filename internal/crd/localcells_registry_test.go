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
