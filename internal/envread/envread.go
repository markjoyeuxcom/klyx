// Package envread is the only way product code reaches the process
// environment (Settings S1 §3.1). Every variable Klyx reads is declared
// here as a Name, an opaque handle only this package can make; every walk
// and every child environment names a declared Family. A scan of product
// code (internal/settings) fails on any other reference to the
// environment, so a variable Klyx reads is always declared, described in
// Settings, and never read in a way the declarations do not show.
package envread

import (
	"errors"
	"os"
	"sort"
	"strings"
)

// Name is a declared environment variable. Its field is unexported, so
// outside this package only the zero Name can be constructed, and every
// function here refuses it before any access.
type Name struct{ d *decl }

// Family is a declared set of variables a walk or a child process takes.
type Family struct{ f *family }

// Purpose is a declared reason to read a variable whose name comes from
// configuration (the AI key's environment variable).
type Purpose struct{ p *purpose }

type decl struct {
	key      string
	kind     string // product, diagnostic, tool-override, library, injected, owner, platform, inherited, harness
	writable bool
}

type family struct {
	id         string
	names      map[string]bool
	prefixes   []string
	inheritAll bool
}

type purpose struct{ id string }

var (
	names    = map[*decl]bool{}
	families = map[*family]bool{}
	purposes = map[*purpose]bool{}
	order    []*decl
)

func declare(key, kind string) Name {
	d := &decl{key: key, kind: kind}
	names[d] = true
	order = append(order, d)
	return Name{d}
}

func declareFamily(id string, inheritAll bool, keys []string, prefixes ...string) Family {
	f := &family{id: id, names: map[string]bool{}, prefixes: prefixes, inheritAll: inheritAll}
	for _, k := range keys {
		f.names[k] = true
	}
	families[f] = true
	return Family{f}
}

func declarePurpose(id string) Purpose {
	p := &purpose{id: id}
	purposes[p] = true
	return Purpose{p}
}

// The declarations, by kind.
var (
	// product
	KlyxConfig = declare("KLYX_CONFIG", "product")
	// developer diagnostics, read by the product binary
	KlyxRowsTrace     = declare("KLYX_ROWS_TRACE", "diagnostic")
	KlyxLabNoiseBytes = declare("KLYX_LAB_NOISE_BYTES", "diagnostic")
	// tool path overrides (clibin)
	KlyxGitPath     = declare("KLYX_GIT_PATH", "tool-override")
	KlyxKubectlPath = declare("KLYX_KUBECTL_PATH", "tool-override")
	KlyxHelmPath    = declare("KLYX_HELM_PATH", "tool-override")
	KlyxFluxPath    = declare("KLYX_FLUX_PATH", "tool-override")
	// read through a library's own constant (client-go's
	// RecommendedConfigPathEnvVar; a test checks they agree)
	Kubeconfig = declare("KUBECONFIG", "library")
	// read through an injected function
	Terminal      = declare("TERMINAL", "injected")
	SSHAuthSock   = declare("SSH_AUTH_SOCK", "injected")
	XDGConfigHome = declare("XDG_CONFIG_HOME", "injected")
	// the owner's basic environment, which Git's allowlist takes
	Home   = declare("HOME", "owner")
	Path   = writable(declare("PATH", "owner"))
	User   = declare("USER", "owner")
	Tmpdir = declare("TMPDIR", "owner")
	// Windows install roots, for finding tools
	ProgramFiles      = declare("ProgramFiles", "platform")
	ProgramFilesX86   = declare("ProgramFiles(x86)", "platform")
	ProgramData       = declare("ProgramData", "platform")
	ChocolateyInstall = declare("ChocolateyInstall", "platform")
	// honoured by libraries; Klyx reads only whether they are set
	HTTPProxy       = declare("HTTP_PROXY", "inherited")
	HTTPSProxy      = declare("HTTPS_PROXY", "inherited")
	NoProxy         = declare("NO_PROXY", "inherited")
	HTTPProxyLower  = declare("http_proxy", "inherited")
	HTTPSProxyLower = declare("https_proxy", "inherited")
	NoProxyLower    = declare("no_proxy", "inherited")
	// the smoke and eval harnesses (build-tagged; not product settings)
	KlyxSmokeOut       = declare("KLYX_SMOKE_OUT", "harness")
	KlyxSmokeTheme     = declare("KLYX_SMOKE_THEME", "harness")
	KlyxSmokeWidth     = declare("KLYX_SMOKE_WIDTH", "harness")
	KlyxSmokeHeight    = declare("KLYX_SMOKE_HEIGHT", "harness")
	KlyxSmokeCluster   = declare("KLYX_SMOKE_CLUSTER", "harness")
	KlyxEvalOut        = declare("KLYX_EVAL_OUT", "harness")
	KlyxEvalScenarios  = declare("KLYX_EVAL_SCENARIOS", "harness")
	KlyxEvalA3         = declare("KLYX_EVAL_A3", "harness")
	KlyxEvalLab        = declare("KLYX_EVAL_LAB", "harness")
	KlyxEvalKind       = declare("KLYX_EVAL_KIND", "harness")
	KlyxEvalRuns       = declare("KLYX_EVAL_RUNS", "harness")
	KlyxClaudeBin      = declare("KLYX_CLAUDE_BIN", "harness")
	KlyxClaudeContract = declare("KLYX_CLAUDE_CONTRACT", "harness")
)

// The families.
var (
	// NativeTools is kubectl's, helm's and flux's: the whole parent
	// environment, with extras last (an exec plugin's own variables must
	// reach it).
	NativeTools = declareFamily("native-tools", true, nil)
	// ClaudeCLI is the Claude Code CLI's allowlist (claudecli_contract.go
	// adds its fixed lockdown values after it).
	ClaudeCLI = declareFamily("claude-cli", false, []string{"HOME", "PATH", "TMPDIR", "LANG", "USER", "LOGNAME"}, "LC_")
	// GitLocale is the locale Git's allowlist takes.
	GitLocale = declareFamily("git-locale", false, []string{"LANG"}, "LC_")
)

// AIKey is the purpose of reading the variable ai.apiKeyEnv names.
var AIKey = declarePurpose("ai-key")

func writable(n Name) Name {
	n.d.writable = true
	return n
}

// The raw access, in one place.
var (
	lookupEnv = os.LookupEnv
	environ   = os.Environ
	setenv    = os.Setenv
)

// ErrUndeclared: a zero or undeclared handle, refused before any access.
var ErrUndeclared = errors.New("envread: an undeclared environment handle")

func (n Name) valid() bool    { return n.d != nil && names[n.d] }
func (f Family) valid() bool  { return f.f != nil && families[f.f] }
func (p Purpose) valid() bool { return p.p != nil && purposes[p.p] }

// Key is the variable's name, for display and for passing to a library.
func (n Name) Key() string {
	if !n.valid() {
		return ""
	}
	return n.d.key
}

// Get is the variable's value, "" when unset or the handle is undeclared.
func Get(n Name) string {
	v, _ := Lookup(n)
	return v
}

// Lookup is the variable's value and whether it is set. An undeclared
// handle reads nothing.
func Lookup(n Name) (string, bool) {
	if !n.valid() {
		return "", false
	}
	return lookupEnv(n.d.key)
}

// Set sets a variable declared writable (PATH, for the desktop launch).
func Set(n Name, v string) error {
	if !n.valid() || !n.d.writable {
		return ErrUndeclared
	}
	return setenv(n.d.key, v)
}

// admits reports whether family f takes the variable key.
func (f *family) admits(key string) bool {
	if f.inheritAll || f.names[key] {
		return true
	}
	for _, p := range f.prefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// Walk is the parent environment's entries ("K=V") the family takes, in
// the environment's order; nil for an undeclared family.
func Walk(f Family) []string {
	if !f.valid() {
		return nil
	}
	var out []string
	for _, kv := range environ() {
		k, _, ok := strings.Cut(kv, "=")
		if ok && f.f.admits(k) {
			out = append(out, kv)
		}
	}
	return out
}

// ForChild is a child process's environment for the family, with extra
// entries last (os/exec keeps the last of a duplicated key, so they win).
// Only a declared inherit-all family with no extra gives nil, which os/exec
// reads as "inherit the parent unchanged" - today's behaviour for kubectl,
// helm and flux. Everything else is never nil, because nil would inherit
// everything (Codex's S1 implementation review, R2): an undeclared family
// gives only the extras, and an allowlist that admits nothing gives an
// empty environment.
func ForChild(f Family, extra []string) []string {
	if !f.valid() {
		return append([]string{}, extra...)
	}
	if f.f.inheritAll && len(extra) == 0 {
		return nil
	}
	return append(append([]string{}, Walk(f)...), extra...)
}

// Dynamic is a reader for variables whose names come from configuration,
// for one declared purpose; a reader for an undeclared purpose reads
// nothing.
func Dynamic(p Purpose) func(string) string {
	if !p.valid() {
		return func(string) string { return "" }
	}
	return func(key string) string {
		v, _ := lookupEnv(key)
		return v
	}
}

// Declared is every declared variable, for the registry's coverage test
// and Diagnostics: its key and kind, in declaration order.
func Declared() []DeclaredName {
	out := make([]DeclaredName, 0, len(order))
	for _, d := range order {
		out = append(out, DeclaredName{Name: Name{d}, Key: d.key, Kind: d.kind})
	}
	return out
}

// DeclaredName is one declaration, as Declared lists it.
type DeclaredName struct {
	Name Name
	Key  string
	Kind string
}

// PurposeIDs are the declared dynamic purposes' ids, sorted.
func PurposeIDs() []string {
	var out []string
	for p := range purposes {
		out = append(out, p.id)
	}
	sort.Strings(out)
	return out
}

// FamilyIDs are the declared families' ids, sorted.
func FamilyIDs() []string {
	var out []string
	for f := range families {
		out = append(out, f.id)
	}
	sort.Strings(out)
	return out
}
