package gitcli

import (
	"strings"

	"github.com/moomora/klyx/internal/envread"
)

// Env is the owner's environment values git may see (parent §7: an
// allowlist built from scratch). Everything else - every other GIT_*
// variable, cloud credentials, KUBECONFIG - never reaches git.
type Env struct {
	Home, Path, User, Tmpdir, SSHAuthSock, XDGConfigHome string
	Locale                                               []string // LANG and LC_*

	// systemConfig stands in for git's system config file; set only by this
	// package's tests, which cannot touch the machine's real one.
	systemConfig string
}

// OwnerEnv reads the allowlist once: getenv for the named values, environ
// for the locale (ProcessEnv passes envread's GitLocale walk).
func OwnerEnv(getenv func(envread.Name) string, environ []string) Env {
	e := Env{Home: getenv(envread.Home), Path: getenv(envread.Path), User: getenv(envread.User), Tmpdir: getenv(envread.Tmpdir),
		SSHAuthSock: getenv(envread.SSHAuthSock), XDGConfigHome: getenv(envread.XDGConfigHome)}
	for _, kv := range environ {
		if strings.HasPrefix(kv, "LANG=") || strings.HasPrefix(kv, "LC_") {
			e.Locale = append(e.Locale, kv)
		}
	}
	return e
}

// ProcessEnv is OwnerEnv of this process's environment.
func ProcessEnv() Env { return OwnerEnv(envread.Get, envread.Walk(envread.GitLocale)) }

func (e Env) base() []string {
	var out []string
	add := func(k, v string) {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	add("HOME", e.Home)
	add("PATH", e.Path)
	add("USER", e.User)
	add("TMPDIR", e.Tmpdir)
	return append(out, e.Locale...)
}

// minimal is enough to run `git --version`.
func (e Env) minimal() []string {
	return append(e.base(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}

func (e Env) dir() string {
	if e.Home != "" {
		return e.Home
	}
	return "/"
}

// isolated is the environment of every Klyx git command: the owner's
// allowlisted values, the generated global config, no system config, no
// prompts, no pager, literal pathspecs, and - except for an acquisition
// fetch - no lazy fetch.
func (e Env) isolated(globalConfig, gitDir string, lazyFetch bool) []string {
	out := e.base()
	if e.SSHAuthSock != "" {
		out = append(out, "SSH_AUTH_SOCK="+e.SSHAuthSock)
	}
	out = append(out,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+globalConfig,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"SSH_ASKPASS=/usr/bin/false",
		"SSH_ASKPASS_REQUIRE=never",
		"GCM_INTERACTIVE=never",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"GIT_LITERAL_PATHSPECS=1",
	)
	if gitDir != "" {
		out = append(out, "GIT_DIR="+gitDir)
	}
	if !lazyFetch {
		out = append(out, "GIT_NO_LAZY_FETCH=1")
	}
	return out
}

// owner is the environment for reading the owner's credential helpers: the
// owner's own config scopes, nothing generated (slice 6 note §3.3).
func (e Env) owner() []string {
	out := e.base()
	if e.XDGConfigHome != "" {
		out = append(out, "XDG_CONFIG_HOME="+e.XDGConfigHome)
	}
	if e.systemConfig != "" {
		out = append(out, "GIT_CONFIG_SYSTEM="+e.systemConfig)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "PAGER=cat")
}
