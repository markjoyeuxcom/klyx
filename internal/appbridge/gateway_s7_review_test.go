package appbridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Codex's S7a implementation review, finding 1: a condition message reaches
// no serialized DTO with a credential in a URL - user info plain or with an
// encoded separator - whether the condition came through the parser or was
// built elsewhere.
func TestGatewayDTOsCarryNoURLCredential(t *testing.T) {
	const needle = "S7REVIEWneedle"
	for _, msg := range []string{"cannot fetch https://alice:" + needle + "@example.test/cert", "cannot fetch https://alice:" + needle + "%40example.test/cert"} {
		c := gwapi.Cond{Status: "False", Reason: "InvalidCertificateRef", Message: msg, ObservedGeneration: 1}
		l := gwapi.Listener{Name: "https", Protocol: "HTTPS", Port: 443, Status: &gwapi.ListenerStatus{Conditions: []gwapi.TypedCond{{Type: "ResolvedRefs", Cond: c}}}}
		g := gwapi.GatewayRef{Namespace: "infra", Name: "g", Generation: 1, AcceptedCond: c, ProgrammedCond: c, Listeners: []gwapi.Listener{l}}
		ls := gwapi.ListenerSet{Namespace: "infra", Name: "ls", Generation: 1, AcceptedCond: c, ProgrammedCond: c, Listeners: []gwapi.Listener{l}}
		for name, dto := range map[string]any{
			"gateway list":    toGatewayRefDTO(g),
			"gateway row":     gatewayRefFields(g),
			"topology":        toTopologyDTO(gwapi.Topology{Gateway: gwapi.GatewayNode{Namespace: "infra", Name: "g", Generation: 1, AcceptedCond: c, Listeners: g.Listeners}}),
			"listenerset row": listenerSetRowDTO(healthsrc.Row[healthsrc.ListenerSetRow]{Data: healthsrc.ListenerSetRow{Set: ls}}),
		} {
			b, err := json.Marshal(dto)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), needle) {
				t.Errorf("%s carries the credential from %q", name, msg)
			}
		}
	}
}
