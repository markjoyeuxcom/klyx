package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/readfence"
	"github.com/moomora/klyx/internal/workloads"
)

// A3 §5.1: a write's outcome is written, refused before anything was sent,
// refused by the server, or unverified - never a guess from the text.
func TestWriteOutcomeClassifies(t *testing.T) {
	gr := schema.GroupResource{Group: "apps", Resource: "deployments"}
	for _, c := range []struct {
		name    string
		err     error
		outcome string
		notSent bool
	}{
		{"nil", nil, OutcomeWritten, false},
		{"notSent", notSent(errors.New("revision 2 not found")), OutcomeRefused, true},
		{"notSent replaced", notSent(fmt.Errorf("x: %w", ErrReplaced)), OutcomeRefused, true},
		{"wrapped notSent", fmt.Errorf("scale %w", notSent(errors.New("armed"))), OutcomeRefused, true},
		{"forbidden", apierrors.NewForbidden(gr, "web", errors.New("rbac")), OutcomeRefused, false},
		{"replaced by the server", fmt.Errorf("x: %w", ErrReplaced), OutcomeRefused, false},
		{"internal", apierrors.NewInternalError(errors.New("etcd")), OutcomeUnverified, false},
		{"timeout 408", apierrors.NewTimeoutError("slow", 1), OutcomeUnverified, false},
		{"deadline", context.DeadlineExceeded, OutcomeUnverified, false},
		{"transport", errors.New("connection reset by peer"), OutcomeUnverified, false},
	} {
		got, ns := WriteOutcome(c.err)
		if got != c.outcome || ns != c.notSent {
			t.Errorf("%s: got %s/%v, want %s/%v", c.name, got, ns, c.outcome, c.notSent)
		}
	}
}

func TestNotSentKeepsItsTextAndChain(t *testing.T) {
	inner := fmt.Errorf("Deployment shop/cart: %w", ErrReplaced)
	err := fmt.Errorf("rollout undo %w", notSent(inner))
	if err.Error() != "rollout undo Deployment shop/cart: "+ErrReplaced.Error() {
		t.Fatalf("the refusal's text is its own: %q", err)
	}
	if !errors.Is(err, ErrNotSent) || !errors.Is(err, ErrReplaced) {
		t.Fatalf("both stay in the chain: %v", err)
	}
	if notSent(nil) != nil {
		t.Fatal("nil stays nil")
	}
}

// A receipt is noted only under a recorder: the manual verbs pass none.
func TestReceiptsAreNotedOnlyUnderARecorder(t *testing.T) {
	NoteReceipt(context.Background(), Receipt{UID: "u"}) // no recorder: nothing to hold it
	ctx, rec := WithReceipts(context.Background())
	NoteReceipt(ctx, Receipt{UID: "a", Generation: 1})
	NoteReceipt(ctx, Receipt{UID: "b", Generation: 2})
	NoteReceipt(ctx, Receipt{UID: "a", Generation: 3})
	if rc, ok := rec.Of("a"); !ok || rc.Generation != 3 {
		t.Fatalf("the last write to a: %+v %v", rc, ok)
	}
	if _, ok := rec.Of("c"); ok {
		t.Fatal("no write to c")
	}
	var none *Receipts
	if _, ok := none.Of("a"); ok {
		t.Fatal("a nil recorder holds nothing")
	}
}

// A3 §1: restart's token is unique, so two restarts within one second are
// distinct templates; the receipt holds it and the armed read's generation.
func TestRestartTokenIsUniqueAndReceipted(t *testing.T) {
	resetAttemptTokens(t)
	dep := rolloutDeployment(false, "1")
	dep.Generation = 4
	typed := k8sfake.NewSimpleClientset(dep)
	clk := clock.NewFake(time.Unix(1_700_000_000, 123_000_000))
	c := &ClusterConn{typed: typed, clk: clk}

	ctx, rec := WithReceipts(context.Background())
	if err := c.RolloutRestart(ctx, "Deployment", "shop", "cart", string(depUID)); err != nil {
		t.Fatal(err)
	}
	first, ok := rec.Of(string(depUID))
	if !ok {
		t.Fatal("a written restart has a receipt")
	}
	if first.PriorGeneration != 4 || first.Token == "" {
		t.Fatalf("receipt: %+v", first)
	}
	if _, err := time.Parse(time.RFC3339Nano, first.Token); err != nil || !strings.Contains(first.Token, ".123") {
		t.Fatalf("the token keeps sub-second precision: %q", first.Token)
	}
	clk.Advance(200 * time.Millisecond) // the same second
	ctx2, rec2 := WithReceipts(context.Background())
	if err := c.RolloutRestart(ctx2, "Deployment", "shop", "cart", string(depUID)); err != nil {
		t.Fatal(err)
	}
	second, _ := rec2.Of(string(depUID))
	if second.Token == first.Token {
		t.Fatalf("two restarts in one second wrote one token: %q", second.Token)
	}
	got, _ := typed.AppsV1().Deployments("shop").Get(context.Background(), "cart", metav1.GetOptions{})
	if got.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] != second.Token {
		t.Fatalf("the template carries the receipt's token: %v", got.Spec.Template.Annotations)
	}
}

// A refusal before the write is ErrNotSent, and leaves no receipt.
func TestArmingRefusalsAreNotSent(t *testing.T) {
	typed := k8sfake.NewSimpleClientset(rolloutDeployment(false, "1"))
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	ctx, rec := WithReceipts(context.Background())
	for name, err := range map[string]error{
		"restart": c.RolloutRestart(ctx, "Deployment", "shop", "cart", "another-uid"),
		"scale":   c.ScaleWorkload(ctx, "Deployment", "shop", "cart", "another-uid", 2),
		"pause":   c.RolloutPause(ctx, "shop", "cart", "another-uid", true),
		"undo":    c.RolloutUndo(ctx, "Deployment", "shop", "cart", "another-uid", 1, nil),
		"delete":  c.DeletePod(ctx, "shop", "p", ""),
	} {
		if o, ns := WriteOutcome(err); o != OutcomeRefused || !ns {
			t.Errorf("%s: %v -> %s/%v", name, err, o, ns)
		}
	}
	if _, ok := rec.Of("another-uid"); ok {
		t.Fatal("nothing was written, so nothing was receipted")
	}
	for _, a := range typed.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("a refusal before the write sent %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}

// A3 §1: reconcile's receipt holds the requestedAt token it wrote.
func TestReconcileReceiptHoldsItsToken(t *testing.T) {
	resetAttemptTokens(t)
	listKinds := map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKustomization("app"))
	c := newActionConn(dyn)
	fixed := time.Date(2026, 10, 24, 12, 0, 0, 456_000_000, time.UTC)
	c.clk = clock.NewFake(fixed)
	ctx, rec := WithReceipts(context.Background())
	if err := c.Reconcile(ctx, "Kustomization", "flux-system", "app", "uid-app"); err != nil {
		t.Fatal(err)
	}
	rc, ok := rec.Of("uid-app")
	if !ok || rc.Token != flux.ReconcileToken(fixed) {
		t.Fatalf("receipt: %+v %v", rc, ok)
	}
	// a reconcile Klyx does not offer is refused before anything is sent
	if o, ns := WriteOutcome(c.Reconcile(ctx, "Provider", "flux-system", "p", "uid-p")); o != OutcomeRefused || !ns {
		t.Fatalf("a Provider reconcile: %s/%v", o, ns)
	}
}

// The history's digest is the template's identity whichever way it was
// read: typed, or decoded as unstructured JSON (A1.1's reads).
func TestTemplateDigestAgreesAcrossReads(t *testing.T) {
	rs := rolloutRS("cart-2", "2", "shop/cart:v2", "", time.Unix(0, 0))
	typedDigest, err := workloads.TemplateDigest(rs.Spec.Template, true)
	if err != nil {
		t.Fatal(err)
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(rs)
	if err != nil {
		t.Fatal(err)
	}
	tm := u["spec"].(map[string]any)["template"].(map[string]any)
	unstructuredDigest, _ := workloads.TemplateDigest(tm, true)
	if typedDigest != unstructuredDigest || typedDigest == "" {
		t.Fatalf("typed %s vs unstructured %s", typedDigest, unstructuredDigest)
	}
	// the hash label never distinguishes a Deployment's templates; any other
	// change does
	other := rs.Spec.Template.DeepCopy()
	other.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = "something-else"
	if d, _ := workloads.TemplateDigest(other, true); d != typedDigest {
		t.Fatal("pod-template-hash must not change a Deployment template's digest")
	}
	other.Spec.Containers[0].Image = "shop/cart:v9"
	if d, _ := workloads.TemplateDigest(other, true); d == typedDigest {
		t.Fatal("an image change must change the digest")
	}
}

func undoFixture(t *testing.T) (*k8sfake.Clientset, *ClusterConn, workloads.RolloutRevision) {
	t.Helper()
	dep := rolloutDeployment(false, "3")
	dep.Spec.Template = rolloutRS("cart-3", "3", "shop/cart:v3", "", time.Unix(0, 0)).Spec.Template
	rs2 := rolloutRS("cart-2", "2", "shop/cart:v2", "", time.Unix(0, 0))
	rs2.UID = "rs2-uid"
	rs3 := rolloutRS("cart-3", "3", "shop/cart:v3", "", time.Unix(0, 0))
	rs3.UID = "rs3-uid"
	typed := k8sfake.NewSimpleClientset(dep, rs2, rs3)
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	hist, _, err := c.RolloutHistory(context.Background(), "Deployment", "shop", "cart")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range hist {
		if r.Revision == 2 {
			if r.ObjectUID != "rs2-uid" || r.TemplateDigest == "" {
				t.Fatalf("the history carries the revision's identity: %+v", r)
			}
			return typed, c, r
		}
	}
	t.Fatal("no revision 2")
	return nil, nil, workloads.RolloutRevision{}
}

func patchedDeployments(typed *k8sfake.Clientset) int {
	n := 0
	for _, a := range typed.Actions() {
		if a.GetVerb() == "patch" && a.GetResource().Resource == "deployments" {
			n++
		}
	}
	return n
}

// A3 §4.5: an undo with an expectation writes only the reviewed revision.
func TestUndoWritesOnlyTheReviewedRevision(t *testing.T) {
	typed, c, rev2 := undoFixture(t)
	ctx, rec := WithReceipts(context.Background())
	if err := c.RolloutUndo(ctx, "Deployment", "shop", "cart", string(depUID), 2, &RevisionExpect{ObjectUID: rev2.ObjectUID, TemplateDigest: rev2.TemplateDigest}); err != nil {
		t.Fatal(err)
	}
	if patchedDeployments(typed) != 1 {
		t.Fatal("the reviewed revision is written")
	}
	if _, ok := rec.Of(string(depUID)); !ok {
		t.Fatal("a written undo has a receipt")
	}

	for name, expect := range map[string]*RevisionExpect{
		"another object":   {ObjectUID: "rs-other", TemplateDigest: rev2.TemplateDigest},
		"another template": {ObjectUID: rev2.ObjectUID, TemplateDigest: "0000"},
	} {
		typed, c, _ := undoFixture(t)
		err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 2, expect)
		if !errors.Is(err, ErrRevisionChanged) || !errors.Is(err, ErrNotSent) {
			t.Errorf("%s: %v", name, err)
		}
		if patchedDeployments(typed) != 0 {
			t.Errorf("%s: a changed revision must not be written", name)
		}
	}
}

// The design review's probe: the ReplicaSet at revision 2 changes between
// the history read and the undo's own selection. With the expectation the
// undo refuses before writing; without one it would write the new object.
func TestUndoRefusesAPartnerResampledBetweenListings(t *testing.T) {
	typed, c, rev2 := undoFixture(t)
	lists := 0
	typed.PrependReactor("list", "replicasets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		if lists < 2 {
			return false, nil, nil // the history read sees the reviewed revision
		}
		swapped := rolloutRS("cart-2b", "2", "shop/cart:unreviewed", "", time.Unix(0, 0))
		swapped.UID = "rs2-swapped"
		return true, &appsv1.ReplicaSetList{Items: []appsv1.ReplicaSet{*swapped}}, nil
	})
	err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 2, &RevisionExpect{ObjectUID: rev2.ObjectUID, TemplateDigest: rev2.TemplateDigest})
	if !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("a resampled partner must be refused: %v", err)
	}
	if patchedDeployments(typed) != 0 {
		t.Fatal("the unreviewed template was written")
	}
}

func TestStatefulSetUndoChecksItsControllerRevision(t *testing.T) {
	stsUID := types.UID("sts-uid")
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg", UID: stsUID},
		Status: appsv1.StatefulSetStatus{UpdateRevision: "pg-rev2"}}
	r1 := stsRevision("pg-rev1", 1, "postgres:16.2", stsUID)
	r1.UID = "cr1-uid"
	r2 := stsRevision("pg-rev2", 2, "postgres:16.3", stsUID)
	r2.UID = "cr2-uid"
	typed := k8sfake.NewSimpleClientset(sts, r1, r2)
	typed.PrependReactor("patch", "statefulsets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, sts, nil
	})
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	hist, _, _ := c.RolloutHistory(context.Background(), "StatefulSet", "db", "pg")
	var rev1 workloads.RolloutRevision
	for _, r := range hist {
		if r.Revision == 1 {
			rev1 = r
		}
	}
	if rev1.ObjectUID != "cr1-uid" || rev1.TemplateDigest == "" {
		t.Fatalf("history: %+v", hist)
	}
	if err := c.RolloutUndo(context.Background(), "StatefulSet", "db", "pg", string(stsUID), 1, &RevisionExpect{ObjectUID: "cr2-uid", TemplateDigest: rev1.TemplateDigest}); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("another ControllerRevision must be refused: %v", err)
	}
	if err := c.RolloutUndo(context.Background(), "StatefulSet", "db", "pg", string(stsUID), 1, &RevisionExpect{ObjectUID: rev1.ObjectUID, TemplateDigest: rev1.TemplateDigest}); err != nil {
		t.Fatalf("the reviewed revision: %v", err)
	}
}

// Each matrix verb notes its receipt from the server's answer (A3 §5.1).
func TestMatrixVerbsNoteTheirReceipts(t *testing.T) {
	dep := rolloutDeployment(false, "1")
	dep.Generation = 9
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", UID: "node-uid", Generation: 2}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "p", UID: "pod-uid"}}
	typed := k8sfake.NewSimpleClientset(dep, node, pod)
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	for name, run := range map[string]func(ctx context.Context) (string, error){
		"pause": func(ctx context.Context) (string, error) {
			return string(depUID), c.RolloutPause(ctx, "shop", "cart", string(depUID), true)
		},
		"cordon": func(ctx context.Context) (string, error) { return "node-uid", c.SetCordon(ctx, "n1", "node-uid", true) },
		"delete": func(ctx context.Context) (string, error) { return "pod-uid", c.DeletePod(ctx, "shop", "p", "pod-uid") },
	} {
		ctx, rec := WithReceipts(context.Background())
		uid, err := run(ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rc, ok := rec.Of(uid); !ok || rc.UID != uid {
			t.Errorf("%s: no receipt for %s: %+v", name, uid, rc)
		}
	}
	// a Scale's answer carries the workload's UID and no generation
	typed.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "scale" {
			return false, nil, nil
		}
		return true, &autoscalingv1.Scale{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop", UID: depUID, ResourceVersion: "77"}}, nil
	})
	ctx, rec := WithReceipts(context.Background())
	if err := c.ScaleWorkload(ctx, "Deployment", "shop", "cart", string(depUID), 3); err != nil {
		t.Fatal(err)
	}
	if rc, ok := rec.Of(string(depUID)); !ok || rc.ResourceVersion != "77" || rc.Generation != 0 {
		t.Fatalf("scale receipt: %+v %v", rc, ok)
	}
}

func TestFluxAndKEDARefusalsAreNotSent(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList", kedaGVR("scaledobjects"): "ScaledObjectList"}
	so := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject",
		"metadata": map[string]any{"name": "so", "namespace": "shop", "uid": "uid-so"},
	}}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKustomization("app"), so)
	c := newActionConn(dyn)
	ctx := context.Background()
	for name, err := range map[string]error{
		"suspend, replaced":   c.SetSuspend(ctx, "Kustomization", "flux-system", "app", "uid-other", true),
		"reconcile, replaced": c.Reconcile(ctx, "Kustomization", "flux-system", "app", "uid-other"),
		"keda, replaced":      c.KEDASetPaused(ctx, "scaledobjects", "shop", "so", "uid-other", true),
		"keda, unknown kind":  c.KEDASetPaused(ctx, "deployments", "shop", "so", "uid-so", true),
	} {
		if o, ns := WriteOutcome(err); o != OutcomeRefused || !ns {
			t.Errorf("%s: %v -> %s/%v", name, err, o, ns)
		}
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatalf("a refusal before the write sent a patch: %v", a)
		}
	}
	// a suspension that is written is receipted
	rctx, rec := WithReceipts(ctx)
	if err := c.SetSuspend(rctx, "Kustomization", "flux-system", "app", "uid-app", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.Of("uid-app"); !ok {
		t.Fatal("a written suspension has a receipt")
	}
}

// A revision whose data holds no template has no digest, and never matches
// an expectation, even an empty one.
func TestUndoRefusesARevisionWithoutADigest(t *testing.T) {
	stsUID := types.UID("sts-uid")
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg", UID: stsUID},
		Status: appsv1.StatefulSetStatus{UpdateRevision: "pg-rev2"}}
	bare := stsRevision("pg-rev1", 1, "x", stsUID)
	bare.UID = "cr1-uid"
	bare.Data.Raw = []byte(`{"spec":{}}`)
	typed := k8sfake.NewSimpleClientset(sts, bare, stsRevision("pg-rev2", 2, "postgres:16.3", stsUID))
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	err := c.RolloutUndo(context.Background(), "StatefulSet", "db", "pg", string(stsUID), 1, &RevisionExpect{ObjectUID: "cr1-uid", TemplateDigest: "a-digest"})
	if !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("no digest is never a match: %v", err)
	}
}

// resetAttemptTokens starts the process-wide token clock over, for a test
// that reads its fake clock's token back.
func resetAttemptTokens(t *testing.T) {
	t.Helper()
	tokenMu.Lock()
	lastToken = time.Time{}
	tokenMu.Unlock()
}

// AI A3 stage 1 review, finding 1: there is no undo without the reviewed
// revision, on either path; nothing is sent.
func TestUndoNeedsItsReviewedRevision(t *testing.T) {
	for name, expect := range map[string]*RevisionExpect{
		"none":      nil,
		"no object": {TemplateDigest: "d"},
		"no digest": {ObjectUID: "rs-cart-2"},
	} {
		typed, c, _ := undoFixture(t)
		before := len(typed.Actions())
		err := c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 2, expect)
		if !errors.Is(err, ErrNoRevisionExpectation) || !errors.Is(err, ErrNotSent) {
			t.Errorf("%s: %v", name, err)
		}
		if len(typed.Actions()) != before {
			t.Errorf("%s: nothing is read or written before the expectation", name)
		}
	}
	stsUID := types.UID("sts-uid")
	typed := k8sfake.NewSimpleClientset(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg", UID: stsUID}},
		stsRevision("pg-rev1", 1, "postgres:16.2", stsUID))
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	if err := c.undoFromControllerRevision(context.Background(), "StatefulSet", "db", "pg", string(stsUID), 1, nil); !errors.Is(err, ErrNoRevisionExpectation) {
		t.Fatalf("the ControllerRevision path refuses too: %v", err)
	}
}

// AI A3 stage 1 review, finding 3: every attempt's token is its own, with a
// clock that stands still, steps back, or serves writes at once.
func TestAttemptTokensAreUnique(t *testing.T) {
	resetAttemptTokens(t)
	fixed := time.Date(2026, 10, 24, 12, 0, 0, 123, time.UTC)
	a, b := attemptToken(fixed), attemptToken(fixed)
	if a == b {
		t.Fatalf("an unchanged clock repeated a token: %s", a)
	}
	back := attemptToken(fixed.Add(-time.Hour))
	ta, _ := time.Parse(time.RFC3339Nano, b)
	tb, _ := time.Parse(time.RFC3339Nano, back)
	if !tb.After(ta) {
		t.Fatalf("a stepped-back clock went backwards: %s after %s", back, b)
	}
	seen := sync.Map{}
	var wg sync.WaitGroup
	dup := false
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, loaded := seen.LoadOrStore(attemptToken(fixed), true); loaded {
				dup = true
			}
		}()
	}
	wg.Wait()
	if dup {
		t.Fatal("two concurrent attempts shared a token")
	}
}

// A reconcile already handled, then an attempt whose answer is lost: the
// attempt keeps its own token, which is not the handled one, and has no
// receipt.
func TestALostReconcileKeepsItsOwnToken(t *testing.T) {
	resetAttemptTokens(t)
	listKinds := map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKustomization("app"))
	c := newActionConn(dyn)
	c.clk = clock.NewFake(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	first, rec1 := WithReceipts(context.Background())
	if err := c.Reconcile(first, "Kustomization", "flux-system", "app", "uid-app"); err != nil {
		t.Fatal(err)
	}
	handled, _ := rec1.Of("uid-app")
	dyn.PrependReactor("patch", "kustomizations", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("etcd timeout"))
	})
	second, rec2 := WithReceipts(context.Background())
	err := c.Reconcile(second, "Kustomization", "flux-system", "app", "uid-app") // the clock has not moved
	if o, _ := WriteOutcome(err); o != OutcomeUnverified {
		t.Fatalf("a lost answer is unverified: %v", err)
	}
	if _, ok := rec2.Of("uid-app"); ok {
		t.Fatal("a lost answer has no receipt")
	}
	att, ok := rec2.AttemptOf("uid-app")
	if !ok || att.Token == "" || att.Token == handled.Token {
		t.Fatalf("the attempt's own token, not the handled one: %+v vs %s", att, handled.Token)
	}
}

// AI A3 stage 1 review, finding 4: int64 values above 2^53 keep their
// identity, typed, unstructured or as a ControllerRevision's raw data.
func TestDigestKeepsExactIntegers(t *testing.T) {
	tmpl := func(grace int64) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{TerminationGracePeriodSeconds: &grace, Containers: []corev1.Container{{Name: "c", Image: "i"}}}}
	}
	lo, hi := int64(9007199254740992), int64(9007199254740993)
	dLo, _ := workloads.TemplateDigest(tmpl(lo), false)
	dHi, _ := workloads.TemplateDigest(tmpl(hi), false)
	if dLo == dHi {
		t.Fatal("two int64 values above 2^53 became one template")
	}
	u, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: tmpl(hi)}})
	if d, _ := workloads.TemplateDigest(u["spec"].(map[string]any)["template"], false); d != dHi {
		t.Fatal("typed and unstructured reads digest alike")
	}
	// a ControllerRevision's data is the server's serialisation of the template
	raw, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": tmpl(hi)}})
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("the raw data carries the exact value: %s", raw)
	}
	m, err := workloads.RevisionDataTemplate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := workloads.TemplateDigest(m, false); d != dHi {
		t.Fatal("a ControllerRevision's raw data digests alike")
	}
	// the undo refuses a template changed only in such a value
	typed, c, rev2 := undoFixture(t)
	rs, _ := typed.AppsV1().ReplicaSets("shop").Get(context.Background(), "cart-2", metav1.GetOptions{})
	rs.Spec.Template.Spec.TerminationGracePeriodSeconds = &lo
	_, _ = typed.AppsV1().ReplicaSets("shop").Update(context.Background(), rs, metav1.UpdateOptions{})
	hist, _, _ := c.RolloutHistory(context.Background(), "Deployment", "shop", "cart")
	var pinned string
	for _, r := range hist {
		if r.Revision == 2 {
			pinned = r.TemplateDigest
		}
	}
	rs.Spec.Template.Spec.TerminationGracePeriodSeconds = &hi
	_, _ = typed.AppsV1().ReplicaSets("shop").Update(context.Background(), rs, metav1.UpdateOptions{})
	before := patchedDeployments(typed)
	err = c.RolloutUndo(context.Background(), "Deployment", "shop", "cart", string(depUID), 2, &RevisionExpect{ObjectUID: rev2.ObjectUID, TemplateDigest: pinned})
	if !errors.Is(err, ErrRevisionChanged) || patchedDeployments(typed) != before {
		t.Fatalf("a changed int64 must be refused: %v", err)
	}
}

// AI A3 stage 1 review, finding 5: a fence that refuses the write before it
// is sent is a refusal, nothing sent - through a real client and transport.
func TestAFenceBeforeThePatchIsNotSent(t *testing.T) {
	var patches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patches.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"cart","namespace":"shop","uid":"dep-uid-1","generation":2}}`))
	}))
	defer srv.Close()
	typed, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, WrapTransport: readfence.Transport})
	if err != nil {
		t.Fatal(err)
	}
	c := &ClusterConn{typed: typed, clk: clock.NewFake(time.Unix(0, 0))}
	var checks atomic.Int32
	fence := func() error {
		if checks.Add(1) > 1 { // the arming GET passes; the PATCH does not
			return errors.New("the connection was replaced")
		}
		return nil
	}
	ctx, rec := WithReceipts(readfence.With(context.Background(), fence))
	err = c.RolloutPause(ctx, "shop", "cart", "dep-uid-1", true)
	if o, ns := WriteOutcome(err); o != OutcomeRefused || !ns {
		t.Fatalf("a fence before the PATCH: %s/%v %v", o, ns, err)
	}
	if !readfence.Failed(err) || patches.Load() != 0 {
		t.Fatalf("the fence's error stays in the chain, and no PATCH was sent: %v %d", err, patches.Load())
	}
	if _, ok := rec.Of("dep-uid-1"); ok {
		t.Fatal("no receipt")
	}
}
