package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/observe"
)

type failingDiscovery struct{}

func (failingDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	return nil, errors.New("the server is currently unable to handle the request")
}
func (failingDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return nil, errors.New("unreachable")
}

func probeConn(disc discoveryLike) *ClusterConn {
	c := NewClusterConn("x", fake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	c.disc = newCachedDiscovery(disc, nil)
	return c
}

// The health Observer's per-kind probe (1c addendum §3.3).
func TestCRDServedProbe(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.Resources = fluxSplitDiscovery()
	complete := probeConn(typed.Discovery())
	ctx := context.Background()
	for _, tc := range []struct {
		name, group, resource, fallback string
		want                            observe.Probe
	}{
		{"served at the preferred version", "kustomize.toolkit.fluxcd.io", "kustomizations", "v1", observe.Probe{Served: true, Version: "v1"}},
		{"served only at a non-preferred version", "source.toolkit.fluxcd.io", "ocirepositories", "v1", observe.Probe{Served: true, Version: "v1beta2"}},
		{"complete discovery without the resource is absence", "source.toolkit.fluxcd.io", "buckets", "v1", observe.Probe{Served: false}},
		{"a group missing from a successful discovery is absence, never the fallback", "cert-manager.io", "certificates", "v1", observe.Probe{Served: false}},
	} {
		got, err := complete.crdServed(ctx, tc.group, tc.resource, tc.fallback)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}

	// One unreadable version is uncertainty, never absence: the fallback
	// that could not be ruled out is tried.
	partial := probeConn(partialDiscovery{typed.Discovery()})
	if got, err := partial.crdServed(ctx, "source.toolkit.fluxcd.io", "buckets", "v1beta2"); err != nil || got != (observe.Probe{Served: true, Version: "v1beta2"}) {
		t.Errorf("partial discovery: got %+v, %v", got, err)
	}

	// A discovery error confirms nothing: it is an error, not absence.
	if _, err := probeConn(failingDiscovery{}).crdServed(ctx, "cert-manager.io", "certificates", "v1"); err == nil {
		t.Error("a discovery failure must be reported, never read as absence")
	}
}

// blockingDiscovery never answers until released (a hung discovery).
type blockingDiscovery struct{ release chan struct{} }

func (b blockingDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	<-b.release
	return nil, errors.New("released")
}
func (b blockingDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	<-b.release
	return nil, errors.New("released")
}

// A hung discovery never holds a probe past its deadline, and a cancelled
// probe returns at once; either way it confirms nothing (a timeout class).
func TestCRDServedProbeIsBoundedAndCancellable(t *testing.T) {
	b := blockingDiscovery{release: make(chan struct{})}
	defer close(b.release)
	c := probeConn(b)
	old := crdProbeTimeout
	crdProbeTimeout = 50 * time.Millisecond
	defer func() { crdProbeTimeout = old }()

	start := time.Now()
	_, err := c.crdServed(context.Background(), "cert-manager.io", "certificates", "v1")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("a hung discovery must time out at the probe deadline: %v after %v", err, time.Since(start))
	}
	crdProbeTimeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start = time.Now()
	if _, err := c.crdServed(ctx, "cert-manager.io", "certificates", "v1"); !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Fatalf("a cancelled probe returns at once: %v after %v", err, time.Since(start))
	}
}
