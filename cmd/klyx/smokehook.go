package main

import "github.com/wailsapp/wails/v3/pkg/application"

// smokeHook is the native smoke's (The Line slice 12b): set only by the
// `smoke` build tag (smoke.go), nil in every ordinary build, where main never
// changes its identity, registers no smoke service and opens its window as
// usual.
type smokeHook interface {
	// UniqueID is the single-instance key: the smoke runs as its own app, so
	// it starts and reports while the owner's Klyx is open.
	UniqueID() string
	// Service is the smoke-only bound service the runner reports through.
	Service() application.Service
	// Window adjusts the main window: hidden, sized, the post-load handshake.
	Window(*application.WebviewWindowOptions)
	// Start arms the watchdog once the app exists.
	Start(*application.App)
}

var smoke smokeHook
