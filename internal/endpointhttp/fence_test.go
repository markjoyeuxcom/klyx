package endpointhttp

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// Settings S3b §3.1: a request inside the fence.
func TestTheFenceAroundARequest(t *testing.T) {
	moved := errors.New("moved")
	ok := func(context.Context) (int, []byte, error) { return 200, []byte("ok"), nil }

	// the check runs before: a moved read sends nothing
	sent := false
	f := Fence{Check: func() error { return moved }}
	if _, _, err := f.Do(context.Background(), func(context.Context) (int, []byte, error) { sent = true; return 200, nil, nil }); !errors.Is(err, moved) || sent {
		t.Fatalf("before: %v, sent %v", err, sent)
	}
	// and after: a move during the request discards its answer
	calls := 0
	f = Fence{Check: func() error {
		calls++
		if calls == 2 {
			return moved
		}
		return nil
	}}
	if _, body, err := f.Do(context.Background(), ok); !errors.Is(err, moved) || body != nil {
		t.Fatalf("after: %v %q", err, body)
	}
	// a 401 or 403 is recorded before the request returns
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		denied := 0
		f = Fence{Check: func() error { return nil }, Denied: func() { denied++ }}
		_, _, _ = f.Do(context.Background(), func(context.Context) (int, []byte, error) { return code, nil, nil })
		if denied != 1 {
			t.Fatalf("%d: denied %d", code, denied)
		}
	}
	// the slot is held while the request runs, and given back after
	held, released := false, false
	f = Fence{Check: func() error { return nil }, Slot: func(context.Context) (func(), error) {
		held = true
		return func() { released = true }, nil
	}}
	_, _, _ = f.Do(context.Background(), func(context.Context) (int, []byte, error) {
		if !held || released {
			t.Fatal("the request ran without its slot")
		}
		return 200, nil, nil
	})
	if !released {
		t.Fatal("the slot was not given back")
	}
	// a slot not had sends nothing
	sent = false
	f = Fence{Check: func() error { return nil }, Slot: func(ctx context.Context) (func(), error) { return nil, context.Canceled }}
	if _, _, err := f.Do(context.Background(), func(context.Context) (int, []byte, error) { sent = true; return 200, nil, nil }); !errors.Is(err, context.Canceled) || sent {
		t.Fatalf("no slot: %v, sent %v", err, sent)
	}
	// the lease's end cancels the request
	life, end := context.WithCancel(context.Background())
	f = Fence{Check: func() error { return nil }, Life: life}
	_, _, err := f.Do(context.Background(), func(ctx context.Context) (int, []byte, error) {
		end()
		<-ctx.Done()
		return 0, nil, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lease ended: %v", err)
	}
}
