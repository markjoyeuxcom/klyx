// Package smoke holds the native WKWebView smoke's Go side (The Line slice
// 12b, note §3): the result it writes, the watchdog that writes a failure
// without depending on the page, and the build's provenance. It is wired
// only by the `smoke` build tag (cmd/klyx/smoke.go); ordinary builds never
// reference it.
package smoke

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

// Deadlines (note §3.3): the runner must start within StartWithin of the
// window opening, and report within ReportWithin.
const (
	StartWithin  = 30 * time.Second
	ReportWithin = 5 * time.Minute
)

// Results.
const (
	Pass       = "pass"
	Fail       = "fail"
	Incomplete = "incomplete"
)

// Provenance names what was run: the commit, whether the tree was clean, and
// the embedded frontend assets' hash.
type Provenance struct {
	Commit    string `json:"commit"`
	Dirty     bool   `json:"dirty"`
	AssetHash string `json:"assetHash"`
}

// Settings are what the runner is told: the run's theme and window, and the
// provenance it reports back.
type Settings struct {
	Theme      string     `json:"theme"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	Cluster    string     `json:"cluster"`
	Provenance Provenance `json:"provenance"`
}

// Watchdog writes exactly one result: the runner's report, or - if the
// runner never starts, or never reports - an incomplete result of its own,
// then quits. It never waits on the page to fail.
type Watchdog struct {
	Out      string
	Settings Settings
	Quit     func()
	After    func(time.Duration) <-chan time.Time // a test seam; time.After by default

	mu      sync.Mutex
	started chan struct{}
	done    bool
}

// Arm starts the watchdog; call once, when the window opens.
func (w *Watchdog) Arm() {
	if w.After == nil {
		w.After = time.After
	}
	w.mu.Lock()
	w.started = make(chan struct{})
	w.mu.Unlock()
	start, report := w.After(StartWithin), w.After(ReportWithin)
	go func() {
		select {
		case <-w.started:
		case <-start:
			w.fail("the runner did not start within " + StartWithin.String())
			return
		}
		<-report
		w.fail("the runner did not report within " + ReportWithin.String())
	}()
}

// Started records that the runner began.
func (w *Watchdog) Started() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started != nil {
		select {
		case <-w.started:
		default:
			close(w.started)
		}
	}
}

// Report writes the runner's report as the result and quits. A report that
// is not JSON, or names no result, is written as incomplete: it never passes.
func (w *Watchdog) Report(report string) error {
	var r map[string]any
	if err := json.Unmarshal([]byte(report), &r); err != nil {
		return w.write(map[string]any{"result": Incomplete, "why": "the report is not JSON: " + err.Error()})
	}
	switch r["result"] {
	case Pass, Fail, Incomplete:
	default:
		r["result"] = Incomplete
		r["why"] = "the report names no result"
	}
	return w.write(r)
}

func (w *Watchdog) fail(why string) { _ = w.write(map[string]any{"result": Incomplete, "why": why}) }

// write writes the first result only, with the provenance, then quits.
func (w *Watchdog) write(r map[string]any) error {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return nil
	}
	w.done = true
	w.mu.Unlock()
	r["provenance"] = w.Settings.Provenance
	r["settings"] = w.Settings
	b, err := json.MarshalIndent(r, "", " ")
	if err == nil {
		tmp := w.Out + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, w.Out)
		}
	}
	if w.Quit != nil {
		w.Quit()
	}
	return err
}

// BuildProvenance reads the commit from the build's VCS stamp and hashes the
// embedded frontend assets.
func BuildProvenance(assets fs.FS) Provenance {
	p := Provenance{Commit: "unknown"}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				p.Commit = s.Value
			case "vcs.modified":
				p.Dirty = s.Value == "true"
			}
		}
	}
	p.AssetHash = hashFS(assets)
	return p
}

// hashFS hashes every file's path and content, in path order.
func hashFS(fsys fs.FS) string {
	if fsys == nil {
		return ""
	}
	var paths []string
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(p), len(b)) // a hash never fails to write
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}
