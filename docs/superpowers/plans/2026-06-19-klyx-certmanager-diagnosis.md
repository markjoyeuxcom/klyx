# Klyx M11 Implementation Plan (cert-manager diagnosis)

**Goal:** A capability-gated "Certificates" cluster section that diagnoses a
stuck cert-manager Certificate without a terminal - the M10 Flux-diagnosis
pattern applied to cert-manager. Read or day-2-drive only.

**Spec:** `docs/superpowers/specs/2026-06-19-klyx-certmanager-diagnosis-design.md`

**Architecture:** Hybrid, exactly like Flux. Lazy-watch Certificates + Issuers +
ClusterIssuers (push `certmanager:updated`); resolve the
`Certificate -> CertificateRequest -> Order -> Challenge` chain on demand when a
cert is selected. New pure package `internal/certmanager` (clones
`internal/gitops/flux`); fleet watch + on-demand resolver; `CertManagerService`
+ DTOs; a new `"certs"` React section.

**Tech stack:** Go + client-go (unstructured, dynamic informers, on-demand
LISTs); React + TS + Zustand. No cert-manager Go API dependency.

## Sub-milestones (ship independently, in order)

### M11-a - Capability detection - SHIPPED (commit "detect cert-manager capability")
- [x] `internal/capability`: `CertManagerCapability` (flattened like
      NetworkCapability: Present/Healthy/HasACME) on `Set`; `detectCertManager`
      (cert-manager.io presence + `cert-manager` controller Deployment health;
      acme.cert-manager.io -> HasACME). Detector table tests.
- [x] `internal/appbridge/dto.go`: `CertManagerTier/Reason/Present/Healthy` on
      `ClusterDTO` + `ToDTO`.
- [x] frontend: 4 fields on `ClusterDTO`; `certManagerCapability` row in the
      sidebar CapabilitiesBlock.

### M11-b - Certificates lens + section - SHIPPED (commit "cert-manager Certificates lens")
- [x] `internal/certmanager/certmanager.go`: `Certificate`/`Issuer`,
      `ParseCertificate`/`ParseIssuer`, Issuing-precedence `readyFromConditions`.
      Parser tests.
- [x] `internal/fleet/certmanagerwatch.go`: lazy informers (certificates/issuers/
      clusterissuers); `CertManagerCertificates`/`CertManagerIssuers`/
      `CertManagerObject`; `Conn` interface + `fakeConn`. Watch tests.
- [x] `internal/appbridge`: `CertManagerService` (`Open`/`Close`/`pushLoop`,
      `certmanager:updated`), `CertificateDTO`/`IssuerDTO` (+server-side expiry),
      `main.go` registration. Service tests.
- [x] frontend: `"certs"` section (5 touch points), `CertManagerSlice`,
      `bridge/certmanager.ts`, `CertsView.tsx` (broken-first two-pane list +
      issuers filter + per-cert inspector with the bound-issuer headline). View
      tests.

### M11-c - Issuance chain inspector - SHIPPED (commit "cert-manager issuance chain inspector")
- [x] `internal/certmanager/chain.go`: `BoundIssuer`, `ChainNode`/`Chain`,
      `ParseChainNode` (Ready condition / ACME `status.state`), `FirstFailingLink`.
      Chain tests.
- [x] `internal/fleet/certmanagerchain.go`: `GetCertificateChain` - bound issuer
      health + latest CertificateRequest -> Order -> Challenge via ownerRef
      traversal; non-ACME Order-list errors swallowed; soft failures -> warnings.
      `Conn` + `fakeConn`. Resolver tests.
- [x] `internal/appbridge`: `CertChainDTO`/`ChainNodeDTO` + `GetCertificateChain`
      bound method. Chain DTO + cluster-miss tests.
- [x] frontend: chain types + slice; `getCertificateChain` bridge; inspector
      renders the request->order->challenge tree, flags the failing link "stuck
      here", server-resolved bound issuer drives the headline. Chain view test.

### M11-d - Renew (optional day-2 verb) - DEFERRED
- [ ] `internal/cmctl`: `Available()` + `Renew(ctx, kubeconfig, context, ns, name)`
      shell-out (clone `internal/fluxcli`). cmctl renew sets the Issuing status
      condition - drives the controller like `flux reconcile`; no desired-state
      authoring.
- [ ] `ClusterConn.RenewCertificate` (gated on `cmctl.Available()`);
      `CertManagerService.Renew` -> `ActionResultDTO`; `CmctlAvailable()`.
- [ ] frontend: a **Renew** button in the inspector, shown only when available,
      behind the existing `ConfirmDialog` (Protected-aware).
- [ ] Hold until the owner has natively eyeballed M11-c (it is the one write;
      read-only may be preferred first).

## Verification / gate

- Go (in the devcontainer): `go test ./internal/...`, `go test -race`
  (certmanager/fleet/appbridge), `go vet ./internal/...`, `gofmt -l`. ALL GREEN.
  (Root `go test ./...` fails on the embedded `frontend/dist` - test
  `./internal/...` directly, per the Mac-rebuild handoff.)
- Frontend (in the devcontainer): `npx vitest run src/store/ src/cluster/CertsView.test.tsx`
  GREEN (the new store + CertsView specs). `npx tsc --noEmit` clean modulo the
  pre-existing untracked `bindings/` errors. Full `vitest`/`npm run build` need
  the wails-generated `bindings/` dir, which only exists after `wails3 build`
  (Mac/cloud only) - so the Sidebar/full suite + binding regen run there.
- Native (owner, on the Mac): `wails3 build` regenerates the `CertManagerService`
  binding; eyeball the Certificates section against the homelab and a cluster
  running cert-manager + ACME (see spec section 7).

## Self-review

- Non-goal check: every M11 capability is read (capability, list, chain) or, in
  the deferred M11-d, day-2-drive (Renew = the cmctl status trigger). Nothing
  authors desired state.
- Identity: capability-gated (Absent -> honest empty state), sentence case, mono
  for K8s identifiers, Tabler icons, light+dark, `nil != 0` for expiry. No new
  heavy frontend deps.
- Pattern fidelity: parser clones `internal/gitops/flux`; watch clones
  `gitopswatch.go`; on-demand resolver clones `GetGatewayTopology`; service
  clones `GitOpsService`; view clones `GitOps.tsx`.
