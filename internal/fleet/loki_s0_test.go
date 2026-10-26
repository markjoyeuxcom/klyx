package fleet

import (
	"testing"

	"github.com/moomora/klyx/internal/config"
)

// Settings review slice 0 (Codex's cross-check probe): a Loki block with
// neither an endpoint nor a serviceRef - a match alone - is no log history,
// and is never advertised as one; an Alertmanager's match-only block is
// discovery, which this does not change.
func TestASourceLessLokiIsNoLogHistory(t *testing.T) {
	for _, c := range []struct {
		name    string
		cfg     *config.EndpointConfig
		present bool
	}{
		{"none", nil, false},
		{"a match alone", &config.EndpointConfig{Match: map[string]string{"cluster": "prd"}}, false},
		{"an endpoint", &config.EndpointConfig{Endpoint: "https://loki.example", Match: map[string]string{"cluster": "prd"}}, true},
		{"a serviceRef", &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "mon", Name: "loki", Port: "http"}}, true},
	} {
		e := newLokiEnv(t, c.cfg)
		if got := e.c.logHistoryPresent(); got != c.present {
			t.Errorf("%s: WithLoki present %v", c.name, got)
		}
		e.c.SetLokiConfig(c.cfg)
		if got := e.c.logHistoryPresent(); got != c.present {
			t.Errorf("%s: SetLokiConfig present %v", c.name, got)
		}
	}
}
