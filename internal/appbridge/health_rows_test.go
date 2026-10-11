package appbridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/externalsecrets"
	"github.com/moomora/klyx/internal/gitops/flux"
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

// The ESO parts (slice 3 §5.2): the captured evaluation and identity, and
// the absolute refresh time beside the capture-relative age - a pushed row
// is re-sent only when it changes, so the lens ticks the age itself (§3).
func TestExternalSecretRowsCarryTheCaptureAndAnAbsoluteRefreshTime(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	synced := externalsecrets.ExternalSecret{Namespace: "shop", Name: "db", State: externalsecrets.Synced,
		StoreKind: externalsecrets.ClusterSecretStoreKind, StoreName: "vault", RefreshInterval: time.Hour, RefreshTime: at.Add(-90 * time.Second)}
	never := externalsecrets.ExternalSecret{Namespace: "shop", Name: "new", State: externalsecrets.Failed, StoreName: "vault"}
	set := healthsrc.RowSet[externalsecrets.ExternalSecret]{Rows: []healthsrc.Row[externalsecrets.ExternalSecret]{
		{Ref: healthsrc.RowRef{UID: "e1", GVR: "external-secrets.io/v1beta1/externalsecrets", Namespace: "shop", Name: "db", Generation: 7},
			Eval: health.NativeEval{Included: true, Role: health.Degraded, Label: "Flagged at capture"}, Data: synced},
		{Ref: healthsrc.RowRef{UID: "e2", Namespace: "shop", Name: "new"}, Eval: externalsecrets.ExternalSecretRule(never), Data: never},
	}}
	out, err := rowsConverters[healthsrc.TaskESO+"/externalsecrets"](set, at)
	if err != nil {
		t.Fatal(err)
	}
	dtos := out.([]ExternalSecretDTO)
	if dtos[0].Rule != (RuleDTO{Included: true, Role: "degraded", Label: "Flagged at capture"}) {
		t.Fatalf("the converter re-evaluated instead of copying: %+v", dtos[0].Rule)
	}
	if dtos[0].UID != "e1" || dtos[0].GVR != "external-secrets.io/v1beta1/externalsecrets" || dtos[0].Generation != 7 {
		t.Fatalf("identity - UID, served GVR and the captured generation: %+v", dtos[0])
	}
	b, err := json.Marshal(dtos[0])
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil || back["uid"] != "e1" || back["generation"] != float64(7) || back["lastRefreshUnix"] == nil {
		t.Fatalf("serialization keeps identity, generation and the refresh time: %s", b)
	}
	if dtos[0].LastRefreshUnix != at.Add(-90*time.Second).Unix() || dtos[0].LastRefreshAgeSeconds != 90 || !dtos[0].Refreshed {
		t.Fatalf("refresh time: unix=%d age=%d refreshed=%v", dtos[0].LastRefreshUnix, dtos[0].LastRefreshAgeSeconds, dtos[0].Refreshed)
	}
	if dtos[1].LastRefreshUnix != 0 || dtos[1].Refreshed || dtos[1].UID != "e2" {
		t.Fatalf("a never-synced secret has no refresh time: %+v", dtos[1])
	}
}

func TestStoreRowsCarryTheCaptureAndIdentity(t *testing.T) {
	st := externalsecrets.Store{Kind: externalsecrets.SecretStoreKind, Namespace: "shop", Name: "vault", State: externalsecrets.Synced, Provider: "vault"}
	set := healthsrc.RowSet[externalsecrets.Store]{Rows: []healthsrc.Row[externalsecrets.Store]{
		{Ref: healthsrc.RowRef{UID: "s1", GVR: "external-secrets.io/v1beta1/secretstores", Namespace: "shop", Name: "vault", Generation: 3},
			Eval: health.NativeEval{Included: true, Role: health.Fault, Label: "Flagged at capture"}, Data: st},
	}}
	out, err := rowsConverters[healthsrc.TaskESO+"/stores"](set, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dtos := out.([]StoreDTO)
	if len(dtos) != 1 || dtos[0].UID != "s1" || dtos[0].GVR != "external-secrets.io/v1beta1/secretstores" || dtos[0].Generation != 3 || dtos[0].Provider != "vault" || dtos[0].Kind != "SecretStore" {
		t.Fatalf("store row: %+v", dtos)
	}
	if dtos[0].Rule != (RuleDTO{Included: true, Role: "fault", Label: "Flagged at capture"}) {
		t.Fatalf("the converter re-evaluated instead of copying: %+v", dtos[0].Rule)
	}
	if b, err := json.Marshal(dtos[0]); err != nil || !strings.Contains(string(b), `"generation":3`) || !strings.Contains(string(b), `"uid":"s1"`) {
		t.Fatalf("serialization keeps identity and generation: %s %v", b, err)
	}
	if _, err := rowsConverters[healthsrc.TaskESO+"/stores"]("not rows", time.Now()); err == nil {
		t.Fatal("a payload of the wrong type is an error, never an empty list")
	}
}

// The Flux parts (slice 3 §5.3): the captured evaluation and identity, and
// the absolute Ready transition time beside the capture-relative age (§3).
func TestFluxRowsCarryTheCaptureAndAnAbsoluteReadySince(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	ready := flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "apps", Ready: flux.Ready, ReadySince: at.Add(-2 * time.Minute), Generation: 1}
	set := healthsrc.RowSet[flux.Resource]{Rows: []healthsrc.Row[flux.Resource]{
		{Ref: healthsrc.RowRef{UID: "k1", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "flux-system", Name: "apps", Generation: 9},
			Eval: health.NativeEval{Included: true, Role: health.Fault, Label: "Flagged at capture"}, Data: ready},
	}}
	out, err := rowsConverters[healthsrc.TaskFlux+"/reconciliations"](set, at)
	if err != nil {
		t.Fatal(err)
	}
	d := out.([]FluxResourceDTO)[0]
	if d.Rule != (RuleDTO{Included: true, Role: "fault", Label: "Flagged at capture"}) {
		t.Fatalf("the converter re-evaluated instead of copying: %+v", d.Rule)
	}
	if d.UID != "k1" || d.GVR != "kustomize.toolkit.fluxcd.io/v1/kustomizations" || d.Generation != 9 {
		t.Fatalf("identity - the capture's generation, not the data's: %+v", d)
	}
	if d.ReadySinceUnix != at.Add(-2*time.Minute).Unix() || d.ReadySinceSeconds != 120 {
		t.Fatalf("ready since: unix=%d age=%d", d.ReadySinceUnix, d.ReadySinceSeconds)
	}
	if b, err := json.Marshal(d); err != nil || !strings.Contains(string(b), `"readySinceUnix":`) || !strings.Contains(string(b), `"generation":9`) {
		t.Fatalf("serialization: %s %v", b, err)
	}

	src := flux.Source{Kind: flux.GitRepositoryKind, Namespace: "flux-system", Name: "repo", Ready: flux.Ready, URL: "https://x/y"}
	sset := healthsrc.RowSet[flux.Source]{Rows: []healthsrc.Row[flux.Source]{
		{Ref: healthsrc.RowRef{UID: "g1", GVR: "source.toolkit.fluxcd.io/v1/gitrepositories", Generation: 2},
			Eval: health.NativeEval{Role: health.OK, Label: "Ready at capture"}, Data: src},
	}}
	sout, err := rowsConverters[healthsrc.TaskFlux+"/sources"](sset, at)
	if err != nil {
		t.Fatal(err)
	}
	sd := sout.([]FluxSourceDTO)[0]
	if sd.UID != "g1" || sd.GVR != "source.toolkit.fluxcd.io/v1/gitrepositories" || sd.Generation != 2 || sd.URL != "https://x/y" || sd.Rule.Label != "Ready at capture" {
		t.Fatalf("source row: %+v", sd)
	}
	if _, err := rowsConverters[healthsrc.TaskFlux+"/sources"]("not rows", at); err == nil {
		t.Fatal("a payload of the wrong type is an error, never an empty list")
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
	served := &atomic.Bool{}
	served.Store(true)
	return startCertObserverServed(t, cluster, gate, served)
}

// startCertObserverServed: served switches cert-manager's discovery, and
// kinds are re-probed every 20ms, so absence and reappearance are observed.
func startCertObserverServed(t *testing.T, cluster string, gate chan struct{}, servedFlag *atomic.Bool) *obsHarness {
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
		return observe.Probe{Served: group == "cert-manager.io" && servedFlag.Load(), Version: fallback}, nil
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
	o := observe.New(observe.Config{Cluster: cluster, Sink: NewHealthSink(rec), Kinds: kinds, Domains: domains, Throttle: 10 * time.Millisecond, ReprobeEvery: 20 * time.Millisecond})
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

// Codex infra review #3: an absent part is a valid publication - empty rows
// beside its absent Part - never a conversion error, through the real
// service and sink; live -> absent clears the rows, and reappearance
// brings them back.
func TestAbsentPartsAreRowsNotErrors(t *testing.T) {
	served := &atomic.Bool{}
	h := startCertObserverServed(t, "c1", nil, served) // cert-manager not served
	waitRows(t, "certs absent", func() bool {
		s, ok := h.o.Latest()
		if !ok {
			return false
		}
		for _, tk := range s.Tasks {
			if tk.ID == healthsrc.TaskCerts {
				return tk.Coverage == observe.Absent
			}
		}
		return false
	})
	reply := h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "t1")
	if reply.Error != "" || reply.Rows == nil || reply.Rows.Part.Coverage != observe.Absent || len(reply.Rows.Rows.([]CertificateDTO)) != 0 {
		t.Fatalf("an initially absent part opens with empty rows and its absent Part: %+v", reply)
	}
	name := HealthRowsEventPrefix + "c1:certs/certificates"
	served.Store(true)
	waitRows(t, "the kind appears: rows flow", func() bool {
		h.rec.mu.Lock()
		defer h.rec.mu.Unlock()
		l := h.rec.rows[name]
		return len(l) > 0 && l[len(l)-1].Part.Coverage == observe.Live && len(l[len(l)-1].Rows.([]CertificateDTO)) == 3
	})
	served.Store(false)
	waitRows(t, "live -> absent publishes empty rows and the absent Part", func() bool {
		h.rec.mu.Lock()
		defer h.rec.mu.Unlock()
		l := h.rec.rows[name]
		last := l[len(l)-1]
		return last.Part.Coverage == observe.Absent && len(last.Rows.([]CertificateDTO)) == 0
	})
	if _, err := rowsConverters[healthsrc.TaskCerts+"/certificates"]("not rows", time.Now()); err == nil {
		t.Fatal("a genuine shape error stays an error")
	}
}

// Codex infra review #5: a close cancels a bootstrap still waiting on its
// capture - it answers Closed at once, not when the capture finishes.
func TestCloseCancelsAWaitingBootstrap(t *testing.T) {
	gate := make(chan struct{})
	h := startCertObserver(t, "c1", gate)
	defer close(gate) // the loop is held at the gate until the test ends
	waitRows(t, "certs live", live(h))
	done := make(chan RowsReplyDTO, 1)
	go func() { done <- h.svc.OpenRows("c1", healthsrc.TaskCerts, "certificates", "t1") }()
	time.Sleep(50 * time.Millisecond) // the capture is held
	h.svc.CloseRows("t1")
	select {
	case r := <-done:
		if !r.Closed {
			t.Fatalf("a cancelled bootstrap answers Closed: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("the bootstrap kept waiting after its close")
	}
}
