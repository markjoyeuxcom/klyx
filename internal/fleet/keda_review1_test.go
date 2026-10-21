package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/identity"
	"github.com/moomora/klyx/internal/keda"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
)

// Implementation review 1 (Codex's probes, kept as regression tests, and
// the cases added with the fixes): an incomplete operator read never
// establishes a handler, a version or a Pause; the inverse identity join
// needs a handling user; projected reference cuts reach the inspector; a
// coalesced HPA cycle and a capped scaler kind keep episode clocks honest.
func TestReview1CandidateCapCannotProveHandlerOrArmPause(t *testing.T) {
	so := kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}}, nil)
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{so})
	// First candidate applies. The next three are proven outside the namespace.
	// Candidate five, left unread, might apply too.
	for i := 2; i <= 5; i++ {
		c.caps.KEDA.Operators = append(c.caps.KEDA.Operators, capability.KEDAOperator{Namespace: fmt.Sprintf("keda%d", i), Name: "operator", UID: fmt.Sprintf("dep-%d", i)})
	}
	for i := 2; i <= 5; i++ {
		ns := fmt.Sprintf("keda%d", i)
		rsUID := fmt.Sprintf("rs-%d", i)
		depUID := fmt.Sprintf("dep-%d", i)
		env := []corev1.EnvVar{}
		if i < 5 {
			env = append(env, corev1.EnvVar{Name: keda.EnvWatchNamespace, Value: "elsewhere"})
		}
		rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rs", UID: types.UID(rsUID), OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: types.UID(depUID), Controller: kyes()}}}}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "pod", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", UID: types.UID(rsUID), Controller: kyes()}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: "ghcr.io/kedacore/keda:2.21.0", Env: env}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if _, err := c.typed.AppsV1().ReplicaSets(ns).Create(context.Background(), rs, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.typed.CoreV1().Pods(ns).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	in, err := c.KEDAInspect(context.Background(), "scaledobjects", "team", "web", "so-1")
	if err != nil {
		t.Fatal(err)
	}
	if !in.CandidatesCut {
		t.Fatal("invalid control: candidate cap not hit")
	}
	if in.Handler != "" {
		t.Errorf("capped candidates still established unique handler %q", in.Handler)
	}
	if err := c.KEDASetPaused(context.Background(), "scaledobjects", "team", "web", "so-1", true); err == nil {
		t.Error("Pause wrote despite an unread possible second handler")
	}
}

func TestReview1InspectionSaysAProjectedReferenceWasCut(t *testing.T) {
	var refs []any
	for i := 0; i < 17; i++ {
		refs = append(refs, map[string]any{"parameter": "p", "name": fmt.Sprintf("s%d", i), "key": "k"})
	}
	a := kedaObj("TriggerAuthentication", "team", "ta", "ta-1", map[string]any{"secretTargetRef": refs}, nil)
	so := kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": []any{authRef("ta", "")}}, nil)
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{a, so})
	in, err := c.KEDAInspect(context.Background(), "scaledobjects", "team", "web", "so-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Auths) != 1 || len(in.Auths[0].Refs) != 16 {
		t.Fatalf("invalid control: %+v", in)
	}
	if !in.Auths[0].RefsCut {
		t.Fatal("17th reference was omitted, but inspector reports no cut")
	}
}

func TestReview1InverseIdentityNeedsAHandlingUser(t *testing.T) {
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", []corev1.EnvVar{{Name: keda.EnvWatchNamespace, Value: "team"}}, nil)
	f := newFakeKEDAObs()
	// Unused authentication in a namespace the operator does not watch.
	a := keda.ProjectAuth(kedaObj("TriggerAuthentication", "foreign", "unrelated", "a", map[string]any{"podIdentity": map[string]any{"provider": "aws"}}, nil), false)
	for _, kind := range []string{healthsrc.KindTriggerAuths, healthsrc.KindClusterTriggerAuths} {
		f.states[kind] = f.states[healthsrc.KindScaledObjects]
		f.complete[kind] = true
	}
	f.objs[healthsrc.KindTriggerAuths] = []any{a}
	got, read := c.kedaAuthsFor(context.Background(), f, "keda", "keda-operator")
	if len(got) > 0 {
		t.Fatalf("inverse identity claims unrelated authentication uses this operator SA: %+v (%s)", got, read)
	}
	_ = identity.ReadComplete
}

func TestReview1ContinuedPodsCannotEstablishConfiguration(t *testing.T) {
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, nil)
	typed := c.typed.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	})
	typed.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "more-pods"}, Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "keda", Name: "operator-a", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", UID: "rs-1", Controller: kyes()}}},
			Spec:       corev1.PodSpec{ServiceAccountName: "operator", Containers: []corev1.Container{{Name: "operator", Image: "ghcr.io/kedacore/keda:2.21.0"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}}}, nil
	})
	cs, cut, err := c.KEDAOperatorConfigs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) > 0 && cs[0].Watches("team", nil, false) == keda.Yes && cs[0].Version.Known && !cut {
		t.Fatalf("continued pod list certified configuration and version: %+v", cs[0])
	}
}

func TestReview1CoalescedHPACycleRestartsEpisode(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	t0 := now
	f := newFakeKEDAObs()
	f.objs[healthsrc.KindScaledObjects] = []any{epScaledObject("so1", "10", false)}
	startKEDAEpisodes(context.Background(), f, func() time.Time { return now }, func() keda.Version { return keda.Version{Known: true, Major: 2, Minor: 21} })
	f.bump(healthsrc.KindScaledObjects, 1)
	now = now.Add(3 * time.Minute)
	// The SO was not written, but its HPA was created then deleted while events coalesced.
	f.bump(healthsrc.KindHPAs, 2)
	e := f.last()["so1"][keda.EpAwaitingHPA]
	if !e.Start.Equal(now) || e.Observed {
		t.Fatalf("coalesced HPA cycle retained old start %v (initial %v), want new lower bound %v", e, t0, now)
	}
}

func TestReview1RetainedScalerInCappedKindStillAges(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	t0 := now
	f := newFakeKEDAObs()
	so := epScaledObject("so1", "10", false)
	f.objs[healthsrc.KindScaledObjects] = []any{so}
	st := f.states[healthsrc.KindScaledObjects]
	st.Truncated = true
	f.states[healthsrc.KindScaledObjects] = st
	f.complete[healthsrc.KindScaledObjects] = false
	v := keda.Version{Known: true, Major: 2, Minor: 21}
	startKEDAEpisodes(context.Background(), f, func() time.Time { return now }, func() keda.Version { return v })
	f.bump(healthsrc.KindScaledObjects, 1)
	now = now.Add(3 * time.Minute)
	f.bump(healthsrc.KindScaledObjects, 0)
	got := keda.ScaledObjectRule(so, keda.SOInputs{Now: now, Version: v, HPA: keda.HPAEvidence{Current: true, Complete: true}, Episodes: f.last()["so1"]})
	if got.Eval.Role != health.Fault {
		t.Fatalf("retained SO has had no HPA since %v, but at %v got %+v; episodes=%+v", t0, now, got.Eval, f.last())
	}
}

// The inverse join, positive and qualified: an authentication used by a
// scaler whose proven handler runs as this ServiceAccount and sees it is
// linked, complete; an unread candidate or a cut trigger list qualifies
// the read; an authentication the handler's selector excludes is not
// linked.
func TestReview1InverseIdentityLinks(t *testing.T) {
	auth := func(ns, name string, labels map[string]any) *keda.Auth {
		u := kedaObj("TriggerAuthentication", ns, name, "ta-"+name, map[string]any{"podIdentity": map[string]any{"provider": "aws"}}, nil)
		if labels != nil {
			u.Object["metadata"].(map[string]any)["labels"] = labels
		}
		return keda.ProjectAuth(u, false)
	}
	user := func(name, authName string) *keda.ScaledObject {
		return keda.ProjectScaledObject(kedaObj("ScaledObject", "team", name, "so-"+name, map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": []any{authRef(authName, "")}}, nil))
	}
	obsWith := func(objs map[string][]any) *fakeKEDAObs {
		f := newFakeKEDAObs()
		for _, kind := range []string{healthsrc.KindTriggerAuths, healthsrc.KindClusterTriggerAuths} {
			f.states[kind] = f.states[healthsrc.KindScaledObjects]
			f.complete[kind] = true
		}
		for k, v := range objs {
			f.objs[k] = v
		}
		return f
	}
	env := []corev1.EnvVar{{Name: keda.EnvWatchNamespace, Value: "team"}, {Name: keda.EnvWatchAuthSelector, Value: "keda=yes"}}
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", env, nil)
	used, hidden := auth("team", "aws-auth", map[string]any{"keda": "yes"}), auth("team", "hidden", map[string]any{"keda": "no"})
	f := obsWith(map[string][]any{healthsrc.KindTriggerAuths: {used, hidden}, healthsrc.KindScaledObjects: {user("web", "aws-auth"), user("api", "aws-auth"), user("batch", "hidden")}})
	got, read := c.kedaAuthsFor(context.Background(), f, "keda", "keda-operator")
	if read != identity.ReadComplete || len(got) != 1 || got[0].Name != "aws-auth" ||
		got[0].Via != "the KEDA operator keda/keda-operator runs as this ServiceAccount and handles ScaledObject team/api and 1 more" {
		t.Fatalf("links: %+v (%s)", got, read)
	}
	// another ServiceAccount in the operator's namespace: nothing
	if got, _ := c.kedaAuthsFor(context.Background(), f, "keda", "other"); len(got) != 0 {
		t.Fatalf("another ServiceAccount: %+v", got)
	}
	// a cut trigger list: the omitted triggers may name another authentication
	cut := user("cut", "aws-auth")
	cut.TriggersCut = true
	f = obsWith(map[string][]any{healthsrc.KindTriggerAuths: {used}, healthsrc.KindScaledObjects: {cut}})
	if _, read := c.kedaAuthsFor(context.Background(), f, "keda", "keda-operator"); read != identity.ReadPartial {
		t.Fatalf("a cut trigger list: %s", read)
	}
	// more operators than are read: the handler is unverified
	for i := 2; i <= 5; i++ {
		c.caps.KEDA.Operators = append(c.caps.KEDA.Operators, capability.KEDAOperator{Namespace: fmt.Sprintf("keda%d", i), Name: "operator", UID: fmt.Sprintf("dep-%d", i)})
	}
	f = obsWith(map[string][]any{healthsrc.KindTriggerAuths: {used}, healthsrc.KindScaledObjects: {user("web", "aws-auth")}})
	if got, read := c.kedaAuthsFor(context.Background(), f, "keda", "keda-operator"); read != identity.ReadPartial || len(got) != 0 {
		t.Fatalf("an unread candidate: %+v (%s)", got, read)
	}
}

func TestReview1InspectionReplyBudget(t *testing.T) {
	var objs []runtime.Object
	var triggers []any
	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("ta-%02d", i)
		triggers = append(triggers, authRef(name, ""))
		var refs []any
		for j := 0; j < 16; j++ {
			refs = append(refs, map[string]any{"parameter": "p", "name": fmt.Sprintf("s%02d", j) + strings.Repeat("a", 250), "key": strings.Repeat("k", 200)})
		}
		a := kedaObj("TriggerAuthentication", "team", name, fmt.Sprintf("ta-%d", i), map[string]any{"secretTargetRef": refs, "configMapTargetRef": refs}, nil)
		if got := keda.ProjectAuth(a, false).Size; got > keda.MaxAuthObject {
			t.Fatalf("control: auth exceeds its admission bound: %d", got)
		}
		objs = append(objs, a)
	}
	objs = append(objs, kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": triggers}, nil))
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, objs)
	in, err := c.KEDAInspect(context.Background(), "scaledobjects", "team", "web", "so-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 256<<10 {
		t.Fatalf("inspection is %d bytes for 16 individually admitted authentications; limit is %d", len(b), 256<<10)
	}
	// nothing lost uncounted: every authentication's 32 references are
	// listed or counted
	for _, a := range in.Auths {
		if len(a.Refs)+a.RefsOmitted != 32 || (a.RefsOmitted > 0 && !a.RefsCut) {
			t.Fatalf("%s: %d listed, %d omitted, cut %v", a.Name, len(a.Refs), a.RefsOmitted, a.RefsCut)
		}
	}
}

// The operator read's other incomplete shapes: a continued ReplicaSet list
// and more running pods than are read leave the candidate unresolved, so
// neither the inspector nor the verb claims a handler; the verb names the
// candidate cap when that is why.
func TestReview1IncompleteOperatorReads(t *testing.T) {
	so := kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}}, nil)
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{so})
	c.typed.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	}).PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		// the operator's own ReplicaSet, and more not returned
		return true, &appsv1.ReplicaSetList{ListMeta: metav1.ListMeta{Continue: "more"}, Items: []appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Namespace: "keda", Name: "keda-operator-1", UID: "rs-1",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "keda-operator", UID: "dep-1", Controller: kyes()}}}}}}, nil
	})
	if in, err := c.KEDAInspect(context.Background(), "scaledobjects", "team", "web", "so-1"); err != nil || in.Handler != "" || in.Version != "" {
		t.Fatalf("a continued ReplicaSet list: %+v %v", in, err)
	}
	if err := c.KEDASetPaused(context.Background(), "scaledobjects", "team", "web", "so-1", true); err == nil {
		t.Fatal("Pause wrote on a continued ReplicaSet list")
	}

	c = kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{so})
	for i := 0; i < kedaMaxOperatorPods; i++ {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "keda", Name: fmt.Sprintf("keda-operator-1-%d", i),
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "keda-operator-1", UID: "rs-1", Controller: kyes()}}},
			Spec:   corev1.PodSpec{ServiceAccountName: "keda-operator", Containers: []corev1.Container{{Name: "keda-operator", Image: "ghcr.io/kedacore/keda:2.21.0"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if _, err := c.typed.CoreV1().Pods("keda").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	cs, _, err := c.KEDAOperatorConfigs(context.Background())
	if err != nil || len(cs) != 1 || cs[0].Version.Known || cs[0].Watches("team", nil, false) != keda.Unknown {
		t.Fatalf("nine running pods, eight read: %+v %v", cs, err)
	}

	c = kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{so})
	for i := 2; i <= 5; i++ {
		c.caps.KEDA.Operators = append(c.caps.KEDA.Operators, capability.KEDAOperator{Namespace: fmt.Sprintf("keda%d", i), Name: "operator", UID: fmt.Sprintf("dep-%d", i)})
	}
	if err := c.KEDASetPaused(context.Background(), "scaledobjects", "team", "web", "so-1", true); err == nil || !strings.Contains(err.Error(), "more KEDA operators run than Klyx reads") {
		t.Fatalf("the candidate cap's refusal: %v", err)
	}
}

// The inverse join's other unproven shapes: visibility the handler's
// selector cannot decide (the authentication's labels cut) and a handler
// whose pods disagree on the ServiceAccount qualify the read.
func TestReview1InverseIdentityUnproven(t *testing.T) {
	obsWith := func(objs map[string][]any) *fakeKEDAObs {
		f := newFakeKEDAObs()
		for _, kind := range []string{healthsrc.KindTriggerAuths, healthsrc.KindClusterTriggerAuths} {
			f.states[kind] = f.states[healthsrc.KindScaledObjects]
			f.complete[kind] = true
		}
		for k, v := range objs {
			f.objs[k] = v
		}
		return f
	}
	labels := map[string]any{}
	for i := 0; i < 16; i++ {
		labels[fmt.Sprintf("a%02d", i)] = "x"
	}
	labels["keda"] = "yes" // sorted past the sixteen kept: cut
	ta := kedaObj("TriggerAuthentication", "team", "aws-auth", "ta-1", map[string]any{"podIdentity": map[string]any{"provider": "aws"}}, nil)
	ta.Object["metadata"].(map[string]any)["labels"] = labels
	auth := keda.ProjectAuth(ta, false)
	if !auth.LabelsCut {
		t.Fatal("invalid control: labels not cut")
	}
	user := keda.ProjectScaledObject(kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": []any{authRef("aws-auth", "")}}, nil))
	f := obsWith(map[string][]any{healthsrc.KindTriggerAuths: {auth}, healthsrc.KindScaledObjects: {user}})
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", []corev1.EnvVar{{Name: keda.EnvWatchAuthSelector, Value: "keda=yes"}}, nil)
	if got, read := c.kedaAuthsFor(context.Background(), f, "keda", "keda-operator"); read != identity.ReadPartial || len(got) != 0 {
		t.Fatalf("visibility undecided: %+v (%s)", got, read)
	}
	// the operator's pods disagree on the ServiceAccount
	c = kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, nil)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "keda", Name: "keda-operator-1-b",
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "keda-operator-1", UID: "rs-1", Controller: kyes()}}},
		Spec:   corev1.PodSpec{ServiceAccountName: "other", Containers: []corev1.Container{{Name: "keda-operator", Image: "ghcr.io/kedacore/keda:2.21.0"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if _, err := c.typed.CoreV1().Pods("keda").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	plain := keda.ProjectAuth(kedaObj("TriggerAuthentication", "team", "aws-auth", "ta-1", map[string]any{"podIdentity": map[string]any{"provider": "aws"}}, nil), false)
	f = obsWith(map[string][]any{healthsrc.KindTriggerAuths: {plain}, healthsrc.KindScaledObjects: {user}})
	if got, read := c.kedaAuthsFor(context.Background(), f, "keda", "keda-operator"); read != identity.ReadPartial || len(got) != 0 {
		t.Fatalf("a disputed ServiceAccount: %+v (%s)", got, read)
	}
}

// The inverse join is per namespace: an operator in another namespace
// whose ServiceAccount has the same name is not the handler's.
func TestReview1InverseIdentityOtherNamespace(t *testing.T) {
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, nil)
	c.caps.KEDA.Operators = append(c.caps.KEDA.Operators, capability.KEDAOperator{Namespace: "keda2", Name: "keda-operator", UID: "dep-2"})
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "keda2", Name: "rs", UID: "rs-2", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: "dep-2", Controller: kyes()}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "keda2", Name: "pod", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", UID: "rs-2", Controller: kyes()}}},
		Spec: corev1.PodSpec{ServiceAccountName: "keda-operator", Containers: []corev1.Container{{Name: "keda-operator", Image: "ghcr.io/kedacore/keda:2.21.0",
			Env: []corev1.EnvVar{{Name: keda.EnvWatchNamespace, Value: "elsewhere"}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if _, err := c.typed.AppsV1().ReplicaSets("keda2").Create(context.Background(), rs, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.typed.CoreV1().Pods("keda2").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f := newFakeKEDAObs()
	for _, kind := range []string{healthsrc.KindTriggerAuths, healthsrc.KindClusterTriggerAuths} {
		f.states[kind] = f.states[healthsrc.KindScaledObjects]
		f.complete[kind] = true
	}
	f.objs[healthsrc.KindTriggerAuths] = []any{keda.ProjectAuth(kedaObj("TriggerAuthentication", "team", "aws-auth", "ta-1", map[string]any{"podIdentity": map[string]any{"provider": "aws"}}, nil), false)}
	f.objs[healthsrc.KindScaledObjects] = []any{keda.ProjectScaledObject(kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": []any{authRef("aws-auth", "")}}, nil))}
	if got, read := c.kedaAuthsFor(context.Background(), f, "keda", "keda-operator"); read != identity.ReadComplete || len(got) != 1 {
		t.Fatalf("the handler's own account: %+v (%s)", got, read)
	}
	if got, read := c.kedaAuthsFor(context.Background(), f, "keda2", "keda-operator"); read != identity.ReadComplete || len(got) != 0 {
		t.Fatalf("another namespace's operator: %+v (%s)", got, read)
	}
}
