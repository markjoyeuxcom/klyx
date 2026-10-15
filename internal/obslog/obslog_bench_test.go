package obslog

import (
	"fmt"
	"testing"

	"github.com/moomora/klyx/internal/observe"
)

// The log's cost on a full log (review of a0c5cf9): a small batch and the
// largest supported batch (a full turnover of 50,000 keys, 100,000 items).
//
//	go test ./internal/obslog -run '^$' -bench Apply -benchmem

func fullLog() *Log {
	l := newLog()
	for i := 0; i < MaxEntries; i++ {
		l.Apply(batch(observe.BatchChanges, 0, item(fmt.Sprint("f", i), "ok", "ok")))
	}
	return l
}

func items(n int, prefix, from, to string) []observe.Transition {
	its := make([]observe.Transition, n)
	for i := range its {
		its[i] = item(fmt.Sprint(prefix, i), from, to)
	}
	return its
}

func BenchmarkApplyFullLog100(b *testing.B) {
	l := fullLog()
	ch := batch(observe.BatchChanges, 1, items(100, "h", "fault", "ok")...)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Apply(ch)
	}
}

func BenchmarkApplyFullLogTurnover50k(b *testing.B) {
	l := fullLog()
	turn := append(items(50000, "old", "fault", observe.StateGone), items(50000, "new", observe.StateAbsent, "fault")...)
	ch := batch(observe.BatchAfterGap, 1, turn...)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Apply(ch)
	}
}
