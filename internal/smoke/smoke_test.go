package smoke

import (
	"encoding/json"
	"go/build"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// The Line slice 12b: the watchdog writes one result - the runner's, or an
// incomplete one of its own when the runner never starts or never reports.

type clock struct{ start, report chan time.Time }

func rig(t *testing.T) (*Watchdog, *clock, *atomic.Int32, string) {
	out := filepath.Join(t.TempDir(), "report.json")
	c := &clock{start: make(chan time.Time, 1), report: make(chan time.Time, 1)}
	quits := &atomic.Int32{}
	w := &Watchdog{Out: out, Settings: Settings{Theme: "night", Provenance: Provenance{Commit: "abc"}}, Quit: func() { quits.Add(1) },
		After: func(d time.Duration) <-chan time.Time {
			if d == StartWithin {
				return c.start
			}
			return c.report
		}}
	return w, c, quits, out
}

func result(t *testing.T, out string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, err := os.ReadFile(out)
		if err == nil {
			var r map[string]any
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			return r
		}
		if time.Now().After(deadline) {
			t.Fatal("no result written")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestARunnerThatNeverStartsIsIncomplete(t *testing.T) {
	w, c, quits, out := rig(t)
	w.Arm()
	c.start <- time.Now()
	r := result(t, out)
	if r["result"] != Incomplete || r["why"] != "the runner did not start within 30s" {
		t.Fatalf("%v", r)
	}
	if quits.Load() != 1 {
		t.Fatal("did not quit")
	}
}

func TestARunnerThatNeverReportsIsIncomplete(t *testing.T) {
	w, c, _, out := rig(t)
	w.Arm()
	w.Started()
	c.report <- time.Now()
	if r := result(t, out); r["result"] != Incomplete || r["why"] != "the runner did not report within 5m0s" {
		t.Fatalf("%v", r)
	}
}

func TestTheFirstResultIsTheOnlyOne(t *testing.T) {
	w, c, quits, out := rig(t)
	w.Arm()
	w.Started()
	if err := w.Report(`{"result":"fail","steps":[{"name":"lens Pods","ok":false,"why":"console error"}]}`); err != nil {
		t.Fatal(err)
	}
	c.report <- time.Now() // the watchdog firing later changes nothing
	time.Sleep(20 * time.Millisecond)
	r := result(t, out)
	if r["result"] != Fail || r["provenance"].(map[string]any)["commit"] != "abc" || quits.Load() != 1 {
		t.Fatalf("%v quits %d", r, quits.Load())
	}
}

func TestAReportWithoutAResultNeverPasses(t *testing.T) {
	for _, report := range []string{`not json`, `null`, `[]`, `{"steps":[]}`, `{"result":"green"}`} {
		w, _, _, out := rig(t)
		w.Arm()
		w.Started()
		_ = w.Report(report)
		if r := result(t, out); r["result"] != Incomplete {
			t.Fatalf("%q: %v", report, r)
		}
	}
}

func TestProvenanceHashesTheAssets(t *testing.T) {
	a := fstest.MapFS{"index.html": {Data: []byte("a")}, "assets/x.js": {Data: []byte("b")}}
	b := fstest.MapFS{"index.html": {Data: []byte("a")}, "assets/x.js": {Data: []byte("c")}}
	if hashFS(a) == hashFS(b) || hashFS(a) != hashFS(fstest.MapFS{"assets/x.js": {Data: []byte("b")}, "index.html": {Data: []byte("a")}}) {
		t.Fatal("the hash must follow content and not order")
	}
}

// The smoke hook and its service exist only under the tag: an ordinary build
// of the app never compiles smoke.go.
func TestTheSmokeFileIsOnlyInTheSmokeBuild(t *testing.T) {
	dir, err := filepath.Abs("../../cmd/klyx")
	if err != nil {
		t.Fatal(err)
	}
	ctx := build.Default
	ctx.BuildTags = nil
	plain, err := ctx.ImportDir(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx.BuildTags = []string{"smoke"}
	tagged, err := ctx.ImportDir(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plain.GoFiles, "smoke.go") || !slices.Contains(tagged.GoFiles, "smoke.go") {
		t.Fatalf("plain %v tagged %v", plain.GoFiles, tagged.GoFiles)
	}
}
