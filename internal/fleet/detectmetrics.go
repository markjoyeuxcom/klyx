package fleet

import (
	"context"
	"fmt"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
)

// Settings S3b §6: Detect for metrics, in one member, proposes and never
// proves. Its discovery is stricter than reading's ranked one: every
// named Service present and every label-selector match, one Service found
// both ways counted once and distinct ports or paths kept apart. One
// candidate is sampled; several are named and none is proposed; and a
// discovery that could not be read is never taken as the whole list. The
// sample is metrics.SampleMetadata's: label names and fleet names, never
// series or sample values.

const (
	metricsDetectMax     = 2
	metricsDetectTimeout = 30 * time.Second
)

// metricsDetects is Detect's own pool: at most two metrics Detects in
// flight fleet-wide.
var metricsDetects = make(chan struct{}, metricsDetectMax)

// MetricsCandidate is one Service Detect found, and how.
type MetricsCandidate struct {
	metrics.ServiceCandidate
	ByName     bool
	BySelector string
}

// MetricsDetect is what Detect for metrics read in one member.
type MetricsDetect struct {
	Cluster string
	// Candidates are every Service found; Unreadable, the discovery reads
	// that failed, so the list is not the whole one; Err, why nothing was
	// read
	Candidates []MetricsCandidate
	Unreadable []string
	Err        string
	// the sample of the one candidate: when, and what; Unread is why it
	// could not be read
	At     time.Time
	Sample *metrics.MetadataSample
	Unread string
}

// DetectMetrics runs Detect for metrics in cluster (S3b §6): a place in
// Detect's own pool, waited for within the member's 30 s deadline, a lease
// on the member, its discovery, and for exactly one candidate one metadata
// sample through the member's Service proxy, each request in one of its
// host slots. A retirement or the deadline discards what it read before
// the lease is released.
func (r *Registry) DetectMetrics(ctx context.Context, cluster string, fleetNames []string) (out MetricsDetect) {
	out = MetricsDetect{Cluster: cluster}
	r.mu.RLock()
	b := r.alerts
	r.mu.RUnlock()
	if b == nil {
		out.Err = "the installations are not available"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, metricsDetectTimeout)
	defer cancel()
	select {
	case metricsDetects <- struct{}{}:
		defer func() { <-metricsDetects }()
	case <-ctx.Done():
		out.Err = fmt.Sprintf("Detect in %s did not run in time: %v", cluster, ctx.Err())
		return out
	}
	conn, lctx, release, err := b.lease(cluster, "Detect's metadata sample in "+cluster)
	if err != nil {
		out.Err = fmt.Sprintf("%s is not connected: %v", cluster, err)
		return out
	}
	defer release()
	c, ok := conn.(*ClusterConn)
	if !ok {
		out.Err = cluster + " cannot be read for Detect"
		return out
	}
	stop := context.AfterFunc(lctx, cancel) // the member's retirement ends it
	defer stop()
	// settled before the lease is released: an ended attempt's discovery
	// and sample are discarded, never proposed (S3a-4b, R1)
	defer func() {
		switch {
		case lctx.Err() != nil:
			out = MetricsDetect{Cluster: cluster, Err: cluster + " was reconnected or removed while Detect read it, so what it read is discarded"}
		case ctx.Err() != nil:
			out = MetricsDetect{Cluster: cluster, Err: fmt.Sprintf("Detect in %s did not finish in time: %v", cluster, ctx.Err())}
		}
	}()
	out.Candidates, out.Unreadable = c.metricsCandidates(ctx)
	if len(out.Unreadable) > 0 || len(out.Candidates) != 1 {
		return out
	}
	rc, err := c.proxyREST()
	if err != nil {
		out.Unread = "the service-proxy client could not be built: " + err.Error()
		return out
	}
	fence := metrics.Fence{
		Check: func() error { return lctx.Err() },
		Slot:  func(ctx context.Context) (func(), error) { return b.slots.take(ctx, config.EndpointMetrics, cluster) },
		Life:  lctx,
	}
	out.At = b.clk.Now()
	smp, err := metrics.SampleMetadata(ctx, rc, out.Candidates[0].ServiceCandidate, fence, fleetNames, out.At)
	if err != nil {
		out.Unread = err.Error()
		return out
	}
	out.Sample = &smp
	return out
}

// metricsCandidates are every Prometheus Service in the member: each named
// one present, at its port, and each port of every Service a selector
// matches; one found both ways is one candidate. A read that fails is
// named, never taken as an absence.
func (c *ClusterConn) metricsCandidates(ctx context.Context) ([]MetricsCandidate, []string) {
	var out []MetricsCandidate
	var unreadable []string
	at := map[string]int{}
	add := func(cand metrics.ServiceCandidate, byName bool, sel string) {
		key := cand.Namespace + "/" + cand.Name + ":" + cand.Port + "/" + cand.Path
		if i, ok := at[key]; ok {
			out[i].ByName = out[i].ByName || byName
			if out[i].BySelector == "" {
				out[i].BySelector = sel
			}
			return
		}
		at[key] = len(out)
		out = append(out, MetricsCandidate{ServiceCandidate: cand, ByName: byName, BySelector: sel})
	}
	for _, cand := range namedCandidates {
		_, err := c.typed.CoreV1().Services(cand.Namespace).Get(ctx, cand.Name, metav1.GetOptions{})
		switch {
		case err == nil:
			add(cand, true, "")
		case !apierrors.IsNotFound(err):
			unreadable = append(unreadable, fmt.Sprintf("the Service %s/%s could not be read: %v", cand.Namespace, cand.Name, err))
		}
	}
	for _, sel := range labelSelectors {
		list, err := c.typed.CoreV1().Services("").List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("the Services matching %s could not be listed: %v", sel, err))
			continue
		}
		for _, s := range list.Items {
			for _, p := range s.Spec.Ports {
				add(metrics.ServiceCandidate{Namespace: s.Namespace, Name: s.Name, Port: strconv.Itoa(int(p.Port)), Scheme: "http"}, false, sel)
			}
		}
	}
	if err := ctx.Err(); err != nil && len(unreadable) == 0 {
		unreadable = append(unreadable, "discovery did not finish: "+err.Error())
	}
	return out, unreadable
}
