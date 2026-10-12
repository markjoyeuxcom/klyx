# Klyx health 1c, slice 6: the final coverage-parity gate

Status: Codex APPROVE-WITH-CHANGES (2026-10-12); amended below per its seven required changes (§1 isolation and oracle, §2 case 6 and the endpoint fixture, §3 recovery timing, the settled barrier and rendered parity, §5 acceptance wording), then approved with one fixture amendment (§1: the Recreate strategy for the controller taken down). Parent spec: `2026-10-11-klyx-health-1c-cluster-summary.md` (§9 slice 6, §8 "not covered"). Slices 1-5 are closed: every attention surface - the open cluster's mast, tree and Overview, and the fleet's spine, meters and cards - reads the Observer's summary (plus the open lens's rows) through `nav/clusterAttention.ts`, and the health list endpoints are gone.

## 0. Outcome and limits

The parent spec's deletion condition (layout spec §4): "not covered" and the transitional qualification of the quiet state go only after a gate on a kind fixture shows the Observer covers every case the old surfaces covered. This slice builds that gate, runs it, and - only on a pass, and after that product change is reviewed - removes "not covered".

The gate is evidence, not a CI test: it runs against a local kind cluster through the production connection, like the §7 lab (`internal/fleet/lab_test.go`). It is skipped unless its environment variables are set, and it refuses any target but the owned parity cluster (§1).

Not in this slice: any policy change to a domain's rules (they belong to that domain's health spec), and the real-cluster smoke test (the owner's).

## 1. The fixture cluster

**Isolation (review 1).** Every mutating command - the script's and the Go stage's - accepts exactly one target: the kind cluster `klyx-parity` (context `kind-klyx-parity`) that the script itself created.

- `up` creates it with `kind create cluster --name klyx-parity` and then writes an ownership marker: a ConfigMap `kube-system/klyx-parity-owner` holding a random token, the token also kept in the script's state file under the scratch directory. If a `klyx-parity` cluster already exists without a matching marker, `up` aborts; it never adopts or deletes a cluster it did not create.
- Every later command (`case`, `revert`, `down`) and the Go stage check, before any write: the context name is exactly `kind-klyx-parity`; its API server equals the one `kind get kubeconfig --name klyx-parity` reports, on a loopback address; and the marker's token matches the state file. Any mismatch aborts with the reason.
- `kind-klyx` is a read-only CRD source: the script only runs `kubectl get crd` against it. It never writes there (the owner's lab state and the §7 load fixture live there).

**Contents.**

- CRDs only - no real controllers - for Flux (kustomize, helm, source), Argo CD (Application, ApplicationSet, AppProject), cert-manager (Certificate, CertificateRequest, Issuer, ClusterIssuer, Order, Challenge), External Secrets (ExternalSecret, SecretStore, ClusterSecretStore), Gateway API (standard: GatewayClass, Gateway, HTTPRoute, GRPCRoute, ReferenceGrant) and Karpenter (NodePool, NodeClaim), copied from `kind-klyx` with server-set metadata and status stripped (no network). The script fails with a named reason if a CRD is missing there.
- Status is written through the status subresource (`kubectl patch --subresource=status`) where the CRD has one; Argo CD's Application has none (as upstream), so its status is written with the object. With no controllers, nothing rewrites it.
- Stand-in controllers under the detector's conventional names (`internal/capability/detector.go`): `kustomize-controller` in `flux-system`, the `argocd-application-controller` StatefulSet in `argocd`, `cert-manager` in `cert-manager`, `external-secrets` in `external-secrets`, `karpenter` in `karpenter`. Each runs the pause image already on the kind node (`registry.k8s.io/pause:3.10`, `imagePullPolicy: Never`), so it becomes Available without a registry. A controller is taken down by an unsatisfiable `nodeSelector` on a stand-in with `replicas: 1` and `strategy.type: Recreate`, so the old pod is removed before the new one stays Pending: the case waits for `availableReplicas` 0 and the Pending pod. Under the default RollingUpdate the old pod would stay (25% of one replica rounds down to zero unavailable) and the detector would keep reading it healthy; scaling to zero would not do either - the detector reads 0 of 0 as ready. The revert waits for the Pending pod to go and the healthy replacement to be Available.
- A GatewayClass with `Accepted=True` (status-patched), so the network tier is healthy until a case says otherwise.

**The oracle (review 2).** Expectations are declared, independent of Klyx's rules, on each fixture object and in the case table:

- per object, the annotations `klyx.dev/parity-role` (`fault`, `degraded`, `unknown`, `progressing`, `paused`, `ok`) and `klyx.dev/parity-included` (`true`/`false`) - Included is declared apart from Role, since a progressing object can be included;
- per part, the expected counted membership: the UIDs the part counts (for routes, the attached routes after the part's reduction), its total and its role partition;
- verdict parts compare `Counts`; native parts compare `Native` (roles and included); Events compares its evidence total only (§2);
- for a forbidden or stale capture, only the objects the identity could read, or the explicitly last-good set, are compared - never every current admin object.

The core parts' reference is an admin list of the same kinds (workloads, pods, nodes), with the case's declared core expectations (case 6 below); kind's own workloads are otherwise healthy and the fixture adds none.

## 2. The cases

The baseline B is everything healthy: controllers Available, one healthy object per counted kind of every part (Flux Kustomization, HelmRelease and each source kind; an Application and an ApplicationSet; a Certificate with its Issuer and a ClusterIssuer; an ExternalSecret with its SecretStore and a ClusterSecretStore; the accepted GatewayClass, a programmed Gateway and an attached, resolved HTTPRoute whose selector-less backend Service has an EndpointSlice with one ready endpoint; a NodePool and a launched NodeClaim), kind's own workloads, pods and nodes, and no Helm release.

Each case is applied on B, asserted at the settled barrier (§3), then reverted, and B is asserted again at the barrier before the next case. One fixture per case:

| # | Case | Expected |
|---|------|----------|
| B | baseline | every expected area present and quiet, every counted kind live, no gap, no controller issue: the "cluster ok" reading (mast, spine healthy lamp, card band 0) |
| 1 | a failed unused Issuer (`Ready=False`, referenced by no Certificate) | `certs/issuers` lit with 1; `certs/certificates` quiet |
| 2 | a failed unused SecretStore (`Ready=False`, referenced by no ExternalSecret) | `eso/stores` lit with 1; `eso/externalsecrets` quiet |
| 3 | an ApplicationSet failure (`ErrorOccurred=True`) | `argo/applicationsets` lit with 1; `argo/applications` quiet |
| 4 | a failing Flux source (GitRepository `Ready=False`) | `flux/sources` lit with 1; `flux/reconciliations` quiet |
| 5a | a healthy Gateway with a route whose backend is not found (`ResolvedRefs=False`, `BackendNotFound`) | `gateway/routes` lit with 1; `gateway/gateways` quiet (programmed) |
| 5b | a healthy Gateway with a route whose resolved backend Service has an EndpointSlice with endpoints, none ready (one endpoint `ready=false`) - "no ready endpoints" (review 6) | `gateway/routes` lit with 1; `gateway/gateways` quiet |
| 5c | the same with an EndpointSlice holding zero endpoints - the distinct "no endpoints" branch (`internal/gwapi/rules.go`) | `gateway/routes` lit with 1; `gateway/gateways` quiet |
| 6 | a controller down with no cert-manager object fault (`cert-manager` held unschedulable) | the `certs` area lit by "controller not ready" with no object count; every certs part quiet. Declared core expectations: `workloads` has the `cert-manager` Deployment unavailable and `pods` its Pending pod - the core attention this case causes, asserted, not ignored |
| 7 | a forbidden kind from the first read (no `list` and no `watch` on ClusterIssuers, so watch-list cannot bypass the denial) | `certs/issuers` a named gap, "ClusterIssuers could not be read (forbidden)"; the Issuers it can read are compared; never quiet, never a count presented as complete |
| 8 | RBAC revoked after a sync (`list` and `watch` on ExternalSecrets removed once the part is verified Live) | `eso/externalsecrets` reads last-good: stale, the recorded forbidden fault, the retained identities and counts unchanged, and the original `LastGoodAt`; never quiet, never "checking" |

Events is evidence, never a quiet or lit verdict area: Warning events the cases create (FailedScheduling in case 6, for one) stay in the evidence total after a revert, so B's re-assertion compares Events only as evidence present, never as zero.

## 3. The two stages

**Go stage** (`internal/fleet/parity_lab_test.go`; `KLYX_PARITY_CONTEXT=kind-klyx-parity` and `KLYX_PARITY_OUT=<dir>`; the target checks of §1 first).

*Identity and timing (review 3).*

- Cases B and 1-6 run as the cluster admin through the production connection (`DefaultConnFactory`), with its health sink read as `TestLab` does.
- Cases 7 and 8 run on a second production connection as a ServiceAccount bound to a ClusterRole with the Observer's read verbs (`list`, `watch`, `get`) on every kind but the case's. Its token comes from the TokenRequest API with a lifetime covering the run (one hour), written to a scratch kubeconfig (`ClusterConfig.Kubeconfig`) that the test deletes on cleanup.
- Case 8 keeps the same connection, Observer and incarnation throughout: the part is first verified Live; then `list` and `watch` on ExternalSecrets are removed from the ClusterRole. RBAC removal does not close an open watch, so the test then ends the authorized stream by restarting the parity cluster's API server (stopping its static-pod container on the verified parity node container `klyx-parity-control-plane` only; the kubelet restarts it), and waits for the reflector's real failed re-list.
- Controller health changes only on the capability re-probe (`capReprobeEvery`, 5m in production). The test sets that package seam to 10s for the run - a cadence-only lab override; the production detector, probe and mapping run unchanged - and discloses it in the report. Per-case bounds: 90s for a case to settle, 120s for the API-server restart in case 8; a timeout fails the case naming the last summary.

*The settled barrier (review 4).* A case is captured only when, in one summary, all of these match together: the full expected task and part set; every counted kind's coverage (live, stale with its last-good time, unreadable with its class); every controller state; every part's counts by the oracle (§1); and the connection overlay (Synced, or the case's declared state). Then, with every part's rows subscribed, the rows' included count is compared with the summary part's at the same incarnation and the same per-part `Ver`. The same barrier is re-asserted after each revert, including settled core recovery (case 6's Deployment Available and its pod gone).

*Capture.* Each case writes `$KLYX_PARITY_OUT/<case>.json`: the summary, the connection's `ClusterDTO`, the matching rows envelopes, the case's oracle expectations, and the run's identity (a run id, the commit SHA, the start time). The frontend stage refuses a capture whose run id or commit differs from the manifest the Go stage writes last, so a missing or stale artifact cannot pass.

**Frontend stage** (`nav/parityGate.test.tsx`, skipped unless `KLYX_PARITY_OUT` is set).

- *Model.* For each captured case it loads the summary and the rows into the real health store and asserts, through `clusterAttention`, `mastReading`, `spineLamp`, `healthReading` and `clusterDiagnosticScore`: every step 7e area (the eleven `TASKS`) present with the oracle's counts; the case's part lit; the controller-only light with no count; the forbidden gap named; the last-good line stale with its time; `notCovered` empty in every case; the healthy lamp, mast "no attention" and band 0 in B only.
- *Rendered (review 5).* It also mounts the mast (`TopBar`), the tree (`NavBand`), the spine (`FleetSpine`), the card (`ClusterCard`) and `Overview` on each capture, with the real store and adapters and the transport stubbed, and asserts the part-specific number and wording, the controller-only light, the named gap and the last-good time where each surface shows them, and that no list endpoint is called. Where the one-number rule is tested, the matching rows are loaded (core rows envelopes carry no native Included value; their parts compare counts).

The gate passes when both stages pass every case. The run's report (cases, counts, durations, the cadence override) goes in the commit message and the PR.

## 4. After the pass: "not covered" goes

- `nav/clusterAttention.ts`: `NotCovered` and the fail-closed entry go. A task or part the current summary does not carry - capability detection ahead of the Observer's, or a summary from before the capability appeared - is answered as "checking…" (a gap; never quiet, never a count) until a summary or rows answer it. `mastReading`'s `notCovered` field goes. The expected-task mapping, the current-incarnation fencing, the capability-unknown qualification and the confirmed-absence omission stay as they are.
- Its readers: the Overview's not-covered rows and the quiet line's "N not covered by the summary" clause; the card's "unverified" not-covered entries and the "not covered" tool values; the spine's "not covered by the summary" line; `healthGaps`; the tree's not-covered notes. Each reads "checking…" instead, with "capability not yet known" kept where capability is not yet known.
- Tests that asserted "not covered" are rewritten to the checking reading; a test proves a part missing from a summary is never quiet.

## 5. Acceptance text (review 7)

The acceptance note (in `docs/backlog.md`'s 1c entry and the PR) says: attention is derived from the cluster summary, with per-kind coverage and as-of times. The informer-backed parts are live. The Helm and Events parts are periodic snapshots (Helm every 5 minutes, plus coordinated reads; Events every 60 seconds), not watches, and Events is evidence only, never attention. The remaining request reads, each with an as-of time, are the Helm and Events lens lists, the Gateway topology, Argo CD projects, and Karpenter's controller and activity reads.

## 6. Order of work

One reviewed commit each:

1. The fixture script, both gate stages, and the run's report (no product change).
2. On a pass: the "not covered" removal (§4) and the acceptance text (§5).

If a case fails, the failure is fixed in the Observer or the model as its own reviewed commit before step 2, and the gate is re-run in full.

## 7. The gate run (2026-10-12)

PASS at commit `5b37f36` (tracked tree clean), run `48dfa639b5e1faf9`, capability re-probe overridden to 10s for the run (the production detector unchanged), on `kind-klyx-parity` (kind v0.33.0, node v1.37.0).

Go stage (`TestParityGate`, 64s): every case settled against its oracle in one summary - every part's classifier, counted kinds, coverage, roles and included count, every controller, the connection - with every part's rows at the same incarnation and Ver and counted membership equal by UID.

| Case | Settled | Baseline back |
|------|---------|---------------|
| B | 0.97s | - |
| 1 failed unused Issuer | 1.00s | 0.96s |
| 2 failed unused SecretStore | 0.95s | 0.97s |
| 3 ApplicationSet failure | 0.96s | 0.97s |
| 4 failing GitRepository | 0.97s | 1.02s |
| 5a route, backend not found | 1.03s | 1.05s |
| 5b route, no ready endpoints | 0.97s | 1.03s |
| 5c route, no endpoints | 1.03s | 0.97s |
| 6 cert-manager down | 8.73s | 10.19s |
| 7 ClusterIssuers forbidden from the first read | 0.97s (incarnation 2) | - |
| 8 ExternalSecrets revoked after Live | at the restart's return (incarnation 3, unchanged) | - |

Case 8: last good 13:51:26.280, between verified Live (13:51:24.457) and the API-server restart's return (13:51:27.421), unchanged 15s later; the recorded fault forbidden; the retained set equal to the Live oracle.

Frontend stage (`nav/parityGate.test.tsx`): 2/2 - the model from the summary and with the open cluster's rows (one number either way), and the mast, tree, spine, card and Overview mounted on every capture with no list endpoint called. "cluster ok" (the healthy lamp, band 0, the mast's unqualified quiet) in B only.

Mutation checks are listed in the step 1 commit (`5b37f36`): eight frontend mutants and two Observer-rule mutants caught; a stale run id and a missing case fail the frontend stage.
