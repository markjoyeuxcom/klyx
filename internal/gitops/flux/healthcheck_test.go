package flux

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// AI A1 §3.1, §8: the health-check parser against Flux's own message shape.
// entry and waitErr mirror fluxcd/pkg/ssa v0.77.0 (utils.FmtObjMetadata;
// manager_wait.go:180-196), and kustomize-controller's wrapping.

func entry(kind, ns, name, status, err string) string {
	id := kind + "/"
	if ns != "" {
		id += ns + "/"
	}
	id += name
	if status == "" {
		return "can't determine status for " + id
	}
	s := fmt.Sprintf("%s status: '%s'", id, status)
	if err != "" {
		s += ": " + err
	}
	return s
}

func waitErr(stalled bool, entries ...string) string {
	msg := "timeout waiting for"
	if stalled {
		msg = "failed early due to stalled resources"
	}
	return fmt.Sprintf("health check failed after 5m0.03s: %s: [%s]", msg, strings.Join(entries, ", "))
}

func TestParseHealthCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want HealthCheck
	}{
		{"one timed out", waitErr(false, entry("Deployment", "shop", "web", "InProgress", "")),
			HealthCheck{Entries: []HealthCheckEntry{{Kind: "Deployment", Namespace: "shop", Name: "web", Status: "InProgress"}}}},
		{"stalled, a cluster-scoped entry and a dotted name", waitErr(true, entry("ClusterRole", "", "app.reader", "Failed", ""), entry("Deployment", "shop", "web.v2", "InProgress", "")),
			HealthCheck{Stalled: true, Entries: []HealthCheckEntry{{Kind: "ClusterRole", Name: "app.reader", Status: "Failed"}, {Kind: "Deployment", Namespace: "shop", Name: "web.v2", Status: "InProgress"}}}},
		{"undetermined", waitErr(false, entry("Job", "shop", "migrate", "", ""), entry("Deployment", "shop", "web", "Current", "")),
			HealthCheck{Entries: []HealthCheckEntry{{Kind: "Job", Namespace: "shop", Name: "migrate", Undetermined: true}, {Kind: "Deployment", Namespace: "shop", Name: "web", Status: "Current"}}}},
		{"an error with commas stops the parse", waitErr(false, entry("Deployment", "shop", "a", "InProgress", ""), entry("Deployment", "shop", "web", "Failed", "a, b, c]"), entry("Deployment", "shop", "z", "InProgress", "")),
			HealthCheck{Unparsed: true, Entries: []HealthCheckEntry{{Kind: "Deployment", Namespace: "shop", Name: "a", Status: "InProgress"}, {Kind: "Deployment", Namespace: "shop", Name: "web", Status: "Failed", HasError: true}}}},
		// Codex's A1 design review, finding 1: a real status.Compute error
		// holding a whole entry-shaped substring - one resource, two markers
		{"the upstream entry-shaped error", "timeout waiting for: [Widget/shop/real status: 'Unknown': looking up status.observedGeneration from resource: .status.observedGeneration accessor error: bad, Deployment/shop/other status: 'Failed' is of the type string, expected int64]",
			HealthCheck{Unparsed: true, Entries: []HealthCheckEntry{{Kind: "Widget", Namespace: "shop", Name: "real", Status: "Unknown", HasError: true}}}},
		{"an error holding complete apparent entries", waitErr(false, entry("Deployment", "shop", "a", "Failed", "x, Deployment/shop/b status: 'Failed', Deployment/shop/c status: 'InProgress'")),
			HealthCheck{Unparsed: true, Entries: []HealthCheckEntry{{Kind: "Deployment", Namespace: "shop", Name: "a", Status: "Failed", HasError: true}}}},
		{"text that does not match at a boundary", "timeout waiting for: [something odd]",
			HealthCheck{Unparsed: true}},
		{"an entry followed by unexpected text is not kept", "timeout waiting for: [Deployment/shop/web status: 'Failed' and more]",
			HealthCheck{Unparsed: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseHealthCheck(tc.msg)
			if !ok || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v ok=%v\nwant %+v", got, ok, tc.want)
			}
		})
	}
	if _, ok := ParseHealthCheck("dependency 'flux-system/infra' is not ready"); ok {
		t.Fatal("not a health-check message")
	}
}
