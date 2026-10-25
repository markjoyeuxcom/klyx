package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/clibin"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/envread"
	"github.com/moomora/klyx/internal/settings"
)

func noTool(string) (string, error) { return "", errors.New("not on PATH") }

// standIn writes an executable shell script and returns a tool resolving to
// it.
func standIn(t *testing.T, name, body string, args []string, parse func([]byte) (string, bool)) diagTool {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return diagTool{name: name, bin: clibin.Binary{Name: name, LookPath: func(string) (string, error) { return p, nil }}, args: args, parse: parse}
}

func settingsWith(tools ...diagTool) *SettingsService {
	return &SettingsService{tools: tools, contained: func() bool { return true }, deadline: versionDeadline}
}

func TestRegistryIsTheSettingsRegistry(t *testing.T) {
	r := settingsWith().Registry()
	if len(r.Entries) != len(settings.Registry()) || len(r.Sections) != 8 || r.Sections[0] != "fleet" {
		t.Fatalf("%d entries, sections %v", len(r.Entries), r.Sections)
	}
}

// S1 §5: the resolution as the resolver found it - a missing tool, the
// override chosen, an override set but missing (with PATH and with a
// fallback) - without running anything.
func TestDiagnosticsResolvesWithoutRunning(t *testing.T) {
	exists := func(p string) bool { return p == "/custom/kubectl" || p == "/fallback/helm" }
	t.Setenv("KLYX_KUBECTL_PATH", "/custom/kubectl")
	t.Setenv("KLYX_HELM_PATH", "/gone/helm")
	t.Setenv("KLYX_FLUX_PATH", "/gone/flux")
	s := settingsWith(
		diagTool{name: "git", bin: clibin.Binary{Name: "git", Override: envread.KlyxGitPath, LookPath: noTool}},
		diagTool{name: "kubectl", bin: clibin.Binary{Name: "kubectl", Override: envread.KlyxKubectlPath, LookPath: noTool, ExecutableExists: exists}},
		diagTool{name: "helm", bin: clibin.Binary{Name: "helm", Override: envread.KlyxHelmPath, LookPath: noTool, ExecutableExists: exists, FallbackPaths: func() []string { return []string{"/fallback/helm"} }}},
		diagTool{name: "flux", bin: clibin.Binary{Name: "flux", Override: envread.KlyxFluxPath, LookPath: func(string) (string, error) { return "/usr/bin/flux", nil }, ExecutableExists: exists}},
	)
	got := s.Diagnostics().Tools
	want := []DiagToolDTO{
		{Name: "git", Source: "not found", Override: "KLYX_GIT_PATH"},
		{Name: "kubectl", Path: "/custom/kubectl", Source: "override", Override: "KLYX_KUBECTL_PATH"},
		{Name: "helm", Path: "/fallback/helm", Source: "fallback", Override: "KLYX_HELM_PATH", OverrideMissing: "/gone/helm"},
		{Name: "flux", Path: "/usr/bin/flux", Source: "path", Override: "KLYX_FLUX_PATH", OverrideMissing: "/gone/flux"},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The proxy variables that are set, by name, never their values.
func TestDiagnosticsNamesProxiesWithoutValues(t *testing.T) {
	for _, n := range proxyNames {
		t.Setenv(n.Key(), "")
	}
	t.Setenv("HTTPS_PROXY", "http://user:s3cr3t@proxy.example:3128")
	t.Setenv("no_proxy", "localhost")
	d := settingsWith().Diagnostics()
	if strings.Join(d.Proxies, ",") != "HTTPS_PROXY,no_proxy" {
		t.Fatalf("%v", d.Proxies)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "s3cr3t") || strings.Contains(string(b), "proxy.example") {
		t.Fatal("a proxy value reached Diagnostics")
	}
	if len(d.Bounds) == 0 || d.Bounds[0].Label == "" || d.Bounds[0].Value <= 0 {
		t.Fatalf("bounds %+v", d.Bounds)
	}
}

// The fleet file's load states: loaded, the startup error, and not loading
// now; and whether KLYX_CONFIG set the path.
func TestDiagnosticsSaysTheFleetFilesState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: a\n    context: a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KLYX_CONFIG", path)
	cs := NewConfigService(path, &config.Config{}, func(config.ClusterConfig) error { return nil })
	s := settingsWith()
	s.config = cs
	if f := s.Diagnostics().Fleet; f.State != "loaded" || !f.FromEnv || f.Path != path {
		t.Fatalf("%+v", f)
	}
	if err := os.WriteFile(path, []byte("clusters: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f := s.Diagnostics().Fleet; f.State != "not-loading-now" || f.FileError == "" {
		t.Fatalf("%+v", f)
	}
	WithConfigLoadError(cs, errors.New("the file did not parse"))
	t.Setenv("KLYX_CONFIG", "")
	if f := s.Diagnostics().Fleet; f.State != "startup-error" || f.LoadError == "" || f.FromEnv {
		t.Fatalf("%+v", f)
	}
}

func TestVersionsAreNotCheckedWithoutContainment(t *testing.T) {
	s := settingsWith(standIn(t, "kubectl", "touch ran; exit 0", []string{"version"}, parseKubectlVersion))
	s.contained = func() bool { return false }
	d := s.Diagnostics()
	if d.CanCheck || d.CannotCheck == "" {
		t.Fatalf("%+v", d)
	}
	if v := s.CheckVersions(context.Background()); len(v) != 1 || v[0].Version != "" || !strings.Contains(v[0].Failure, "containment") {
		t.Fatalf("%+v", v)
	}
}

func TestVersionParsers(t *testing.T) {
	for _, c := range []struct {
		parse func([]byte) (string, bool)
		in    string
		want  string
	}{
		{parseKubectlVersion, `{"clientVersion":{"gitVersion":"v1.31.2"}}`, "v1.31.2"},
		{parseKubectlVersion, `{"clientVersion":{"gitVersion":"$(rm -rf)"}}`, ""},
		{parseKubectlVersion, `Client Version: v1.31.2`, ""},
		{parseHelmVersion, "v3.16.2+g13654a5\n", "v3.16.2+g13654a5"},
		{parseHelmVersion, "WARNING: x\n", ""},
		{parseFluxVersion, "flux: v2.4.0\n", "v2.4.0"},
		{parseFluxVersion, "flux: v2.4.0\ndistribution: flux-v2.4.0\n", "v2.4.0"},
		{parseFluxVersion, "something else\n", ""},
	} {
		got, ok := c.parse([]byte(c.in))
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%q: %q %v", c.in, got, ok)
		}
	}
	if got := shownVersion("v1\x1b[31m.2‮" + strings.Repeat("9", 200)); strings.ContainsAny(got, "\x1b‮") || len([]rune(got)) != versionShown+1 {
		t.Fatalf("%q", got)
	}
}
