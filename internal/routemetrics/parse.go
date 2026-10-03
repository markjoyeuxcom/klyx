package routemetrics

import (
	"strings"

	"github.com/moomora/klyx/internal/metrics"
)

// parseClusterName extracts "<ns>/<name>" from an Envoy cluster name of the
// exact form httproute/<ns>/<name>/rule/<number>. Any other shape returns
// ok=false (skip, never guess). K8s namespaces/names cannot contain "/", so a
// 5-segment split is unambiguous.
func parseClusterName(name string) (routeKey string, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 5 {
		return "", false
	}
	if parts[0] != "httproute" || parts[3] != "rule" {
		return "", false
	}
	if parts[1] == "" || parts[2] == "" || !isAllDigits(parts[4]) {
		return "", false
	}
	return parts[1] + "/" + parts[2], true
}

// buildSelector builds the anchored, regex-escaped envoy_cluster_name matcher
// for a set of route keys ("<ns>/<name>"), as a valid PromQL string literal.
// Returns "" for empty input (the caller must guard and not query with an
// empty alternation).
func buildSelector(routeKeys []string) string {
	if len(routeKeys) == 0 {
		return ""
	}
	return metrics.MatchAnyOf("envoy_cluster_name", "httproute/", routeKeys, "/rule/[0-9]+")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
