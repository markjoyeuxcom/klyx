package main

import (
	"context"

	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/config"
)

// evalHook is the AI eval's (docs/superpowers/specs/2026-10-22-klyx-ai-eval.md):
// set only by the `eval` build tag (eval.go), after its preflight; nil in
// every ordinary build, where main never calls it.
type evalHook interface {
	// Config is the configuration the preflight checked: main runs on it
	// instead of reading the file again.
	Config() *config.Config
	// Resolve is the fleet's connection: the preflight's resolved REST
	// config for the lab cluster, and a refusal for any other.
	Resolve(cc config.ClusterConfig) (*rest.Config, error)
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
