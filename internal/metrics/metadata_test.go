package metrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

// Settings S3b §6: Detect's metadata sample keeps label names and values
// equal to a fleet cluster's name only, reads at most eight labels in a
// fixed order, examines each answer's first 2,000 values, counted before
// any filtering, and says what it cut, failed or did not ask for.

const proxyPrefix = "/api/v1/namespaces/monitoring/services/http:prometheus-operated:9090/proxy"

type metadataRig struct {
	mu      sync.Mutex
	answers map[string]string // path under the Service -> body
	status  map[string]int
	asked   []string
	windows []string
}

func (m *metadataRig) sample(t *testing.T, fence Fence) (MetadataSample, error) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, proxyPrefix)
		m.mu.Lock()
		m.asked = append(m.asked, path)
		start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
		m.windows = append(m.windows, strconv.FormatInt(end-start, 10))
		code, body := m.status[path], m.answers[path]
		m.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		if body == "" {
			body = `{"status":"success","data":[]}`
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(api.Close)
	rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	if fence.Check == nil {
		fence.Check = func() error { return nil }
	}
	c := ServiceCandidate{Namespace: "monitoring", Name: "prometheus-operated", Port: "9090"}
	return SampleMetadata(context.Background(), rc, c, fence, []string{"dev", "prd"}, time.Unix(1_800_000_000, 0))
}

func values(vs ...string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = strconv.Quote(v)
	}
	return `{"status":"success","data":[` + strings.Join(q, ",") + `]}`
}

func TestAMetadataSampleKeepsNamesAndFleetNamesOnly(t *testing.T) {
	m := &metadataRig{answers: map[string]string{
		"/api/v1/labels":                          values("__name__", "job", "env", "k8s_cluster_name", "weird-name", "cluster"),
		"/api/v1/label/cluster/values":            values("dev", "prd", "S3B-OTHER-VALUE", "3.14"),
		"/api/v1/label/k8s_cluster_name/values":   values("dev"),
		"/api/v1/label/env/values":                values("dev", "staging"),
		"/api/v1/label/job/values":                values("prometheus"),
		"/api/v1/label/cluster_name/values":       `{"status":"error","errorType":"bad_data","error":"S3B-ERROR-BODY"}`,
		"/api/v1/label/kubernetes_cluster/values": values(),
	}, status: map[string]int{"/api/v1/label/cluster_name/values": http.StatusBadRequest}}
	s, err := m.sample(t, Fence{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"cluster", "k8s_cluster_name", "cluster_name", "kubernetes_cluster", "env", "job"}; !reflect.DeepEqual(s.Queried, want) {
		t.Fatalf("queried %v", s.Queried)
	}
	if want := map[string][]string{"cluster": {"dev", "prd"}, "k8s_cluster_name": {"dev"}, "env": {"dev"}}; !reflect.DeepEqual(s.Members, want) {
		t.Fatalf("members %v", s.Members)
	}
	if !reflect.DeepEqual(s.Failed, []string{"cluster_name"}) || s.NotQueried != 1 || s.NamesCut || len(s.Cut) != 0 {
		t.Fatalf("%+v", s)
	}
	if !reflect.DeepEqual(s.Names, []string{"__name__", "cluster", "env", "job", "k8s_cluster_name", "weird-name"}) {
		t.Fatalf("names %v", s.Names)
	}
	if text := fmt.Sprintf("%+v", s); strings.Contains(text, "S3B-OTHER-VALUE") || strings.Contains(text, "3.14") || strings.Contains(text, "S3B-ERROR-BODY") || strings.Contains(text, "staging") {
		t.Fatalf("a value that is not a fleet cluster's name was kept: %s", text)
	}
	for _, p := range m.asked {
		if strings.Contains(p, "query") || strings.Contains(p, "__name__") || strings.Contains(p, "weird") {
			t.Fatalf("asked %s", p)
		}
	}
	for _, w := range m.windows {
		if w != "3600" {
			t.Fatalf("a window of %s seconds", w)
		}
	}
	if !s.End.Equal(time.Unix(1_800_000_000, 0)) || s.End.Sub(s.Start) != time.Hour {
		t.Fatalf("window %v to %v", s.Start, s.End)
	}
}

// At most eight labels are read; the rest are counted, not asked for.
func TestAMetadataSampleReadsAtMostEightLabels(t *testing.T) {
	names := []string{}
	for i := 0; i < 12; i++ {
		names = append(names, fmt.Sprintf("l%02d", i))
	}
	m := &metadataRig{answers: map[string]string{"/api/v1/labels": values(names...)}}
	s, err := m.sample(t, Fence{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Queried) != 8 || s.Queried[4] != "l00" || s.Queried[7] != "l03" || s.NotQueried != 8 || len(m.asked) != 9 {
		t.Fatalf("%+v, asked %d", s, len(m.asked))
	}
}

// The first 2,000 values, counted before any filtering: a fleet name past
// them is not examined, and the answer is said to be cut (§14, RR3).
func TestAMetadataSampleExaminesTheFirstTwoThousandValues(t *testing.T) {
	many := make([]string, 0, 2001)
	for i := 0; i < 2000; i++ {
		many = append(many, fmt.Sprintf("other-%d", i))
	}
	m := &metadataRig{answers: map[string]string{
		"/api/v1/labels":               values(append(append([]string{}, many...), "late")...),
		"/api/v1/label/cluster/values": values(append(append([]string{}, many...), "dev")...),
	}}
	s, err := m.sample(t, Fence{})
	if err != nil {
		t.Fatal(err)
	}
	if !s.NamesCut || len(s.Names) != 2000 || contains(s.Names, "late") {
		t.Fatalf("names: cut %v, %d", s.NamesCut, len(s.Names))
	}
	if !reflect.DeepEqual(s.Cut, []string{"cluster"}) || len(s.Members["cluster"]) != 0 {
		t.Fatalf("a member past the sample counted: %+v", s.Members)
	}
	// the control: the same fleet name within the first 2,000
	m = &metadataRig{answers: map[string]string{"/api/v1/label/cluster/values": values(append([]string{"dev"}, many[:1999]...)...)}}
	if s, err := m.sample(t, Fence{}); err != nil || !reflect.DeepEqual(s.Members["cluster"], []string{"dev"}) || len(s.Cut) != 0 {
		t.Fatalf("control: %+v %v", s, err)
	}
}

// An answer larger than 1 MiB is cut at its cap and not examined.
func TestAMetadataAnswerOverItsCapIsNotExamined(t *testing.T) {
	big := values("dev", strings.Repeat("x", metadataMaxBytes))
	m := &metadataRig{answers: map[string]string{"/api/v1/label/cluster/values": big}}
	s, err := m.sample(t, Fence{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Cut, []string{"cluster"}) || len(s.Members) != 0 {
		t.Fatalf("%+v", s)
	}
}

// Without the names, nothing is said of the members; the fence's check
// discards a request across a move.
func TestAMetadataSampleFailsWhole(t *testing.T) {
	m := &metadataRig{answers: map[string]string{"/api/v1/labels": `{"status":"error","error":"S3B-ERROR-BODY"}`}, status: map[string]int{"/api/v1/labels": 500}}
	if s, err := m.sample(t, Fence{}); err == nil || strings.Contains(err.Error(), "S3B-ERROR-BODY") || len(s.Queried) != 0 {
		t.Fatalf("%+v %v", s, err)
	}
	moved := errors.New("the member was reconnected")
	m = &metadataRig{}
	if _, err := m.sample(t, Fence{Check: func() error { return moved }}); !errors.Is(err, moved) || len(m.asked) != 0 {
		t.Fatalf("%v, asked %v", err, m.asked)
	}
}

func TestFirstValuesIsStrict(t *testing.T) {
	for name, body := range map[string]string{
		"not success":   `{"status":"error","data":["dev"]}`,
		"no data":       `{"status":"success"}`,
		"data twice":    `{"status":"success","data":["dev"],"data":["prd"]}`,
		"not strings":   `{"status":"success","data":[1]}`,
		"trailing":      `{"status":"success","data":["dev"]} {}`,
		"not an object": `["dev"]`,
		"an open list":  `{"status":"success","data":["dev"`,
	} {
		if _, _, err := firstValues([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if v, cut, err := firstValues([]byte(`{"data":["dev"],"warnings":["w"],"status":"success"}`)); err != nil || cut || !reflect.DeepEqual(v, []string{"dev"}) {
		t.Fatalf("%v %v %v", v, cut, err)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
