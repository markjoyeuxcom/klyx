package hubble

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/metrics"
)

const emptyVec = `{"status":"success","data":{"resultType":"vector","result":[]}}`

func vec(elems ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(elems, ",") + `]}}`
}

func el(labels, val string) string {
	return `{"metric":{` + labels + `},"value":[1,"` + val + `"]}`
}

// scriptedQ answers each metric-set query by name-match on the promql.
type scriptedQ struct {
	flows, drops, dns, http string
	err                     error
}

func (q *scriptedQ) InstantQuery(_ context.Context, promql string) (int, []byte, error) {
	if q.err != nil {
		return 0, nil, q.err
	}
	pick := func(s string) (int, []byte, error) {
		if s == "" {
			s = emptyVec
		}
		return 200, []byte(s), nil
	}
	switch {
	case strings.Contains(promql, mFlows):
		return pick(q.flows)
	case strings.Contains(promql, mDrops):
		return pick(q.drops)
	case strings.Contains(promql, mDNS):
		return pick(q.dns)
	case strings.Contains(promql, mHTTP):
		return pick(q.http)
	}
	return pick("")
}

func (q *scriptedQ) RangeQuery(context.Context, string, time.Time, time.Time, time.Duration) (int, []byte, error) {
	return 200, []byte(emptyVec), nil
}

func TestQueryFlowStats_FullReadout(t *testing.T) {
	q := &scriptedQ{
		flows: vec(el(`"verdict":"FORWARDED"`, "120.5"), el(`"verdict":"DROPPED"`, "3.25")),
		drops: vec(
			el(`"reason":"Policy denied","protocol":"TCP"`, "3.0"),
			el(`"reason":"Invalid destination mac","protocol":"UDP"`, "0.25"),
			el(`"reason":"Stale reason","protocol":"TCP"`, "0"), // zero-rate: settled history, dropped
		),
		dns:  vec(el(`"rcode":"No Error"`, "50"), el(`"rcode":"Non-Existent Domain"`, "2.5")),
		http: vec(el(`"status":"200"`, "80"), el(`"status":"503"`, "1.5")),
	}
	stats, st, err := NewSource(metrics.NewClient(q)).QueryFlowStats(context.Background())
	if err != nil || !st.Available {
		t.Fatalf("want available, got %+v (%v)", st, err)
	}
	if stats.ForwardRate == nil || *stats.ForwardRate != 120.5 || stats.DropRate == nil || *stats.DropRate != 3.25 {
		t.Fatalf("verdict rates: %+v", stats)
	}
	// Reasons sorted by rate desc; the zero-rate reason excluded.
	if len(stats.DropReasons) != 2 || stats.DropReasons[0].Reason != "Policy denied" || stats.DropReasons[0].Rate != 3.0 {
		t.Fatalf("drop reasons: %+v", stats.DropReasons)
	}
	if stats.DNSTotalRate == nil || *stats.DNSTotalRate != 52.5 || *stats.DNSErrorRate != 2.5 {
		t.Fatalf("dns: %+v", stats)
	}
	if stats.HTTPTotalRate == nil || *stats.HTTPTotalRate != 81.5 || *stats.HTTPErrorRate != 1.5 {
		t.Fatalf("http: %+v", stats)
	}
}

func TestQueryFlowStats_OptInSectionsStayNil(t *testing.T) {
	// Only the flow metric set is enabled: dns/http/drop sections stay nil/empty
	// (never fabricated zeros).
	q := &scriptedQ{flows: vec(el(`"verdict":"FORWARDED"`, "10"))}
	stats, st, err := NewSource(metrics.NewClient(q)).QueryFlowStats(context.Background())
	if err != nil || !st.Available {
		t.Fatalf("want available: %v %+v", err, st)
	}
	if stats.DNSTotalRate != nil || stats.HTTPTotalRate != nil || len(stats.DropReasons) != 0 {
		t.Fatalf("disabled sections must stay nil: %+v", stats)
	}
	if stats.DropRate != nil {
		t.Fatalf("no DROPPED series -> nil drop rate, got %+v", stats.DropRate)
	}
}

func TestQueryFlowStats_NothingEnabledSaysHow(t *testing.T) {
	stats, st, err := NewSource(metrics.NewClient(&scriptedQ{})).QueryFlowStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Empty() || !st.Available {
		t.Fatalf("want empty+available, got %+v %+v", stats, st)
	}
	if !strings.Contains(st.Message, "hubble.metrics") {
		t.Fatalf("the empty state must say HOW to enable: %q", st.Message)
	}
}

func TestQueryFlowStats_TransportErrorIsDegradedNotEmpty(t *testing.T) {
	_, _, err := NewSource(metrics.NewClient(&scriptedQ{err: errors.New("connection refused")})).QueryFlowStats(context.Background())
	if err == nil {
		t.Fatal("a transport failure must error, never render as empty")
	}
}
