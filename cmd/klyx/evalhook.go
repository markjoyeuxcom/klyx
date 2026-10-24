package main

import (
	"context"

	"github.com/moomora/klyx/internal/appbridge"
)

// evalHook is the AI eval's (docs/superpowers/specs/2026-10-22-klyx-ai-eval.md):
// set only by the `eval` build tag (eval.go), after its preflight; nil in
// every ordinary build, where main never calls it.
type evalHook interface {
	// Run takes over once main has wired every service, before any window
	// or app exists, and returns the process's exit code.
	Run(ctx context.Context, d evalDeps) int
}

// evalDeps are the wired services the eval drives. conn is a cluster's
// connection when chat would accept it, and otherwise why not.
type evalDeps struct {
	chat *appbridge.ChatService
	conn func(name string) (appbridge.ChatConn, string)
}

var evalRun evalHook
