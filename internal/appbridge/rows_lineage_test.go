package appbridge

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// ---- the lineage: deltas against the last published state (addendum §3.3) --

var podsKey = lineageKey{cluster: "c", incarnation: 1, task: healthsrc.TaskPods, part: "pods"}

func podsMsg(inc, ver uint64, rows []PodRowDTO) RowsDTO {
	return RowsDTO{Cluster: "c", Incarnation: inc, Task: healthsrc.TaskPods, Part: observe.Part{ID: "pods", Ver: ver}, Rows: rows}
}

// client is one subscriber: it holds a Ver and its rows by UID, and applies
// a message the way the frontend does.
type client struct {
	inc, ver uint64
	rows     map[string]PodRowDTO
	meta     RowsDTO
}

func (c *client) apply(m RowsDTO) error {
	if m.Incarnation == c.inc && m.Part.Ver <= c.ver {
		return nil // older or equal: ignored
	}
	if m.Full {
		c.rows = map[string]PodRowDTO{}
		for _, r := range m.Rows.([]PodRowDTO) {
			c.rows[r.UID] = r
		}
	} else {
		if m.Incarnation != c.inc || m.Base != c.ver {
			return fmt.Errorf("delta base %d/%d, held %d/%d", m.Incarnation, m.Base, c.inc, c.ver)
		}
		if ups, ok := m.Upserts.([]PodRowDTO); ok {
			for _, r := range ups {
				c.rows[r.UID] = r
			}
		}
		for _, uid := range m.Removed {
			delete(c.rows, uid)
		}
	}
	c.inc, c.ver, c.meta = m.Incarnation, m.Part.Ver, m
	return nil
}

func sameRows(t *testing.T, got map[string]PodRowDTO, want []PodRowDTO) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rows: %d, want %d", len(got), len(want))
	}
	for _, w := range want {
		if g, ok := got[w.UID]; !ok || !reflect.DeepEqual(g, w) {
			t.Fatalf("row %s: %+v, want %+v", w.UID, g, w)
		}
	}
}

func upsertUIDs(m RowsDTO) []string {
	var out []string
	if ups, ok := m.Upserts.([]PodRowDTO); ok {
		for _, r := range ups {
			out = append(out, r.UID)
		}
	}
	sort.Strings(out)
	return out
}

func podRow(uid, name string, restarts int) PodRowDTO {
	return PodRowDTO{Namespace: "ns", Name: name, UID: uid, Restarts: restarts, Health: HealthDTO{Kind: "ok", Label: "Running"},
		Containers: []PodRowContainerDTO{{Name: "app", Ready: true, Restarts: restarts, State: "running"}}}
}

// Under randomized churn, every delta applied to the previous state equals
// the next full; a consumer that joins from the lineage mid-stream applies
// every later message (its Ver is always one the broadcast published).
func TestDeltasReplayToTheFullUnderChurn(t *testing.T) {
	l := newRowsLineage()
	l.retain(podsKey)
	rng := rand.New(rand.NewPCG(1, 2))
	live := map[string]PodRowDTO{}
	next := 0
	snapshot := func() []PodRowDTO {
		out := make([]PodRowDTO, 0, len(live))
		for _, r := range live {
			out = append(out, r)
		}
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] }) // order is not part of the contract
		return out
	}
	for range 40 {
		next++
		live[fmt.Sprintf("u%d", next)] = podRow(fmt.Sprintf("u%d", next), fmt.Sprintf("p%d", next), 0)
	}
	var first, late client
	joined := false
	for ver := uint64(1); ver <= 300; ver++ {
		for range rng.IntN(6) {
			uids := make([]string, 0, len(live))
			for u := range live {
				uids = append(uids, u)
			}
			sort.Strings(uids)
			switch op := rng.IntN(4); {
			case op == 0 || len(uids) == 0: // add
				next++
				live[fmt.Sprintf("u%d", next)] = podRow(fmt.Sprintf("u%d", next), fmt.Sprintf("p%d", next), 0)
			case op == 1: // remove
				delete(live, uids[rng.IntN(len(uids))])
			case op == 2: // change
				u := uids[rng.IntN(len(uids))]
				r := live[u]
				r.Restarts++
				r.Containers = []PodRowContainerDTO{{Name: "app", Restarts: r.Restarts, State: "waiting:CrashLoopBackOff"}}
				live[u] = r
			default: // recreate: same name, new UID
				u := uids[rng.IntN(len(uids))]
				name := live[u].Name
				delete(live, u)
				next++
				live[fmt.Sprintf("u%d", next)] = podRow(fmt.Sprintf("u%d", next), name, 0)
			}
		}
		want := snapshot()
		msg, emit := l.publish(podsMsg(1, ver, want))
		if !emit {
			t.Fatalf("ver %d not emitted", ver)
		}
		if ver > 1 && msg.Full {
			t.Fatalf("ver %d: a valid set with a state travels as a delta", ver)
		}
		if err := first.apply(msg); err != nil {
			t.Fatalf("ver %d: %v", ver, err)
		}
		sameRows(t, first.rows, want)
		if joined {
			if err := late.apply(msg); err != nil {
				t.Fatalf("late consumer at ver %d: %v", ver, err)
			}
			sameRows(t, late.rows, want)
		}
		if ver == 150 {
			cur, ok := l.current(podsKey)
			if !ok || cur.Part.Ver != ver || !cur.Full {
				t.Fatalf("the lineage answers its published state in full: %+v %v", cur.Part, ok)
			}
			if err := late.apply(cur); err != nil {
				t.Fatal(err)
			}
			joined = true
		}
	}
}

// Time-stable rows: the real pod summaries and verdicts captured a minute
// apart, with one pod changed, upsert only that pod and the pods whose
// captured verdict text really changed - a restart note's minute and a
// pending pod crossing its start allowance. Ageing alone (and a pending
// pod's ageing message, which the row never sends) changes nothing.
func TestAdvancingClockUpsertsOnlyRealChanges(t *testing.T) {
	t1 := time.Unix(1_800_000_000, 0)
	t2 := t1.Add(time.Minute)
	old := metav1.NewTime(t1.Add(-2 * time.Hour))
	running := func(name string, restarts int32, lastFinished time.Time) corev1.Pod {
		cs := corev1.ContainerStatus{Name: "app", Ready: true, RestartCount: restarts, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
		if !lastFinished.IsZero() {
			cs.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.NewTime(lastFinished)}
		}
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID("uid-" + name), CreationTimestamp: old},
			Spec: corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "app"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &old, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: old}},
				ContainerStatuses: []corev1.ContainerStatus{cs}}}
	}
	pending := func(name string, created time.Time) corev1.Pod {
		c := metav1.NewTime(created)
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID("uid-" + name), CreationTimestamp: c},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	}
	pods := func(changedRestarts int32) []corev1.Pod {
		return []corev1.Pod{
			running("steady", 0, time.Time{}),
			pending("stuck", t1.Add(-time.Hour)), // overdue: its message ages, its row does not
			running("changed", changedRestarts, time.Time{}),
			running("noted", 1, t1.Add(-90*time.Second)),                      // "restarted 1m ago" -> "2m ago"
			pending("starting", t1.Add(-health.PodStartGrace+30*time.Second)), // progressing -> overdue at t2
		}
	}
	rowsAt := func(ps []corev1.Pod, now time.Time) RowsDTO {
		sums := workloads.SummarizePods(ps, now)
		set := healthsrc.RowSet[workloads.PodSummary]{}
		for _, s := range sums {
			set.Rows = append(set.Rows, healthsrc.Row[workloads.PodSummary]{Ref: healthsrc.RowRef{UID: string(s.UID), GVR: "v1/pods", Namespace: s.Namespace, Name: s.Name}, Data: s})
		}
		dto, ok := toRowsDTO(observe.Rows{Cluster: "c", Incarnation: 1, Task: healthsrc.TaskPods, Part: observe.Part{ID: "pods"}, EvaluatedAt: now, Rows: set})
		if !ok {
			t.Fatal("convert")
		}
		return dto
	}
	l := newRowsLineage()
	l.retain(podsKey)
	m1 := rowsAt(pods(0), t1)
	m1.Part.Ver = 1
	if _, emit := l.publish(m1); !emit {
		t.Fatal("first")
	}
	m2 := rowsAt(pods(3), t2)
	m2.Part.Ver = 2
	msg, _ := l.publish(m2)
	if msg.Full || len(msg.Removed) != 0 {
		t.Fatalf("a delta with nothing removed: %+v", msg)
	}
	if got, want := upsertUIDs(msg), []string{"uid-changed", "uid-noted", "uid-starting"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upserts %v, want %v", got, want)
	}
	// and a third capture with the clock advanced alone upserts only the note
	m3 := rowsAt(pods(3), t2.Add(time.Minute))
	m3.Part.Ver = 3
	if msg, _ := l.publish(m3); !reflect.DeepEqual(upsertUIDs(msg), []string{"uid-noted"}) {
		t.Fatalf("ageing alone: upserts %v", upsertUIDs(msg))
	}
}

func TestRemovalAndRecreationTravelByUID(t *testing.T) {
	l := newRowsLineage()
	l.retain(podsKey)
	var c client
	_ = c.apply(mustPublish(t, l, podsMsg(1, 1, []PodRowDTO{podRow("a", "web", 0), podRow("b", "api", 0)})))
	msg := mustPublish(t, l, podsMsg(1, 2, []PodRowDTO{podRow("a2", "web", 0)})) // web recreated, api removed
	if msg.Full || !reflect.DeepEqual(upsertUIDs(msg), []string{"a2"}) || !reflect.DeepEqual(msg.Removed, []string{"a", "b"}) {
		t.Fatalf("recreation: upserts %v removed %v", upsertUIDs(msg), msg.Removed)
	}
	if err := c.apply(msg); err != nil {
		t.Fatal(err)
	}
	sameRows(t, c.rows, []PodRowDTO{podRow("a2", "web", 0)})
}

func mustPublish(t *testing.T, l *rowsLineage, dto RowsDTO) RowsDTO {
	t.Helper()
	msg, emit := l.publish(dto)
	if !emit {
		t.Fatalf("ver %d not emitted", dto.Part.Ver)
	}
	return msg
}

// A row without a UID, or two rows sharing one, cannot be keyed: the set
// travels in full, and so does the next one.
func TestEmptyOrDuplicateUIDsForceFull(t *testing.T) {
	l := newRowsLineage()
	l.retain(podsKey)
	mustPublish(t, l, podsMsg(1, 1, []PodRowDTO{podRow("a", "web", 0)}))
	if m := mustPublish(t, l, podsMsg(1, 2, []PodRowDTO{podRow("a", "web", 0), podRow("", "x", 0)})); !m.Full {
		t.Fatal("an empty UID forces full")
	}
	if m := mustPublish(t, l, podsMsg(1, 3, []PodRowDTO{podRow("a", "web", 0)})); !m.Full {
		t.Fatal("after an unkeyed set, the next is full")
	}
	if m := mustPublish(t, l, podsMsg(1, 4, []PodRowDTO{podRow("a", "web", 0), podRow("a", "web-dup", 0)})); !m.Full {
		t.Fatal("a duplicate UID forces full")
	}
	mustPublish(t, l, podsMsg(1, 5, []PodRowDTO{podRow("a", "web", 0)}))
	if m := mustPublish(t, l, podsMsg(1, 6, []PodRowDTO{podRow("a", "web", 1)})); m.Full {
		t.Fatal("keyed again: a delta")
	}
}

// An older or equal Ver is never emitted; a newer incarnation publishes
// full and drops the older one's state; a Final drops its states; the last
// close drops a key's state; an unretained key keeps none and goes full.
func TestLineageVersionsIncarnationsAndLifecycle(t *testing.T) {
	l := newRowsLineage()
	l.retain(podsKey)
	mustPublish(t, l, podsMsg(1, 5, []PodRowDTO{podRow("a", "web", 0)}))
	for _, v := range []uint64{5, 4} {
		if _, emit := l.publish(podsMsg(1, v, []PodRowDTO{podRow("a", "web", 9)})); emit {
			t.Fatalf("ver %d (held 5) emitted", v)
		}
	}
	// a second consumer of the same key shares the state; releasing one keeps it
	l.retain(podsKey)
	l.release(podsKey)
	if _, ok := l.current(podsKey); !ok {
		t.Fatal("one consumer left: the state stays")
	}
	// a newer incarnation: its first message is full, the old state is gone
	k2 := podsKey
	k2.incarnation = 2
	l.retain(k2)
	if m := mustPublish(t, l, podsMsg(2, 1, []PodRowDTO{podRow("a", "web", 0)})); !m.Full {
		t.Fatal("a new incarnation starts full")
	}
	if _, ok := l.current(podsKey); ok || l.states[podsKey] != nil {
		t.Fatal("the older incarnation's state is dropped")
	}
	if m := mustPublish(t, l, podsMsg(1, 6, []PodRowDTO{podRow("a", "web", 1)})); !m.Full {
		t.Fatal("an older incarnation keeps no lineage")
	}
	if _, ok := l.current(podsKey); ok {
		t.Fatal("no state is kept for an older incarnation")
	}
	// Final, through the sink, drops the incarnation's states and fences
	// later seeds
	sink := &HealthSink{em: &recEmitter{}, lineage: l}
	sink.Summary(observe.Summary{Cluster: "c", Incarnation: 2, Final: true})
	if _, ok := l.current(k2); ok {
		t.Fatal("a Final drops its states")
	}
	if l.seedFrom(podsMsg(2, 3, []PodRowDTO{podRow("a", "web", 0)})); len(l.states) != 0 {
		t.Fatal("a finalized incarnation is never seeded")
	}
	// the last close drops the state; an unretained key keeps none
	k3 := podsKey
	k3.incarnation = 3
	l.retain(k3)
	mustPublish(t, l, podsMsg(3, 1, []PodRowDTO{podRow("a", "web", 0)}))
	l.release(k3)
	if _, ok := l.current(k3); ok || len(l.states) != 0 {
		t.Fatal("the last close drops the state")
	}
	if m := mustPublish(t, l, podsMsg(3, 2, []PodRowDTO{podRow("a", "web", 1)})); !m.Full || len(l.states) != 0 {
		t.Fatal("unretained: full, no state")
	}
	// native parts never travel as deltas
	cert := lineageKey{cluster: "c", incarnation: 3, task: healthsrc.TaskCerts, part: "certificates"}
	l.retain(cert)
	for v := uint64(1); v <= 2; v++ {
		m, _ := l.publish(RowsDTO{Cluster: "c", Incarnation: 3, Task: healthsrc.TaskCerts, Part: observe.Part{ID: "certificates", Ver: v}, Rows: []CertificateDTO{}})
		if !m.Full {
			t.Fatal("a native part keeps full pushes")
		}
	}
}

// An empty delta still carries a fact-only, freshness-only or coverage-only
// change - atomically, beside the (absent) row changes.
func TestEmptyDeltaCarriesTheMetadata(t *testing.T) {
	l := newRowsLineage()
	l.retain(podsKey)
	rows := []PodRowDTO{podRow("a", "web", 0)}
	base := podsMsg(1, 1, rows)
	base.EvaluatedAt = time.Unix(100, 0)
	base.Facts = map[string]any{"fluxPresent": false}
	mustPublish(t, l, base)
	cases := []func(*RowsDTO){
		func(d *RowsDTO) { d.Facts = map[string]any{"fluxPresent": true} },
		func(d *RowsDTO) { d.EvaluatedAt = time.Unix(200, 0) },
		func(d *RowsDTO) { d.Part.Coverage = observe.Stale },
	}
	for i, change := range cases {
		d := base
		d.Part.Ver = uint64(2 + i)
		change(&d)
		m := mustPublish(t, l, d)
		if m.Full || len(upsertUIDs(m)) != 0 || len(m.Removed) != 0 || m.Base != uint64(1+i) {
			t.Fatalf("case %d: an empty delta: %+v", i, m)
		}
		if !reflect.DeepEqual(m.Facts, d.Facts) || !m.EvaluatedAt.Equal(d.EvaluatedAt) || m.Part.Coverage != d.Part.Coverage {
			t.Fatalf("case %d: the metadata travels on the empty delta: %+v", i, m)
		}
		b, _ := json.Marshal(m)
		var wire map[string]any
		_ = json.Unmarshal(b, &wire)
		if wire["full"] != false || wire["base"] == nil {
			t.Fatalf("case %d: wire %s", i, b)
		}
	}
}

// A bootstrap with no state seeds the lineage with its capture; a broadcast
// that published first wins, even when the capture ran ahead of it.
func TestSeedFromAndTheBroadcastRace(t *testing.T) {
	l := newRowsLineage()
	l.retain(podsKey)
	got := l.seedFrom(podsMsg(1, 4, []PodRowDTO{podRow("a", "web", 0)}))
	if !got.Full || got.Part.Ver != 4 {
		t.Fatalf("the capture is the reply: %+v", got.Part)
	}
	if cur, ok := l.current(podsKey); !ok || cur.Part.Ver != 4 {
		t.Fatal("and the published state")
	}
	if _, emit := l.publish(podsMsg(1, 4, []PodRowDTO{podRow("a", "web", 0)})); emit {
		t.Fatal("the owed broadcast of the seeded Ver is held already")
	}
	if m := mustPublish(t, l, podsMsg(1, 5, []PodRowDTO{podRow("a", "web", 1)})); m.Full || m.Base != 4 {
		t.Fatalf("the next delta chains from the seed: %+v", m)
	}

	l2 := newRowsLineage()
	l2.retain(podsKey)
	mustPublish(t, l2, podsMsg(1, 3, []PodRowDTO{podRow("a", "web", 0)})) // the broadcast landed first
	if got := l2.seedFrom(podsMsg(1, 4, []PodRowDTO{podRow("a", "web", 1)})); got.Part.Ver != 3 {
		t.Fatalf("a capture ahead of the published state never answers: %+v", got.Part)
	}
	if m := mustPublish(t, l2, podsMsg(1, 4, []PodRowDTO{podRow("a", "web", 1)})); m.Base != 3 {
		t.Fatal("the broadcast of the captured Ver chains from the published state")
	}

	l3 := newRowsLineage() // unretained (the subscription closed): no state
	if got := l3.seedFrom(podsMsg(1, 4, nil)); !got.Full || len(l3.states) != 0 {
		t.Fatal("an unretained key is answered but never seeded")
	}
}
