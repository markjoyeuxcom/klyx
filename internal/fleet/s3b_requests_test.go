package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/clock"
)

// Coverage S3b §3.1, §8: each write path declares its request, and its
// error carries it; an attributed refusal - a call failure's 500 among
// them - wrote nothing; a policy denial whose text says "uid" is not a
// replacement.

func webhookDenial() error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 403,
		Message: `admission webhook "deny.lab.klyx.dev" denied the request: widgets labelled deny are refused`}}
}

func callFailure() error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 500, Reason: metav1.StatusReasonInternalError,
		Message: `Internal error occurred: failed calling webhook "deny.lab.klyx.dev": failed to call webhook: Post "https://h.lab.svc:443/v?timeout=10s": no endpoints available for service "h"`}}
}

func vapUIDText() error {
	msg := "ValidatingAdmissionPolicy 'needs-uuid' with binding 'b' denied request: the uuid label is required, see the guide"
	return &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 422, Reason: metav1.StatusReasonInvalid,
		Message: `deployments.apps "api" is forbidden: ` + msg, Details: &metav1.StatusDetails{Name: "api", Group: "apps", Kind: "deployments", Causes: []metav1.StatusCause{{Message: msg}}}}}
}

func requestOf(t *testing.T, err error) admission.Request {
	t.Helper()
	var re *admission.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("no request in %v", err)
	}
	return re.Request
}

func TestS3bWorkloadVerbsDeclareTheirRequests(t *testing.T) {
	want := map[string]admission.Request{
		"restart deployment":  {Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "default"},
		"restart statefulset": {Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "statefulsets", Namespace: "db"},
		"restart daemonset":   {Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "daemonsets", Namespace: "kube-system"},
		"scale deployment":    {Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "deployments", Subresource: "scale", Namespace: "default"},
		"scale statefulset":   {Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "statefulsets", Subresource: "scale", Namespace: "db"},
		"pause":               {Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "default"},
		"undo":                {Operation: "UPDATE", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "default"},
	}
	for _, v := range workloadVerbs() {
		for name, refusal := range map[string]error{"denied": webhookDenial(), "call failure": callFailure(), "policy saying uid": vapUIDText()} {
			t.Run(v.name+"/"+name, func(t *testing.T) {
				typed := typedfake.NewClientset(seedFor(v))
				if v.name == "undo" {
					dep := rolloutDeployment(false, "2")
					dep.Namespace, dep.Name, dep.UID = "default", "api", "uid-api"
					old := rolloutRS("api-1", "1", "v1", "", time.Unix(0, 0))
					old.Namespace, old.OwnerReferences[0].UID = "default", "uid-api"
					cur := rolloutRS("api-2", "2", "v2", "", time.Unix(0, 0))
					cur.Namespace, cur.OwnerReferences[0].UID = "default", "uid-api"
					typed = typedfake.NewClientset(dep, old, cur)
				}
				fail := func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, refusal }
				typed.PrependReactor("patch", "*", fail)
				typed.PrependReactor("update", "*", fail)
				err := v.run(newPodActionsConn(typed, clock.NewFake(time.Unix(0, 0))), "uid-"+v.obj)
				if err == nil || errors.Is(err, ErrReplaced) || !strings.Contains(err.Error(), "nothing was written") {
					t.Fatalf("an attributed refusal wrote nothing and is no replacement: %v", err)
				}
				if got := requestOf(t, err); got != want[v.name] {
					t.Fatalf("request %+v, want %+v", got, want[v.name])
				}
			})
		}
	}
}

func TestS3bNodePodAndFluxDeclareTheirRequests(t *testing.T) {
	conn := newCordonTestConn("node-1", false)
	conn.typed.(*typedfake.Clientset).PrependReactor("patch", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, callFailure() })
	err := conn.SetCordon(context.Background(), "node-1", "uid-node-1", true)
	if got := requestOf(t, err); got != (admission.Request{Operation: "UPDATE", Version: "v1", Resource: "nodes"}) || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("cordon: %+v %v", got, err)
	}

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: "x", UID: "uid-web"}}
	typed := typedfake.NewClientset(pod)
	typed.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, webhookDenial() })
	err = newPodActionsConn(typed, clock.Real{}).DeletePod(context.Background(), "x", "web-0", "uid-web")
	if got := requestOf(t, err); got != (admission.Request{Operation: "DELETE", Version: "v1", Resource: "pods", Namespace: "x"}) {
		t.Fatalf("delete pod: %+v", got)
	}

	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"},
		seedKustomizationWithSource("app", "GitRepository", "flux-system"))
	dyn.PrependReactor("patch", "kustomizations", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, vapUIDText() })
	err = newActionConn(dyn).Reconcile(context.Background(), "Kustomization", "flux-system", "app", "uid-app")
	if errors.Is(err, ErrReplaced) {
		t.Fatalf("a policy denial saying uid is no replacement: %v", err)
	}
	if got := requestOf(t, err); got.Operation != "UPDATE" || got.Resource != "kustomizations" || got.Namespace != "flux-system" {
		t.Fatalf("reconcile: %+v", got)
	}
}

// S3b §2.3: each step keeps its own outcome, in both orders; the source's
// refusal is reported even when the target is written.
func TestS3bReconcileWithSourceReportsEachStep(t *testing.T) {
	gitRepoGVR := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	for _, tc := range []struct {
		name                 string
		sourceErr, targetErr error
	}{
		{"the source written, the target denied", nil, webhookDenial()},
		{"the source denied, the target written", webhookDenial(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList", gitRepoGVR: "GitRepositoryList"},
				seedKustomizationWithSource("app", "GitRepository", "flux-system"), seedGitRepoSource("flux-system"))
			dyn.PrependReactor("patch", "gitrepositories", func(k8stesting.Action) (bool, runtime.Object, error) {
				if tc.sourceErr != nil {
					return true, nil, tc.sourceErr
				}
				return false, nil, nil
			})
			dyn.PrependReactor("patch", "kustomizations", func(k8stesting.Action) (bool, runtime.Object, error) {
				if tc.targetErr != nil {
					return true, nil, tc.targetErr
				}
				return false, nil, nil
			})
			steps, err := newActionConn(dyn).ReconcileWithSourceSteps(context.Background(), "Kustomization", "flux-system", "app", "uid-app")
			if len(steps) != 2 || steps[0].Written != (tc.sourceErr == nil) || steps[1].Written != (tc.targetErr == nil) || (err != nil) != (tc.targetErr != nil) {
				t.Fatalf("steps %+v, err %v", steps, err)
			}
			if !strings.HasPrefix(steps[0].Subject, "GitRepository flux-system/flux-system") || !strings.HasPrefix(steps[1].Subject, "Kustomization flux-system/app") {
				t.Fatalf("subjects: %+v", steps)
			}
			for _, s := range steps {
				if s.Err != nil {
					if q := requestOf(t, s.Err); q.Operation != "UPDATE" {
						t.Fatalf("%+v", q)
					}
				}
			}
		})
	}
}
