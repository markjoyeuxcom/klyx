package secretstore

import (
	"errors"
	"testing"
)

// The in-memory store keeps the OS store's distinctions: nothing stored,
// a failure, and refusal of an empty secret.
func TestMemoryStore(t *testing.T) {
	var s Memory
	if _, err := s.Get("a"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty: %v", err)
	}
	if err := s.Set("a", ""); err == nil {
		t.Fatal("an empty secret is refused")
	}
	if err := s.Set("a", "v"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("a"); err != nil || v != "v" {
		t.Fatalf("round trip: %q %v", v, err)
	}
	s.FailGet = &KeychainError{Err: errors.New("locked")}
	var kc *KeychainError
	if _, err := s.Get("a"); !errors.As(err, &kc) || errors.Is(err, ErrNoKey) {
		t.Fatalf("a locked store is not nothing stored: %v", err)
	}
	s.FailGet = nil
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("deleted: %v", err)
	}
}
