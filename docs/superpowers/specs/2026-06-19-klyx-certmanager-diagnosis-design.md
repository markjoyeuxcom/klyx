# Klyx M11 design (cert-manager diagnosis)

Date: 2026-06-19
Status: CM-a/b/c shipped; CM-d (Renew) deferred pending native eyeball
Scope: M11, a new diagnostic lens for cert-manager. Makes Klyx able to *diagnose
a stuck Certificate without dropping to a terminal*. Read or day-2-drive only
(never authors desired state - CLAUDE.md non-goal holds).

## 1. Context: where M11 came from, the gap it closes

The owner dropped four screenshots of a different desktop K8s client ("Klustr")
into `images/` and asked to build the worth-having ideas into Klyx, the Klyx way.
The standout was Klustr's cert-manager view: a `Certificate` detail surfacing the
failing condition ("Issuing certificate as Secret does not exist") with the
issuer it references.

Klyx already *detects* cert-manager CRDs in the generic Resources browser
(`frontend/src/cluster/resourceRisk.ts`) but had no capability tier and no lens.
A platform engineer's most common cert-manager failure is "the cert is stuck
Issuing", and the root cause lives one or two objects away: a denied
`CertificateRequest`, an errored ACME `Order`, a failing `Challenge`, or a
not-ready bound `Issuer`/`ClusterIssuer`. Walking that chain by hand is
`kubectl describe` x4 - exactly the terminal round-trip Klyx exists to remove.

This is the **same problem M10 solved for Flux** (bound-source health -> failing
reason -> blocked-by -> events), applied to cert-manager. Bound-**issuer** health
is to a stuck `Certificate` what bound-**source** health is to a stuck
`Kustomization`. So M11 is a near-direct clone of the M3/M10 vertical, not new
architecture.

## 2. Scope and honest boundaries

In scope:
- **M11-a Capability detection.** A cert-manager tier (`cert-manager.io` CRD
  presence + controller-Deployment health; `acme.cert-manager.io` tracked as
  `HasACME`). Surfaced as a sidebar capability row.
- **M11-b Certificates lens.** A capability-gated "Certificates" cluster section:
  a broken-first `Certificate` triage list (status, issuer, secret, expiry,
  reason), an issuers filter showing `Issuer`/`ClusterIssuer` health, and a
  per-cert inspector. Lazy-watch the three list kinds; push `certmanager:updated`.
- **M11-c Issuance chain inspector.** On expand, resolve the bound issuer's
  health (the headline) and the `Certificate -> CertificateRequest -> Order ->
  Challenge` tree, singling out the deepest stuck node as the failing link.

Deferred:
- **M11-d Renew (day-2 verb).** A gated `cmctl renew` shell-out (drives the
  controller, like reconcile; never authors state). Held until the owner
  natively eyeballs M11-c, since it is the one write and read-only may be
  preferred first.

Explicitly NOT in M11:
- No editing/creating Certificates or Issuers (Git owns desired state).
- No ACME account management, no RBAC writes.
- No certificate content decode/inspection beyond status (the Secret stays
  masked like every other secret).

## 3. Data layer

### 3.1 `internal/certmanager` - pure parsers (no cert-manager Go API dep)
- `Certificate` / `ParseCertificate`: Ready/Issuing state, issuerRef, secretName,
  dnsNames, notAfter, renewalTime.
- `Issuer` / `ParseIssuer`: Ready state of an Issuer/ClusterIssuer (ns "" for
  ClusterIssuer).
- `readyFromConditions`: adapted from `flux.readyFromConditions`. cert-manager
  carries a Ready condition and, mid-issuance, an Issuing condition; **Issuing
  takes precedence** (the "actively working on it" signal, like Flux's
  Reconciling) but the Ready condition's reason is preferred for the headline.
- `chain.go`: `BoundIssuer`, `ChainNode`/`Chain`, `ParseChainNode` (Ready
  condition for CertificateRequests; `status.state`+`status.reason` for ACME
  Orders/Challenges via `acmeStateToReady`), `FirstFailingLink` (deepest
  not-Ready node, children before parents).

### 3.2 `internal/fleet`
- `certmanagerwatch.go`: lazy dynamic informers on `certificates`, `issuers`,
  `clusterissuers` (mirrors `gitopswatch.go`). `OpenCertManager`/`CloseCertManager`,
  `CertManagerCertificates`, `CertManagerIssuers`, `CertManagerObject`.
- `certmanagerchain.go`: `GetCertificateChain(ctx, ns, name)` - on-demand
  ownerRef traversal (mirrors `GetGatewayTopology`). Resolves the bound issuer
  from the watch store, lists CertificateRequests, picks the latest by
  `cert-manager.io/certificate-revision`, and resolves its Order -> Challenge
  subtree. Non-ACME issuers have no Orders: the list error is swallowed (not a
  warning). RBAC/other soft failures -> `Warnings`, chain still renders.
- Both added to the `Conn` interface + `fakeConn`.

## 4. appbridge
- `CertManagerService` (mirrors `GitOpsService`): `Open`/`Close`/`pushLoop`
  emitting `certmanager:updated {cluster, certificates, issuers}` on a 1s tick;
  `GetCertificateChain` request/response (30s ctx; cluster-miss/error ->
  warnings-only DTO).
- DTOs: `CertificateDTO` (expiry humanised server-side via `now`), `IssuerDTO`,
  `ChainNodeDTO`, `CertChainDTO`. `ClusterDTO` gains
  `certManagerTier/Reason/Present/Healthy`.

## 5. Frontend
- Store: `CertificateDTO`/`IssuerDTO`/`ChainNodeDTO`/`CertChainDTO`,
  `CertManagerSlice`, `"certs"` `ClusterSection`.
- Section touch points: `ClusterSection` union, `SECTION_LABELS`, Sidebar
  `SECTION_GROUPS` (delivery group, `IconCertificate`), `ClusterDetail` switch,
  `Placeholder` exhaustiveness.
- `bridge/certmanager.ts`: `openCertManager` (subscribe), `getCertificateChain`.
- `CertsView.tsx`: two-pane list+inspector (clone of `GitOps.tsx`). Broken-first
  rank (Failed/expired -> Issuing -> expiring-soon -> Ready). Inspector: the
  not-ready bound issuer is the danger headline; an "issuance chain" section
  renders the request->order->challenge tree with the failing link flagged
  "stuck here". Absent clusters get an honest "cert-manager not detected" state.

## 6. Decisions log

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Build it into Klyx (vs studying Klustr) | Owner directive; Klustr stays a reference |
| 2 | Hybrid: lazy-watch the list + on-demand resolve the chain | Certificates are list-stable; requests/orders/challenges are ephemeral and only needed per-selected-cert. Same split as Flux list-watch + on-demand detail / Gateway on-demand graph |
| 3 | Issuing takes precedence over Ready in the state mapping | cert-manager's Issuing=True is the truthful "in progress" signal; matches how Flux's Reconciling overrides Ready |
| 4 | Bound-issuer health is the detail headline | The diagnostic question is "is *this* cert's issuer working" - answer it where you're looking (M10 decision 1) |
| 5 | Resolve only the latest CertificateRequest's subtree | The current attempt is the actionable chain; older requests are historical noise and N extra LISTs |
| 6 | No Order list error => no warning | Non-ACME issuers (CA/SelfSigned/Vault) legitimately have no Orders; warning there would cry wolf |
| 7 | Renew via `cmctl` shell-out, deferred | Renew drives the controller (status trigger), not authoring; the CLI is version-robust. Held until native eyeball of the read-only lens |

## 7. Testing
- `internal/certmanager`: parser table tests (Ready/Issuing/Failed, ClusterIssuer
  ns "", ACME state mapping, dependsOn-style chain build, FirstFailingLink depth).
- `internal/fleet`: watch test (seed dynamic fake -> sorted list, issuer/object
  lookup), chain resolver test (full broken ACME chain -> failing link is the
  deepest stuck Challenge; non-ACME cert -> clean empty chain).
- `internal/appbridge`: DTO/expiry mapping, snapshot, push lifecycle, chain DTO
  tree mapping, cluster-miss warning.
- `internal/capability`: cert-manager tier (absent/degraded/healthy, HasACME).
- frontend: CertsView (absent empty state, broken-first order, issuer headline,
  issuers filter, chain tree + "stuck here").
- Native handoff (owner, on the Mac): the screenshots' k3d demo (ACME staging
  issuer + a stuck Certificate) reproduces the failure - Certificates section
  lists broken-first; expanding shows the issuer headline + the chain with the
  failing link; the issuers filter lists issuer health; a no-cert-manager
  cluster reads "cert-manager not detected".
