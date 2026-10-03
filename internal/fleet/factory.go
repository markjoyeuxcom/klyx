package fleet

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/cluster"
	"github.com/moomora/klyx/internal/config"
)

// DefaultConnFactory returns a ConnFactory that builds real client-go clients.
func DefaultConnFactory(clk clock.Clock) ConnFactory {
	return func(cc config.ClusterConfig) (Conn, error) {
		rc, err := cluster.RESTConfig(cc)
		if err != nil {
			return nil, err
		}
		typed, err := kubernetes.NewForConfig(rc)
		if err != nil {
			return nil, fmt.Errorf("typed client for %q: %w", cc.Name, err)
		}
		mclient, err := metadata.NewForConfig(rc)
		if err != nil {
			return nil, fmt.Errorf("metadata client for %q: %w", cc.Name, err)
		}
		dyn, err := dynamic.NewForConfig(rc)
		if err != nil {
			return nil, fmt.Errorf("dynamic client for %q: %w", cc.Name, err)
		}
		var mc config.MetricsConfig
		if cc.Metrics != nil {
			mc = *cc.Metrics
		}
		det := capability.NewDetector(typed)
		conn := NewClusterConn(cc.Name, typed, mclient, dyn, det, clk, mc)
		conn.WithKubeContext(cc.Context)
		conn.WithKubeconfig(cc.Kubeconfig)
		conn.WithRESTConfig(rc)
		return conn, nil
	}
}
