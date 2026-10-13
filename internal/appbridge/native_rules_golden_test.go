package appbridge

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/externalsecrets"
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/helmcli"
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
	// Only Periodic refresh goes stale (Codex review 2026-10-13 #8).
	secret("synced once (CreatedOnce), long ago", externalsecrets.ExternalSecret{State: externalsecrets.Synced, RefreshPolicy: "CreatedOnce", RefreshInterval: time.Hour, RefreshTime: now.Add(-72 * time.Hour)})
	secret("refreshes on change (OnChange), long ago", externalsecrets.ExternalSecret{State: externalsecrets.Synced, RefreshPolicy: "OnChange", RefreshInterval: time.Hour, RefreshTime: now.Add(-72 * time.Hour)})
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

	reconciliation := func(name string, r flux.Resource) {
		r.Kind, r.Namespace, r.Name = flux.KustomizationKind, "flux-system", name
		dto := ToFluxDTO(r, now)
		out["fluxReconciliations"] = append(out["fluxReconciliations"], goldenCase{name, nowU, dto, dto.Rule})
	}
	reconciliation("ready", flux.Resource{Ready: flux.Ready})
	reconciliation("failed", flux.Resource{Ready: flux.Failed, Reason: "BuildFailed"})
	reconciliation("reconciling", flux.Resource{Ready: flux.Reconciling})
	reconciliation("unknown while progressing", flux.Resource{Ready: flux.Unknown, Reason: "Progressing"})
	reconciliation("unknown while progressing with retry", flux.Resource{Ready: flux.Unknown, Reason: "ProgressingWithRetry"})
	reconciliation("unknown while reconciling", flux.Resource{Ready: flux.Unknown, Reason: "Reconciling"})
	reconciliation("unknown, no reason", flux.Resource{Ready: flux.Unknown})
	reconciliation("suspended and failed (paused, not attention)", flux.Resource{Ready: flux.Failed, Suspended: true})
	reconciliation("suspended and ready", flux.Resource{Ready: flux.Ready, Suspended: true})
	reconciliation("spec unobserved (Syncing…)", flux.Resource{Ready: flux.Ready, Stale: true, Generation: 4, ObservedGeneration: 3})
	reconciliation("failed while unobserved", flux.Resource{Ready: flux.Failed, Stale: true})
	reconciliation("no ready state", flux.Resource{})
	reconciliation("unrecognised ready state", flux.Resource{Ready: "Stalled"})

	source := func(name string, src flux.Source) {
		src.Kind, src.Namespace, src.Name = flux.GitRepositoryKind, "flux-system", name
		dto := toSourceDTO(src)
		out["fluxSources"] = append(out["fluxSources"], goldenCase{name, nowU, dto, dto.Rule})
	}
	source("ready", flux.Source{Ready: flux.Ready})
	source("failed", flux.Source{Ready: flux.Failed, Reason: "GitOperationFailed"})
	source("reconciling", flux.Source{Ready: flux.Reconciling})
	source("unknown while progressing", flux.Source{Ready: flux.Unknown, Reason: "Progressing"})
	source("unknown", flux.Source{Ready: flux.Unknown})
	source("suspended and failed", flux.Source{Ready: flux.Failed, Suspended: true})
	source("no ready state", flux.Source{})

	app := func(name string, a argo.App) {
		a.Namespace, a.Name, a.Project = "argocd", name, "default"
		dto := toArgoAppDTO(a)
		out["argoApplications"] = append(out["argoApplications"], goldenCase{name, nowU, dto, dto.Rule})
	}
	for _, h := range []string{"Healthy", "Progressing", "Degraded", "Suspended", "Missing", "Unknown", ""} {
		for _, sync := range []string{"Synced", "OutOfSync", "Unknown"} {
			app(sync+"/"+h, argo.App{SyncStatus: sync, HealthStatus: h})
		}
	}

	appSet := func(name string, a argo.ApplicationSet) {
		a.Namespace, a.Name = "argocd", name
		dto := toArgoAppSetDTO(a)
		out["argoApplicationSets"] = append(out["argoApplicationSets"], goldenCase{name, nowU, dto, dto.Rule})
	}
	appSet("healthy", argo.ApplicationSet{Healthy: true})
	appSet("unhealthy", argo.ApplicationSet{Conditions: []argo.ApplicationSetCondition{{Type: "ErrorOccurred", Status: "True", Message: "boom"}}})

	cond := func(status string, observed int64) gwapi.Cond {
		return gwapi.Cond{Status: status, Reason: "R", Message: "m", ObservedGeneration: observed}
	}
	gateway := func(name string, accepted, programmed gwapi.Cond, gen int64) {
		dto := toGatewayRefDTO(gwapi.GatewayRef{Namespace: "infra", Name: name, ClassName: "eg", AcceptedCond: accepted, ProgrammedCond: programmed, Generation: gen})
		out["gateways"] = append(out["gateways"], goldenCase{name, nowU, dto, dto.Rule})
	}
	gateway("no status reported", gwapi.Cond{}, gwapi.Cond{}, 2)
	gateway("acceptance pending", cond("Unknown", 2), gwapi.Cond{}, 2)
	gateway("not accepted", cond("False", 2), cond("True", 2), 2)
	gateway("not accepted, last observed", cond("False", 1), cond("True", 1), 2)
	gateway("programming not reported", cond("True", 2), gwapi.Cond{}, 2)
	gateway("programming", cond("True", 2), cond("Unknown", 2), 2)
	gateway("not programmed", cond("True", 2), cond("False", 2), 2)
	gateway("not programmed, last observed", cond("True", 2), cond("False", 1), 2)
	gateway("programmed", cond("True", 2), cond("True", 2), 2)
	gateway("programmed, accepted last observed (syncing)", cond("True", 1), cond("True", 2), 2)
	gateway("no generation recorded", cond("True", 0), cond("True", 0), 0)

	listeners := []gwapi.Listener{{Name: "https", Protocol: "HTTPS", Port: 443}, {Name: "http", Protocol: "HTTP", Port: 80}}
	ok := func(n int) []gwapi.Backend {
		var bs []gwapi.Backend
		for i := 0; i < n; i++ {
			bs = append(bs, gwapi.Backend{Kind: "Service", Namespace: "shop", Name: fmt.Sprintf("web-%d", i), Port: 80, State: gwapi.BackendResolved, Endpoints: &gwapi.EndpointCount{Ready: 3, Total: 3}})
		}
		return bs
	}
	withPrimary := func(b gwapi.Backend, others int) []gwapi.Backend { return append([]gwapi.Backend{b}, ok(others)...) }
	resolved := func(ep *gwapi.EndpointCount) gwapi.Backend {
		return gwapi.Backend{Kind: "Service", Namespace: "shop", Name: "web", Port: 80, State: gwapi.BackendResolved, Endpoints: ep}
	}
	type routeDTOWithParent struct {
		Route     RouteNodeDTO  `json:"route"`
		Listeners []ListenerDTO `json:"listeners"`
	}
	listenerDTOs := []ListenerDTO{}
	for _, l := range listeners {
		listenerDTOs = append(listenerDTOs, ListenerDTO{Name: l.Name, Protocol: l.Protocol, Port: l.Port})
	}
	route := func(name string, rn gwapi.RouteNode) {
		rn.Namespace, rn.Name = "shop", name
		if rn.Generation == 0 {
			rn.Generation = 3
		}
		dto := toRouteNodeDTO(rn, listeners)
		out["routes"] = append(out["routes"], goldenCase{name, nowU, routeDTOWithParent{dto, listenerDTOs}, dto.Rule})
	}
	acc := func(rn gwapi.RouteNode) gwapi.RouteNode {
		rn.ParentFound, rn.AcceptedCond, rn.ResolvedRefsCond = true, cond("True", 3), cond("True", 3)
		return rn
	}
	route("resolved", acc(gwapi.RouteNode{Backends: ok(1)}))
	route("no status for this Gateway", gwapi.RouteNode{Backends: ok(1)})
	route("not accepted", func() gwapi.RouteNode {
		r := acc(gwapi.RouteNode{Backends: ok(1)})
		r.AcceptedCond = cond("False", 3)
		return r
	}())
	route("not accepted, last observed", func() gwapi.RouteNode {
		r := acc(gwapi.RouteNode{Backends: ok(1)})
		r.AcceptedCond = cond("False", 2)
		return r
	}())
	route("acceptance pending outranks refs not reported", gwapi.RouteNode{ParentFound: true, AcceptedCond: cond("Unknown", 3), Backends: ok(1)})
	route("acceptance not reported", gwapi.RouteNode{ParentFound: true, ResolvedRefsCond: cond("True", 3), Backends: ok(1)})
	route("unresolved refs", func() gwapi.RouteNode {
		r := acc(gwapi.RouteNode{Backends: ok(1)})
		r.ResolvedRefsCond = cond("False", 3)
		return r
	}())
	route("unresolved refs, last observed", func() gwapi.RouteNode {
		r := acc(gwapi.RouteNode{Backends: ok(1)})
		r.ResolvedRefsCond = cond("False", 1)
		return r
	}())
	route("refs pending", func() gwapi.RouteNode {
		r := acc(gwapi.RouteNode{Backends: ok(1)})
		r.ResolvedRefsCond = cond("Unknown", 3)
		return r
	}())
	route("status syncing", func() gwapi.RouteNode {
		r := acc(gwapi.RouteNode{Backends: ok(1)})
		r.AcceptedCond = cond("True", 2)
		return r
	}())
	route("listener missing", acc(gwapi.RouteNode{ParentSectionName: "grpc", Backends: ok(1)}))
	route("listener present", acc(gwapi.RouteNode{ParentSectionName: "https", Backends: ok(1)}))
	route("no backendRefs", acc(gwapi.RouteNode{}))
	route("unsupported backend kind", acc(gwapi.RouteNode{Backends: []gwapi.Backend{{Kind: "ServiceImport", Name: "x", State: gwapi.BackendUnsupported}}}))
	route("backend with no state", acc(gwapi.RouteNode{Backends: []gwapi.Backend{{Kind: "Service", Name: "x"}}}))
	route("backend not found", acc(gwapi.RouteNode{Backends: []gwapi.Backend{{Kind: "Service", Name: "gone", State: gwapi.BackendNotFound}}}))
	route("primary backend not found of several", acc(gwapi.RouteNode{Backends: withPrimary(gwapi.Backend{Kind: "Service", Name: "gone", State: gwapi.BackendNotFound}, 1)}))
	route("backend unreadable", acc(gwapi.RouteNode{Backends: []gwapi.Backend{{Kind: "Service", Name: "x", State: gwapi.BackendUnreadable, Error: "forbidden"}}}))
	route("endpoints not read", acc(gwapi.RouteNode{Backends: []gwapi.Backend{resolved(nil)}}))
	route("endpoints unreadable", acc(gwapi.RouteNode{Backends: []gwapi.Backend{resolved(&gwapi.EndpointCount{Error: "forbidden"})}}))
	route("no endpoints", acc(gwapi.RouteNode{Backends: []gwapi.Backend{resolved(&gwapi.EndpointCount{})}}))
	route("no endpoints, primary of several", acc(gwapi.RouteNode{Backends: withPrimary(resolved(&gwapi.EndpointCount{}), 2)}))
	route("no ready endpoints", acc(gwapi.RouteNode{Backends: []gwapi.Backend{resolved(&gwapi.EndpointCount{Ready: 0, Total: 2})}}))
	route("some endpoints ready", acc(gwapi.RouteNode{Backends: []gwapi.Backend{resolved(&gwapi.EndpointCount{Ready: 2, Total: 3})}}))
	// Every traffic-carrying backend is diagnosed (Codex review 2026-10-13 #6).
	route("second backend of two has no endpoints", acc(gwapi.RouteNode{Backends: append(ok(1), func() gwapi.Backend {
		b := resolved(&gwapi.EndpointCount{})
		b.Name = "web-b"
		return b
	}())}))
	route("every carrying backend has no endpoints", acc(gwapi.RouteNode{Backends: func() []gwapi.Backend {
		a, b := resolved(&gwapi.EndpointCount{}), resolved(&gwapi.EndpointCount{Ready: 0, Total: 2})
		a.Name, b.Name = "web-a", "web-b"
		return []gwapi.Backend{a, b}
	}()}))
	route("a weight-0 backend is not diagnosed", acc(gwapi.RouteNode{Backends: append(ok(1), func() gwapi.Backend {
		b := resolved(&gwapi.EndpointCount{})
		b.Name, b.Weight, b.WeightSet = "canary", 0, true
		return b
	}())}))
	route("every backend has weight 0", acc(gwapi.RouteNode{Backends: func() []gwapi.Backend {
		bs := ok(2)
		for i := range bs {
			bs[i].WeightSet = true
		}
		return bs
	}()}))
	route("second backend partly ready", acc(gwapi.RouteNode{Backends: append(ok(1), func() gwapi.Backend {
		b := resolved(&gwapi.EndpointCount{Ready: 1, Total: 3})
		b.Name = "web-b"
		return b
	}())}))
	route("not accepted and no endpoints (fault first)", func() gwapi.RouteNode {
		r := acc(gwapi.RouteNode{Backends: []gwapi.Backend{resolved(&gwapi.EndpointCount{})}})
		r.AcceptedCond = cond("False", 3)
		return r
	}())

	release := func(name string, r helmcli.Release) {
		r.Namespace, r.Name, r.Chart, r.Revision = "shop", name, "web-1.2.3", 4
		dto := toHelmReleaseDTO(r, now)
		out["helmReleases"] = append(out["helmReleases"], goldenCase{name, nowU, dto, dto.Rule})
	}
	release("deployed", helmcli.Release{Status: "deployed", UpdatedUnix: nowU - 86400})
	release("failed", helmcli.Release{Status: "failed", UpdatedUnix: nowU - 60})
	release("pending-install within the deadline", helmcli.Release{Status: "pending-install", UpdatedUnix: nowU - 60})
	release("pending-upgrade at exactly the deadline", helmcli.Release{Status: "pending-upgrade", UpdatedUnix: nowU - 600})
	release("pending-upgrade one second past the deadline", helmcli.Release{Status: "pending-upgrade", UpdatedUnix: nowU - 601})
	release("pending-rollback with no updated time", helmcli.Release{Status: "pending-rollback"})
	release("uninstalling past the deadline", helmcli.Release{Status: "uninstalling", UpdatedUnix: nowU - 3600})
	release("superseded (historical)", helmcli.Release{Status: "superseded", UpdatedUnix: nowU - 3600})
	release("uninstalled (historical)", helmcli.Release{Status: "uninstalled", UpdatedUnix: nowU - 3600})
	release("no status", helmcli.Release{})
	release("unrecognised status", helmcli.Release{Status: "unknown"})

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

// A route with no backendRefs (redirect-only) must reach the lens as empty
// arrays, never null: the lane code reads backends[0] and used to crash.
func TestRouteNodeDTOSlicesSerializeAsArrays(t *testing.T) {
	b, err := json.Marshal(toRouteNodeDTO(gwapi.RouteNode{Namespace: "shop", Name: "redirect", ParentFound: true}, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"backends":[]`, `"services":[]`, `"matches":[]`, `"hostnames":[]`, `"policies":[]`} {
		if !bytes.Contains(b, []byte(field)) {
			t.Errorf("want %s in %s", field, b)
		}
	}
}
