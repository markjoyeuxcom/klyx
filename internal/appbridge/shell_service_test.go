package appbridge

import (
	"errors"
	"testing"
)

func TestShellServiceNilSafe(t *testing.T) {
	s := NewShellService(nil, nil, nil, nil, nil)
	s.SetMainTitle("x") // must not panic
	s.SetBadge(3)
	s.SetBadge(0)
	if r := s.Notify("t", "b"); r.OK {
		t.Fatal("notify without a backend must report unavailable")
	}
}

func TestShellServiceTitle(t *testing.T) {
	var got string
	s := NewShellService(func(t string) { got = t }, nil, nil, nil, nil)
	s.SetMainTitle("  prd-weu · Pods — Klyx  ")
	if got != "prd-weu · Pods — Klyx" {
		t.Fatalf("title = %q", got)
	}
	got = "unchanged"
	s.SetMainTitle("   ")
	if got != "unchanged" {
		t.Fatal("empty title must be ignored")
	}
}

func TestShellServiceBadge(t *testing.T) {
	var set, removed []string
	s := NewShellService(nil,
		func(l string) error { set = append(set, l); return nil },
		func() error { removed = append(removed, "x"); return nil },
		nil, nil)
	s.SetBadge(4)
	s.SetBadge(0)
	s.SetBadge(-1)
	if len(set) != 1 || set[0] != "4" {
		t.Fatalf("set = %v", set)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v", removed)
	}
}

func TestShellServiceNotifyAuthorization(t *testing.T) {
	authCalls := 0
	sent := 0
	s := NewShellService(nil, nil, nil,
		func(title, body string) error { sent++; return nil },
		func() (bool, error) { authCalls++; return true, nil })
	if r := s.Notify("a", "b"); !r.OK {
		t.Fatalf("notify: %+v", r)
	}
	if r := s.Notify("a", "b"); !r.OK {
		t.Fatalf("notify: %+v", r)
	}
	if authCalls != 1 {
		t.Fatalf("authorization must be requested once, got %d", authCalls)
	}
	if sent != 2 {
		t.Fatalf("sent = %d", sent)
	}

	denied := NewShellService(nil, nil, nil,
		func(string, string) error { t.Fatal("must not send when denied"); return nil },
		func() (bool, error) { return false, nil })
	if r := denied.Notify("a", "b"); r.OK || r.Error != "notifications not authorized" {
		t.Fatalf("denied notify = %+v", r)
	}

	failing := NewShellService(nil, nil, nil,
		func(string, string) error { return errors.New("boom") },
		nil)
	if r := failing.Notify("a", "b"); r.OK || r.Error != "boom" {
		t.Fatalf("failing notify = %+v", r)
	}
}
