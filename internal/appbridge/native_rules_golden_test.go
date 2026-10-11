package appbridge

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/externalsecrets"
	"github.com/moomora/klyx/internal/karpenter"
)

// The 1c addendum §2 golden parity gate for relocated lens rules. Every case
// is a domain object; the fixture records the lens DTO it becomes (the
// lens's input) and the Go rule's verdict. A vitest harness
// (frontend/src/health/nativeRules.test.ts) runs the lens's own TypeScript
// rule on each DTO and must reach the same verdict - proving the relocation
// is verbatim before the lens switches to the Go fields.
//
// Regenerate after an intended rule change: go test ./internal/appbridge -run NativeRules -update

var updateGolden = flag.Bool("update", false, "rewrite golden fixtures")

const nativeRulesFixture = "../../cmd/klyx/frontend/src/health/fixtures/native-rules.json"

type goldenCase struct {
	Case    string  `json:"case"`
	NowUnix int64   `json:"nowUnix"`
	DTO     any     `json:"dto"`
	Want    RuleDTO `json:"want"`
}

func TestNativeRulesGolden(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	nowU := now.Unix()
	out := map[string][]goldenCase{}

	cert := func(name string, c certmanager.Certificate) {
		c.Namespace, c.Name = "shop", name
		dto := toCertificateDTO(c, now)
		out["certificates"] = append(out["certificates"], goldenCase{name, nowU, dto, dto.Rule})
	}
	cert("ready, expires in 30d", certmanager.Certificate{Ready: certmanager.Ready, NotAfter: now.Add(30 * 24 * time.Hour)})
	cert("ready, no notAfter", certmanager.Certificate{Ready: certmanager.Ready})
	cert("ready but expired an hour ago", certmanager.Certificate{Ready: certmanager.Ready, NotAfter: now.Add(-time.Hour)})
	cert("expires exactly now", certmanager.Certificate{Ready: certmanager.Ready, NotAfter: now})
	cert("expires in one second", certmanager.Certificate{Ready: certmanager.Ready, NotAfter: now.Add(time.Second)})
	cert("issuing (included, progressing)", certmanager.Certificate{Ready: certmanager.Issuing})
	cert("failed", certmanager.Certificate{Ready: certmanager.Failed, Reason: "IncorrectIssuer"})
	cert("unknown", certmanager.Certificate{Ready: certmanager.Unknown})
	cert("no ready state", certmanager.Certificate{})
	cert("ready but spec unobserved (Syncing…)", certmanager.Certificate{Ready: certmanager.Ready, Stale: true, Generation: 3, ObservedGeneration: 2, NotAfter: now.Add(time.Hour)})
	cert("unobserved and expired", certmanager.Certificate{Ready: certmanager.Ready, Stale: true, NotAfter: now.Add(-time.Minute)})
	cert("failed while unobserved", certmanager.Certificate{Ready: certmanager.Failed, Stale: true})
	cert("unrecognised ready state", certmanager.Certificate{Ready: "Pending"})

	issuer := func(name string, i certmanager.Issuer) {
		i.Kind, i.Name = certmanager.ClusterIssuerKind, name
		dto := toIssuerDTO(i)
		out["issuers"] = append(out["issuers"], goldenCase{name, nowU, dto, dto.Rule})
	}
	issuer("ready", certmanager.Issuer{Ready: certmanager.Ready})
	issuer("failed", certmanager.Issuer{Ready: certmanager.Failed})
	issuer("unknown", certmanager.Issuer{Ready: certmanager.Unknown})
	issuer("no ready state", certmanager.Issuer{})
	issuer("issuing (unusual for an issuer)", certmanager.Issuer{Ready: certmanager.Issuing})

	secret := func(name string, e externalsecrets.ExternalSecret) {
		e.Namespace, e.Name, e.StoreKind, e.StoreName, e.TargetSecret = "shop", name, externalsecrets.SecretStoreKind, "vault", name
		dto := toExternalSecretDTO(e, now)
		out["externalSecrets"] = append(out["externalSecrets"], goldenCase{name, nowU, dto, dto.Rule})
	}
	secret("synced and fresh", externalsecrets.ExternalSecret{State: externalsecrets.Synced, RefreshInterval: time.Hour, RefreshTime: now.Add(-10 * time.Minute)})
	secret("synced but refresh overdue", externalsecrets.ExternalSecret{State: externalsecrets.Synced, RefreshInterval: time.Hour, RefreshTime: now.Add(-3 * time.Hour), Stale: true})
	secret("synced, refresh disabled", externalsecrets.ExternalSecret{State: externalsecrets.Synced, RefreshTime: now.Add(-48 * time.Hour)})
	secret("synced, never refreshed", externalsecrets.ExternalSecret{State: externalsecrets.Synced, RefreshInterval: time.Hour})
	secret("failed", externalsecrets.ExternalSecret{State: externalsecrets.Failed, Reason: "SecretSyncedError"})
	secret("failed and overdue", externalsecrets.ExternalSecret{State: externalsecrets.Failed, RefreshInterval: time.Hour, RefreshTime: now.Add(-3 * time.Hour), Stale: true})
	secret("unknown", externalsecrets.ExternalSecret{State: externalsecrets.Unknown})
	secret("no state", externalsecrets.ExternalSecret{})
	secret("unrecognised state", externalsecrets.ExternalSecret{State: "Deleted"})

	store := func(name string, s externalsecrets.Store) {
		s.Kind, s.Name = externalsecrets.ClusterSecretStoreKind, name
		dto := toStoreDTO(s)
		out["stores"] = append(out["stores"], goldenCase{name, nowU, dto, dto.Rule})
	}
	store("ready", externalsecrets.Store{State: externalsecrets.Synced})
	store("failed", externalsecrets.Store{State: externalsecrets.Failed})
	store("unknown", externalsecrets.Store{State: externalsecrets.Unknown})
	store("no state", externalsecrets.Store{})
	store("unrecognised state", externalsecrets.Store{State: "Invalid"})

	claim := func(name string, c karpenter.NodeClaim) {
		c.Name, c.Pool = name, "default"
		c.Created = now.Add(-time.Duration(c.AgeSeconds) * time.Second)
		dto := toKarpenterClaimDTO(c)
		out["nodeClaims"] = append(out["nodeClaims"], goldenCase{name, nowU, dto, dto.Rule})
	}
	claim("ready", karpenter.NodeClaim{Ready: "ready", NodeName: "n1", AgeSeconds: 4000})
	claim("not ready, proven link", karpenter.NodeClaim{Ready: "not-ready", FailingType: "Launched", FailingStatus: "False", AgeSeconds: 300})
	claim("not ready, unproven link", karpenter.NodeClaim{Ready: "not-ready", FailingType: "Registered", FailingStatus: "Unknown", AgeSeconds: 300})
	claim("not ready, no link", karpenter.NodeClaim{Ready: "not-ready", AgeSeconds: 300})
	claim("launching within the deadline", karpenter.NodeClaim{Ready: "unknown", AgeSeconds: 100})
	claim("launching at exactly the deadline", karpenter.NodeClaim{Ready: "unknown", AgeSeconds: 900})
	claim("launching past the deadline", karpenter.NodeClaim{Ready: "unknown", AgeSeconds: 901})
	claim("initializing within the deadline", karpenter.NodeClaim{Ready: "unknown", NodeName: "n2", AgeSeconds: 600})
	claim("initializing past the deadline", karpenter.NodeClaim{Ready: "unknown", NodeName: "n2", AgeSeconds: 2000})

	pool := func(name string, p karpenter.NodePool) {
		p.Name = name
		dto := toKarpenterPoolDTO(p)
		out["nodePools"] = append(out["nodePools"], goldenCase{name, nowU, dto, dto.Rule})
	}
	pool("ready", karpenter.NodePool{Ready: "ready"})
	pool("not ready", karpenter.NodePool{Ready: "not-ready", Reason: "NodeClassNotReady"})
	pool("unknown", karpenter.NodePool{Ready: "unknown"})
	pool("no state", karpenter.NodePool{})

	got, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(nativeRulesFixture), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(nativeRulesFixture, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(nativeRulesFixture)
	if err != nil {
		t.Fatalf("read %s (run with -update to create it): %v", nativeRulesFixture, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date with the Go rules; regenerate with -update and re-run the vitest parity harness", nativeRulesFixture)
	}
}
