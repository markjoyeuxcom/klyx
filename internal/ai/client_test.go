package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestComplete_Hosted(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"the challenge is stuck"}]}`)
	}))
	defer srv.Close()

	c := NewClient(BackendHosted, srv.URL, "sk-test", "claude-sonnet-5", srv.Client())
	out, err := c.Complete(context.Background(), "sys", "usr", 512)
	if err != nil {
		t.Fatal(err)
	}
	if out != "the challenge is stuck" {
		t.Fatalf("text: %q", out)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path: %q", gotPath)
	}
	if gotKey != "sk-test" || gotVersion != anthropicVersion {
		t.Errorf("auth headers: key=%q version=%q", gotKey, gotVersion)
	}
	if gotBody["system"] != "sys" || gotBody["model"] != "claude-sonnet-5" {
		t.Errorf("body: %+v", gotBody)
	}
}

func TestComplete_Local_OpenAICompat(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"local says hi"}}]}`)
	}))
	defer srv.Close()

	// No api key -> no Authorization header (a local endpoint may need none).
	c := NewClient(BackendLocal, srv.URL, "", "llama3.1", srv.Client())
	out, err := c.Complete(context.Background(), "sys", "usr", 256)
	if err != nil {
		t.Fatal(err)
	}
	if out != "local says hi" {
		t.Fatalf("text: %q", out)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: %q", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("no key must send no Authorization header, got %q", gotAuth)
	}
}

func TestComplete_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid key"}`)
	}))
	defer srv.Close()

	c := NewClient(BackendHosted, srv.URL, "bad", "m", srv.Client())
	_, err := c.Complete(context.Background(), "s", "u", 10)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want an HTTP 401 error, got %v", err)
	}
}

func TestComplete_ContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"late"}]}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	c := NewClient(BackendHosted, srv.URL, "k", "m", srv.Client())
	if _, err := c.Complete(ctx, "s", "u", 10); err == nil {
		t.Fatal("want a deadline error")
	}
}
