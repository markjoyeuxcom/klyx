package appbridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/config"
)

// Settings review slice 0 regressions.

func configServiceAt(t *testing.T, doc string, cfg *config.Config) *ConfigService {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if doc != "" {
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := NewConfigService(path, cfg, func(config.ClusterConfig) error { return nil })
	s.kubeconfigPath = func() string { return "/tmp/kubeconfig" }
	s.kubeContexts = func(string) ([]string, error) { return []string{"a", "new"}, nil }
	return s
}

// An existing fleet.yaml that did not load is said, shown without a long
// pasted value, and adding clusters waits for it; a missing one is a first
// run, not an error.
func TestAFleetFileThatDidNotLoadIsSaid(t *testing.T) {
	bad := "clusters:\n  - name: a\n    alertmanager:\n      matches: {cluster: a}\n"
	s := configServiceAt(t, bad, &config.Config{})
	_, err := config.Load(s.path)
	if err == nil {
		t.Fatal("the bad file loaded")
	}
	WithConfigLoadError(s, err)
	dto := s.GetFleetConfig()
	if !strings.Contains(dto.LoadError, `"matches"`) || !strings.Contains(dto.LoadError, `did you mean "match"?`) || s.LoadError() != dto.LoadError {
		t.Fatalf("%+v", dto)
	}
	if r := s.AddClusters([]string{"new"}); r.OK || !strings.Contains(r.Error, "did not load when Klyx started") {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(s.path); string(b) != bad {
		t.Fatal("the bad file was changed")
	}

	first := configServiceAt(t, "", &config.Config{})
	_, err = config.Load(first.path)
	WithConfigLoadError(first, err)
	if dto := first.GetFleetConfig(); dto.LoadError != "" || dto.FileError != "" {
		t.Fatalf("a first run is not an error: %+v", dto)
	}
}

// Fleet rows are the running configuration: a hand edit to a cluster's
// protection, placement or presence is said until a restart applies it,
// and a file that does not load now says so.
func TestARunningClusterSaysHowTheFileNowDiffers(t *testing.T) {
	running := &config.Config{Clusters: []config.ClusterConfig{
		{Name: "a", Context: "a", Group: "we"},
		{Name: "gone", Context: "gone"},
	}}
	s := configServiceAt(t, "clusters:\n  - name: a\n    context: a\n    group: ne\n    protected: true\n  - name: added-by-hand\n", running)
	dto := s.GetFleetConfig()
	byName := map[string]FleetClusterDTO{}
	for _, c := range dto.Clusters {
		byName[c.Name] = c
	}
	if a := byName["a"]; a.Protected || !strings.Contains(a.Drift, "the file protects it; it is not protected until Klyx restarts") || !strings.Contains(a.Drift, "group") {
		t.Fatalf("a: %+v", a)
	}
	if g := byName["gone"]; !strings.Contains(g.Drift, "not in fleet.yaml any more") {
		t.Fatalf("gone: %+v", g)
	}
	if len(dto.FileOnly) != 1 || dto.FileOnly[0] != "added-by-hand" {
		t.Fatalf("file only: %+v", dto.FileOnly)
	}
	if !s.IsProtected("gone") && s.IsProtected("a") {
		t.Fatal("protection follows the running fleet, not the file, until a restart")
	}

	same := configServiceAt(t, "clusters:\n  - name: a\n    context: a\n    group: we\n", &config.Config{Clusters: []config.ClusterConfig{{Name: "a", Context: "a", Group: "we"}}})
	if dto := same.GetFleetConfig(); dto.Clusters[0].Drift != "" || len(dto.FileOnly) != 0 || dto.FileError != "" {
		t.Fatalf("an unchanged file differs: %+v", dto)
	}

	broken := configServiceAt(t, "clusters: [\n", running)
	if dto := broken.GetFleetConfig(); dto.FileError == "" || dto.Clusters[0].Drift != "" {
		t.Fatalf("a file that does not load now: %+v", dto)
	}
	// the sidebar badge never reads the file
	broken.readSnapshot = func(string) (config.Snapshot, error) {
		t.Fatal("NewContextCount read fleet.yaml")
		return config.Snapshot{}, errors.New("x")
	}
	if n := broken.NewContextCount(); n != 1 {
		t.Fatalf("new contexts: %d", n)
	}
}

// A row shows an endpoint's unverified certificate, its scope and a
// Service's scheme and path; a source-less Loki is never "discovered".
func TestAnEndpointRowShowsItsTLSAndScope(t *testing.T) {
	doc := `clusters:
  - name: a
    alertmanager:
      endpoint: https://am.example
      tlsSkipVerify: true
      match: {cluster: a, env: dev}
    loki:
      match: {cluster: a}
  - name: b
    metrics:
      serviceRef: {namespace: mon, name: mimir, port: http, scheme: https, path: /prometheus}
      tlsSkipVerify: true
    alertmanager:
      endpoint: https://am.example
`
	svc, _, _, _ := newAMServiceEnv(t, doc)
	rows := map[string]EndpointRowDTO{}
	for _, r := range svc.GetEndpoints().Rows {
		rows[r.Cluster+"/"+r.Kind] = r
	}
	if r := rows["a/alertmanager"]; !r.TLSOff || r.Scope != "cluster=a, env=dev" || r.Unscoped {
		t.Fatalf("a/alertmanager: %+v", r)
	}
	if r := rows["a/loki"]; !strings.Contains(r.Source, "Klyx does not discover Loki") || strings.Contains(r.Source, "discovered in the cluster") {
		t.Fatalf("a/loki: %+v", r)
	}
	if r := rows["b/alertmanager"]; r.TLSOff || r.Scope != "" || !r.Unscoped {
		t.Fatalf("b/alertmanager: %+v", r)
	}
	// a proxied endpoint's TLS is the API server's: tlsSkipVerify does not apply
	if r := rows["b/metrics"]; r.Source != "Service mon/mimir:http (https, path /prometheus, through the API server)" || r.Unscoped || r.TLSOff {
		t.Fatalf("b/metrics: %+v", r)
	}
	cfg, err := config.Load(svc.path)
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(cfg.Warnings(), "\n"); !strings.Contains(w, `cluster "a": loki has no endpoint or serviceRef`) {
		t.Fatalf("warnings: %s", w)
	}
}

// Codex's cross-check probe: a hand edit to the notifications block before
// a toggle survives it - the toggle is refused, and the file is unchanged.
func TestANotificationToggleNeverOverwritesAHandEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: prd\n    context: prd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewNotificationsService(path, &config.Config{}, func(string) (config.ClusterConfig, bool) { return config.ClusterConfig{}, false }, nil, nil, nil)
	edited := "clusters:\n  - name: prd\n    context: prd\nnotifications:\n  enabled: false\n  scope: all\n  recoveries: true\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	r := s.SetSettings(NotificationSettingsDTO{Enabled: true})
	if r.OK || !strings.Contains(r.Error, "edited after Klyx read it") {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(path); string(b) != edited {
		t.Fatalf("the hand edit was overwritten:\n%s", b)
	}
	if got := s.Settings(); got.Enabled {
		t.Fatalf("a refused save applied: %+v", got)
	}
}

// Codex's review of slice 0, finding 6, through Settings: a broken link is
// said at startup and now, not taken for a first run.
func TestABrokenFleetLinkIsSaidInSettings(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "fleet.yaml")
	if err := os.Symlink(filepath.Join(dir, "gone.yaml"), link); err != nil {
		t.Fatal(err)
	}
	s := NewConfigService(link, &config.Config{}, func(config.ClusterConfig) error { return nil })
	s.kubeconfigPath = func() string { return "/tmp/kubeconfig" }
	s.kubeContexts = func(string) ([]string, error) { return nil, nil }
	_, err := config.Load(link)
	WithConfigLoadError(s, err)
	if dto := s.GetFleetConfig(); !strings.Contains(dto.LoadError, "symlink whose target does not exist") || !strings.Contains(dto.FileError, "symlink whose target does not exist") {
		t.Fatalf("%+v", dto)
	}
}

// Codex's re-review of slice 0: a long key pasted into the file never
// reaches a diagnostic - a top-level warning, a match label's error, YAML's
// duplicate-key error - in Load's strings or in Settings' payload; a short
// key stays named, to be fixed.
func TestALongPastedKeyNeverReachesADiagnostic(t *testing.T) {
	key := "sk-synthetic-0123456789abcdefghijklmnop"
	for _, c := range []struct {
		name, doc string
	}{
		{"a top-level key", "clusters:\n  - name: a\n" + key + ": true\n"},
		{"a match label", "clusters:\n  - name: a\n    alertmanager:\n      endpoint: https://am.example\n      match: {" + key + ": a}\n"},
		{"a duplicate key", "clusters:\n  - name: a\n    " + key + ": 1\n    " + key + ": 2\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := configServiceAt(t, c.doc, &config.Config{Clusters: []config.ClusterConfig{{Name: "a", Context: "a"}}})
			cfg, err := config.Load(s.path)
			if err != nil && strings.Contains(err.Error(), key) {
				t.Fatalf("Load: %v", err)
			}
			if cfg != nil {
				s.cfg = cfg
				if w := strings.Join(cfg.Warnings(), "\n"); strings.Contains(w, key) || !strings.Contains(w, "a key of 39 characters") {
					t.Fatalf("warnings: %s", w)
				}
			}
			WithConfigLoadError(s, err)
			b, _ := json.Marshal(s.GetFleetConfig())
			if strings.Contains(string(b), key) || !strings.Contains(string(b), "a key of 39 characters") {
				t.Fatalf("Settings: %s", b)
			}
			if _, err := config.ReadSnapshot(s.path); err != nil && strings.Contains(err.Error(), key) {
				t.Fatalf("ReadSnapshot: %v", err)
			}
		})
	}
	short := configServiceAt(t, "clusters:\n  - name: a\nnotification: {}\n", &config.Config{})
	if cfg, err := config.Load(short.path); err != nil || !strings.Contains(strings.Join(cfg.Warnings(), "\n"), `"notification"`) {
		t.Fatalf("a short key is named: %v", err)
	}
}

// Through Settings: an anchor or a custom tag never crosses the payload,
// nor a writer's refusal into an AI Save's answer.
func TestNoYAMLErrorCrossesSettings(t *testing.T) {
	key := "sk-synthetic-0123456789abcdefghijklmnop"
	for _, doc := range []string{
		"clusters:\n  - name: a\n    protected: *" + key + "\n",
		"clusters:\n  - name: a\n    protected: !" + key + " true\n",
	} {
		s := configServiceAt(t, doc, &config.Config{Clusters: []config.ClusterConfig{{Name: "a", Context: "a"}}})
		_, err := config.Load(s.path)
		WithConfigLoadError(s, err)
		b, _ := json.Marshal(s.GetFleetConfig())
		if strings.Contains(string(b), key) || !strings.Contains(string(b), "loadError") {
			t.Fatalf("Settings: %s", b)
		}
	}
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	doc := "clusters:\n  - name: a\nextra:\n  ? [" + key + "]\n  : ignored\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAI(path, config.AIConfig{Provider: "off"}); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("SaveAI: %v", err)
	}
}

// Through Settings and an AI Save: a tagged scalar's value never crosses.
func TestATaggedScalarNeverCrossesSettings(t *testing.T) {
	ticked := "sk-synthetic-012345`6789abcdefghijklmnop"
	s := configServiceAt(t, "clusters:\n  - name: a\n    protected: !!int '"+ticked+"'\n", &config.Config{Clusters: []config.ClusterConfig{{Name: "a", Context: "a"}}})
	_, err := config.Load(s.path)
	WithConfigLoadError(s, err)
	if b, _ := json.Marshal(s.GetFleetConfig()); strings.Contains(string(b), "6789abcdefghijklmnop") || !strings.Contains(string(b), "withheld") {
		t.Fatalf("Settings: %s", b)
	}
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: a\nextra: !!int sk-synthetic-0123456789abcdefghijklmnop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAI(path, config.AIConfig{Provider: "off"}); err == nil || strings.Contains(err.Error(), "0123456789abcdefghijklmnop") {
		t.Fatalf("SaveAI: %v", err)
	}
}
