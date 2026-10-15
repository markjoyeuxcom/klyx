package fleetindex

import (
	"bytes"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	utilnet "k8s.io/apimachinery/pkg/util/net"
)

// caRefresh is how often a reader re-reads its CA file: client-go's own
// cadence. A test seam.
var caRefresh = 5 * time.Minute

// caHolder is the index's equivalent of client-go's CA-file rotation (its
// atomicTransportHolder, which a cloned transport leaves behind - re-review
// of e76155a): at most once per caRefresh it re-reads the CA file, and on a
// change swaps in a clone of its own transport with the new roots, keeping
// the header bound, and closes the old transport's idle connections. A
// failed read or parse keeps the current transport, as client-go does.
type caHolder struct {
	caFile string

	mu        sync.RWMutex
	transport *http.Transport
	caData    []byte
	checked   time.Time
}

var _ utilnet.RoundTripperWrapper = &caHolder{}

// newCAHolder takes the roots and their committed bytes from one read of
// the CA file: client-go's cached transport is keyed by the file's path and
// may still trust an older CA than the file now holds (re-review of
// 312db52). Unreadable or unparseable at start, it keeps client-go's roots
// with no committed bytes, so the first request re-reads the file. t is the
// index's own clone, its TLS config a clone too.
func newCAHolder(caFile string, t *http.Transport) *caHolder {
	h := &caHolder{caFile: caFile, transport: t}
	if data, err := os.ReadFile(caFile); err == nil && len(data) > 0 {
		if pool, err := rootPool(data); err == nil {
			t.TLSClientConfig.RootCAs = pool
			h.caData, h.checked = data, time.Now()
		}
	}
	return h
}

func (h *caHolder) RoundTrip(req *http.Request) (*http.Response, error) {
	return h.current().RoundTrip(req)
}

// WrappedRoundTripper lets client-go's helpers (idle-connection closing)
// reach the current transport.
func (h *caHolder) WrappedRoundTripper() http.RoundTripper {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.transport
}

func (h *caHolder) current() *http.Transport {
	h.mu.RLock()
	t, fresh := h.transport, time.Since(h.checked) < caRefresh
	h.mu.RUnlock()
	if fresh {
		return t
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.checked) < caRefresh {
		return h.transport
	}
	h.checked = time.Now() // once per caRefresh, even when the reload fails
	data, err := os.ReadFile(h.caFile)
	if err != nil || len(data) == 0 || bytes.Equal(data, h.caData) {
		return h.transport
	}
	pool, err := rootPool(data)
	if err != nil {
		return h.transport
	}
	next := h.transport.Clone() // keeps MaxResponseHeaderBytes
	next.TLSClientConfig.RootCAs = pool
	old := h.transport
	h.transport, h.caData = next, data
	old.CloseIdleConnections()
	return h.transport
}

func rootPool(pem []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("fleetindex: no certificates in the CA file")
	}
	return pool, nil
}
