package fleet

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// Settings S3a §6: Detect keeps label names, and the values of labels equal
// to a fleet cluster's name; never an annotation, any other value, or a
// decoded alert. Its sample is bounded.

func TestADetectSampleKeepsOnlyNamesAndClusterValues(t *testing.T) {
	body := `[
 {"labels":{"alertname":"WebDown","cluster":"dev","team":"S3A-CANARY"},"annotations":{"summary":"token S3A-SECRET leaked"}},
 {"labels":{"alertname":"StgDown","cluster":"stg","k8s_cluster":"prd"}},
 {"labels":{"alertname":"Elsewhere","cluster":"not-a-member"}}
]`
	names, members, n, cut, err := sampleLabels([]byte(body), []string{"dev", "stg", "prd"})
	if err != nil || n != 3 || cut {
		t.Fatalf("%d %v %v", n, cut, err)
	}
	if strings.Join(names, ",") != "alertname,cluster,k8s_cluster,team" {
		t.Fatalf("names: %v", names)
	}
	if !reflect.DeepEqual(members, map[string][]string{"cluster": {"dev", "stg"}, "k8s_cluster": {"prd"}}) {
		t.Fatalf("members: %v", members)
	}
	kept := fmt.Sprint(names, members)
	for _, never := range []string{"S3A-CANARY", "S3A-SECRET", "not-a-member", "WebDown", "summary"} {
		if strings.Contains(kept, never) {
			t.Fatalf("kept %s: %s", never, kept)
		}
	}
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i <= detectMaxAlerts; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"labels":{"alertname":"A%d","cluster":"dev"}}`, i)
	}
	b.WriteString("]")
	if _, _, n, cut, err := sampleLabels([]byte(b.String()), []string{"dev"}); err != nil || n != detectMaxAlerts || !cut {
		t.Fatalf("the bound: %d %v %v", n, cut, err)
	}
	if _, _, _, _, err := sampleLabels([]byte(`{"labels":{}}`), nil); err == nil {
		t.Fatal("not a list")
	}
}

// Detect in one cluster: the installation its discovery proves, and one
// sample through that cluster's Service proxy; a sample over 1 MiB is said
// unread; a cluster without one says none.
func TestDetectReadsOneSampleWhereDiscoveryProvesOne(t *testing.T) {
	inst := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "kps-alertmanager"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"alertmanager": "main"}, Ports: []corev1.ServicePort{{Name: "http-web", Port: 9093}}}}
	typed := typedfake.NewSimpleClientset(append(governed("alertmanager-main-0"), inst)...)
	servedAM(typed)
	e := newAMEnv(t, nil, typed, amDyn(amResource("monitoring", "main", amUID, map[string]any{"routePrefix": "/am"})))
	sample := `[{"labels":{"alertname":"A","cluster":"dev"}}]`
	api := httptest.NewServer(http.StripPrefix("/api/v1/namespaces/monitoring/services/http:kps-alertmanager:http-web/proxy/am", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sample))
	})))
	t.Cleanup(api.Close)
	e.c.restConfig = &rest.Config{Host: api.URL}
	usable(e.c, 1)
	sources, err := NewAlertSources(nil, []config.ClusterConfig{{Name: "prd"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := &Registry{retired: map[ConnID]bool{}}
	reg.alerts = newAlertBuckets(ctx, sources, Credentials{}, clock.Real{}, func(name string) (*ClusterConn, bool) { return e.c, name == "prd" })
	got := reg.DetectAlertmanager(ctx, "prd", []string{"prd", "dev"})
	if !got.Found || got.Service != "kps-alertmanager" || got.Port != "http-web" || got.Path != "/am" || got.Alerts != 1 || got.Unread != "" {
		t.Fatalf("%+v", got)
	}
	if !reflect.DeepEqual(got.Members, map[string][]string{"cluster": {"dev"}}) || got.At.IsZero() {
		t.Fatalf("%+v", got)
	}
	sample = `[` + strings.Repeat(`{"labels":{"alertname":"`+strings.Repeat("x", 1024)+`"}},`, 1100) + `{}]`
	if got := reg.DetectAlertmanager(ctx, "prd", nil); !got.Found || got.Unread != "the sample was larger than 1 MiB, so it was not read" || got.Alerts != 0 {
		t.Fatalf("over the byte bound: %+v", got)
	}
	if got := reg.DetectAlertmanager(ctx, "stg", nil); got.Err == "" {
		t.Fatalf("a cluster not running: %+v", got)
	}
	none := newAMEnv(t, nil, typedfake.NewSimpleClientset(), amDyn())
	reg.alerts.host = func(string) (*ClusterConn, bool) { return none.c, true }
	if got := reg.DetectAlertmanager(ctx, "prd", nil); !got.None || got.Found {
		t.Fatalf("no Alertmanager: %+v", got)
	}
}
