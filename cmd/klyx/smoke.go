//go:build smoke

package main

import (
	"io/fs"
	"log"
	"os"
	"strconv"

	"github.com/wailsapp/wails/v3/pkg/application"

	smokepkg "github.com/moomora/klyx/internal/smoke"
)

// The native WKWebView smoke (The Line slice 12b, note §3): built only with
// `-tags smoke`, run with KLYX_SMOKE_OUT (the report), KLYX_SMOKE_THEME,
// KLYX_SMOKE_WIDTH/HEIGHT and KLYX_SMOKE_CLUSTER, and KLYX_CONFIG naming the
// lab's fleet config in a smoke-only directory.

func init() {
	out := os.Getenv("KLYX_SMOKE_OUT")
	if out == "" {
		log.Fatal("the smoke build needs KLYX_SMOKE_OUT")
	}
	if os.Getenv("KLYX_CONFIG") == "" {
		log.Fatal("the smoke build needs KLYX_CONFIG: the lab's fleet config, never the owner's")
	}
	atoi := func(k string, d int) int {
		if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
			return n
		}
		return d
	}
	dist, _ := fs.Sub(assets, "frontend/dist")
	smoke = &nativeSmoke{w: &smokepkg.Watchdog{Out: out, Settings: smokepkg.Settings{
		Theme:      os.Getenv("KLYX_SMOKE_THEME"),
		Width:      atoi("KLYX_SMOKE_WIDTH", 1440),
		Height:     atoi("KLYX_SMOKE_HEIGHT", 900),
		Cluster:    os.Getenv("KLYX_SMOKE_CLUSTER"),
		Provenance: smokepkg.BuildProvenance(dist),
	}}}
}

type nativeSmoke struct{ w *smokepkg.Watchdog }

func (n *nativeSmoke) UniqueID() string { return "com.moomora.klyx.smoke" }

func (n *nativeSmoke) Service() application.Service {
	return application.NewService(&SmokeService{w: n.w})
}

// Window opens hidden at the run's size and, once the page has loaded (this
// Wails runs a window's JS after navigation finishes), raises the flag and
// the event the app shell starts the runner on - whichever it sees first.
func (n *nativeSmoke) Window(o *application.WebviewWindowOptions) {
	o.Hidden = true
	o.Width, o.Height = n.w.Settings.Width, n.w.Settings.Height
	o.JS = "window.__KLYX_SMOKE__ = true; window.dispatchEvent(new Event('klyx-smoke'));"
}

func (n *nativeSmoke) Start(app *application.App) {
	n.w.Quit = app.Quit
	n.w.Arm()
}

// SmokeService is what the runner reports through, by name
// (main.SmokeService.*): it exists only in the smoke build.
type SmokeService struct{ w *smokepkg.Watchdog }

// Settings are the run's theme, window, lab cluster and provenance.
func (s *SmokeService) Settings() smokepkg.Settings { return s.w.Settings }

// Started tells the watchdog the runner began.
func (s *SmokeService) Started() { s.w.Started() }

// Report writes the runner's result and quits.
func (s *SmokeService) Report(report string) error { return s.w.Report(report) }
