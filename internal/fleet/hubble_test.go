package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestHubbleFlowStats_GatesOnCapability(t *testing.T) {
	c := NewClusterConn("x", k8sfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	c.caps = capability.Set{} // HasHubble false

	_, st := c.HubbleFlowStats(context.Background())
	if st.Available || !strings.Contains(st.Message, "Hubble not detected") {
		t.Fatalf("no Hubble must be an honest unavailable, got %+v", st)
	}
}
