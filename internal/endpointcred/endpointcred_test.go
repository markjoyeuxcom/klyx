package endpointcred

import (
	"errors"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/secretstore"
)

func spec(endpoint, auth string) config.EndpointConfig {
	s := config.EndpointConfig{Endpoint: endpoint}
	switch auth {
	case "bearer":
		s.Auth = &config.EndpointAuth{Type: "bearer"}
	case "basic":
		s.Auth = &config.EndpointAuth{Type: "basic", Username: "klyx"}
	}
	return s
}

func stored(t *testing.T, s *secretstore.Memory, rec Record) {
	t.Helper()
	v, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(Account("prd", "metrics"), v); err != nil {
		t.Fatal(err)
	}
}

// Coverage C5 §3: every state, kept distinct.
func TestResolveStates(t *testing.T) {
	const origin = "https://mimir.example:443"
	in := func(sp config.EndpointConfig) Input { return Input{Cluster: "prd", Kind: "metrics", Spec: sp} }

	if c := Resolve(nil, in(spec("https://mimir.example", ""))); c.State != NotNeeded || !c.Usable() || c.Header() != "" {
		t.Fatalf("no auth: %+v", c)
	}
	if c := Resolve(nil, in(spec("", "bearer"))); c.State != NotSent || !c.Usable() || c.Header() != "" {
		t.Fatalf("auth on a proxied endpoint: %+v", c)
	}
	if c := Resolve(nil, in(spec("https://u:p@mimir.example", "bearer"))); c.State != BadEndpoint || c.Usable() {
		t.Fatalf("userinfo: %+v", c)
	}
	if c := Resolve(nil, in(spec("ftp://mimir.example", "bearer"))); c.State != BadEndpoint {
		t.Fatalf("no origin: %+v", c)
	}
	if c := Resolve(nil, in(spec("https://mimir.example", "bearer"))); c.State != Unavailable {
		t.Fatalf("no store: %+v", c)
	}

	s := &secretstore.Memory{}
	if c := Resolve(s, in(spec("https://mimir.example", "bearer"))); c.State != Missing || c.Usable() {
		t.Fatalf("missing: %+v", c)
	}
	stored(t, s, Record{V: 1, Origin: origin, Type: "bearer", Secret: "tok-1"})
	c := Resolve(s, in(spec("https://MIMIR.example:443/prometheus", "bearer")))
	if c.State != Stored || c.Header() != "Bearer tok-1" || c.Origin != origin {
		t.Fatalf("stored, matched canonically: %+v %q", c, c.Header())
	}
	if c := Resolve(s, in(spec("https://mimir2.example", "bearer"))); c.State != OtherOrigin || !strings.Contains(c.Why, origin) || c.Header() != "" {
		t.Fatalf("another origin: %+v", c)
	}
	if c := Resolve(s, in(spec("https://mimir.example", "basic"))); c.State != Unusable || !strings.Contains(c.Why, "it is bearer") {
		t.Fatalf("type mismatch: %+v", c)
	}
	in2 := in(spec("https://mimir.example", "bearer"))
	in2.Revoked = true
	if c := Resolve(s, in2); c.State != Revoked || c.Usable() {
		t.Fatalf("revoked: %+v", c)
	}
	for name, raw := range map[string]string{"not json": "{", "version": `{"v":2,"origin":"https://mimir.example:443","type":"bearer","secret":"x"}`, "empty": `{"v":1,"origin":"https://mimir.example:443","type":"bearer","secret":""}`} {
		_ = s.Set(Account("prd", "metrics"), raw)
		if c := Resolve(s, in(spec("https://mimir.example", "bearer"))); c.State != Unusable {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	s.FailGet = &secretstore.KeychainError{Err: errors.New("user interaction is not allowed")}
	if c := Resolve(s, in(spec("https://mimir.example", "bearer"))); c.State != Unavailable || !strings.Contains(c.Why, "keychain unavailable") {
		t.Fatalf("locked: %+v", c)
	}
}

// Coverage C5 §6: the legacy token wins while it is in the file, for
// whatever origin the file names; a keychain copy is noted.
func TestResolveLegacyWins(t *testing.T) {
	s := &secretstore.Memory{}
	in := Input{Cluster: "prd", Kind: "metrics", Spec: spec("https://anywhere.example", ""), LegacyToken: "file-tok"}
	c := Resolve(s, in)
	if c.State != Legacy || c.Header() != "Bearer file-tok" || c.StoredToo {
		t.Fatalf("legacy: %+v", c)
	}
	stored(t, s, Record{V: 1, Origin: "https://other.example:443", Type: "bearer", Secret: "kc-tok"})
	c = Resolve(s, in)
	if c.State != Legacy || c.Header() != "Bearer file-tok" || !c.StoredToo {
		t.Fatalf("legacy wins, the keychain copy noted: %+v", c)
	}
}

// Basic auth: the header, and every form redacted.
func TestBasicCredential(t *testing.T) {
	s := &secretstore.Memory{}
	stored(t, s, Record{V: 1, Origin: "https://loki.example:443", Type: "basic", Secret: "pa55"})
	c := Resolve(s, Input{Cluster: "prd", Kind: "metrics", Spec: spec("https://loki.example", "basic")})
	if c.Header() != "Basic a2x5eDpwYTU1" {
		t.Fatalf("basic header: %q", c.Header())
	}
	if got := c.Redactor().Text("echo: Basic a2x5eDpwYTU1 / a2x5eDpwYTU1 / pa55"); strings.Contains(got, "pa55") || strings.Contains(got, "a2x5eDpwYTU1") {
		t.Fatalf("redacted: %s", got)
	}
}

// Coverage C5 §7: revisions, tombstones and the liveness gate.
func TestRegistry(t *testing.T) {
	r := NewRegistry()
	live := r.Live("prd", "metrics", r.Revision("prd", "metrics"))
	if !live() {
		t.Fatal("a fresh client is live")
	}
	r.Revoke("prd", "metrics")
	if live() || !r.Revoked("prd", "metrics") {
		t.Fatal("a revocation stops the client and tombstones the endpoint")
	}
	if r.Revoked("prd", "loki") || r.Revision("dev", "metrics") != 0 {
		t.Fatal("other endpoints are untouched")
	}
	r.Bump("prd", "metrics", false)
	if !r.Revoked("prd", "metrics") {
		t.Fatal("a bump without lift keeps the tombstone")
	}
	r.Bump("prd", "metrics", true)
	if r.Revoked("prd", "metrics") {
		t.Fatal("a Set lifts it")
	}
	var nilReg *Registry
	if nilReg.Revision("a", "b") != 0 || nilReg.Revoked("a", "b") || !nilReg.Live("a", "b", 0)() {
		t.Fatal("a nil registry is inert")
	}
}
