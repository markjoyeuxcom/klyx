# Klyx health 1c, slice 6: the final coverage-parity gate

Status: draft for Codex review (2026-10-12). Parent spec: `2026-10-11-klyx-health-1c-cluster-summary.md` (§9 slice 6, §8 "not covered"). Slices 1-5 are closed: every attention surface - the open cluster's mast, tree and Overview, and the fleet's spine, meters and cards - reads the Observer's summary (plus the open lens's rows) through `nav/clusterAttention.ts`, and the health list endpoints are gone.

## 0. Outcome and limits

The parent spec's deletion condition (layout spec §4): "not covered" and the transitional qualification of the quiet state go only after a gate on a kind fixture shows the Observer covers every case the old surfaces covered. This slice builds that gate, runs it, and - only on a pass - removes "not covered".

The gate is evidence, not a CI test: it runs against a local kind cluster through the production connection, like the §7 lab (`internal/fleet/lab_test.go`). It is skipped unless its environment variable names a `kind-*` context, and it refuses any other context.

Not in this slice: any policy change to a domain's rules (they belong to that domain's health spec), and the real-cluster smoke test (the owner's).

## 1. The fixture cluster

- A dedicated kind cluster, `kind-klyx-parity`, created and deleted by `scripts/lab/klyx-parity.sh up|case <name>|revert <name>|down`. It never touches `kind-klyx` (the owner's lab state and the §7 load fixture live there). The script refuses any context that is not `kind-*` on a loopback API server, as `klyx-load.sh` does.
- CRDs only - no real controllers - for Flux (kustomize, helm, source), Argo CD (Application, ApplicationSet, AppProject), cert-manager (Certificate, CertificateRequest, Issuer, ClusterIssuer, Order, Challenge), External Secrets (ExternalSecret, SecretStore, ClusterSecretStore), Gateway API (standard: GatewayClass, Gateway, HTTPRoute, GRPCRoute, ReferenceGrant) and Karpenter (NodePool, NodeClaim). They are copied from `kind-klyx` (no network); the script fails with a named reason if a CRD is missing there.
- Object status is written through the status subresource (`kubectl patch --subresource=status`); with no controllers, nothing rewrites it.
- Stand-in controllers under the detector's conventional names (`internal/capability/detector.go`): `kustomize-controller` in `flux-system`, the `argocd-application-controller` StatefulSet in `argocd`, `cert-manager` in `cert-manager`, `external-secrets` in `external-secrets`, `karpenter` in `karpenter`. Each runs the pause image already loaded on the kind node (`imagePullPolicy: Never`), so it becomes Available without a registry. A controller is taken down by an unsatisfiable `nodeSelector` (zero available); its objects are untouched.
- A GatewayClass with `Accepted=True` (status-patched), so the network tier is healthy until a case says otherwise.
- Every fixture object carries the annotation `klyx.dev/parity-expect: <role>` (`fault`, `degraded`, `unknown`, `progressing`, `paused`, `ok`, or `none` for a kind the part does not count). The reference counts in §3 come from these annotations, not from Klyx code.

## 2. The cases

The baseline is everything healthy: controllers Available, one healthy object per counted kind of every part (Flux Kustomization, HelmRelease and each source kind; an Application and an ApplicationSet; a Certificate with its Issuer and a ClusterIssuer; an ExternalSecret with its SecretStore and a ClusterSecretStore; the accepted GatewayClass, a programmed Gateway and an attached, resolved HTTPRoute whose backend Service has a ready endpoint; a NodePool and a launched NodeClaim), kind's own workloads, pods and nodes, and no Helm release.

Each case is applied on the baseline, asserted, then reverted, and the baseline is asserted again before the next case. One fixture per case:

| # | Case | Expected |
|---|------|----------|
| B | baseline | every expected area present and quiet, every counted kind live, no gap, no controller issue: the "cluster ok" reading (mast, spine healthy lamp, card band 0) |
| 1 | a failed unused Issuer (`Ready=False`, referenced by no Certificate) | `certs/issuers` lit with 1; `certs/certificates` quiet |
| 2 | a failed unused SecretStore (`Ready=False`, referenced by no ExternalSecret) | `eso/stores` lit with 1; `eso/externalsecrets` quiet |
| 3 | an ApplicationSet failure (`ErrorOccurred=True`) | `argo/applicationsets` lit with 1; `argo/applications` quiet |
| 4 | a failing Flux source (GitRepository `Ready=False`) | `flux/sources` lit with 1; `flux/reconciliations` quiet |
| 5a | a healthy Gateway with a route whose backend is not found (`ResolvedRefs=False`, `BackendNotFound`) | `gateway/routes` lit with 1; `gateway/gateways` quiet (programmed) |
| 5b | a healthy Gateway with a route whose backend Service has no ready endpoints | `gateway/routes` lit with 1; `gateway/gateways` quiet |
| 6 | a controller down with no object fault (`cert-manager` at zero available) | the `certs` area lit by "controller not ready" with no object count; every certs part quiet |
| 7 | a forbidden kind (the Observer's identity may not list ClusterIssuers) | `certs/issuers` a named gap, "ClusterIssuers could not be read (forbidden)"; never quiet, never a count presented as complete |
| 8 | RBAC revoked after a sync (list on ExternalSecrets removed once the part is live) | `eso/externalsecrets` reads as last-good: stale, with its last-good time; never quiet, never "checking" |

Cases 1-6 run as the cluster admin. Cases 7 and 8 run as a ServiceAccount bound to a ClusterRole with the Observer's read verbs on every kind but the case's; its token comes from the TokenRequest API and is written to a scratch kubeconfig that the test deletes on cleanup. Case 8 removes the rule from the ClusterRole only after the part is live.

"cluster ok" must appear in B and in no other case (parent §9: "only with everything live and no unknowns").

## 3. The two stages

**Go stage** (`internal/fleet/parity_lab_test.go`, `KLYX_PARITY_CONTEXT=kind-klyx-parity`). For each case it drives the script, then reads summaries from the production connection's health sink (as `TestLab` does) until the case's expected part changes or 60s pass (a timeout fails the case, naming the last summary). It asserts:

- per part, the summary's role counts equal the reference counts: for the capability parts, the annotations on the fixture's objects (the fixture is their only source); for the core parts (workloads, pods, nodes), the totals of an admin list of the same kinds with every role but ok at zero - kind's own workloads are healthy and the fixture adds none;
- the case's controller issue and coverage (live, stale with last-good, unreadable with class) as in the table;
- with every part's rows subscribed, the rows' included count equals the summary part's included count at one watermark (parent §2's one-number rule, now live).

It writes each case's last summary and the connection's `ClusterDTO` to `$KLYX_PARITY_OUT/<case>.json`.

**Frontend stage** (`nav/parityGate.test.ts`, skipped unless `KLYX_PARITY_OUT` is set). For each captured case it loads the summary into the real health store and asserts, through `clusterAttention`, `mastReading`, `spineLamp`, `healthReading` and `clusterDiagnosticScore`:

- every step 7e area (the eleven `TASKS`) is present with the Go stage's counts;
- the case's part lit, the controller-only light with no count, the forbidden gap named, the last-good line stale with its time;
- `notCovered` is empty in every case;
- the healthy lamp, mast "no attention" and band 0 in B only.

The gate passes when both stages pass every case. The run's output (cases, counts, durations) goes in the commit message and the PR.

## 4. After the pass: "not covered" goes

- `nav/clusterAttention.ts`: `NotCovered` and the fail-closed entry go. A task or part the current summary does not carry - capability detection ahead of the Observer's, or a summary from before the capability appeared - is answered as "checking…" (a gap; never quiet, never a count) until a summary or rows answer it. `mastReading`'s `notCovered` field goes.
- Its readers: the Overview's not-covered rows and the quiet line's "N not covered by the summary" clause; the card's "unverified" not-covered entries and the "not covered" tool values; the spine's "not covered by the summary" line; `healthGaps`; the tree's not-covered notes. Each reads "checking…" instead, with the same capability qualification where capability is not yet known ("capability not yet known").
- Tests that asserted "not covered" are rewritten to the checking reading; a test proves a part missing from a summary is never quiet.

## 5. Acceptance text

The acceptance note (in `docs/backlog.md`'s 1c entry and the PR) names the remaining per-fetch surfaces: the Helm and Events lenses and the Gateway topology are request reads with an as-of time, not live rows; their summary parts are live, so attention is live while those lenses' lists are snapshots.

## 6. Order of work

One reviewed commit each:

1. The fixture script, both gate stages, and the run's report (no product change).
2. On a pass: the "not covered" removal (§4) and the acceptance text (§5).

If a case fails, the failure is fixed in the Observer or the model as its own reviewed commit before step 2, and the gate is re-run in full.
