package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const moveDoc = `# my fleet
clusters:
  - name: prd
    context: prd-ctx
    metrics:
      endpoint: https://mimir.example/prometheus   # the Mimir gateway
      token: "plain-tok"
      tenant: platform
  - name: dev
    context: dev-ctx
    metrics:
      endpoint: https://prom.dev.example
      token: dev-tok
`

func writeFleetMode(t *testing.T, doc string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(p, []byte(doc), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// Coverage C5 §6: the move rewrites only that cluster's token line, keeps
// comments, other clusters and the file's mode.
func TestMoveMetricsTokenToAuth(t *testing.T) {
	p := writeFleetMode(t, moveDoc, 0o644)
	snap, err := ReadSnapshot(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := MoveMetricsTokenToAuth(snap, "prd")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	want := strings.Replace(moveDoc, `      token: "plain-tok"`+"\n", "      auth: {type: bearer}\n", 1)
	if string(after) != want {
		t.Fatalf("the file:\n%s\nwant:\n%s", after, want)
	}
	if m := cfg.Clusters[0].Metrics; m.Token != "" || m.Auth == nil || m.Auth.Type != "bearer" || m.Tenant != "platform" {
		t.Fatalf("decoded: %+v", m)
	}
	if cfg.Clusters[1].Metrics.Token != "dev-tok" {
		t.Fatal("another cluster's token was touched")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode kept: %o", st.Mode().Perm())
	}
}

// The file changed after the snapshot (an editor, another action): the
// newer file is left untouched - whether its bytes or only its mode moved.
func TestMoveRefusesAFileChangedSinceTheSnapshot(t *testing.T) {
	for _, change := range []string{"bytes", "mode"} {
		t.Run(change, func(t *testing.T) {
			p := writeFleetMode(t, moveDoc, 0o600)
			snap, err := ReadSnapshot(p)
			if err != nil {
				t.Fatal(err)
			}
			edited := strings.Replace(moveDoc, "plain-tok", "rotated-tok", 1)
			if change == "bytes" {
				if err := os.WriteFile(p, []byte(edited), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				edited = moveDoc
				if err := os.Chmod(p, 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := MoveMetricsTokenToAuth(snap, "prd"); !errors.Is(err, ErrFileChangedSinceSnapshot) {
				t.Fatalf("refused: %v", err)
			}
			if got, _ := os.ReadFile(p); string(got) != edited {
				t.Fatal("the newer file was changed")
			}
			if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".fleet-*")); len(left) != 0 {
				t.Fatalf("a temporary file was left: %v", left)
			}
		})
	}
}

// A change on disk between the move's checks and its rename is refused too.
func TestMoveRefusesAChangeJustBeforeTheRename(t *testing.T) {
	p := writeFleetMode(t, moveDoc, 0o600)
	snap, _ := ReadSnapshot(p)
	beforeReplace = func() { _ = os.WriteFile(p, []byte(moveDoc+"# edited\n"), 0o600) }
	defer func() { beforeReplace = nil }()
	if _, err := MoveMetricsTokenToAuth(snap, "prd"); !errors.Is(err, ErrFileChangedSinceSnapshot) {
		t.Fatalf("refused: %v", err)
	}
}

// Only a plain one-line token is edited.
func TestMoveRefusesOtherTokenShapes(t *testing.T) {
	for name, doc := range map[string]string{
		"flow":  "clusters:\n  - name: prd\n    metrics: {endpoint: https://m.example, token: t}\n",
		"block": "clusters:\n  - name: prd\n    metrics:\n      endpoint: https://m.example\n      token: |\n        t\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := writeFleetMode(t, doc, 0o600)
			snap, err := ReadSnapshot(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := MoveMetricsTokenToAuth(snap, "prd"); err == nil || !strings.Contains(err.Error(), "by hand") {
				t.Fatalf("refused: %v", err)
			}
			if got, _ := os.ReadFile(p); string(got) != doc {
				t.Fatal("the file changed")
			}
		})
	}
}

// An endpoint's revision follows what the person saw: the endpoint, a
// legacy token, the file's mode - and nothing of another endpoint.
func TestEndpointRevision(t *testing.T) {
	rev := func(doc string, mode os.FileMode, cluster, kind string) string {
		t.Helper()
		s, err := ReadSnapshot(writeFleetMode(t, doc, mode))
		if err != nil {
			t.Fatal(err)
		}
		return s.EndpointRevision(cluster, kind)
	}
	base := rev(moveDoc, 0o600, "prd", EndpointMetrics)
	if base == "" || base != rev(moveDoc, 0o600, "prd", EndpointMetrics) {
		t.Fatal("a revision is stable")
	}
	for name, other := range map[string]string{
		"endpoint": rev(strings.Replace(moveDoc, "mimir.example", "mimir2.example", 1), 0o600, "prd", EndpointMetrics),
		"token":    rev(strings.Replace(moveDoc, "plain-tok", "rotated", 1), 0o600, "prd", EndpointMetrics),
		"tenant":   rev(strings.Replace(moveDoc, "tenant: platform", "tenant: other", 1), 0o600, "prd", EndpointMetrics),
		"mode":     rev(moveDoc, 0o640, "prd", EndpointMetrics),
	} {
		if other == base {
			t.Errorf("%s: the revision did not move", name)
		}
	}
	if rev(strings.Replace(moveDoc, "dev-tok", "x", 1), 0o600, "prd", EndpointMetrics) != base {
		t.Fatal("another cluster's change moved this endpoint's revision")
	}
	if rev(moveDoc, 0o600, "prd", EndpointLoki) != "" {
		t.Fatal("an endpoint that is not configured has no revision")
	}
}

// Coverage S8a §2, §4: match and serviceRef.path are part of what the person
// reviewed - an edit to either moves the endpoint's revision.
func TestEndpointRevisionCoversMatchAndPath(t *testing.T) {
	doc := "clusters:\n  - name: prd\n    alertmanager:\n      serviceRef: {namespace: m, name: am, port: web, path: /alertmanager}\n      match: {cluster: prd}\n"
	rev := func(d string) string {
		t.Helper()
		s, err := ReadSnapshot(writeFleetMode(t, d, 0o600))
		if err != nil {
			t.Fatal(err)
		}
		return s.EndpointRevision("prd", EndpointAlertmanager)
	}
	base := rev(doc)
	for name, edited := range map[string]string{
		"match value": strings.Replace(doc, "cluster: prd", "cluster: stg", 1),
		"match label": strings.Replace(doc, "{cluster: prd}", "{cluster: prd, env: prd}", 1),
		"path":        strings.Replace(doc, "/alertmanager", "/am", 1),
	} {
		if r := rev(edited); r == "" || r == base {
			t.Errorf("%s: the revision did not move", name)
		}
	}
}
