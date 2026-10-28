package endpointhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/rest"
)

// Codex's review of S3b-2b: the fence's check runs at every round trip a
// request makes - a redirect's hop, client-go's retry - so nothing is sent
// once the terms move (R1); and a 401 or 403 is recorded when its headers
// arrive, whatever its body then does (R3). Each negative has its control.

// moveAfter is a fence whose terms move after n checks.
func moveAfter(n int32, moved error) (Fence, *atomic.Int32) {
	var calls atomic.Int32
	return Fence{Check: func() error {
		if calls.Add(1) > n {
			return moved
		}
		return nil
	}}, &calls
}

func TestARedirectAfterAMoveSendsNothing(t *testing.T) {
	moved := errors.New("the installation changed")
	for _, move := range []bool{false, true} {
		var followed atomic.Int32
		var f Fence
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/follow" {
				followed.Add(1)
				_, _ = io.WriteString(w, "{}")
				return
			}
			http.Redirect(w, r, "/follow", http.StatusFound)
		}))
		// the check before the request, then one at its first round trip;
		// a move makes the redirect's hop the next
		f, _ = moveAfter(map[bool]int32{false: 100, true: 2}[move], moved)
		d := NewDirect(api.URL, Auth{}, &http.Client{}, Limits{MaxBytes: 1 << 20, TooLarge: errBig})
		_, _, err := f.Do(context.Background(), func(ctx context.Context) (int, []byte, error) { return d.Get(ctx, "/api/v1/query_range", nil) })
		api.Close()
		if move && (followed.Load() != 0 || !errors.Is(err, moved)) {
			t.Fatalf("a redirect's hop was sent after the move: %d, %v", followed.Load(), err)
		}
		if !move && (followed.Load() != 1 || err != nil) {
			t.Fatalf("control: the redirect was not followed: %d, %v", followed.Load(), err)
		}
	}
	// the hosted transports, through the guarded proxy: the same
	for _, move := range []bool{false, true} {
		var followed atomic.Int32
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/follow") {
				followed.Add(1)
				_, _ = io.WriteString(w, "{}")
				return
			}
			http.Redirect(w, r, r.URL.Path+"/follow", http.StatusFound)
		}))
		f, _ := moveAfter(map[bool]int32{false: 100, true: 2}[move], moved)
		p := rawProxy(t, api.URL, 1<<20)
		_, _, err := f.Do(context.Background(), func(ctx context.Context) (int, []byte, error) { return p.Get(ctx, "/x", nil, nil) })
		api.Close()
		if move && (followed.Load() != 0 || !errors.Is(err, moved)) || !move && (followed.Load() != 1 || err != nil) {
			t.Fatalf("hosted, moved %v: %d, %v", move, followed.Load(), err)
		}
	}
}

// client-go retries a 429 that names a Retry-After: the retry is a round
// trip, and a move before it sends nothing.
func TestARetryAfterAMoveSendsNothing(t *testing.T) {
	moved := errors.New("the installation changed")
	for _, move := range []bool{false, true} {
		var sent atomic.Int32
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sent.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = io.WriteString(w, "{}")
		}))
		rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL}, Limits{MaxBytes: 1 << 20, TooLarge: errBig})
		if err != nil {
			t.Fatal(err)
		}
		p := NewProxy(rc, Service{Namespace: "monitoring", Name: "prom", Port: "9090"}, "", Limits{MaxBytes: 1 << 20, TooLarge: errBig})
		f, _ := moveAfter(map[bool]int32{false: 100, true: 2}[move], moved)
		_, _, err = f.Do(context.Background(), func(ctx context.Context) (int, []byte, error) { return p.Get(ctx, "/api/v1/query", nil) })
		api.Close()
		if move && (sent.Load() != 1 || !errors.Is(err, moved)) {
			t.Fatalf("a retry was sent after the move: %d, %v", sent.Load(), err)
		}
		if !move && (sent.Load() != 2 || err != nil) {
			t.Fatalf("control: client-go did not retry: %d, %v", sent.Load(), err)
		}
	}
}

// A 401 or 403 is a denial though its body is cut at its cap or ends
// early, for the direct transport and both hosted ones.
func TestADenialIsRecordedWhateverItsBody(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, body := range []string{"oversized", "truncated"} {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body == "truncated" {
					w.Header().Set("Content-Length", "64")
					w.WriteHeader(code)
					_, _ = io.WriteString(w, "x")
					return
				}
				w.WriteHeader(code)
				_, _ = io.WriteString(w, strings.Repeat("x", 64))
			}))
			lim := Limits{MaxBytes: 16, TooLarge: errBig}
			rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL}, lim)
			if err != nil {
				t.Fatal(err)
			}
			for name, get := range map[string]func(context.Context) (int, []byte, error){
				"direct": func(ctx context.Context) (int, []byte, error) {
					return NewDirect(api.URL, Auth{}, &http.Client{}, lim).Get(ctx, "/x", nil)
				},
				"raw proxy": func(ctx context.Context) (int, []byte, error) {
					return rawProxy(t, api.URL, 16).Get(ctx, "/x", nil, nil)
				},
				"proxy": func(ctx context.Context) (int, []byte, error) {
					return NewProxy(rc, Service{Name: "s", Port: "1"}, "", lim).Get(ctx, "/x", nil)
				},
			} {
				denied := 0
				f := Fence{Check: func() error { return nil }, Denied: func() { denied++ }}
				_, _, err := f.Do(context.Background(), get)
				if err == nil || denied != 1 {
					t.Errorf("%s, %d, %s body: denied %d, err %v", name, code, body, denied, err)
				}
			}
			api.Close()
		}
	}
	// the control: a 500 with a broken body is no denial
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "64")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "x")
	}))
	defer api.Close()
	denied := 0
	f := Fence{Check: func() error { return nil }, Denied: func() { denied++ }}
	if _, _, err := f.Do(context.Background(), func(ctx context.Context) (int, []byte, error) {
		return NewDirect(api.URL, Auth{}, &http.Client{}, Limits{MaxBytes: 16, TooLarge: errBig}).Get(ctx, "/x", nil)
	}); err == nil || denied != 0 {
		t.Fatalf("a 500: denied %d, %v", denied, err)
	}
}
