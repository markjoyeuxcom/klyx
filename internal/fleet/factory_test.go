package fleet

import (
	"path/filepath"
	"testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func TestDefaultFactoryBuildsConn(t *testing.T) {
	cc := config.ClusterConfig{
		Name:       "plt-sea-prd-we-aks-01",
		Context:    "prd-we",
		Kubeconfig: filepath.Join("..", "cluster", "testdata", "kubeconfig.yaml"),
	}
	f := DefaultConnFactory(clock.Real{}, Credentials{})
	conn, err := f(cc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.Name() != "plt-sea-prd-we-aks-01" {
		t.Fatalf("want name set, got %q", conn.Name())
	}
}
