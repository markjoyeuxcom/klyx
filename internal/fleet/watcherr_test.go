package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// TestWatchErrTrackerRecordsError is the error-honesty unit: record(err) makes
// lastError report the error string, nil is a no-op, and the most recent error
// wins.
func TestWatchErrTrackerRecordsError(t *testing.T) {
	tr := &watchErrTracker{}
	if got := tr.lastError(); got != "" {
		t.Fatalf("fresh tracker lastError = %q, want empty", got)
	}

	// nil must not clobber or record anything.
	tr.record(nil)
	if got := tr.lastError(); got != "" {
		t.Fatalf("after record(nil) lastError = %q, want empty", got)
	}

	tr.record(errors.New("forbidden: RBAC denied"))
	if got := tr.lastError(); got != "forbidden: RBAC denied" {
		t.Fatalf("lastError = %q, want the recorded error", got)
	}

	// A subsequent nil must not wipe the recorded error.
	tr.record(nil)
	if got := tr.lastError(); got != "forbidden: RBAC denied" {
		t.Fatalf("record(nil) wiped lastError to %q", got)
	}

	// Most recent non-nil error wins.
	tr.record(errors.New("the CRD version is not served"))
	if got := tr.lastError(); got != "the CRD version is not served" {
		t.Fatalf("lastError = %q, want the latest error", got)
	}
}

// TestGitOpsWatchHealthReportsFailingWatch is the fleet-level error-honesty
// seam: a lazy watch whose list keeps failing (RBAC denied here) must report
// synced=false with a non-empty lastErr, so the view renders an error instead
// of an empty, healthy-looking resource list.
func TestGitOpsWatchHealthReportsFailingWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, sourceGVRToListKinds())
	// Fail every list: the reflector never syncs and its watch-error handler
	// fires, which trackWatchErrors records into the tracker.
	dyn.PrependReactor("list", "kustomizations", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("kustomizations.kustomize.toolkit.fluxcd.io is forbidden")
	})

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx

	c.OpenGitOps()
	defer c.CloseGitOps()

	waitFor(t, 2*time.Second, func() bool {
		_, lastErr := c.GitOpsWatchHealth()
		return lastErr != ""
	})

	synced, lastErr := c.GitOpsWatchHealth()
	if synced {
		t.Fatal("GitOpsWatchHealth reported synced despite a failing kustomization watch")
	}
	if lastErr == "" {
		t.Fatal("GitOpsWatchHealth reported no error despite a failing kustomization watch")
	}
}
