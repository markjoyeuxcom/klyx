package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/expiry"
	"github.com/moomora/klyx/internal/fleet"
)

type fakeExpiryConn struct {
	result fleet.ExpiryScanResult
	err    error
}

func (f *fakeExpiryConn) ExpiryScan(context.Context) (fleet.ExpiryScanResult, error) {
	return f.result, f.err
}

func TestGetExpiryScanMapsItems(t *testing.T) {
	when := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	conn := &fakeExpiryConn{result: fleet.ExpiryScanResult{
		Items:    []expiry.Item{{Source: "webhook-ca", Name: "cm-webhook", Detail: "webhook.cert-manager.io", Subject: "CN=ca", NotAfter: when}},
		Warnings: []string{"tls secrets: forbidden"},
	}}
	s := NewExpiryService(func(string) (ExpiryConn, bool) { return conn, true })
	dto := s.GetExpiryScan("c")
	if dto.Error != "" || len(dto.Items) != 1 || dto.Items[0].NotAfterUnix != when.Unix() {
		t.Fatalf("dto: %+v", dto)
	}
	if len(dto.Warnings) != 1 {
		t.Fatalf("warnings must carry: %+v", dto)
	}
}

func TestGetExpiryScanHonestFailures(t *testing.T) {
	miss := NewExpiryService(func(string) (ExpiryConn, bool) { return nil, false })
	if dto := miss.GetExpiryScan("nope"); dto.Error == "" || dto.Items == nil {
		t.Fatalf("cluster miss: %+v", dto)
	}
	failing := NewExpiryService(func(string) (ExpiryConn, bool) {
		return &fakeExpiryConn{err: errors.New("boom")}, true
	})
	if dto := failing.GetExpiryScan("c"); dto.Error != "boom" {
		t.Fatalf("error fold-in: %+v", dto)
	}
}
