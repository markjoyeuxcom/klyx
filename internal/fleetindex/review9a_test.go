package fleetindex

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	certutil "k8s.io/client-go/util/cert"
)

// The design review of c03308d (slice 9a): each finding, as a regression.

// Finding 5: an expired continue restarts under a fresh mark, so what only
// the abandoned snapshot held is swept.
func TestExpiredContinueSweepsTheAbandonedSnapshot(t *testing.T) {
	var starts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("continue") != "" {
			w.WriteHeader(410)
			return
		}
		if starts.Add(1) == 1 {
			fmt.Fprintf(w, `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1","continue":"expired"},"items":[%s]}`, obj("gone", "ns", "old"))
			return
		}
		fmt.Fprint(w, `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"2"},"items":[]}`)
	}))
	defer srv.Close()
	ix := New(testSettings(refConfigMaps))
	defer ix.Stop()
	c := ix.coordFor("a")
	c.store.begin(1)
	if _, err := c.list(context.Background(), 1, &Reader{Client: srv.Client(), Host: srv.URL}, planned{ref: refConfigMaps}, cmGVR, make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if got := uids(c.store, "/configmaps"); len(got) != 0 {
		t.Fatalf("the abandoned snapshot's objects survived: %v", got)
	}
}

// Finding 6: an accepted watch after a refusal or an error is live again.
func TestWatchRecoveryRestoresLive(t *testing.T) {
	api := newFakeAPI(t)
	api.items["configmaps"] = []string{obj("one", "ns", "one")}
	api.watchStatus["configmaps"] = 403
	ix := New(testSettings(refConfigMaps))
	defer ix.Stop()
	ix.Attach("a", newFakeObs(1), api.acquire())
	ix.SetConnection("a", "connected")
	eventually(t, "watch denied", func() bool { return kindStatus(ix, "a", "/configmaps").State == StateWatchDenied })
	api.mu.Lock()
	api.watchStatus["configmaps"] = 0
	api.mu.Unlock()
	eventually(t, "recovered", func() bool { api.mu.Lock(); defer api.mu.Unlock(); return api.watching["configmaps"] == 1 })
	eventually(t, "live", func() bool { return kindStatus(ix, "a", "/configmaps").State == StateLive })
}

// Finding 7: coverage counts what the store holds, watch events included.
func TestCoverageCountsFollowWatchEvents(t *testing.T) {
	api := newFakeAPI(t)
	api.items["configmaps"] = []string{obj("one", "ns", "one")}
	ix := New(testSettings(refConfigMaps))
	defer ix.Stop()
	ix.Attach("a", newFakeObs(1), api.acquire())
	ix.SetConnection("a", "connected")
	eventually(t, "watching", func() bool { api.mu.Lock(); defer api.mu.Unlock(); return api.watching["configmaps"] == 1 })
	api.ch("configmaps") <- `{"type":"ADDED","object":` + obj("two", "ns", "two") + `}`
	eventually(t, "two", func() bool { return kindStatus(ix, "a", "/configmaps").Entries == 2 })
}

// Finding 8: only a complete, valid metadata list completes a read; a
// skipped value is validated as it streams.
func TestInvalidResponsesNeverCompleteARead(t *testing.T) {
	item := func(meta string) string {
		return `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1"},"items":[{"kind":"PartialObjectMetadata","metadata":` + meta + `}]}`
	}
	for name, doc := range map[string]string{
		"empty object":          `{}`,
		"no items":              `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1"}}`,
		"no resourceVersion":    `{"kind":"PartialObjectMetadataList","metadata":{},"items":[]}`,
		"a bad escape":          item(`{"uid":"u","name":"n","annotations":{"a":"\q"}}`),
		"a bare word":           item(`{"uid":"u","name":"n","annotations":{"a":nonsense}}`),
		"a bad number":          item(`{"uid":"u","name":"n","generation":01}`),
		"a control character":   item("{\"uid\":\"u\",\"name\":\"n\",\"annotations\":{\"a\":\"x\x01\"}}"),
		"a missing colon":       item(`{"uid":"u","name":"n","annotations":{"a" "b"}}`),
		"an item without a UID": item(`{"name":"n"}`),
		"data after the list":   `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1"},"items":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, doc)
			}))
			defer srv.Close()
			if _, _, err := (&Reader{Client: srv.Client(), Host: srv.URL}).ListPage(context.Background(), cmGVR, "", func(Source) error { return nil }); err == nil {
				t.Fatalf("accepted %s", doc)
			}
		})
	}
	// a valid skipped value of every shape still passes
	ok := item(`{"uid":"u","name":"n","generation":-1.5e+3,"annotations":{"a":"é\n\"","b":[true,false,null,{},[]]},"managedFields":[{"x":0}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ok)
	}))
	defer srv.Close()
	n := 0
	if _, _, err := (&Reader{Client: srv.Client(), Host: srv.URL}).ListPage(context.Background(), cmGVR, "", func(Source) error { n++; return nil }); err != nil || n != 1 {
		t.Fatalf("a valid list: %v (%d)", err, n)
	}
	// a watch event of an unknown type, or without its object
	for _, ev := range []string{`{"type":"SURPRISE","object":` + obj("u", "ns", "n") + `}`, `{"type":"ADDED"}`} {
		w := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, ev)
		}))
		if _, err := (&Reader{Client: w.Client(), Host: w.URL}).Watch(context.Background(), cmGVR, "1", time.Minute, nil, func(string, Source) error { return nil }); err == nil {
			t.Fatalf("accepted %s", ev)
		}
		w.Close()
	}
}

// Finding 9: a continue token is kept whole up to its bound, and refused
// past it - never silently cut.
func TestContinueTokenKeptOrRefused(t *testing.T) {
	for _, n := range []int{1500, continueBytes, continueBytes + 1} {
		token := strings.Repeat("a", n)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1","continue":%q},"items":[]}`, token)
		}))
		_, next, err := (&Reader{Client: srv.Client(), Host: srv.URL}).ListPage(context.Background(), cmGVR, "", func(Source) error { return nil })
		srv.Close()
		if n <= continueBytes && (err != nil || next != token) {
			t.Fatalf("%d bytes: got %d (%v)", n, len(next), err)
		}
		if n > continueBytes && err != ErrOversize {
			t.Fatalf("%d bytes: want oversize, got %v", n, err)
		}
	}
}

// Finding 1: the index's reader speaks HTTP/1.1 even to a server offering
// HTTP/2, so an unread body is held by the socket, not a 4 MiB stream
// window; idle connections close when a stream ends.
func TestReaderBuffersFitTheStreamReserve(t *testing.T) {
	wrote := make(chan struct{})
	var mu sync.Mutex
	open := map[net.Conn]http.ConnState{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/configmaps" && r.URL.Query().Get("watch") == "" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1"},"items":[]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		b := make([]byte, 32<<10)
		for i := range b {
			b[i] = ' '
		}
		for i := 0; i < 128; i++ {
			if _, err := w.Write(b); err != nil {
				break
			}
			if i == 3 {
				close(wrote)
			}
		}
		<-r.Context().Done()
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		if s == http.StateClosed || s == http.StateHijacked {
			delete(open, c)
		} else {
			open[c] = s
		}
	}
	srv.StartTLS()
	defer srv.Close()
	rd, err := NewReader(&rest.Config{Host: srv.URL, TLSClientConfig: rest.TLSClientConfig{Insecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	// a list page leaves no idle connection behind
	if _, _, err := rd.ListPage(context.Background(), cmGVR, "", func(Source) error { return nil }); err != nil {
		t.Fatal(err)
	}
	eventually(t, "no idle connection after a list", func() bool { mu.Lock(); defer mu.Unlock(); return len(open) == 0 })
	// an open body the reader does not read
	before := heapNow()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/body", nil)
	resp, err := rd.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 1 {
		t.Fatalf("the index's reader negotiated %s", resp.Proto)
	}
	<-wrote
	time.Sleep(50 * time.Millisecond)
	growth := int64(heapNow()) - int64(before)
	t.Logf("an unread HTTP/1.1 body holds %d KiB", growth>>10)
	if growth > StreamReserve {
		t.Fatalf("one open stream holds %d bytes; reserve %d", growth, StreamReserve)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
}

// Finding 3: choosing an Observer object's labels allocates within a fixed
// bound, whatever its label count, before anything is admitted.
func TestObserverLabelSelectionIsBounded(t *testing.T) {
	labels := make(map[string]string, 60000)
	for i := 0; i < 60000; i++ {
		labels[fmt.Sprintf("k%05d", i)] = ""
	}
	s := Source{UID: "u", Name: "n", Labels: labels}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	size := estimate(s)
	runtime.ReadMemStats(&after)
	if b := after.TotalAlloc - before.TotalAlloc; b > 16<<10 {
		t.Fatalf("estimating a 60,000-label object allocated %d bytes", b)
	}
	e := build(s, labelHash(labels), size)
	if len(e.Labels) != MaxLabels || e.Labels[0].Key != "k00000" || e.Labels[MaxLabels-1].Key != fmt.Sprintf("k%05d", MaxLabels-1) || !e.LabelsTruncated {
		t.Fatalf("kept %d labels from %q", len(e.Labels), e.Labels[0].Key)
	}
}

// Finding 4: the scanner's label projection is bounded by count as well as
// bytes; empty keys are never kept.
func TestScannerBoundsLabelCount(t *testing.T) {
	read := func(doc string) scanned {
		var out scanned
		if err := newScanner(strings.NewReader(doc)).object(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	empty := read(`{"metadata":{"uid":"u","labels":{` + strings.Repeat(`"":"",`, 50000) + `"":""}}}`)
	if len(empty.src.kept) != 0 || !empty.src.truncated {
		t.Fatalf("empty keys kept: %d", len(empty.src.kept))
	}
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"k%03d":""`, i)
	}
	many := read(`{"metadata":{"uid":"u","labels":{` + b.String() + `}}}`)
	if len(many.src.kept) != MaxLabels || !many.src.truncated {
		t.Fatalf("kept %d labels", len(many.src.kept))
	}
}

// Finding 2: a map's capacity stays charged after deletions until a
// compaction releases it; an emptied kind retains no more than it is
// charged.
func TestDeletionKeepsMapCapacityCharged(t *testing.T) {
	b := NewBudget(DefaultLimits)
	c := newClusterStore("a", b, func() time.Time { return t0 })
	c.begin(1)
	mark, _ := c.beginRead(1, "/configmaps", ClassOther)
	before := heapNow() // the store and its empty kind exist: what follows is the entries
	for i := 0; i < 50000; i++ {
		if c.put(1, "/configmaps", mark, Source{UID: fmt.Sprintf("uid-%06d", i), Name: "cm"}) != putApplied {
			t.Fatal("put")
		}
	}
	c.endRead(1, "/configmaps", mark, KindStatus{State: StateLive})
	// deletions one by one keep the capacity charged until a compaction
	for i := 0; i < 10000; i++ {
		c.remove(1, "/configmaps", fmt.Sprintf("uid-%06d", i))
	}
	if _, charged, _, _ := b.Used("a"); charged < 50000*mapSlot {
		t.Fatalf("deletions released the map's capacity: %d", charged)
	}
	empty, _ := c.beginRead(1, "/configmaps", ClassOther)
	c.endRead(1, "/configmaps", empty, KindStatus{State: StateLive})
	after := heapNow()
	_, charged, _, _ := b.Used("a")
	growth := int64(after) - int64(before)
	runtime.KeepAlive(c)
	t.Logf("emptied kind: retained %d KiB, charged %d KiB", growth>>10, charged>>10)
	if growth > charged+16<<10 { // GC noise
		t.Fatalf("retained %d > charged %d", growth, charged)
	}
}

// Re-review of 06eb92e: response headers are bounded too - an oversized
// header fails the watch before it is opened, and holds nothing after.
func TestReaderRefusesOversizedHeaders(t *testing.T) {
	value := strings.Repeat("x", 1<<20)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Test", value)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	rd, err := NewReader(&rest.Config{Host: srv.URL, TLSClientConfig: rest.TLSClientConfig{Insecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	before := heapNow()
	opened := false
	_, err = rd.Watch(context.Background(), cmGVR, "1", time.Minute, func() { opened = true }, func(string, Source) error { return nil })
	if err == nil || opened {
		t.Fatalf("an oversized header: err %v, opened %v", err, opened)
	}
	if growth := int64(heapNow()) - int64(before); growth > StreamReserve {
		t.Fatalf("a refused watch retains %d bytes", growth)
	}
	runtime.KeepAlive(value)
	// an ordinary header still works
	ok := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Audit-Id", "c0f86a1b-87a5-4d0d-a283-95a62f94931f")
		fmt.Fprint(w, `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1"},"items":[]}`)
	}))
	defer ok.Close()
	rd2, _ := NewReader(&rest.Config{Host: ok.URL, TLSClientConfig: rest.TLSClientConfig{Insecure: true}})
	if _, _, err := rd2.ListPage(context.Background(), cmGVR, "", func(Source) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// Re-review of e76155a: the bounded transport keeps CA-file rotation - the
// reader follows a rotated CA file (as client-go would) and its rotated
// transport keeps the header bound.
func TestReaderFollowsCARotation(t *testing.T) {
	defer func(d time.Duration) { caRefresh = d }(caRefresh)
	caRefresh = 0 // check on every request
	certA, keyA, err := certutil.GenerateSelfSignedCertKey("127.0.0.1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	certB, keyB, err := certutil.GenerateSelfSignedCertKey("127.0.0.1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pairA, _ := tls.X509KeyPair(certA, keyA)
	pairB, _ := tls.X509KeyPair(certB, keyB)
	var current atomic.Pointer[tls.Certificate]
	current.Store(&pairA)
	big := false
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if big {
			w.Header().Set("X-Test", strings.Repeat("x", 1<<20))
		}
		fmt.Fprint(w, `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1"},"items":[]}`)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{Certificates: []tls.Certificate{*current.Load()}}, nil
	}}
	srv.StartTLS()
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certA, 0o600); err != nil {
		t.Fatal(err)
	}
	rd, err := NewReader(&rest.Config{Host: srv.URL, TLSClientConfig: rest.TLSClientConfig{CAFile: caFile}})
	if err != nil {
		t.Fatal(err)
	}
	list := func() error {
		_, _, err := rd.ListPage(context.Background(), cmGVR, "", func(Source) error { return nil })
		return err
	}
	if err := list(); err != nil {
		t.Fatalf("initial: %v", err)
	}
	// the server and the CA file rotate together
	if err := os.WriteFile(caFile, certB, 0o600); err != nil {
		t.Fatal(err)
	}
	current.Store(&pairB)
	if err := list(); err != nil {
		t.Fatalf("after the CA rotated: %v", err)
	}
	// the rotated transport keeps the header bound
	big = true
	if err := list(); err == nil {
		t.Fatal("an oversized header passed the rotated transport")
	}
}

// Re-review of 312db52: the holder's roots and committed bytes come from one
// read - a client-go transport cached with an older CA, while the file
// already holds the new one, never leaves the reader trusting the old.
func TestReaderTrustsTheCAFileItRead(t *testing.T) {
	defer func(d time.Duration) { caRefresh = d }(caRefresh)
	certA, keyA, _ := certutil.GenerateSelfSignedCertKey("127.0.0.1", nil, nil)
	certB, keyB, _ := certutil.GenerateSelfSignedCertKey("127.0.0.1", nil, nil)
	pairA, _ := tls.X509KeyPair(certA, keyA)
	pairB, _ := tls.X509KeyPair(certB, keyB)
	var current atomic.Pointer[tls.Certificate]
	current.Store(&pairA)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"1"},"items":[]}`)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{Certificates: []tls.Certificate{*current.Load()}}, nil
	}}
	srv.StartTLS()
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certA, 0o600); err != nil {
		t.Fatal(err)
	}
	rc := &rest.Config{Host: srv.URL, TLSClientConfig: rest.TLSClientConfig{CAFile: caFile, NextProtos: []string{"http/1.1"}}}
	// client-go caches a transport trusting A ...
	warm, err := rest.HTTPClientFor(rc)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := warm.Get(srv.URL); err != nil {
		t.Fatal(err)
	} else {
		_ = resp.Body.Close()
	}
	// ... then the file and the server move to B before the reader is built
	if err := os.WriteFile(caFile, certB, 0o600); err != nil {
		t.Fatal(err)
	}
	current.Store(&pairB)
	for _, refresh := range []time.Duration{time.Hour, 0} {
		caRefresh = refresh
		rd, err := NewReader(rc)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, _, err := rd.ListPage(context.Background(), cmGVR, "", func(Source) error { return nil }); err != nil {
				t.Fatalf("refresh %v, list %d: %v", refresh, i, err)
			}
		}
	}
	runtime.KeepAlive(warm)
}
