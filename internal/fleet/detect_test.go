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
	"k8s.io/apimachinery/pkg/runtime"
	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

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

// Codex's review of S3a-4b, R1: what Detect read settles only while its
// lease holds. A lease that ends during discovery, or after the sample's
// body is written, leaves no sample and no proposal - however far the reads
// got - and the lease is released once; an unchanged lease is the control.
func TestDetectDiscardsWhatItReadOnceItsLeaseEnds(t *testing.T) {
	for _, when := range []string{"never", "during discovery", "after the body", "its deadline after the body"} {
		t.Run(when, func(t *testing.T) {
			inst := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "kps-alertmanager"},
				Spec: corev1.ServiceSpec{Selector: map[string]string{"alertmanager": "main"}, Ports: []corev1.ServicePort{{Name: "http-web", Port: 9093}}}}
			typed := typedfake.NewSimpleClientset(append(governed("alertmanager-main-0"), inst)...)
			servedAM(typed)
			lctx, end := context.WithCancel(context.Background())
			defer end()
			ctx, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			if when == "during discovery" {
				typed.PrependReactor("*", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
					end()
					return false, nil, nil
				})
			}
			e := newAMEnv(t, nil, typed, amDyn(amResource("monitoring", "main", amUID, map[string]any{"routePrefix": "/am"})))
			api := httptest.NewServer(http.StripPrefix("/api/v1/namespaces/monitoring/services/http:kps-alertmanager:http-web/proxy/am", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`[{"labels":{"alertname":"A","cluster":"dev"}}]`))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				switch when {
				case "after the body":
					end()
				case "its deadline after the body":
					cancelCaller()
				}
			})))
			t.Cleanup(api.Close)
			e.c.restConfig = &rest.Config{Host: api.URL}
			usable(e.c, 1)
			sources, err := NewAlertSources(nil, []config.ClusterConfig{{Name: "prd"}})
			if err != nil {
				t.Fatal(err)
			}
			reg := &Registry{retired: map[ConnID]bool{}}
			reg.alerts = newAlertBuckets(context.Background(), sources, Credentials{}, clock.Real{}, func(name string) (*ClusterConn, bool) { return e.c, name == "prd" })
			released := 0
			reg.alerts.leaser = func(host, what string) (Conn, context.Context, func(), error) {
				return e.c, lctx, func() { released++ }, nil
			}
			got := reg.DetectAlertmanager(ctx, "prd", []string{"prd", "dev"})
			if released != 1 {
				t.Fatalf("released %d times", released)
			}
			if when == "never" {
				if !got.Found || got.Alerts != 1 || got.Err != "" {
					t.Fatalf("control: %+v", got)
				}
				return
			}
			want := map[bool]string{false: "reconnected or removed", true: "did not finish in time"}[when == "its deadline after the body"]
			if got.Found || got.Alerts != 0 || len(got.Members) != 0 || len(got.LabelNames) != 0 || got.Service != "" || !strings.Contains(got.Err, want) {
				t.Fatalf("an ended lease's reads settled: %+v", got)
			}
		})
	}
}

// R3: a complete sample is one list and nothing after it; a list cut at
// its bound stays a qualified partial sample.
func TestASampleIsOneCompleteList(t *testing.T) {
	for body, ok := range map[string]bool{
		`[{"labels":{"cluster":"dev"}}]`:           true,
		" [{\"labels\":{\"cluster\":\"dev\"}}] \n": true,
		`[{"labels":{"cluster":"dev"}}]NOT-JSON`:   false,
		`[{"labels":{"cluster":"dev"}}][]`:         false,
		`[{"labels":{"cluster":"dev"}}`:            false,
	} {
		_, _, n, cut, err := sampleLabels([]byte(body), []string{"dev"})
		if ok != (err == nil) || ok && (n != 1 || cut) {
			t.Fatalf("%q: n=%d cut=%v err=%v", body, n, cut, err)
		}
		if err != nil && strings.Contains(err.Error(), "NOT-JSON") {
			t.Fatalf("an error echoes the body: %v", err)
		}
	}
	long := "[" + strings.Repeat(`{"labels":{"cluster":"dev"}},`, detectMaxAlerts) + `{"labels":{}}]trailing`
	if _, _, n, cut, err := sampleLabels([]byte(long), []string{"dev"}); err != nil || !cut || n != detectMaxAlerts {
		t.Fatalf("the bound: n=%d cut=%v err=%v", n, cut, err)
	}
}
