package fleetindex

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

func src(i int, labels int) Source {
	s := Source{UID: fmt.Sprintf("0b6c1a2e-5f3d-4c1e-9a7b-%012d", i), Namespace: fmt.Sprintf("team-%d", i%40), Name: fmt.Sprintf("web-%d-7d9f8c6b5-x2k4p", i), Created: time.Unix(int64(1792000000+i), 0)}
	if labels > 0 {
		s.Labels = map[string]string{}
		for j := 0; j < labels; j++ {
			s.Labels[fmt.Sprintf("app.kubernetes.io/part-%d", j)] = fmt.Sprintf("value-%d-%d", i, j)
		}
	}
	return s
}

// The labels cap: at most LabelBytes of keys plus values, kept in key order,
// with the truncation marked.
func TestLabelsCapped(t *testing.T) {
	s := Source{UID: "u", Name: "n", Labels: map[string]string{}}
	for i := 0; i < 100; i++ {
		s.Labels[fmt.Sprintf("k%03d", i)] = strings.Repeat("v", 40)
	}
	e := build(s, labelHash(s.Labels), estimate(s))
	n := 0
	for i, l := range e.Labels {
		n += len(l.Key) + len(l.Value)
		if i > 0 && e.Labels[i-1].Key >= l.Key {
			t.Fatal("labels kept in key order")
		}
	}
	if !e.LabelsTruncated || n > LabelBytes || len(e.Labels) != LabelBytes/44 {
		t.Fatalf("kept %d labels, %d bytes, truncated %v", len(e.Labels), n, e.LabelsTruncated)
	}
	small := build(src(1, 3), 0, 0)
	if small.LabelsTruncated || len(small.Labels) != 3 {
		t.Fatal("under the cap every label is kept")
	}
	if e := build(Source{UID: "u", Labels: map[string]string{"a": "b"}, NoLabels: true}, 0, 0); e.Labels != nil {
		t.Fatal("a source that keeps no labels has none")
	}
}

// An entry's comparison sees every field it keeps, and label order never
// matters.
func TestSameAndLabelHash(t *testing.T) {
	s := src(7, 4)
	e := build(s, labelHash(s.Labels), estimate(s))
	if !e.same(s, labelHash(s.Labels)) {
		t.Fatal("an entry is the same as its source")
	}
	grown := src(7, 5)
	if e.same(grown, labelHash(grown.Labels)) {
		t.Fatal("a grown label set differs")
	}
	if labelHash(map[string]string{"a": "b", "c": "d"}) != labelHash(map[string]string{"c": "d", "a": "b"}) {
		t.Fatal("order-independent")
	}
	if labelHash(map[string]string{"ab": "c"}) == labelHash(map[string]string{"a": "bc"}) {
		t.Fatal("a key and value boundary is part of the hash")
	}
	if n := testing.AllocsPerRun(100, func() { _ = e.same(s, labelHash(s.Labels)) }); n != 0 {
		t.Fatalf("comparison allocates %v", n)
	}
	if estimate(Source{UID: "u", Labels: map[string]string{"a": "b"}, NoLabels: true}) != entryOverhead+1 {
		t.Fatal("a source without labels is charged none")
	}
}

// §2.6: the estimate is conservative - the heap an entry keeps after GC,
// in a store shaped like the index's, never exceeds what it is charged.
func TestEstimateCoversRetainedHeap(t *testing.T) {
	for _, labels := range []int{0, 1, 3, 12, 40} {
		t.Run(fmt.Sprintf("labels=%d", labels), func(t *testing.T) {
			const n = 20000
			var charged int64 = kindOverhead
			before := heap()
			store := make(map[string]*Entry)
			for i := 0; i < n; i++ {
				// fresh strings per source, as a decoded object's are its own;
				// the source's label map is garbage once the entry is built
				s := src(i, labels)
				size := estimate(s)
				charged += size
				store[s.UID] = build(s, labelHash(s.Labels), size)
			}
			after := heap()
			runtime.KeepAlive(store)
			used := int64(after) - int64(before)
			t.Logf("labels=%d: retained %d B/entry, charged %d B/entry", labels, used/n, charged/n)
			if used > charged {
				t.Fatalf("retained %d > charged %d", used, charged)
			}
			if charged > 3*used {
				t.Fatalf("the estimate is far from the heap: charged %d, retained %d", charged, used)
			}
		})
	}
}

func heap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}
