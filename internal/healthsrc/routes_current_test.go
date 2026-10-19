package healthsrc

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Codex's S7b implementation review, finding 1 (its probe, adopted): a
// stale Gateway, GatewayClass or Service read publishes no current verdict.

func TestStaleRouteDependenciesPublishNoCurrentVerdict(t *testing.T) {
	for _, dep := range []string{KindGateways, KindGatewayClasses, KindGatewayServices} {
		t.Run(dep, func(t *testing.T) {
			u := httpRoute("web", []string{"eg"}, "web", nil)
			c := routesCapture(t, []*unstructured.Unstructured{u}, gw("eg", "True"))
			c.Current[dep], c.Complete[dep] = false, false
			rs := evalRoutes("review")(c).Rows.(RowSet[RouteRow])
			if len(rs.Rows) != 1 || rs.Rows[0].Data.Role != "unknown" {
				t.Fatalf("stale %s yielded current row: %+v", dep, rs.Rows)
			}
		})
	}
}
