package observe

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
)

// Slice 2b §7: the cost of the ledger on a large unsubscribed part -
// capture, a synthetic evaluator emitting one role per object, membership,
// diff and emission - for a one-object change and a full UID turnover.
//
//	go test ./internal/observe -run '^$' -bench Ledger -benchmem

type discardSink struct{}

func (discardSink) Summary(Summary)             {}
func (discardSink) Rows(Rows)                   {}
func (discardSink) Transitions(TransitionBatch) {}

func benchModel(n int, prefix string) map[string]any {
	m := make(map[string]any, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s%d", prefix, i)
		m["ns/"+name] = &cmObj{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name)}}
	}
	return m
}

func benchObserver(b *testing.B, n int) (*Observer, *partState, *kindState) {
	b.Helper()
	clk := testingclock.NewFakeClock(t0)
	o := New(Config{Cluster: "bench", Clock: clk, Sink: discardSink{}, RoleCap: 100000,
		Kinds:   []KindSpec{ledgerKind("pods", newSource())},
		Domains: []DomainSpec{{ID: "pods", Parts: []PartSpec{ledgerPart("pods", []string{"pods"})}}}})
	ks := o.kinds["pods"]
	ks.refs, ks.probed, ks.served = 1, true, true
	ks.gen = &kindGen{o: o, ks: ks, id: 1, cancel: func() {}, fresh: freshness{cov: Live}, model: benchModel(n, "p")}
	p := o.domains[0].parts[0]
	o.evaluate(p, clk.Now(), false) // the baseline
	return o, p, ks
}

func benchOneChange(b *testing.B, n int) {
	o, p, ks := benchObserver(b, n)
	obj := ks.gen.model["ns/p0"].(*cmObj)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		obj.Bad = !obj.Bad // one role flips per evaluation
		o.evaluate(p, o.clk.Now(), false)
	}
}

func benchTurnover(b *testing.B, n int) {
	o, p, ks := benchObserver(b, n)
	a, z := ks.gen.model, benchModel(n, "q")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			ks.gen.model = z
		} else {
			ks.gen.model = a
		}
		o.evaluate(p, o.clk.Now(), false) // 2n items: every key gone plus every key new
	}
}

func BenchmarkLedgerOneChange10k(b *testing.B) { benchOneChange(b, 10000) }
func BenchmarkLedgerOneChange50k(b *testing.B) { benchOneChange(b, 50000) }
func BenchmarkLedgerTurnover10k(b *testing.B)  { benchTurnover(b, 10000) }
func BenchmarkLedgerTurnover50k(b *testing.B)  { benchTurnover(b, 50000) }

// The same part without a ledger: the baseline cost the ledger adds to.
func BenchmarkNoLedgerOneChange50k(b *testing.B) {
	clk := testingclock.NewFakeClock(t0)
	part := ledgerPart("pods", []string{"pods"})
	part.Ledger = false
	o := New(Config{Cluster: "bench", Clock: clk, Sink: discardSink{},
		Kinds:   []KindSpec{ledgerKind("pods", newSource())},
		Domains: []DomainSpec{{ID: "pods", Parts: []PartSpec{part}}}})
	ks := o.kinds["pods"]
	ks.refs, ks.probed, ks.served = 1, true, true
	ks.gen = &kindGen{o: o, ks: ks, id: 1, cancel: func() {}, fresh: freshness{cov: Live}, model: benchModel(50000, "p")}
	p := o.domains[0].parts[0]
	obj := ks.gen.model["ns/p0"].(*cmObj)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		obj.Bad = !obj.Bad
		o.evaluate(p, clk.Now(), false)
	}
}
