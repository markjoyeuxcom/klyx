package healthsrc

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/safehttp"
)

// Coverage S8a §8: the admitted worst shapes under the 8 MiB wire caps - not
// an impossible product of every cap. Each is parsed, reduced and made into
// display values as a read would; the retained heap and the encoded rows
// are reported beside their bounds, and the part's evaluation against 5,000
// held pods is timed (its 50 ms asserted only with KLYX_LOAD=1, without
// -race).
//
//	KLYX_LOAD=1 go test ./internal/healthsrc -run TestAlertsBudget -v
func TestAlertsBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("calibration")
	}
	const wireCap = 8 << 20
	alertJSON := func(i int, labels, annotations map[string]string) string {
		b, _ := json.Marshal(map[string]any{"fingerprint": fmt.Sprintf("%016x", i), "labels": labels, "annotations": annotations,
			"startsAt": "2026-10-19T10:00:00Z", "updatedAt": "2026-10-19T10:00:00Z", "endsAt": "2026-10-19T14:00:00Z",
			"status":    map[string]any{"state": "active", "silencedBy": []string{}, "inhibitedBy": []string{}, "mutedBy": []string{}},
			"receivers": []map[string]string{{"name": "team-a"}, {"name": "team-b"}, {"name": "pager"}, {"name": "audit"}}, "generatorURL": "https://prom.example/graph?g0.expr=up"})
		return string(b)
	}
	body := func(n int, mk func(i int) string) []byte {
		var sb strings.Builder
		sb.WriteString("[")
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(mk(i))
		}
		sb.WriteString("]")
		return []byte(sb.String())
	}
	// shape A: 5,000 alerts filling 8 MiB (about 1.6 KiB each)
	shapeA := body(5000, func(i int) string {
		return alertJSON(i, map[string]string{"alertname": fmt.Sprintf("KubePodCrashLooping%d", i%40), "severity": []string{"critical", "warning", "info"}[i%3],
			"namespace": fmt.Sprintf("team-%03d", i%200), "pod": fmt.Sprintf("api-%05d", i), "container": "app", "cluster": "prd", "job": "kube-state-metrics",
			"instance": "10.0.0.1:8080", "service": "ksm", "endpoint": "http", "prometheus": "monitoring/k8s", "reason": "CrashLoopBackOff"},
			map[string]string{"summary": strings.Repeat("s", 220), "description": strings.Repeat("d", 620), "runbook_url": "https://runbooks.example/" + strings.Repeat("r", 60)})
	})
	// shape B: alerts at their display caps (64 labels of 256 B, a 512 B
	// summary and 4 KiB annotations), as many as fit in 8 MiB
	big := func(i int) string {
		labels := map[string]string{"alertname": fmt.Sprintf("Big%d", i), "severity": "critical", "namespace": "team", "pod": fmt.Sprintf("p-%d", i)}
		for j := 0; j < 60; j++ {
			labels[fmt.Sprintf("label_%02d", j)] = strings.Repeat("v", 256)
		}
		ann := map[string]string{"summary": strings.Repeat("s", 512)}
		for j := 0; j < 16; j++ {
			ann[fmt.Sprintf("note_%02d", j)] = strings.Repeat("a", 4096)
		}
		return alertJSON(i, labels, ann)
	}
	perBig := len(big(0)) + 1
	shapeB := body(wireCap/perBig-2, big) // later alerts carry longer numbers
	if len(shapeA) > wireCap || len(shapeB) > wireCap {
		t.Fatalf("a shape exceeds the wire cap: %d %d", len(shapeA), len(shapeB))
	}
	// the groups at their 20,000-appearance cap
	var gb strings.Builder
	gb.WriteString("[")
	for g := 0; g < 4; g++ {
		if g > 0 {
			gb.WriteString(",")
		}
		fmt.Fprintf(&gb, `{"labels":{"alertname":"g%d"},"receiver":{"name":"team-%d"},"alerts":[`, g, g)
		for i := 0; i < 5000; i++ {
			if i > 0 {
				gb.WriteString(",")
			}
			fmt.Fprintf(&gb, `{"fingerprint":"%016x","labels":{},"status":{"state":"active","silencedBy":[],"inhibitedBy":[],"mutedBy":[]}}`, i)
		}
		gb.WriteString("]}")
	}
	gb.WriteString("]")
	groupsBody := []byte(gb.String())

	red := safehttp.NewRedactor(safehttp.Bearer("budget-token"))
	read := func(alertsBody, groups []byte) alertmanager.Read {
		alerts, err := alertmanager.ParseAlerts(alertsBody)
		if err != nil {
			t.Fatal(err)
		}
		gs, err := alertmanager.ParseGroups(groups)
		if err != nil {
			t.Fatal(err)
		}
		r := alertmanager.Read{Identity: "id", Source: "src"}
		for _, x := range alertmanager.Reduce(alerts, gs) {
			r.Alerts = append(r.Alerts, alertmanager.Item{Subject: alertmanager.SubjectOf(x.Labels), Counted: x.Counted(), Display: alertmanager.Display(x, red)})
		}
		return r
	}
	heap := func() uint64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	for _, tc := range []struct {
		name           string
		alerts, groups []byte
		heapBound      uint64
	}{
		{"5,000 alerts in 8 MiB", shapeA, groupsBody, 48 << 20},
		{fmt.Sprintf("%d cap-sized alerts in 8 MiB", wireCap/perBig-2), shapeB, []byte("[]"), 32 << 20},
	} {
		before := heap()
		r := read(tc.alerts, tc.groups)
		retained := heap() - before
		rows := make([]AlertRow, 0, len(r.Alerts))
		for _, it := range r.Alerts {
			rows = append(rows, AlertRow{Alert: it.Display})
		}
		enc, _ := json.Marshal(rows)
		t.Logf("%s: wire %.1f MiB, retained %.1f MiB (bound %d MiB), rows encoded %.1f MiB, alerts %d",
			tc.name, float64(len(tc.alerts))/(1<<20), float64(retained)/(1<<20), tc.heapBound>>20, float64(len(enc))/(1<<20), len(r.Alerts))
		if retained > tc.heapBound {
			t.Errorf("%s: retained %d bytes, over the %d-byte bound", tc.name, retained, tc.heapBound)
		}
		runtime.KeepAlive(r)
	}

	// the part's evaluation: 5,000 alerts against 5,000 held pods, with rows
	r := read(shapeA, groupsBody)
	pods := make([]any, 0, 5000)
	for i := 0; i < 5000; i++ {
		pods = append(pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fmt.Sprintf("team-%03d", i%200), Name: fmt.Sprintf("api-%05d", i), UID: types.UID(fmt.Sprintf("uid-%d", i))}})
	}
	c := observe.Capture{Now: time.Now(), Rows: true, Objects: map[string][]any{SnapAlerts: {r}, KindPods: pods},
		Complete: map[string]bool{KindPods: true}, Current: map[string]bool{KindPods: true}}
	start := time.Now()
	const runs = 20
	for i := 0; i < runs; i++ {
		evalAlerts("c1", c)
	}
	per := time.Since(start) / runs
	t.Logf("evaluation with rows: %v per run (5,000 alerts, 5,000 pods)", per)
	// the time is a calibration, asserted only on request and without
	// -race (the race detector, and a loaded machine, distort every
	// timing; CI runs -race): KLYX_LOAD=1 go test ./internal/healthsrc
	// -run TestAlertsBudget -v. The memory bounds above always hold.
	if os.Getenv("KLYX_LOAD") != "" && per > 50*time.Millisecond {
		t.Errorf("evaluation took %v, over 50ms", per)
	}
}
