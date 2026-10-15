package gitcli

import (
	"bytes"
	"context"
	"strings"

	"github.com/moomora/klyx/internal/procrun"
)

// Helpers are the owner's credential helpers, as git would apply them, and
// the ones Klyx refused (slice 6 note §3.3).
type Helpers struct {
	Values  []string
	Refused []string // shell snippets ("!..."): never passed to git
}

// ownerScopes are the config scopes whose helpers Klyx carries over, as
// `git config --show-scope` names them: a vendor's built-in default (Apple
// Git's Xcode gitconfig, which sets osxkeychain, reports "unknown" and is
// read before the system file), the system file, and the global files
// ($XDG_CONFIG_HOME/git/config, then ~/.gitconfig). Never "local",
// "worktree" or "command".
var ownerScopes = map[string]bool{"unknown": true, "system": true, "global": true}

// ReadHelpers reads credential.helper as the owner's own git applies it,
// once at startup: every value in git's read order, with includes followed,
// each tagged with its scope; only the owner scopes are kept, and an empty
// value clears every helper before it. It runs from "/", so no repository
// is ever discovered and no local config is read. (An explicit --global
// read is not enough: when ~/.gitconfig exists it skips the XDG file, which
// git still applies at run time.)
func ReadHelpers(ctx context.Context, g Git, env Env) Helpers {
	r := procrun.Run(ctx, procrun.Spec{Exe: g.Path,
		Args: []string{"config", "--includes", "--show-scope", "-z", "--get-all", "credential.helper"},
		Env:  env.owner(), Dir: "/", StdoutMax: 64 << 10, StderrMax: 4096, Deadline: localDeadline, WaitDelay: waitDelay})
	if r.StartErr != nil || r.TimedOut || r.ExitCode != 0 || r.StdoutOver {
		return Helpers{} // exit 1: no helper anywhere
	}
	return applyHelpers(scopedValues(r.Stdout))
}

// scopedValues reads `-z --show-scope` output - scope NUL value NUL, in
// read order - and keeps the owner scopes' values.
func scopedValues(out []byte) []string {
	f := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	if len(f)%2 != 0 {
		return nil
	}
	var vs []string
	for i := 0; i < len(f); i += 2 {
		if ownerScopes[string(f[i])] {
			vs = append(vs, string(f[i+1]))
		}
	}
	return vs
}

// applyHelpers applies git's list semantics: values accumulate in order and
// an empty value clears the list.
func applyHelpers(values []string) Helpers {
	var h Helpers
	for _, v := range values {
		v = strings.TrimSpace(v)
		switch {
		case v == "":
			h.Values, h.Refused = nil, nil // a cleared snippet would not run either
		case strings.HasPrefix(v, "!"):
			h.Refused = append(h.Refused, v)
		default:
			h.Values = append(h.Values, v)
		}
	}
	return h
}
