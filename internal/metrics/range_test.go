package metrics

import (
	"context"
	"testing"
	"time"
)

// fakeRangeQuerier implements Querier + RangeQuerier with canned responses and
// records the last range call.
type fakeRangeQuerier struct {
	fakeQuerier
	lastStart, lastEnd time.Time
	lastStep           time.Duration
}

func (f *fakeRangeQuerier) RangeQuery(_ context.Context, q string, start, end time.Time, step time.Duration) (int, []byte, error) {
	f.lastQ = q
	f.lastStart, f.lastEnd, f.lastStep = start, end, step
	return f.status, []byte(f.body), f.err
}

var rangeWindow = struct {
	start, end time.Time
	step       time.Duration
}{time.Unix(1000, 0), time.Unix(2800, 0), 60 * time.Second}

func runRange(t *testing.T, body string) ([]Point, error) {
	t.Helper()
	c := NewClient(&fakeRangeQuerier{fakeQuerier: fakeQuerier{status: 200, body: body}})
	return c.RangeSeries(context.Background(), "q", rangeWindow.start, rangeWindow.end, rangeWindow.step)
}

func TestRangeSeries(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{},"values":[[1000,"0.5"],[1060,"NaN"],[1120,"0.75"],[1180,"+Inf"],[1240,"1"]]}
	]}}`
	out, err := runRange(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// NaN and +Inf points are skipped — gaps stay gaps.
	want := []Point{{1000, 0.5}, {1120, 0.75}, {1240, 1}}
	if len(out) != len(want) {
		t.Fatalf("want %d points, got %d: %+v", len(want), len(out), out)
	}
	for i, w := range want {
		if out[i] != w {
			t.Fatalf("point %d: want %+v, got %+v", i, w, out[i])
		}
	}
}

func TestRangeSeriesEmptyMatrix(t *testing.T) {
	out, err := runRange(t, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	if err != nil || len(out) != 0 {
		t.Fatalf("empty matrix: want 0/nil, got %d/%v", len(out), err)
	}
}

func TestRangeSeriesErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"multi-series", `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"a":"1"},"values":[[1000,"1"]]},
			{"metric":{"a":"2"},"values":[[1000,"2"]]}
		]}}`},
		{"non-matrix", `{"status":"success","data":{"resultType":"vector","result":[]}}`},
		{"prom-error", `{"status":"error","error":"boom"}`},
		{"not-prom", `<html>gateway</html>`},
	}
	for _, tc := range cases {
		if _, err := runRange(t, tc.body); err == nil {
			t.Errorf("%s: want error, got nil", tc.name)
		}
	}
	// HTTP failure status.
	c := NewClient(&fakeRangeQuerier{fakeQuerier: fakeQuerier{status: 503, body: "down"}})
	if _, err := c.RangeSeries(context.Background(), "q", rangeWindow.start, rangeWindow.end, rangeWindow.step); err == nil {
		t.Error("want error on HTTP 503")
	}
}

func TestRangeSeriesUnsupportedTransport(t *testing.T) {
	// A Querier without RangeQuery must produce a clear error, not a panic.
	c := NewClient(&fakeQuerier{status: 200, body: "{}"})
	if _, err := c.RangeSeries(context.Background(), "q", rangeWindow.start, rangeWindow.end, rangeWindow.step); err == nil {
		t.Fatal("want error for transport without range support")
	}
}

func TestRangeSeriesPassesWindowThrough(t *testing.T) {
	fq := &fakeRangeQuerier{fakeQuerier: fakeQuerier{status: 200, body: `{"status":"success","data":{"resultType":"matrix","result":[]}}`}}
	c := NewClient(fq)
	if _, err := c.RangeSeries(context.Background(), "my-query", rangeWindow.start, rangeWindow.end, rangeWindow.step); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fq.lastQ != "my-query" || !fq.lastStart.Equal(rangeWindow.start) || !fq.lastEnd.Equal(rangeWindow.end) || fq.lastStep != rangeWindow.step {
		t.Fatalf("window not passed through: q=%q start=%v end=%v step=%v", fq.lastQ, fq.lastStart, fq.lastEnd, fq.lastStep)
	}
}

func TestRangeMatrixKeepsEverySeries(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"envoy_cluster_name":"httproute/a/web/rule/0"},"values":[[1000,"2"],[1060,"NaN"],[1120,"3"]]},
		{"metric":{"envoy_cluster_name":"httproute/a/web/rule/1"},"values":[]}
	]}}`
	c := NewClient(&fakeRangeQuerier{fakeQuerier: fakeQuerier{status: 200, body: body}})
	out, err := c.RangeMatrix(context.Background(), "q", rangeWindow.start, rangeWindow.end, rangeWindow.step)
	if err != nil || len(out) != 2 {
		t.Fatalf("got %+v %v", out, err)
	}
	if out[0].Labels["envoy_cluster_name"] != "httproute/a/web/rule/0" || len(out[0].Points) != 2 || out[0].Points[1] != (Point{1120, 3}) {
		t.Fatalf("first series: %+v", out[0])
	}
	if len(out[1].Points) != 0 {
		t.Fatalf("second series: %+v", out[1])
	}
	if _, err := c.RangeSeries(context.Background(), "q", rangeWindow.start, rangeWindow.end, rangeWindow.step); err == nil {
		t.Fatal("RangeSeries accepted two series")
	}
}
