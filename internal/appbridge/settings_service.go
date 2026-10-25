package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/moomora/klyx/internal/bounds"
	"github.com/moomora/klyx/internal/clibin"
	"github.com/moomora/klyx/internal/envread"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/procrun"
	"github.com/moomora/klyx/internal/settings"
)

// SettingsService serves Settings' registry and its read-only Diagnostics
// (Settings S1 §2, §5). Nothing here changes Klyx's configuration or state,
// or sends anything anywhere; "Check versions" runs the local tools'
// client-only version commands, contained, and only when asked.
type SettingsService struct {
	config *ConfigService
	git    *GitService
	// tools are the four binaries Diagnostics resolves (tests stand them in)
	tools []diagTool
	// contained reports whether this platform has the reviewed process
	// containment (procrun); without it versions are not checked
	contained func() bool
	deadline  time.Duration
}

// diagTool is one binary: how Klyx resolves it, and its version command.
// Args nil: its version is the Git service's, never run here.
type diagTool struct {
	name  string
	bin   clibin.Binary
	args  []string
	parse func([]byte) (string, bool)
}

const (
	versionDeadline = 5 * time.Second
	versionWait     = time.Second
	versionOutMax   = 64 << 10
	versionShown    = 120
)

// NewSettingsService wires the service to the services that own the values
// it reports.
func NewSettingsService(config *ConfigService, git *GitService) *SettingsService {
	return &SettingsService{config: config, git: git, tools: realTools(), contained: procrun.Supported, deadline: versionDeadline}
}

func realTools() []diagTool {
	return []diagTool{
		{name: "git", bin: gitcli.Binary()},
		{name: "kubectl", bin: fleet.KubectlBinary(), args: []string{"version", "--client", "--output=json"}, parse: parseKubectlVersion},
		{name: "helm", bin: helmcli.Binary(), args: []string{"version", "--short"}, parse: parseHelmVersion},
		{name: "flux", bin: fluxcli.Binary(), args: []string{"version", "--client"}, parse: parseFluxVersion},
	}
}

// SettingsRegistryDTO is the registry, for the page, search and the console.
type SettingsRegistryDTO struct {
	Sections []string         `json:"sections"`
	Entries  []settings.Entry `json:"entries"`
}

// Registry is every setting's contract.
func (s *SettingsService) Registry() SettingsRegistryDTO {
	return SettingsRegistryDTO{Sections: settings.Sections, Entries: settings.Registry()}
}

// DiagnosticsDTO is Settings > Diagnostics.
type DiagnosticsDTO struct {
	Fleet       DiagFleetDTO   `json:"fleet"`
	Tools       []DiagToolDTO  `json:"tools"`
	Proxies     []string       `json:"proxies"` // the proxy variables that are set, by name only
	EnvSet      []string       `json:"envSet"`  // every declared product variable that is set, by name only
	Bounds      []DiagBoundDTO `json:"bounds"`
	CanCheck    bool           `json:"canCheck"`              // versions can be checked here
	CannotCheck string         `json:"cannotCheck,omitempty"` // why not
}

// DiagFleetDTO is the fleet file's state.
type DiagFleetDTO struct {
	Path      string   `json:"path"`
	FromEnv   bool     `json:"fromEnv"` // KLYX_CONFIG set it
	State     string   `json:"state"`   // loaded | startup-error | not-loading-now
	LoadError string   `json:"loadError,omitempty"`
	FileError string   `json:"fileError,omitempty"`
	Warnings  []string `json:"warnings"`
	Drifted   int      `json:"drifted"`  // running clusters whose file entry differs
	FileOnly  []string `json:"fileOnly"` // in the file, not running
}

// DiagToolDTO is one binary as the resolver found it, without running it.
type DiagToolDTO struct {
	Name            string `json:"name"`
	Path            string `json:"path,omitempty"`
	Source          string `json:"source"`                    // override | path | fallback | not found
	Override        string `json:"override"`                  // the override variable's name
	OverrideMissing string `json:"overrideMissing,omitempty"` // an override that is set but no executable
}

// DiagBoundDTO is one fixed ceiling.
type DiagBoundDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value int64  `json:"value"`
	Unit  string `json:"unit"`
}

// proxyNames are the proxy variables, upper case first.
var proxyNames = []envread.Name{envread.HTTPProxy, envread.HTTPSProxy, envread.NoProxy, envread.HTTPProxyLower, envread.HTTPSProxyLower, envread.NoProxyLower}

// Diagnostics reads the fleet file's state, resolves the tools without
// running them, and lists the launch environment's proxy names and the
// fixed bounds.
func (s *SettingsService) Diagnostics() DiagnosticsDTO {
	dto := DiagnosticsDTO{Tools: []DiagToolDTO{}, Proxies: []string{}, EnvSet: []string{}, Bounds: []DiagBoundDTO{}}
	if s.config != nil {
		f := s.config.GetFleetConfig()
		dto.Fleet = DiagFleetDTO{Path: f.Path, State: "loaded", LoadError: f.LoadError, FileError: f.FileError, Warnings: f.Warnings, FileOnly: f.FileOnly}
		dto.Fleet.FromEnv = envread.Get(envread.KlyxConfig) != ""
		switch {
		case f.LoadError != "":
			dto.Fleet.State = "startup-error"
		case f.FileError != "":
			dto.Fleet.State = "not-loading-now"
		}
		for _, c := range f.Clusters {
			if c.Drift != "" {
				dto.Fleet.Drifted++
			}
		}
	}
	if dto.Fleet.Warnings == nil {
		dto.Fleet.Warnings = []string{}
	}
	if dto.Fleet.FileOnly == nil {
		dto.Fleet.FileOnly = []string{}
	}
	for _, t := range s.tools {
		r := t.bin.Resolution()
		dto.Tools = append(dto.Tools, DiagToolDTO{Name: t.name, Path: r.Path, Source: string(r.Source), Override: t.bin.Override.Key(), OverrideMissing: r.OverrideMissing})
	}
	for _, n := range proxyNames {
		if v, ok := envread.Lookup(n); ok && v != "" {
			dto.Proxies = append(dto.Proxies, n.Key())
		}
	}
	for _, d := range envread.Declared() {
		if d.Kind == "harness" {
			continue
		}
		if v, ok := envread.Lookup(d.Name); ok && v != "" {
			dto.EnvSet = append(dto.EnvSet, d.Key)
		}
	}
	labels := map[string]string{}
	for _, e := range settings.Registry() {
		labels[e.ID] = e.Label
	}
	for _, b := range bounds.List() {
		dto.Bounds = append(dto.Bounds, DiagBoundDTO{ID: b.ID, Label: labels["bound."+b.ID], Value: b.Value, Unit: b.Unit})
	}
	dto.CanCheck = s.contained()
	if !dto.CanCheck {
		dto.CannotCheck = "Versions are not checked on this platform: Klyx has no reviewed process containment for it."
	}
	return dto
}

// ToolVersionDTO is one tool's version, or why it has none.
type ToolVersionDTO struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Failure string `json:"failure,omitempty"` // a fixed reason, never the raw error
	Exit    *int   `json:"exit,omitempty"`    // the exit status, when it exited non-zero
	Reused  bool   `json:"reused,omitempty"`  // the Git service's version, not run here
}

// CheckVersions runs each tool's client-only version command (S1 §5): the
// path the resolver finds, explicit argv and no shell, PATH, HOME and an
// empty scratch KUBECONFIG only, in its own process group, under a
// deadline, with capped output. git is not run: the Git service's pinned
// path and version are reused. Cancelling the call kills what is running.
func (s *SettingsService) CheckVersions(ctx context.Context) []ToolVersionDTO {
	out := []ToolVersionDTO{}
	if !s.contained() {
		for _, t := range s.tools {
			out = append(out, ToolVersionDTO{Name: t.name, Failure: "not checked: no reviewed process containment on this platform"})
		}
		return out
	}
	for _, t := range s.tools {
		if t.args == nil {
			out = append(out, s.gitVersion(t.name))
			continue
		}
		out = append(out, s.runVersion(ctx, t))
	}
	return out
}

func (s *SettingsService) gitVersion(name string) ToolVersionDTO {
	v := ToolVersionDTO{Name: name, Reused: true}
	if s.git == nil {
		v.Failure = "Git is not running in Klyx"
		return v
	}
	b := s.git.BinaryState()
	switch {
	case !b.Ready:
		v.Failure = "Git is still starting"
	case b.Off != "":
		v.Failure = "Git is off: " + b.Off
	default:
		v.Version = shownVersion(b.Version)
	}
	return v
}

func (s *SettingsService) runVersion(ctx context.Context, t diagTool) ToolVersionDTO {
	v := ToolVersionDTO{Name: t.name}
	if ctx.Err() != nil {
		v.Failure = "cancelled"
		return v
	}
	path, ok := t.bin.Resolve()
	if !ok {
		v.Failure = "not found"
		return v
	}
	scratch, err := os.MkdirTemp("", "klyx-version-")
	if err != nil {
		v.Failure = "no scratch directory for the empty kubeconfig"
		return v
	}
	defer func() { _ = os.RemoveAll(scratch) }() // the scratch kubeconfig, removed afterwards
	kubeconfig := scratch + string(os.PathSeparator) + "kubeconfig"
	if err := os.WriteFile(kubeconfig, nil, 0o600); err != nil {
		v.Failure = "no scratch directory for the empty kubeconfig"
		return v
	}
	env := []string{"KUBECONFIG=" + kubeconfig}
	for _, n := range []envread.Name{envread.Path, envread.Home} {
		if val, ok := envread.Lookup(n); ok {
			env = append(env, n.Key()+"="+val)
		}
	}
	r := procrun.Run(ctx, procrun.Spec{Exe: path, Args: t.args, Env: env, Dir: scratch,
		StdoutMax: versionOutMax, StderrMax: versionOutMax, Deadline: s.deadline, WaitDelay: versionWait})
	switch {
	case r.StartErr != nil:
		v.Failure = "did not start"
	case ctx.Err() != nil:
		v.Failure = "cancelled"
	case r.TimedOut:
		v.Failure = fmt.Sprintf("did not finish within %s", s.deadline)
	case r.ExitCode != 0:
		code := r.ExitCode
		v.Failure, v.Exit = "exited with an error", &code
	case r.StdoutOver:
		v.Failure = fmt.Sprintf("printed more than %d KiB", versionOutMax>>10)
	default:
		if got, ok := t.parse(r.Stdout); ok {
			v.Version = shownVersion(got)
		} else {
			v.Failure = "printed no version Klyx recognises"
		}
	}
	return v
}

// shownVersion is a parsed version with control characters removed, then
// clipped (S1 §5: parsed first, only then clipped).
func shownVersion(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > versionShown {
		s = string(r[:versionShown]) + "…"
	}
	return s
}

func parseKubectlVersion(b []byte) (string, bool) {
	var v struct {
		ClientVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
	}
	if json.Unmarshal(b, &v) != nil || !versionShape.MatchString(v.ClientVersion.GitVersion) {
		return "", false
	}
	return v.ClientVersion.GitVersion, true
}

var (
	versionShape = regexp.MustCompile(`^v?[0-9]+\.[0-9]+(\.[0-9]+)?([-+][0-9A-Za-z.+-]*)?$`)
	fluxLine     = regexp.MustCompile(`(?m)^flux:\s*(\S+)\s*$`)
)

func parseHelmVersion(b []byte) (string, bool) {
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if line = strings.TrimSpace(line); !versionShape.MatchString(line) {
		return "", false
	}
	return line, true
}

func parseFluxVersion(b []byte) (string, bool) {
	m := fluxLine.FindSubmatch(b)
	if m == nil || !versionShape.Match(m[1]) {
		return "", false
	}
	return string(m[1]), true
}
