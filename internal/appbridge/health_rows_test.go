package appbridge

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// ---- the converter: the captured evaluation, never a new one (§2a) ---------

func TestCertificateRowsCopyTheCapturedEvaluation(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	// Ready and current at the capture; a conversion "later" must not
	// re-evaluate it into Expired - and a captured verdict that contradicts
	// the data wins outright.
	ready := certmanager.Certificate{Namespace: "shop", Name: "web", Ready: certmanager.Ready, NotAfter: at.Add(time.Second)}
	set := healthsrc.RowSet[certmanager.Certificate]{Rows: []healthsrc.Row[certmanager.Certificate]{
		{Ref: healthsrc.RowRef{UID: "u1", GVR: "cert-manager.io/v1/certificates", Namespace: "shop", Name: "web", Generation: 2},
			Eval: certmanager.CertificateRule(ready, at), Data: ready},
		{Ref: healthsrc.RowRef{UID: "u2", Namespace: "shop", Name: "flagged"},
			Eval: health.NativeEval{Included: true, Role: health.Fault, Label: "Flagged at capture"}, Data: certmanager.Certificate{Namespace: "shop", Name: "flagged", Ready: certmanager.Ready}},
	}}
	out, err := rowsConverters[healthsrc.TaskCerts+"/certificates"](set, at)
	if err != nil {
		t.Fatal(err)
	}
	dtos := out.([]CertificateDTO)
	if dtos[0].Rule.Role != "ok" || dtos[0].UID != "u1" || dtos[0].ExpiresInSeconds != 1 {
		t.Fatalf("the capture's rule and identity, relative fields at the capture: %+v", dtos[0])
	}
	if dtos[1].Rule != (RuleDTO{Included: true, Role: "fault", Label: "Flagged at capture"}) {
		t.Fatalf("the converter re-evaluated instead of copying: %+v", dtos[1].Rule)
	}
}

func TestSameNameRecreationIsANewRow(t *testing.T) {
	at := time.Now()
	c := certmanager.Certificate{Namespace: "shop", Name: "web", Ready: certmanager.Ready}
	set := healthsrc.RowSet[certmanager.Certificate]{Rows: []healthsrc.Row[certmanager.Certificate]{
		{Ref: healthsrc.RowRef{UID: "old", Namespace: "shop", Name: "web"}, Data: c},
		{Ref: healthsrc.RowRef{UID: "new", Namespace: "shop", Name: "web"}, Data: c},
	}}
	out, _ := rowsConverters[healthsrc.TaskCerts+"/certificates"](set, at)
	dtos := out.([]CertificateDTO)
	if dtos[0].UID == dtos[1].UID {
		t.Fatal("same name, different objects: the UID tells them apart")
	}
	b, err := json.Marshal(dtos[1])
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil || back["uid"] != "new" || back["rule"] == nil {
		t.Fatalf("serialization keeps identity and rule: %s", b)
	}
}

// ---- the transport on a real Observer ----------------------------------------

type rowsSinkRec struct {
	mu   sync.Mutex
	rows map[string][]RowsDTO
}

func (r *rowsSinkRec) Emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if dto, ok := data.(RowsDTO); ok {
		r.rows[name] = append(r.rows[name], dto)
	}
}

func (r *rowsSinkRec) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows[name])
}

func certObj(name, ready string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"name": name, "namespace": "shop", "uid": "uid-" + name, "generation": int64(1)},
		"spec":     map[string]any{"secretName": "tls", "issuerRef": map[string]any{"name": "le", "kind": "ClusterIssuer"}},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": ready, "reason": "R", "observedGeneration": int64(1)}}},
	}}
}

type obsHarness struct {
	o      *observe.Observer
	rec    *rowsSinkRec
	svc    *HealthService
	cancel context.CancelFunc
}

// startCertObserver runs a real Observer over certificates. gate, when set,
// holds every certificates evaluation until it is closed.
func startCertObserver(t *testing.T, cluster string, gate chan struct{}) *obsHarness {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{}
	for _, r := range []string{"certificates", "issuers", "clusterissuers"} {
		listKinds[schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: r}] = "List"
	}
	for _, r := range []string{"externalsecrets", "secretstores", "clustersecretstores"} {
		listKinds[schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1beta1", Resource: r}] = "List"
	}
	for _, r := range []string{"nodepools", "nodeclaims"} {
		listKinds[schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: r}] = "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	for _, c := range []*unstructured.Unstructured{certObj("ok", "True"), certObj("bad", "False"), certObj("odd", "Unknown")} {
		if _, err := dyn.Resource(gvr).Namespace("shop").Create(context.Background(), c, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	served := func(_ context.Context, group, _, fallback string) (observe.Probe, error) {
		return observe.Probe{Served: group == "cert-manager.io", Version: fallback}, nil
	}
	kinds, domains := healthsrc.Native(healthsrc.NativeDeps{Cluster: cluster, Dyn: dyn, Served: served})
	if gate != nil {
		for i := range domains {
			for j := range domains[i].Parts {
				p := &domains[i].Parts[j]
				if domains[i].ID == healthsrc.TaskCerts && p.ID == "certificates" {
					inner := p.Evaluate
					p.Evaluate = func(c observe.Capture) observe.PartResult {
						if c.Rows {
							<-gate
						}
						return inner(c)
					}
				}
			}
		}
	}
	rec := &rowsSinkRec{rows: map[string][]RowsDTO{}}
	o := observe.New(observe.Config{Cluster: cluster, Sink: NewHealthSink(rec), Kinds: kinds, Domains: domains, Throttle: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	h := &obsHarness{o: o, rec: rec, cancel: cancel}
	h.svc = NewHealthService(func() []*observe.Observer { return []*observe.Observer{h.o} })
	t.Cleanup(func() { cancel(); <-o.Done() })
	return h
}

func waitRows(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func live(h *obsHarness) func() bool {
	return func() bool {
		s, ok := h.o.Latest()
		if !ok {
			return false
		}
		for _, tk := range s.Tasks {
			if tk.ID == healthsrc.TaskCerts {
				return tk.Coverage == observe.Live
			}
		}
		return false
	}
}

func TestOpenRowsBootstrapsAndCountsPartitionTheRows(t *testing.T) {
	h := startCertObserver(t, "c1", nil)
	waitRows(t, "certs live", live(h))
	reply := h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "t1")
	if reply.Error != "" || reply.Rows == nil {
		t.Fatalf("open: %+v", reply)
	}
	dtos := reply.Rows.Rows.([]CertificateDTO)
	incl, roles := 0, map[string]int{}
	for _, d := range dtos {
		if d.Rule.Included {
			incl++
		}
		roles[d.Rule.Role]++
		if d.UID == "" {
			t.Fatalf("a row without identity: %+v", d)
		}
	}
	n := reply.Rows.Part.Native
	if n == nil || len(dtos) != n.Total || incl != n.Included || roles["fault"] != n.Roles.Fault || roles["ok"] != n.Roles.OK || roles["unknown"] != n.Roles.Unknown {
		t.Fatalf("rows and counts from one capture partition alike: rows %d incl %d roles %v; part %+v", len(dtos), incl, roles, n)
	}
	// Subscribed: the part's rows now flow as events too.
	name := HealthRowsEventPrefix + "c1:certs/certificates"
	waitRows(t, "rows events", func() bool { return h.rec.count(name) > 0 })
}

func TestTwoConsumersOneClosing(t *testing.T) {
	h := startCertObserver(t, "c1", nil)
	waitRows(t, "certs live", live(h))
	a := h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "a")
	b := h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "b")
	if a.Rows == nil || b.Rows == nil {
		t.Fatalf("both open: %+v %+v", a, b)
	}
	h.svc.CloseRows("a")
	if r := h.svc.RowsBootstrap("b", 7); r.Rows == nil || r.RequestGen != 7 {
		t.Fatalf("closing one consumer leaves the other: %+v", r)
	}
	if r := h.svc.RowsBootstrap("a", 8); !r.Closed {
		t.Fatalf("a closed token answers closed: %+v", r)
	}
}

func TestCloseThenReopenAndLateOpens(t *testing.T) {
	h := startCertObserver(t, "c1", nil)
	waitRows(t, "certs live", live(h))
	h.svc.CloseRows("t1") // the close beat its open
	if r := h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "t1"); !r.Closed {
		t.Fatalf("a late open of a closed token never resurrects it: %+v", r)
	}
	if r := h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "t2"); r.Rows == nil {
		t.Fatalf("a new token opens: %+v", r)
	}
	if r := h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "t2"); r.Rows == nil {
		t.Fatalf("a repeated open is a fresh bootstrap: %+v", r)
	}
	if r := h.svc.OpenRows("c1", healthsrc.TaskCerts, "issuers", "t2"); r.Error == "" {
		t.Fatal("a token belongs to one subscription")
	}
	h.svc.CloseRows("t2")
	h.svc.CloseRows("t2") // idempotent
}

func TestUnmountDuringBootstrap(t *testing.T) {
	gate := make(chan struct{})
	h := startCertObserver(t, "c1", gate)
	waitRows(t, "certs live", live(h))
	done := make(chan RowsReplyDTO, 1)
	go func() { done <- h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "t1") }()
	time.Sleep(50 * time.Millisecond) // the capture is held at the gate
	h.svc.CloseRows("t1")
	close(gate)
	if r := <-done; !r.Closed || r.Rows != nil {
		t.Fatalf("a bootstrap that finished after its close hands back nothing: %+v", r)
	}
}

func TestABAAndAReplacedObserver(t *testing.T) {
	a := startCertObserver(t, "A", nil)
	b := startCertObserver(t, "B", nil)
	waitRows(t, "A live", live(a))
	waitRows(t, "B live", live(b))
	svc := NewHealthService(func() []*observe.Observer { return []*observe.Observer{a.o, b.o} })
	r1 := svc.OpenRows("A", healthsrc.TaskCerts, "certificates", "t1")
	svc.CloseRows("t1")
	r2 := svc.OpenRows("B", healthsrc.TaskCerts, "certificates", "t2")
	svc.CloseRows("t2")
	r3 := svc.OpenRows("A", healthsrc.TaskCerts, "certificates", "t3")
	if r1.Rows == nil || r2.Rows == nil || r3.Rows == nil || r3.Rows.Cluster != "A" || r2.Rows.Cluster != "B" {
		t.Fatalf("A->B->A: %+v %+v %+v", r1, r2, r3)
	}
	if r := svc.RowsBootstrap("t1", 1); !r.Closed {
		t.Fatal("an old token's reply is refused")
	}
	// The Observer for A is replaced: a subscription on the old one ends;
	// a new open attaches to the new one, with its newer incarnation.
	a.cancel()
	<-a.o.Done()
	if r := svc.RowsBootstrap("t3", 2); r.Error == "" {
		t.Fatalf("a subscription on an ended Observer cannot bootstrap: %+v", r)
	}
	a2 := startCertObserver(t, "A", nil)
	waitRows(t, "A2 live", live(a2))
	svc2 := NewHealthService(func() []*observe.Observer { return []*observe.Observer{a2.o, b.o} })
	r4 := svc2.OpenRows("A", healthsrc.TaskCerts, "certificates", "t4")
	if r4.Rows == nil || r4.Rows.Incarnation <= r3.Rows.Incarnation {
		t.Fatalf("the replacement Observer's rows carry a newer incarnation: %+v", r4)
	}
}
