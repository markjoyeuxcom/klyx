package readfence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAFenceIsCheckedBeforeEachRequest(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	client := &http.Client{Transport: Transport(nil)}
	closed := errors.New("the fence changed")
	var fence error
	ctx := With(context.Background(), func() error { return fence })
	get := func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := get(ctx); err != nil || hits != 1 {
		t.Fatalf("a fence that holds reads: %v %d", err, hits)
	}
	fence = closed
	if err := get(ctx); !errors.Is(err, closed) || hits != 1 {
		t.Fatalf("a changed fence stops the next request before it is sent: %v %d", err, hits)
	}
	if err := get(context.Background()); err != nil || hits != 2 {
		t.Fatalf("a context without a fence reads as before: %v", err)
	}
	// an outer fence runs first, and either stops the read
	outer := errors.New("outer")
	nested := With(With(context.Background(), func() error { return outer }), func() error { return nil })
	if err := Check(nested); !errors.Is(err, outer) {
		t.Fatalf("%v", err)
	}
	if err := Check(With(context.Background(), nil)); err != nil {
		t.Fatal(err)
	}
}

// Settings S3b-2b re-review 2, R2-1: every refusal is ErrRefused, which a
// redactor keeps by identity, so a refusal stays one however it is
// wrapped or redacted.
func TestARefusalIsErrRefusedThroughWrapping(t *testing.T) {
	ctx := With(context.Background(), func() error { return errors.New("withdrawn") })
	err := Check(ctx)
	if !errors.Is(err, ErrRefused) || !Failed(err) || !Failed(fmt.Errorf("a read: %w", err)) {
		t.Fatalf("%v", err)
	}
	if Failed(errors.New("a read failed")) || errors.Is(errors.New("x"), ErrRefused) {
		t.Fatal("an ordinary error is a refusal")
	}
}
