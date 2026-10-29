package fleet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// Settings S3b §6: Detect for metrics samples the one candidate it finds,
// names every candidate when it finds several, never takes an unreadable
// discovery as the whole list, waits in its own pool and the host's
// slots within its deadline, and discards what it read once its lease
// ends.

func promService(name string, labels map[string]string, ports ...int32) *corev1.Service {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: name, Labels: labels}}
	for _, p := range ports {
		s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{Port: p})
	}
	return s
}

var serverLabels = map[string]string{"app.kubernetes.io/name": "prometheus", "app.kubernetes.io/component": "server"}

type metricsDetectRig struct {
	reg   *Registry
	c     *ClusterConn
	typed *typedfake.Clientset
	after func()
}

func newMetricsDetectRig(t *testing.T, objs ...runtime.Object) *metricsDetectRig {
	t.Helper()
	r := &metricsDetectRig{typed: typedfake.NewSimpleClientset(objs...)}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path[strings.Index(req.URL.Path, "/proxy")+len("/proxy"):]
		switch p {
		case "/api/v1/labels":
			_, _ = w.Write([]byte(`{"status":"success","data":["cluster","job"]}`))
		case "/api/v1/label/cluster/values":
			_, _ = w.Write([]byte(`{"status":"success","data":["dev","S3B-NOT-A-MEMBER"]}`))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if r.after != nil {
			r.after()
		}
	}))
	t.Cleanup(api.Close)
	r.c = NewClusterConn("prd", r.typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	r.c.restConfig = &rest.Config{Host: api.URL}
	usable(r.c, 1)
	sources, err := NewAlertSources(nil, []config.ClusterConfig{{Name: "prd"}})
	if err != nil {
		t.Fatal(err)
	}
	r.reg = &Registry{retired: map[ConnID]bool{}}
	r.reg.alerts = newAlertBuckets(context.Background(), sources, Credentials{}, clock.Real{}, func(name string) (*ClusterConn, bool) { return r.c, name == "prd" })
	return r
}

func TestMetricsDetectSamplesTheOneCandidate(t *testing.T) {
	r := newMetricsDetectRig(t, promService("prometheus-operated", nil, 9090))
	got := r.reg.DetectMetrics(context.Background(), "prd", []string{"prd", "dev"})
	if got.Err != "" || got.Unread != "" || len(got.Candidates) != 1 || !got.Candidates[0].ByName || got.Sample == nil || got.At.IsZero() {
		t.Fatalf("%+v", got)
	}
	if c := got.Candidates[0]; c.Name != "prometheus-operated" || c.Port != "9090" {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(got.Sample.Members, map[string][]string{"cluster": {"dev"}}) || strings.Contains(got.Sample.Names[0]+got.Sample.Names[1], "S3B") {
		t.Fatalf("%+v", got.Sample)
	}
	// no candidate: nothing sampled
	if got := newMetricsDetectRig(t).reg.DetectMetrics(context.Background(), "prd", nil); len(got.Candidates) != 0 || got.Sample != nil || got.Err != "" {
		t.Fatalf("none: %+v", got)
	}
}

// Every candidate is named, one Service found by name and by a selector
// once, and its distinct ports apart; several propose nothing.
func TestMetricsDetectNamesEveryCandidate(t *testing.T) {
	r := newMetricsDetectRig(t, promService("prometheus-operated", nil, 9090), promService("prometheus-server", serverLabels, 80, 9091))
	got := r.reg.DetectMetrics(context.Background(), "prd", []string{"dev"})
	if got.Sample != nil || len(got.Candidates) != 3 {
		t.Fatalf("%+v", got)
	}
	both := 0
	for _, c := range got.Candidates {
		if c.Name == "prometheus-server" && c.Port == "80" {
			if !c.ByName || c.BySelector == "" {
				t.Fatalf("one Service found two ways is not one candidate: %+v", got.Candidates)
			}
			both++
		}
	}
	if both != 1 {
		t.Fatalf("%+v", got.Candidates)
	}
}

// A discovery read that fails is named, and the one candidate found
// otherwise is not sampled: the list may not be the whole one.
func TestAnUnreadableDiscoveryIsNotTheWholeList(t *testing.T) {
	r := newMetricsDetectRig(t, promService("prom", serverLabels, 9090))
	r.typed.PrependReactor("get", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("S3B forbidden")
	})
	got := r.reg.DetectMetrics(context.Background(), "prd", nil)
	if len(got.Candidates) != 1 || len(got.Unreadable) != len(namedCandidates) || got.Sample != nil || !strings.Contains(got.Unreadable[0], "could not be read") {
		t.Fatalf("%+v", got)
	}
}

// Detect's own pool of two, and the host's slots, are waited for within
// the deadline: with none free, nothing is sampled.
func TestMetricsDetectWaitsWithinItsDeadline(t *testing.T) {
	r := newMetricsDetectRig(t, promService("prometheus-operated", nil, 9090))
	for i := 0; i < metricsDetectMax; i++ {
		metricsDetects <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	got := r.reg.DetectMetrics(ctx, "prd", nil)
	cancel()
	for i := 0; i < metricsDetectMax; i++ {
		<-metricsDetects
	}
	if !strings.Contains(got.Err, "did not run in time") || got.Sample != nil {
		t.Fatalf("pool: %+v", got)
	}
	var held []func()
	for i := 0; i < hostMetricsSlots; i++ {
		rel, err := r.reg.alerts.slots.take(context.Background(), config.EndpointMetrics, "prd")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, rel)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	got = r.reg.DetectMetrics(ctx, "prd", nil)
	cancel()
	for _, rel := range held {
		rel()
	}
	if !strings.Contains(got.Err, "did not finish in time") || got.Sample != nil || len(got.Candidates) != 0 {
		t.Fatalf("host slots: %+v", got)
	}
	if got := r.reg.DetectMetrics(context.Background(), "prd", nil); got.Sample == nil {
		t.Fatalf("with slots free: %+v", got)
	}
}

// What Detect read settles only while its lease holds (S3a-4b, R1): a
// lease that ends after a body leaves nothing, and is released once.
func TestMetricsDetectDiscardsWhatItReadOnceItsLeaseEnds(t *testing.T) {
	r := newMetricsDetectRig(t, promService("prometheus-operated", nil, 9090))
	lctx, end := context.WithCancel(context.Background())
	defer end()
	released := 0
	r.reg.alerts.leaser = func(host, what string) (Conn, context.Context, func(), error) {
		return r.c, lctx, func() { released++ }, nil
	}
	r.after = end
	got := r.reg.DetectMetrics(context.Background(), "prd", []string{"dev"})
	if released != 1 || got.Sample != nil || len(got.Candidates) != 0 || !strings.Contains(got.Err, "reconnected or removed") {
		t.Fatalf("released %d: %+v", released, got)
	}
}
