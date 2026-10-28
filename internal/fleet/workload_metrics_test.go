package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/metrics"
)

type fakeQuerier struct{ body string }

func (f fakeQuerier) InstantQuery(_ context.Context, _ string) (int, []byte, error) {
	return 200, []byte(f.body), nil
}

func TestQueryByPodMapsNamespacePodLabels(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"namespace":"ns","pod":"api-1"},"value":[0,"0.10"]},
		{"metric":{"namespace":"ns","pod":"api-2"},"value":[0,"0.20"]},
		{"metric":{"pod":"no-ns"},"value":[0,"0.99"]}
	]}}`
	cl := metrics.NewClient(metrics.TransportOf(fakeQuerier{body: body}), metrics.Scope{})
	got, err := queryByPod(context.Background(), cl, metrics.PodCPU.With(metrics.Namespace{}))
	if err != nil {
		t.Fatal(err)
	}
	if got["ns/api-1"] != 0.10 || got["ns/api-2"] != 0.20 {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["/no-ns"]; ok {
		t.Fatalf("sample missing a namespace label must be dropped, got %v", got)
	}
}

func TestWorkloadMetricsUnavailableWhenNoSource(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}},
	}
	cs := fake.NewSimpleClientset(dep)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}
	c.caps = capability.Set{}

	usage, st := c.WorkloadMetrics(context.Background(), "")
	if st.Available {
		t.Fatalf("expected unavailable, got %+v", st)
	}
	if usage != nil {
		t.Fatalf("expected nil usage on unavailable, got %v", usage)
	}
	if !strings.HasPrefix(st.Message, "metrics unavailable") {
		t.Fatalf("message should explain unavailability, got %q", st.Message)
	}
}
