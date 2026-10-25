package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/readfence"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Settings F1 implementation review, findings F2-F4 and F6 (Codex's probes,
// adopted): the real typed and dynamic clients over a fake round tripper
// behind the real fence, so the multi-write orchestration and its returned
// outcomes are what is tested, not a fake client's.

type f1RT func(*http.Request) (*http.Response, error)

func (f f1RT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func f1JSON(req *http.Request, code int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b))), Request: req}
}

func f1Conn(t *testing.T, rt f1RT) *ClusterConn {
	t.Helper()
	cfg := &rest.Config{Host: "https://cluster.invalid", Transport: readfence.Transport(rt)}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return NewClusterConn("c", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
}

// f1Fence is a fence that holds until moved.
type f1Fence struct {
	mu    sync.Mutex
	moved bool
}

func (f *f1Fence) move() { f.mu.Lock(); f.moved = true; f.mu.Unlock() }
func (f *f1Fence) check() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moved {
		return errors.New("c: its policy changed")
	}
	return nil
}

func (f *f1Fence) ctx() context.Context {
	return readfence.WithWriteTracker(readfence.With(context.Background(), f.check))
}

var f1CoreV1 = &metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list", "delete", "create"}}}}

const f1PodDoc = "apiVersion: v1\nkind: Pod\nmetadata:\n  name: probe\n  namespace: ns\n"

// F2: the composer's discovery is read under the document's fence - cold
// (nothing cached: no GET goes out) and warm (a cached answer is not used
// past it) - and its refusal is said as itself, never "not served".
func TestTheComposersDiscoveryCarriesItsFence(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "warm"}[warm], func(t *testing.T) {
			var calls []string
			c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path == "/api/v1" {
					return f1JSON(r, 200, f1CoreV1), nil
				}
				return nil, fmt.Errorf("unexpected request %s", r.URL.Path)
			})
			if warm {
				if _, err := c.discoveryFor(context.Background()).ServerResourcesForGroupVersion("v1"); err != nil {
					t.Fatal(err)
				}
				if _, err := c.discoveryFor(context.Background()).ServerResourcesForGroupVersion("v1"); err != nil || len(calls) != 1 {
					t.Fatalf("control: the second read was not cached: %v %v", calls, err)
				}
				calls = nil
			}
			f := &f1Fence{}
			f.move()
			out := c.CreateResourceYAML(f.ctx(), f1PodDoc, false)
			if len(calls) != 0 {
				t.Fatalf("reads after the fence moved: %v", calls)
			}
			if len(out) != 1 || out[0].OK || strings.Contains(out[0].Error, "not served") || !strings.Contains(out[0].Error, "policy changed") {
				t.Fatalf("the fence's refusal is not said as itself: %+v", out)
			}
			if !errors.Is(out[0].Refusal(), readfence.ErrNotForwarded) {
				t.Fatalf("nothing was sent, but not said so: %v", out[0].Refusal())
			}
		})
	}
}

// F2: a Flux verb's version resolution is read under its fence too.
func TestAFluxVerbsDiscoveryCarriesItsFence(t *testing.T) {
	calls := 0
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	f := &f1Fence{}
	f.move()
	err := c.Reconcile(f.ctx(), "Kustomization", "flux-system", "app", "uid-app")
	if calls != 0 {
		t.Fatalf("%d requests after the fence moved", calls)
	}
	if out, notSent := WriteOutcome(err); out != OutcomeRefused || !notSent || !readfence.Failed(err) {
		t.Fatalf("classified %s notSent=%v: %v", out, notSent, err)
	}
}

// F3: the scratch namespace and the pod are two writes. A pod never sent
// says so after the namespace's create was forwarded, and says the
// namespace was created; after an existing namespace it says nothing of
// one.
func TestTheDiagnosticPodHasItsOwnTracker(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "created", true: "existing"}[existing], func(t *testing.T) {
			f := &f1Fence{}
			var calls []string
			c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces" {
					f.move()
					if existing {
						return f1JSON(r, 409, metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonAlreadyExists, Code: 409,
							Details: &metav1.StatusDetails{Name: ScratchNamespace, Kind: "namespaces"}, Message: "namespaces \"klyx-scratch\" already exists"}), nil
					}
					return f1JSON(r, 201, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ScratchNamespace}}), nil
				}
				return nil, fmt.Errorf("unexpected %s", r.URL.Path)
			})
			out := c.LaunchDiagnosticPod(f.ctx(), "probe", "busybox", 60)
			if len(calls) != 1 || out.OK {
				t.Fatalf("control: calls=%v result=%+v", calls, out)
			}
			if !errors.Is(out.Refusal(), readfence.ErrNotForwarded) {
				t.Fatalf("a pod never sent inherited the namespace's forwarding: %v", out.Refusal())
			}
			if said := strings.Contains(out.Error, "namespace was created"); said == existing {
				t.Fatalf("the namespace's own outcome: %q", out.Error)
			}
		})
	}
}

// F3: a lost answer of the pod's own create stays unverified - its retry is
// refused after the first attempt was forwarded.
func TestTheDiagnosticPodsLostAnswerStaysUnverified(t *testing.T) {
	f := &f1Fence{}
	pods := 0
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces":
			return f1JSON(r, 201, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ScratchNamespace}}), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pods"):
			pods++
			f.move()
			resp := f1JSON(r, 503, metav1.Status{Status: metav1.StatusFailure, Code: 503, Reason: metav1.StatusReasonServiceUnavailable})
			resp.Header.Set("Retry-After", "0")
			return resp, nil
		}
		return nil, fmt.Errorf("unexpected %s", r.URL.Path)
	})
	out := c.LaunchDiagnosticPod(f.ctx(), "probe", "busybox", 60)
	if pods != 1 || out.OK {
		t.Fatalf("control: %d pod POSTs, %+v", pods, out)
	}
	if !readfence.Failed(out.Refusal()) {
		t.Fatalf("control: the retry was not refused by the fence: %v", out.Refusal())
	}
	if errors.Is(out.Refusal(), readfence.ErrNotForwarded) {
		t.Fatal("the pod's first attempt was forwarded: never 'not sent'")
	}
}

// F4: a composite reconcile's second write, never forwarded, is a step
// refused and not sent - beside its written source.
func TestACompositeStepSaysItsWriteWasNotSent(t *testing.T) {
	f := &f1Fence{}
	patches := 0
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return f1JSON(r, 200, seedKustomizationWithSource("app", "GitRepository", "flux-system")), nil
		}
		if r.Method == http.MethodPatch {
			patches++
			f.move()
			return f1JSON(r, 200, seedGitRepoSource("flux-system")), nil
		}
		return nil, fmt.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	c.disc = f1Disc{}
	steps, err := c.ReconcileWithSourceSteps(f.ctx(), "Kustomization", "flux-system", "app", "uid-app")
	if err == nil || len(steps) != 2 || patches != 1 || !steps[0].Written {
		t.Fatalf("control: patches=%d steps=%+v err=%v", patches, steps, err)
	}
	if !errors.Is(steps[1].Err, readfence.ErrNotForwarded) {
		t.Fatalf("control: no not-forwarded marker: %v", steps[1].Err)
	}
	if !steps[1].Refused || !steps[1].NotSent || steps[1].Written {
		t.Fatalf("a never-sent target reads as unverified: %+v", steps[1])
	}
	if steps[0].NotSent || steps[0].Refused {
		t.Fatalf("the written source: %+v", steps[0])
	}
	if !strings.Contains(err.Error(), "was already asked to reconcile") {
		t.Fatalf("the source's outcome is lost: %v", err)
	}
}

type f1Disc struct{}

func (f1Disc) ServerGroups() (*metav1.APIGroupList, error) {
	gv := func(g string) metav1.APIGroup {
		v := metav1.GroupVersionForDiscovery{Version: "v1", GroupVersion: g + "/v1"}
		return metav1.APIGroup{Name: g, PreferredVersion: v, Versions: []metav1.GroupVersionForDiscovery{v}}
	}
	return &metav1.APIGroupList{Groups: []metav1.APIGroup{gv("kustomize.toolkit.fluxcd.io"), gv("source.toolkit.fluxcd.io")}}, nil
}

func (f1Disc) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	return &metav1.APIResourceList{GroupVersion: gv, APIResources: []metav1.APIResource{{Name: "kustomizations", Kind: "Kustomization", Namespaced: true}, {Name: "gitrepositories", Kind: "GitRepository", Namespaced: true}}}, nil
}

// F6: a refresh fenced at its first GET sent nothing: refused, not sent.
func TestARefusalBeforeTheWritesReadIsNotSent(t *testing.T) {
	calls := 0
	c := f1Conn(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected forwarding")
	})
	f := &f1Fence{}
	f.move()
	err := c.RefreshArgoApp(f.ctx(), "ns", "app", "uid")
	if calls != 0 || err == nil {
		t.Fatalf("control: %d calls, %v", calls, err)
	}
	if out, notSent := WriteOutcome(err); out != OutcomeRefused || !notSent {
		t.Fatalf("no request was forwarded, but classified %s notSent=%v: %v", out, notSent, err)
	}
}

// F2: version resolution never falls back past a fence - per-resource
// (servedVersion) or among candidates (servedResourceAmong), cold or warm.
func TestVersionResolutionNeverFallsBackPastAFence(t *testing.T) {
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected %s", r.URL.Path)
	})
	c.disc = f1Disc{}
	open := c.discoveryFor(context.Background())
	if v, served, err := servedVersion(open, "source.toolkit.fluxcd.io", "gitrepositories", "v9"); err != nil || !served || v != "v1" {
		t.Fatalf("control: (%q, %v, %v)", v, served, err)
	}
	f := &f1Fence{}
	f.move()
	disc := c.discoveryFor(f.ctx())
	if v, _, err := servedVersion(disc, "source.toolkit.fluxcd.io", "gitrepositories", "v9"); !readfence.Failed(err) || v != "" {
		t.Fatalf("servedVersion fell back past the fence: (%q, %v)", v, err)
	}
	if v, _, err := servedResourceAmong(disc, "source.toolkit.fluxcd.io", "gitrepositories", []string{"v9"}); !readfence.Failed(err) || v != "" {
		t.Fatalf("servedResourceAmong fell back past the fence: (%q, %v)", v, err)
	}
	if _, _, err := c.sourceServedGVR(f.ctx(), "GitRepository"); !readfence.Failed(err) {
		t.Fatalf("sourceServedGVR: %v", err)
	}
}

// f1MovingDisc moves its fence once a read completes: "groups", or the group
// version whose resources read moves it.
type f1MovingDisc struct {
	f1Disc
	f  *f1Fence
	at string
}

func (d f1MovingDisc) ServerGroups() (*metav1.APIGroupList, error) {
	g, err := d.f1Disc.ServerGroups()
	if d.at == "groups" {
		d.f.move()
	}
	return g, err
}

func (d f1MovingDisc) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	rl, err := d.f1Disc.ServerResourcesForGroupVersion(gv)
	if d.at == gv {
		d.f.move()
	}
	return rl, err
}

// F2: a fence moving between the group read and a version's resources read
// is the error too - never "unreadable, so the fallback".
func TestAFenceMovingBetweenDiscoveryReadsIsTheError(t *testing.T) {
	for _, among := range []bool{false, true} {
		c := f1Conn(t, func(r *http.Request) (*http.Response, error) { return nil, fmt.Errorf("unexpected %s", r.URL.Path) })
		f := &f1Fence{}
		c.disc = f1MovingDisc{f: f, at: "groups"}
		disc := c.discoveryFor(f.ctx())
		var v string
		var err error
		if among {
			v, _, err = servedResourceAmong(disc, "source.toolkit.fluxcd.io", "gitrepositories", []string{"v1"})
		} else {
			v, _, err = servedVersion(disc, "source.toolkit.fluxcd.io", "gitrepositories", "v9")
		}
		if !readfence.Failed(err) || v != "" {
			t.Fatalf("among=%v: (%q, %v)", among, v, err)
		}
	}
}

// F2: a move after the target's read stops the source's discovery: nothing
// is written and no step is reported.
func TestAMoveBeforeTheSourcesDiscoveryWritesNothing(t *testing.T) {
	f := &f1Fence{}
	var calls []string
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method)
		if r.Method == http.MethodGet {
			f.move()
			return f1JSON(r, 200, seedKustomizationWithSource("app", "GitRepository", "flux-system")), nil
		}
		return nil, fmt.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	c.disc = f1Disc{}
	steps, err := c.ReconcileWithSourceSteps(f.ctx(), "Kustomization", "flux-system", "app", "uid-app")
	if len(calls) != 1 || len(steps) != 0 {
		t.Fatalf("written after the move: calls=%v steps=%+v", calls, steps)
	}
	if out, notSent := WriteOutcome(err); out != OutcomeRefused || !notSent || !readfence.Failed(err) {
		t.Fatalf("classified %s notSent=%v: %v", out, notSent, err)
	}
}

// F4: a source the fence stopped before its write is a not-sent step, and
// the error says so of it.
func TestASourceStoppedBeforeItsWriteSaysNotSent(t *testing.T) {
	f := &f1Fence{}
	patches := 0
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return f1JSON(r, 200, seedKustomizationWithSource("app", "GitRepository", "flux-system")), nil
		}
		patches++
		return nil, fmt.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	c.disc = f1MovingDisc{f: f, at: "source.toolkit.fluxcd.io/v1"}
	steps, err := c.ReconcileWithSourceSteps(f.ctx(), "Kustomization", "flux-system", "app", "uid-app")
	if patches != 0 || len(steps) != 2 {
		t.Fatalf("control: patches=%d steps=%+v", patches, steps)
	}
	if !steps[0].NotSent || !steps[0].Refused || !steps[1].NotSent {
		t.Fatalf("not-sent steps: %+v", steps)
	}
	if err == nil || !strings.Contains(err.Error(), "was not sent either") {
		t.Fatalf("the source's wording: %v", err)
	}
}

// F2: an ArtifactGenerator verb's version check is read under its fence.
func TestAGeneratorVerbsVersionCheckCarriesItsFence(t *testing.T) {
	calls := 0
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	f := &f1Fence{}
	f.move()
	err := c.FluxOpAct(f.ctx(), "ArtifactGenerator", "flux-system", "gen", "uid-gen", FluxOpReconcile)
	if calls != 0 || !readfence.Failed(err) {
		t.Fatalf("%d requests after the fence moved: %v", calls, err)
	}
	if out, notSent := WriteOutcome(err); out != OutcomeRefused || !notSent {
		t.Fatalf("classified %s notSent=%v", out, notSent)
	}
}

// f1HPA is an HPA whose scale target resolves through discovery.
func f1HPA() map[string]any {
	return map[string]any{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler",
		"metadata": map[string]any{"name": "scale", "namespace": "ns", "uid": "hpa-id",
			"ownerReferences": []any{map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject", "name": "so", "uid": "so-id"}}},
		"spec": map[string]any{"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"}}}
}

// Settings F1 re-review, RR1 (Codex's probe, adopted, with a warm
// control): an instance detail's related references resolve through
// discovery under the read's fence. A move after the root read stops the
// detail - no discovery request, cached or not, and never a core-kind guess
// - and the detail is the fence's refusal.
func TestAnInstanceDetailsRelatedDiscoveryKeepsItsFence(t *testing.T) {
	const root = "/apis/autoscaling/v2/namespaces/ns/horizontalpodautoscalers/scale"
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "warm"}[warm], func(t *testing.T) {
			f := &f1Fence{}
			moveOnRoot := false
			var calls []string
			c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case root:
					if moveOnRoot {
						f.move()
					}
					return f1JSON(r, 200, f1HPA()), nil
				case "/apis/apps/v1":
					return f1JSON(r, 200, &metav1.APIResourceList{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true}}}), nil
				case "/apis/keda.sh/v1alpha1":
					return f1JSON(r, 200, &metav1.APIResourceList{GroupVersion: "keda.sh/v1alpha1", APIResources: []metav1.APIResource{{Name: "scaledobjects", Kind: "ScaledObject", Namespaced: true}}}), nil
				}
				return f1JSON(r, 404, metav1.Status{Status: metav1.StatusFailure, Code: 404, Reason: metav1.StatusReasonNotFound}), nil
			})
			if warm {
				d, err := c.GetInstanceDetail(context.Background(), "autoscaling", "v2", "horizontalpodautoscalers", "ns", "scale")
				if err != nil {
					t.Fatal(err)
				}
				linked := map[string]bool{}
				for _, r := range d.Related {
					linked[r.Relation+" "+r.Plural] = true
				}
				if !linked["scale target deployments"] || !linked["owner scaledobjects"] {
					t.Fatalf("control: the references resolve through discovery: %+v", d.Related)
				}
			}
			calls = nil
			moveOnRoot = true
			_, err := c.GetInstanceDetail(f.ctx(), "autoscaling", "v2", "horizontalpodautoscalers", "ns", "scale")
			for _, call := range calls {
				if strings.HasPrefix(call, "GET /apis/apps") || strings.HasPrefix(call, "GET /apis/keda.sh") {
					t.Fatalf("discovery after the move: %v", calls)
				}
			}
			if !readfence.Failed(err) {
				t.Fatalf("a detail read under a moved fence: %v (calls %v)", err, calls)
			}
		})
	}
}

// RR1: a related reference never falls back to the core-kind table past a
// fence - an owner, a scale target or an authentication reference.
func TestARelatedReferenceNeverGuessesPastAFence(t *testing.T) {
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) { return nil, fmt.Errorf("unexpected %s", r.URL.Path) })
	c.disc = f1Disc{}
	if ref, ok, err := c.refFor(context.Background(), "apps/v1", "Deployment", "ns", "web", "owner"); err != nil || !ok || ref.Plural != "deployments" {
		t.Fatalf("control: the core-kind fallback: %+v %v %v", ref, ok, err)
	}
	f := &f1Fence{}
	f.move()
	if _, ok, err := c.refFor(f.ctx(), "apps/v1", "Deployment", "ns", "web", "owner"); ok || !readfence.Failed(err) {
		t.Fatalf("a core-kind guess past the fence: %v %v", ok, err)
	}
	hpa := &unstructured.Unstructured{Object: f1HPA()}
	if _, err := c.relatedRefs(f.ctx(), "autoscaling", "v2", "horizontalpodautoscalers", "ns", "scale", hpa); !readfence.Failed(err) {
		t.Fatalf("an owner past the fence: %v", err)
	}
	if _, err := c.ownerRefs(f.ctx(), "ns", hpa); !readfence.Failed(err) {
		t.Fatalf("ownerRefs: %v", err)
	}
	sj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledJob",
		"metadata": map[string]any{"name": "job", "namespace": "ns"},
		"spec":     map[string]any{"triggers": []any{map[string]any{"type": "prometheus", "authenticationRef": map[string]any{"name": "ta"}}}}}}
	if _, err := c.relatedRefs(f.ctx(), "keda.sh", "v1alpha1", "scaledjobs", "ns", "job", sj); !readfence.Failed(err) {
		t.Fatalf("an authentication reference past the fence: %v", err)
	}
}

// RR1: a component read that degrades to nothing under a moved fence - a
// Service's backing - leaves the detail as the fence's refusal, never a
// partial answer.
func TestADegradedComponentReadIsTheFencesRefusal(t *testing.T) {
	f := &f1Fence{}
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/namespaces/ns/services/web" {
			f.move()
			return f1JSON(r, 200, map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "web", "namespace": "ns", "uid": "svc-id"}}), nil
		}
		return f1JSON(r, 404, metav1.Status{Status: metav1.StatusFailure, Code: 404, Reason: metav1.StatusReasonNotFound}), nil
	})
	if _, err := c.GetInstanceDetail(f.ctx(), "", "v1", "services", "ns", "web"); !readfence.Failed(err) {
		t.Fatalf("a partial detail under a moved fence: %v", err)
	}
}

// RR1: a refusal of a related reference's discovery ends the detail even
// when the fence holds again by its end.
func TestARelatedRefusalEndsTheDetail(t *testing.T) {
	var mu sync.Mutex
	failNext := false
	fence := func() error {
		mu.Lock()
		defer mu.Unlock()
		if failNext {
			failNext = false
			return errors.New("c: its policy changed")
		}
		return nil
	}
	c := f1Conn(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/apis/autoscaling/v2/namespaces/ns/horizontalpodautoscalers/scale" {
			mu.Lock()
			failNext = r.Header.Get("Accept") == "" || !strings.Contains(r.Header.Get("Accept"), "as=Table")
			mu.Unlock()
			return f1JSON(r, 200, f1HPA()), nil
		}
		return f1JSON(r, 404, metav1.Status{Status: metav1.StatusFailure, Code: 404, Reason: metav1.StatusReasonNotFound}), nil
	})
	c.disc = f1Disc{}
	_, err := c.GetInstanceDetail(readfence.With(context.Background(), fence), "autoscaling", "v2", "horizontalpodautoscalers", "ns", "scale")
	if !readfence.Failed(err) {
		t.Fatalf("a detail without the owner its fence refused: %v", err)
	}
}
