package config

import "testing"

// Settings S1 §3: the schema's paths, as the key check names them.
func TestSchemaPathsNameEveryLeaf(t *testing.T) {
	got := map[string]bool{}
	for _, p := range SchemaPaths() {
		if got[p] {
			t.Fatalf("duplicate %s", p)
		}
		got[p] = true
	}
	for _, want := range []string{"clusters[].protected", "clusters[].tags", "clusters[].alertmanager.match", "clusters[].metrics.serviceRef.path", "ai.provider", "line.source.ref.kind", "git.repos[].matches", "index.kinds", "notifications.recoveries"} {
		if !got[want] {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{"clusters", "clusters[]", "ai", "line.source", "unknownTop"} {
		if got[not] {
			t.Errorf("%s is not a leaf", not)
		}
	}
}
