//go:build smoke

package main

import (
	"io/fs"
	"log"
	"os"
	"strconv"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/envread"
	smokepkg "github.com/moomora/klyx/internal/smoke"
)

// The native WKWebView smoke (The Line slice 12b, note §3): built only with
// `-tags smoke`, run with KLYX_SMOKE_OUT (the report), KLYX_SMOKE_THEME,
// KLYX_SMOKE_WIDTH/HEIGHT and KLYX_SMOKE_CLUSTER, and KLYX_CONFIG naming the
// lab's fleet config in a smoke-only directory.

func init() {
	// The confined build re-runs this binary as a child with an empty
	// environment (cachediff.RunChild, in main): the smoke stands aside, or
	// every render in a smoke build dies at start (found by AI A1's lab).
	if len(os.Args) == 2 && os.Args[1] == cachediff.ChildFlag {
		return
	}
	out := envread.Get(envread.KlyxSmokeOut)
	if out == "" {
		log.Fatal("the smoke build needs KLYX_SMOKE_OUT")
	}
	if envread.Get(envread.KlyxConfig) == "" {
		log.Fatal("the smoke build needs KLYX_CONFIG: the lab's fleet config, never the owner's")
	}
	atoi := func(k envread.Name, d int) int {
		if n, err := strconv.Atoi(envread.Get(k)); err == nil && n > 0 {
			return n
		}
		return d
	}
	dist, _ := fs.Sub(assets, "frontend/dist")
	smoke = &nativeSmoke{w: &smokepkg.Watchdog{Out: out, Settings: smokepkg.Settings{
		Theme:      envread.Get(envread.KlyxSmokeTheme),
		Width:      atoi(envread.KlyxSmokeWidth, 1440),
		Height:     atoi(envread.KlyxSmokeHeight, 900),
		Cluster:    envread.Get(envread.KlyxSmokeCluster),
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

// Progress records (and logs, with its time) the step the runner begins.
func (s *SmokeService) Progress(name string) {
	log.Printf("smoke step: %s", name)
	s.w.Progress(name)
}

// Report writes the runner's result and quits.
func (s *SmokeService) Report(report string) error { return s.w.Report(report) }
