package capability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/karpenter"
)

// Coverage S10 §9, the capability rows.

var s10Now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

const (
	imgAWS   = "public.ecr.aws/karpenter/controller:1.15.0@sha256:0123"
	imgAzure = "mcr.microsoft.com/aks/karpenter/controller:v1.14.5"
	imgOwn   = "registry.example.com/mirror/karpenter/controller:1.15.0"
)

// a candidate controller: a Deployment, its ReplicaSet and running pods
func kctl(ns, name string, labelled, ready bool, images ...string) []runtime.Object {
	sel := map[string]string{"app": ns + "-" + name}
	labels := map[string]string{}
	for k, v := range sel {
		labels[k] = v
	}
	if labelled {
		labels["app.kubernetes.io/name"] = "karpenter"
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(ns + "/" + name), Labels: labels},
		Spec: appsv1.DeploymentSpec{Replicas: ptrInt32(1), Selector: &metav1.LabelSelector{MatchLabels: sel}}}
	if ready {
		dep.Status.AvailableReplicas = 1
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name + "-1", UID: types.UID(ns + "/" + name + "-rs"), Labels: sel,
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: name, UID: dep.UID, Controller: yes()}}}}
	objs := []runtime.Object{dep, rs}
	for i, img := range images {
		p := runningPod(ns, name+"-"+string(rune('a'+i)), types.UID(ns+"/"+name+"-p"+string(rune('a'+i))), rs.UID, img)
		p.Labels = sel
		objs = append(objs, p)
	}
	return objs
}

func autoNode() *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "i-auto", Labels: map[string]string{"eks.amazonaws.com/compute-type": "auto"}}}
}

func napEvent(name string, at time.Time) *corev1.Event {
	return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name},
		Source: corev1.EventSource{Component: napEventSource}, LastTimestamp: metav1.NewTime(at)}
}

// served class kinds, by adapter
func kGroups(adapters ...karpenter.Adapter) []*metav1.APIResourceList {
	out := []*metav1.APIResourceList{{GroupVersion: "karpenter.sh/v1", APIResources: []metav1.APIResource{{Name: "nodepools"}, {Name: "nodeclaims"}}}}
	for _, a := range adapters {
		k, _ := karpenter.KindFor(a)
		out = append(out, &metav1.APIResourceList{GroupVersion: k.Group + "/" + k.Version, APIResources: []metav1.APIResource{{Name: k.Resource, Kind: k.Kind}}})
	}
	return out
}

// raw API reads: the NodePools (their class references), each class kind
// (one item when it has classes), and the NodePool CRD
type rawAPI struct {
	pools     string // a NodePoolList; "" fails the read
	classes   map[string]bool
	crdNAP    bool
	poolsCont bool
}

func (r rawAPI) list(_ context.Context, path string, _ map[string]string) ([]byte, error) {
	switch {
	case path == "/apis/karpenter.sh/v1/nodepools":
		if r.pools == "" {
			return nil, errors.New("forbidden")
		}
		if r.poolsCont {
			return []byte(`{"metadata":{"continue":"t"},"items":[]}`), nil
		}
		return []byte(r.pools), nil
	case strings.HasPrefix(path, "/apis/apiextensions.k8s.io/"):
		if !r.crdNAP {
			return []byte(`{"metadata":{"annotations":{}}}`), nil
		}
		return []byte(`{"metadata":{"annotations":{"meta.helm.sh/release-name":"aks-managed-karpenter-overlay-base","meta.helm.sh/release-namespace":"kube-system"}}}`), nil
	}
	for _, k := range karpenter.ClassKinds {
		if path == "/apis/"+k.Group+"/"+k.Version+"/"+k.Resource {
			if r.classes[k.Resource] {
				return []byte(`{"items":[{}]}`), nil
			}
			return []byte(`{"items":[]}`), nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{}, path)
}

const noPools = `{"metadata":{},"items":[]}`

func poolsRef(group, kind string) string {
	return `{"metadata":{},"items":[{"spec":{"template":{"spec":{"nodeClassRef":{"group":"` + group + `","kind":"` + kind + `","name":"default"}}}}}]}`
}

func detectKarp(groups []*metav1.APIResourceList, api rawAPI, react func(*fake.Clientset), objs ...runtime.Object) KarpenterCapability {
	cs := newFake(groups, objs...)
	if react != nil {
		react(cs)
	}
	d := &Detector{cs: cs, rawList: api.list, now: func() time.Time { return s10Now }}
	return d.detectKarpenter(context.Background(), map[string]bool{"karpenter.sh": true, "karpenter.sh/v1": true,
		"karpenter.k8s.aws/v1": true, "karpenter.azure.com/v1beta1": true, "eks.amazonaws.com/v1": true})
}

func ec2InUse() rawAPI {
	return rawAPI{pools: noPools, classes: map[string]bool{"ec2nodeclasses": true}}
}
func aksInUse() rawAPI {
	return rawAPI{pools: noPools, classes: map[string]bool{"aksnodeclasses": true}}
}
func autoInUse() rawAPI { return rawAPI{pools: noPools, classes: map[string]bool{"nodeclasses": true}} }
func taskOf(k KarpenterCapability) string {
	s, r := k.Task()
	return s + ": " + r
}

func TestKarpenterInventoryAttributesByImage(t *testing.T) {
	k := detectKarp(kGroups(karpenter.AdapterEC2), ec2InUse(), nil, kctl("platform", "karp", true, true, imgAWS, imgAWS)...)
	if len(k.Controllers) != 1 || k.Controllers[0].Provider != ProviderAWS || k.Inventory != InventoryComplete {
		t.Fatalf("a labelled Deployment in a custom namespace, attributed: %+v", k)
	}
	if s, _ := k.Task(); s != KarpenterHealthy || !k.Healthy || !k.Verified || k.Tier != Healthy {
		t.Fatalf("healthy: %s %+v", taskOf(k), k)
	}
	if h, _ := k.Reading(karpenter.ClassKinds[0]); h.State != karpenter.HandlerReady {
		t.Fatalf("ec2 handler: %+v", h)
	}
	// The conventional name, unlabelled, is still a candidate.
	k = detectKarp(kGroups(karpenter.AdapterAKS), aksInUse(), nil, kctl("kube-system", "karpenter", false, true, imgAzure)...)
	if len(k.Controllers) != 1 || k.Controllers[0].Provider != ProviderAzure {
		t.Fatalf("the conventional Deployment: %+v", k.Controllers)
	}
	// A custom or mirrored image proves no provider.
	k = detectKarp(kGroups(karpenter.AdapterEC2), ec2InUse(), nil, kctl("platform", "karp", true, true, imgOwn)...)
	if k.Controllers[0].Provider != "" || k.Inventory != InventoryIncomplete || !strings.Contains(k.InventoryWhy, "unattributed") {
		t.Fatalf("a custom image: %+v", k)
	}
	if s, r := k.Task(); s != KarpenterUnknown || !strings.Contains(r, "could not all be read") {
		t.Fatalf("an unattributed candidate is a gap: %s %s", s, r)
	}
	// Pods that disagree, and a candidate with no running pods.
	k = detectKarp(kGroups(karpenter.AdapterEC2), ec2InUse(), nil, kctl("platform", "karp", true, true, imgAWS, imgAzure)...)
	if k.Controllers[0].Provider != "" || !strings.Contains(k.Controllers[0].Why, "different providers") {
		t.Fatalf("disagreeing pods: %+v", k.Controllers)
	}
	k = detectKarp(kGroups(karpenter.AdapterEC2), ec2InUse(), nil, kctl("platform", "karp", true, false)...)
	if k.Controllers[0].Provider != "" || k.Controllers[0].Why != "no running pods" {
		t.Fatalf("no pods: %+v", k.Controllers)
	}
	// More than eight running pods are not all read.
	imgs := make([]string, karpenterMaxPods+1)
	for i := range imgs {
		imgs[i] = imgAWS
	}
	k = detectKarp(kGroups(karpenter.AdapterEC2), ec2InUse(), nil, kctl("platform", "karp", true, true, imgs...)...)
	if k.Controllers[0].Provider != "" {
		t.Fatalf("more than eight pods: %+v", k.Controllers)
	}
}

func TestKarpenterImageRepo(t *testing.T) {
	for img, want := range map[string]KarpenterProvider{
		imgAWS: ProviderAWS, "public.ecr.aws/karpenter/controller": ProviderAWS, imgAzure: ProviderAzure,
		"public.ecr.aws/karpenter/controller-extra:1": "", imgOwn: "", "localhost:5000/karpenter/controller:1": "",
	} {
		if got := providerOfImage(img); got != want {
			t.Errorf("%s: %q, want %q", img, got, want)
		}
	}
}

func TestKarpenterInventoryOutcomes(t *testing.T) {
	forbidList := func(cs *fake.Clientset) {
		cs.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", errors.New("rbac"))
		})
	}
	continued := func(cs *fake.Clientset) {
		cs.PrependReactor("list", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() != "" {
				return false, nil, nil
			}
			return true, &appsv1.DeploymentList{ListMeta: metav1.ListMeta{Continue: "more"}}, nil
		})
	}
	if k := detectKarp(kGroups(), ec2InUse(), forbidList); k.Inventory != InventoryUnreadable {
		t.Fatalf("forbidden: %+v", k)
	}
	if k := detectKarp(kGroups(), ec2InUse(), continued); k.Inventory != InventoryIncomplete {
		t.Fatalf("continued: %+v", k)
	}
	var objs []runtime.Object
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		objs = append(objs, kctl("ns-"+n, "karp", true, true, imgAWS)...)
	}
	if k := detectKarp(kGroups(karpenter.AdapterEC2), ec2InUse(), nil, objs...); k.Inventory != InventoryIncomplete || len(k.Controllers) != karpenterMaxCandidates {
		t.Fatalf("five candidates: %+v", k)
	}
}

// §5's table and the task's order, row by row.
func TestKarpenterReadingsAndTask(t *testing.T) {
	auto := kGroups(karpenter.AdapterEKSAuto)
	aks := kGroups(karpenter.AdapterAKS)
	forbidList := func(cs *fake.Clientset) {
		cs.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", errors.New("rbac"))
		})
	}
	t.Run("Auto Mode with its node is managed", func(t *testing.T) {
		k := detectKarp(auto, autoInUse(), nil, autoNode())
		if s, r := k.Task(); s != KarpenterManaged || !strings.Contains(r, "managed by EKS Auto Mode") || !k.Healthy || !k.Verified {
			t.Fatalf("%s %s", s, r)
		}
	})
	t.Run("Auto Mode without its node is unknown", func(t *testing.T) {
		k := detectKarp(auto, autoInUse(), nil)
		if s, r := k.Task(); s != KarpenterUnknown || !strings.Contains(r, "no controller for NodeClass found") {
			t.Fatalf("%s %s", s, r)
		}
	})
	t.Run("Auto Mode beside a forbidden Deployment list is unknown, Auto Mode noted", func(t *testing.T) {
		k := detectKarp(auto, autoInUse(), forbidList, autoNode())
		if s, r := k.Task(); s != KarpenterUnknown || !strings.Contains(r, "could not all be read") || !strings.Contains(r, "managed by EKS Auto Mode") {
			t.Fatalf("%s %s", s, r)
		}
	})
	t.Run("a failing controller beside Auto Mode and a read gap: not ready, both said", func(t *testing.T) {
		objs := append(kctl("karpenter", "karpenter", true, false, imgAWS), autoNode())
		objs = append(objs, kctl("other", "karp", true, true, imgOwn)...)
		groups := kGroups(karpenter.AdapterEC2, karpenter.AdapterEKSAuto)
		k := detectKarp(groups, rawAPI{pools: noPools, classes: map[string]bool{"ec2nodeclasses": true, "nodeclasses": true}}, nil, objs...)
		s, r := k.Task()
		if s != KarpenterNotReady || !strings.Contains(r, "karpenter/karpenter (AWS) is not ready") || !strings.Contains(r, "could not all be read") || !strings.Contains(r, "managed by EKS Auto Mode") {
			t.Fatalf("%s %s", s, r)
		}
		if k.Verified != true || k.Healthy {
			t.Fatalf("not ready is verified, not healthy: %+v", k)
		}
	})
	t.Run("an AWS-only controller never certifies an AKSNodeClass", func(t *testing.T) {
		groups := kGroups(karpenter.AdapterEC2, karpenter.AdapterAKS)
		k := detectKarp(groups, rawAPI{pools: noPools, classes: map[string]bool{"ec2nodeclasses": true, "aksnodeclasses": true}}, nil,
			kctl("karpenter", "karpenter", true, true, imgAWS)...)
		if h, text := k.Reading(karpenter.ClassKinds[1]); h.State != karpenter.HandlerUnproven || !h.NoneSupports || text != "no controller for AKSNodeClass found" {
			t.Fatalf("aks: %+v %s", h, text)
		}
		if s, r := k.Task(); s != KarpenterUnknown || !strings.Contains(r, "AKSNodeClass: no controller for AKSNodeClass found") {
			t.Fatalf("%s %s", s, r)
		}
	})
	t.Run("mixed providers with one failing", func(t *testing.T) {
		groups := kGroups(karpenter.AdapterEC2, karpenter.AdapterAKS)
		objs := append(kctl("aws", "karpenter", true, true, imgAWS), kctl("azure", "karpenter", true, false, imgAzure)...)
		k := detectKarp(groups, rawAPI{pools: noPools, classes: map[string]bool{"ec2nodeclasses": true, "aksnodeclasses": true}}, nil, objs...)
		if s, r := k.Task(); s != KarpenterNotReady || !strings.Contains(r, "azure/karpenter (Azure) is not ready") {
			t.Fatalf("%s %s", s, r)
		}
		if h, _ := k.Reading(karpenter.ClassKinds[0]); h.State != karpenter.HandlerReady {
			t.Fatalf("ec2 still has its ready controller: %+v", h)
		}
		if h, _ := k.Reading(karpenter.ClassKinds[1]); h.State != karpenter.HandlerNotReady {
			t.Fatalf("aks: %+v", h)
		}
	})
	t.Run("NAP activity with a complete inventory is managed", func(t *testing.T) {
		k := detectKarp(aks, aksInUse(), nil, napEvent("e1", s10Now.Add(-10*time.Minute)))
		s, r := k.Task()
		if s != KarpenterManaged || !strings.Contains(r, "managed by AKS node auto-provisioning (activity as of 2026-10-20T11:50:00Z)") {
			t.Fatalf("%s %s", s, r)
		}
		if h, _ := k.Reading(karpenter.ClassKinds[1]); h.State != karpenter.HandlerManaged || h.NoneSupports {
			t.Fatalf("NAP is never a ready controller: %+v", h)
		}
	})
	t.Run("NAP activity never clears an unread inventory", func(t *testing.T) {
		k := detectKarp(aks, aksInUse(), forbidList, napEvent("e1", s10Now.Add(-10*time.Minute)))
		if s, r := k.Task(); s != KarpenterUnknown || !strings.Contains(r, "activity as of") || !strings.Contains(r, "could not all be read") {
			t.Fatalf("%s %s", s, r)
		}
	})
	t.Run("NAP installation only", func(t *testing.T) {
		api := aksInUse()
		api.crdNAP = true
		k := detectKarp(aks, api, nil)
		if s, r := k.Task(); s != KarpenterUnknown || !strings.Contains(r, "CRDs installed by AKS NAP; current activity unverified") || !k.NAPInstalled {
			t.Fatalf("%s %s", s, r)
		}
	})
	t.Run("neither", func(t *testing.T) {
		k := detectKarp(aks, aksInUse(), nil)
		if s, r := k.Task(); s != KarpenterUnknown || !strings.Contains(r, "no controller for AKSNodeClass found") || k.NAP.State != NAPNone {
			t.Fatalf("%s %s %+v", s, r, k.NAP)
		}
	})
	t.Run("NoneSupports needs a complete inventory and no platform", func(t *testing.T) {
		k := detectKarp(kGroups(karpenter.AdapterEC2), ec2InUse(), nil, kctl("karpenter", "karpenter", true, true, imgAWS)...)
		if !k.NoneSupports("karpenter.k8s.gcp", "GCENodeClass") || k.NoneSupports("karpenter.k8s.aws", "EC2NodeClass") || !k.NoneSupports("karpenter.azure.com", "AKSNodeClass") {
			t.Fatal("complete AWS inventory")
		}
		k = detectKarp(kGroups(karpenter.AdapterEC2), ec2InUse(), forbidList)
		if k.NoneSupports("karpenter.k8s.gcp", "GCENodeClass") {
			t.Fatal("an unread inventory supports nothing either way")
		}
	})
}

func TestKarpenterKindsInUse(t *testing.T) {
	groups := kGroups(karpenter.AdapterEC2, karpenter.AdapterAKS, karpenter.AdapterEKSAuto)
	inUse := func(api rawAPI) map[string]string {
		k := detectKarp(groups, api, nil, kctl("karpenter", "karpenter", true, true, imgAWS)...)
		out := map[string]string{}
		for _, kk := range k.Kinds {
			if !kk.Served {
				t.Fatalf("%s not served", kk.Kind.Kind)
			}
			if kk.InUse {
				out[kk.Kind.Kind] = kk.InUseWhy
			}
		}
		return out
	}
	if got := inUse(rawAPI{pools: noPools, classes: map[string]bool{}}); len(got) != 0 {
		t.Errorf("served, no classes, no references: none in use, got %v", got)
	}
	if got := inUse(rawAPI{pools: poolsRef("karpenter.azure.com", "AKSNodeClass"), classes: map[string]bool{"ec2nodeclasses": true}}); len(got) != 2 ||
		got["AKSNodeClass"] != "referenced by a NodePool" || got["EC2NodeClass"] != "classes exist" {
		t.Errorf("referenced or with classes: %v", got)
	}
	if got := inUse(rawAPI{classes: map[string]bool{}}); len(got) != 3 {
		t.Errorf("the NodePools unreadable: every served kind in use, got %v", got)
	}
	if got := inUse(rawAPI{pools: noPools, poolsCont: true, classes: map[string]bool{}}); len(got) != 3 {
		t.Errorf("the NodePools continued: every served kind in use, got %v", got)
	}
	// A kind not served is never in use; with nothing served and the
	// controller ready, the task is healthy.
	k := detectKarp(kGroups(), rawAPI{pools: noPools}, nil, kctl("karpenter", "karpenter", true, true, imgAWS)...)
	if s, _ := k.Task(); s != KarpenterHealthy {
		t.Errorf("no kind served: %s", taskOf(k))
	}
}

// §5 NAP activity: the newest time across pages, the timestamp precedence,
// the window, cut and failed reads, and expiry.
func TestNAPActivity(t *testing.T) {
	pages := func(items ...[]corev1.Event) func(*fake.Clientset) {
		return func(cs *fake.Clientset) {
			n := 0
			cs.PrependReactor("list", "events", func(clienttesting.Action) (bool, runtime.Object, error) {
				if n >= len(items) {
					return true, &corev1.EventList{}, nil
				}
				l := &corev1.EventList{Items: items[n]}
				if n++; n < len(items) {
					l.Continue = "next"
				}
				return true, l, nil
			})
		}
	}
	ev := func(at time.Time) corev1.Event { return *napEvent("e", at) }
	read := func(react func(*fake.Clientset)) NAPActivity {
		return detectKarp(kGroups(karpenter.AdapterAKS), aksInUse(), react).NAP
	}
	old, recent := s10Now.Add(-3*time.Hour), s10Now.Add(-5*time.Minute)
	if a := read(pages([]corev1.Event{ev(old)}, []corev1.Event{ev(recent)})); a.State != NAPActive || !a.Newest.Equal(recent) {
		t.Errorf("an old first page and a recent later one: %+v", a)
	}
	if a := read(pages([]corev1.Event{ev(old)})); a.State != NAPNone {
		t.Errorf("expired: %+v", a)
	}
	if a := read(pages([]corev1.Event{{ObjectMeta: metav1.ObjectMeta{Name: "undated"}}})); a.State != NAPNone {
		t.Errorf("undated is no evidence: %+v", a)
	}
	repeated := corev1.Event{FirstTimestamp: metav1.NewTime(old), LastTimestamp: metav1.NewTime(old),
		Series: &corev1.EventSeries{LastObservedTime: metav1.NewMicroTime(recent)}}
	if a := read(pages([]corev1.Event{repeated})); a.State != NAPActive || !a.Newest.Equal(recent) {
		t.Errorf("a repeated Event at its latest observation: %+v", a)
	}
	eventTimeOnly := corev1.Event{EventTime: metav1.NewMicroTime(recent), FirstTimestamp: metav1.NewTime(old)}
	if a := read(pages([]corev1.Event{eventTimeOnly})); a.State != NAPActive {
		t.Errorf("eventTime before firstTimestamp: %+v", a)
	}
	four := [][]corev1.Event{{ev(old)}, {ev(old)}, {ev(old)}, {ev(recent)}}
	if a := read(pages(four...)); a.State != NAPNotEstablished || !strings.Contains(a.Why, "three pages") {
		t.Errorf("a cut read with nothing in the window: %+v", a)
	}
	if a := read(pages([]corev1.Event{ev(recent)}, []corev1.Event{ev(old)}, []corev1.Event{ev(old)}, []corev1.Event{ev(old)})); a.State != NAPActive {
		t.Errorf("a cut read that already found an in-window Event: %+v", a)
	}
	failing := func(cs *fake.Clientset) {
		cs.PrependReactor("list", "events", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("forbidden")
		})
	}
	if a := read(failing); a.State != NAPNotEstablished {
		t.Errorf("a failed read: %+v", a)
	}
}

// §5: a failed refresh keeps the last activity until it leaves the window;
// retained activity after NAP stops ages out.
func TestNAPRefreshExpires(t *testing.T) {
	newest := s10Now.Add(-10 * time.Minute)
	prev := KarpenterCapability{Present: true, Inventory: InventoryComplete, NAP: NAPActivity{State: NAPActive, Newest: newest},
		Kinds: []KarpenterKind{{Kind: karpenter.ClassKinds[1], Served: true, InUse: true}}}
	prev.settle()
	if s, _ := prev.Task(); s != KarpenterManaged {
		t.Fatalf("prev: %s", taskOf(prev))
	}
	failed := prev
	failed.NAP = NAPActivity{State: NAPNotEstablished, Why: "its Events could not be read: forbidden"}
	failed.settle()
	kept := failed.Refreshed(prev, s10Now)
	if kept.NAP.State != NAPActive || !strings.Contains(kept.NAP.Why, "kept from the last read") || !kept.Healthy {
		t.Fatalf("kept within the window: %+v", kept)
	}
	expired := failed.Refreshed(prev, newest.Add(NAPWindow+time.Second))
	if s, _ := expired.Task(); expired.NAP.State != NAPNotEstablished || s != KarpenterUnknown {
		t.Fatalf("expired at the window: %+v %s", expired.NAP, taskOf(expired))
	}
	// After NAP stops, its retained Events age out: a later complete read
	// finds nothing in the window.
	stopped := detectKarp(kGroups(karpenter.AdapterAKS), aksInUse(), nil, napEvent("e1", s10Now.Add(-NAPWindow-time.Minute)))
	if s, _ := stopped.Task(); stopped.NAP.State != NAPNone || s != KarpenterUnknown {
		t.Fatalf("aged out: %+v %s", stopped.NAP, taskOf(stopped))
	}
	// A complete fresh read is never replaced by the last one.
	if got := stopped.Refreshed(prev, s10Now); got.NAP.State != NAPNone {
		t.Fatalf("a successful read stands: %+v", got.NAP)
	}
}
