package fleetindex

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var cmGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

func item(i int, annotation int) string {
	return fmt.Sprintf(`{"kind":"PartialObjectMetadata","apiVersion":"meta.k8s.io/v1","metadata":{"name":"cm-%d","namespace":"ns-%d","uid":"uid-%d","resourceVersion":"%d","creationTimestamp":"2026-10-15T10:00:00Z","labels":{"app":"a%d"},"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"%s"},"managedFields":[{"manager":"kubectl","operation":"Apply"}]}}`,
		i, i%3, i, 100+i, i, strings.Repeat("x", annotation))
}

// listServer serves n items in pages of PageSize, each item carrying an
// annotation of annotation bytes, written as it goes.
func listServer(t *testing.T, n, annotation int, accepts *atomic.Value) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepts.Store(r.Header.Get("Accept"))
		start := 0
		if c := r.URL.Query().Get("continue"); c != "" {
			fmt.Sscanf(c, "p%d", &start)
		}
		if r.URL.Query().Get("limit") != "500" {
			t.Errorf("limit %q", r.URL.Query().Get("limit"))
		}
		end := min(start+PageSize, n)
		next := ""
		if end < n {
			next = fmt.Sprintf("p%d", end)
		}
		w.Header().Set("Content-Type", "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1")
		fmt.Fprintf(w, `{"kind":"PartialObjectMetadataList","apiVersion":"meta.k8s.io/v1","metadata":{"resourceVersion":"77","continue":%q},"items":[`, next)
		for i := start; i < end; i++ {
			if i > start {
				w.Write([]byte(","))
			}
			w.Write([]byte(item(i, annotation)))
		}
		w.Write([]byte("]}"))
	}))
}

func TestListPageStreamsMetadataOnly(t *testing.T) {
	var accepts atomic.Value
	srv := listServer(t, 1200, 10, &accepts)
	defer srv.Close()
	r := &Reader{Client: srv.Client(), Host: srv.URL}
	var got []Source
	cont, pages := "", 0
	for {
		rv, next, err := r.ListPage(context.Background(), cmGVR, cont, func(s Source) error { got = append(got, s); return nil })
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if rv != "77" {
			t.Fatalf("rv %q", rv)
		}
		if next == "" {
			break
		}
		cont = next
	}
	if pages != 3 || len(got) != 1200 {
		t.Fatalf("%d pages, %d items", pages, len(got))
	}
	s := got[1199]
	if s.UID != "uid-1199" || s.Name != "cm-1199" || s.Namespace != "ns-2" || len(s.kept) != 1 || s.kept[0] != (Label{"app", "a1199"}) || s.NoCreated || !s.Created.Equal(time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("source %+v", s)
	}
	a := accepts.Load().(string)
	for _, part := range strings.Split(a, ",") {
		if !strings.Contains(part, "as=PartialObjectMetadata") {
			t.Fatalf("the Accept header admits a content read: %q", a)
		}
	}
}

func TestReadsRefuseContent(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ctype  string
		body   string
		want   int
	}{
		{"denied", 403, "application/json", `{"kind":"Status","reason":"Forbidden","message":"configmaps is forbidden"}`, 403},
		{"unsupported", 406, "application/json", `{}`, 406},
		{"expired", 410, "application/json", `{"reason":"Expired"}`, 410},
		{"a content answer", 200, "application/json", `{"kind":"ConfigMapList","items":[{"metadata":{"name":"x"},"data":{"secret":"x"}}]}`, 406},
		{"items before the kind", 200, "application/json", `{"items":[{"kind":"ConfigMap","metadata":{"name":"x"},"data":{"k":"v"}}],"kind":"PartialObjectMetadataList"}`, 406},
		{"an item of another kind", 200, "application/json", `{"kind":"PartialObjectMetadataList","items":[{"kind":"ConfigMap","metadata":{"name":"x"}}]}`, 406},
		{"an item without a kind", 200, "application/json", `{"kind":"PartialObjectMetadataList","items":[{"metadata":{"name":"x"}}]}`, 406},
		{"not json", 200, "application/vnd.kubernetes.protobuf", `k8s...`, 406},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", c.ctype)
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			}))
			defer srv.Close()
			r := &Reader{Client: srv.Client(), Host: srv.URL}
			applied := 0
			_, _, err := r.ListPage(context.Background(), cmGVR, "", func(Source) error { applied++; return nil })
			if codeOf(err) != c.want || applied != 0 {
				t.Fatalf("err %v (code %d), applied %d", err, codeOf(err), applied)
			}
		})
	}
}

func TestWatchAppliesEventsInOrder(t *testing.T) {
	var accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		if r.URL.Query().Get("resourceVersion") != "77" || r.URL.Query().Get("watch") != "1" {
			t.Errorf("query %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", acceptWatch)
		fmt.Fprintf(w, `{"type":"ADDED","object":%s}`+"\n", item(1, 5))
		fmt.Fprintf(w, `{"type":"BOOKMARK","object":{"kind":"PartialObjectMetadata","metadata":{"resourceVersion":"150"}}}`+"\n")
		fmt.Fprintf(w, `{"type":"MODIFIED","object":%s}`+"\n", item(2, 5))
		fmt.Fprintf(w, `{"type":"DELETED","object":%s}`+"\n", item(1, 5))
	}))
	defer srv.Close()
	r := &Reader{Client: srv.Client(), Host: srv.URL}
	var evs []string
	rv, err := r.Watch(context.Background(), cmGVR, "77", time.Minute, nil, func(ev string, s Source) error { evs = append(evs, ev+" "+s.Name); return nil })
	if err != nil || rv != "101" {
		t.Fatalf("rv %q err %v", rv, err)
	}
	if strings.Join(evs, ",") != "ADDED cm-1,MODIFIED cm-2,DELETED cm-1" {
		t.Fatalf("events %v", evs)
	}
	if strings.Contains(strings.ReplaceAll(accept, "as=PartialObjectMetadata", ""), "application/json,") || accept != acceptWatch {
		t.Fatalf("accept %q", accept)
	}
}

func TestWatchErrorEventAndCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", acceptWatch)
		fmt.Fprintf(w, `{"type":"ERROR","object":{"kind":"Status","code":410,"reason":"Expired","message":"too old resource version"}}`+"\n")
	}))
	defer srv.Close()
	r := &Reader{Client: srv.Client(), Host: srv.URL}
	if _, err := r.Watch(context.Background(), cmGVR, "1", time.Minute, nil, func(string, Source) error { return nil }); codeOf(err) != 410 {
		t.Fatalf("err %v", err)
	}
	// a cancelled context ends an open watch at once
	hold := make(chan struct{})
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", acceptWatch)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	defer open.Close()
	defer close(hold)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (&Reader{Client: open.Client(), Host: open.URL}).Watch(ctx, cmGVR, "1", time.Minute, nil, func(string, Source) error { return nil })
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not stop on cancel")
	}
}

// §2.5 and §5: a paginated list of large objects never holds a page: heap
// growth while it is read stays within the entries kept plus one object's
// decode, whatever the page's size.
func TestListPeakMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 100 MiB")
	}
	const n, annotation = 1000, 100 << 10 // 1,000 objects of 100 KiB: a page is ~50 MiB
	var accepts atomic.Value
	srv := listServer(t, n, annotation, &accepts)
	defer srv.Close()
	r := &Reader{Client: srv.Client(), Host: srv.URL}
	base := heapNow()
	var peak uint64
	var kept []Source
	cont := ""
	for {
		_, next, err := r.ListPage(context.Background(), cmGVR, cont, func(s Source) error {
			kept = append(kept, s)
			if len(kept)%50 == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapInuse)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if next == "" {
			break
		}
		cont = next
	}
	growth := int64(peak) - int64(base)
	t.Logf("peak heap growth %d KiB for %d objects of %d KiB", growth>>10, n, annotation>>10)
	// a page (~50 MiB) held whole would show; one object plus the decoder's
	// buffer and the GC's slack must not
	if growth > 16<<20 {
		t.Fatalf("heap grew %d MiB while streaming", growth>>20)
	}
	runtime.KeepAlive(kept)
}

// §5 (re-review of 13417b2): eight open watches each deliver one object
// with a 1 MiB annotation and 10 MiB of managedFields, then many small
// events. With the large values gone and the watches still open, the heap
// holds no more than the kept entries plus the charged stream reserves -
// nothing grew with an object's size and stayed.
func TestWatchesRetainNoLargeBuffers(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 88 MiB")
	}
	const watches, small = 8, 1000
	var big bytes.Buffer
	big.WriteString(`{"type":"ADDED","object":{"kind":"PartialObjectMetadata","metadata":{"name":"big","namespace":"ns","uid":"big-uid","resourceVersion":"1","annotations":{"a":"`)
	big.WriteString(strings.Repeat("x", 1<<20))
	big.WriteString(`"},"managedFields":[`)
	for big.Len() < 11<<20 {
		big.WriteString(`{"manager":"m","fieldsV1":{"f:spec":{"f:x":{}}}},`)
	}
	big.WriteString(`{}]}}}` + "\n")
	bigEvent := big.Bytes()
	smallEvent := func(i int) string {
		return fmt.Sprintf(`{"type":"MODIFIED","object":{"kind":"PartialObjectMetadata","metadata":{"name":"s%d","namespace":"ns","uid":"u%d","resourceVersion":"%d","labels":{"app":"web"}}}}`+"\n", i, i, i+2)
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", acceptWatch)
		w.Write(bigEvent)
		for i := 0; i < small; i++ {
			io.WriteString(w, smallEvent(i))
		}
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	r := &Reader{Client: srv.Client(), Host: srv.URL}

	base := heapNow()
	var mu sync.Mutex
	var kept []*Entry
	var charged int64
	var applied sync.WaitGroup
	applied.Add(watches)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, watches)
	for w := 0; w < watches; w++ {
		go func() {
			n := 0
			r.Watch(ctx, cmGVR, "0", time.Minute, nil, func(_ string, s Source) error {
				size := estimate(s)
				e := build(s, s.labelsHash(), size)
				mu.Lock()
				kept = append(kept, e)
				charged += size
				mu.Unlock()
				if n++; n == small+1 {
					applied.Done()
				}
				return nil
			})
			done <- struct{}{}
		}()
	}
	applied.Wait()
	// every watch is open, waiting for its next event; the large values
	// are gone
	after := heapNow()
	growth := int64(after) - int64(base)
	limit := charged + watches*StreamReserve
	t.Logf("heap growth %d KiB with %d watches open; entries charged %d KiB, reserves %d KiB", growth>>10, watches, charged>>10, (watches*StreamReserve)>>10)
	if growth > limit {
		t.Fatalf("retained %d KiB > charged %d KiB", growth>>10, limit>>10)
	}
	cancel()
	close(release)
	for w := 0; w < watches; w++ {
		<-done
	}
	runtime.KeepAlive(kept)
	runtime.KeepAlive(bigEvent)
}
