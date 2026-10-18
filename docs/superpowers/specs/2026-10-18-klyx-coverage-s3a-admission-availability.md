# Coverage S3a: admission backend availability and policy inventory, with C3

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S3a, and its shared contract C3, which lands first. S3b (proven attribution of a write error) is a separate note.

The roadmap's S3a:
- **The inventory:** Validating and Mutating webhook configurations, ValidatingAdmissionPolicy and MutatingAdmissionPolicy, with their bindings.
- **The fault:** a fail-closed webhook whose backend is unavailable reads "matching requests may fail". A backend is unavailable when it names a missing Service, the Service has no ready endpoints, or the Service port does not map. It never reads "blocks these objects": rules alone omit selectors, matchConditions, exclusions and bindings.
- **Evidence limits, each said:**
  - a URL-backed webhook has no Service to inspect;
  - unreadable EndpointSlices are not empty;
  - a selectorless Service has its own endpoints;
  - ready endpoints do not prove apiserver reachability, TLS trust or admission success.
- **CEL policies:** their diagnostics are their own (bindings and params, and the type-checking warnings in status).

And C3: "Shared observer kinds: one gate per consumer, three-state":
- **The gate:** any consumer with proven demand opens acquisition; stopping requires every consumer to prove absence; false plus unknown is a coverage gap, never "absent".
- **Consistency:** a consumer's eligibility is captured atomically with its evaluation.
- **No borrowed presence.**

## 1. The API, read from the source

Read from `k8s.io/apiserver` and `k8s.io/kube-aggregator` v0.37.1 and `k8s.io/api` v0.37.1; the lab runs v1.37.0. Notes: scratchpad `s3-research.md`.

**What is served:** `admissionregistration.k8s.io/v1` serves six cluster-scoped kinds:
- ValidatingWebhookConfiguration and MutatingWebhookConfiguration;
- ValidatingAdmissionPolicy and its Binding;
- MutatingAdmissionPolicy (v1 since 1.36) and its Binding.

**How a webhook's Service is reached:**
- **By default** (`--enable-aggregator-routing=false`), the API server calls the Service's ClusterIP on the Service port whose `port` equals the webhook's `service.port` (default 443) (`ResolveCluster`, `proxy.go` 152-179).
  - A headless Service fails: "cannot route to service with ClusterIP None".
  - An ExternalName Service routes to a host outside the cluster.
  - Any type other than ClusterIP, NodePort or LoadBalancer is unsupported.
  - No matching Service port fails: "no service port %d found" (`findServicePort`).
- **With aggregator routing,** it picks a ready endpoint from the EndpointSlices, or fails with "no endpoints available".
- **Either way,** no ready endpoint means the call fails. `failurePolicy` then decides: Fail rejects the request, Ignore skips the webhook. It defaults to Fail.

**Endpoint readiness** (`discovery/v1`):
- `ready` (nil is true) is "serving and not terminating", unless the Service publishes not-ready addresses.
- ClusterIP traffic prefers ready endpoints, then serving-terminating ones when none are ready.
- Slices carry the `kubernetes.io/service-name` label, and their ports carry the Service port's name.

**CEL policies** (`pkg/admission/plugin/policy/generic`):
- **Bindings make hooks:** a policy no binding names never runs; a binding naming a missing policy is skipped (`policy_source.go` 290-358).
- **Configuration errors deny:** a `paramKind` that does not resolve, a param not found under `parameterNotFoundAction: Deny`, and the other `CollectParams` errors deny matching requests under `failurePolicy: Fail` (the default) and are skipped under Ignore (`policy_dispatcher.go` 120-250).
- **Status:** ValidatingAdmissionPolicy has `observedGeneration` and `typeChecking.expressionWarnings`; MutatingAdmissionPolicy has no status.

**Static manifests:** since about 1.36 the API server can load webhook configurations and policies from files on its own disk (`pkg/admission/plugin/manifest`, `webhook/manifest/source`). They are not API objects, so Klyx cannot see them. Managed platforms' own admission is likewise outside the API.

## 2. C3: shared observer kinds

**Today** (code-checked; the Explore map of 2026-10-18):
- **One gate per kind:** a kind has one `Probe`, so its gate is one consumer's. `services` and `endpointslices` are declared by the Gateway task and probe the Gateway API (`healthsrc/gateway.go` 36-52). Without Gateway API they are never watched (`gateway_test.go` 136-149).
- **Shared by name:** kinds are shared by name with one model and generation (`observe/kind.go` 19-20). A second declaration panics (`observer.go` 223-227).
- **Facts are captured atomically:** `captureLocked` copies facts under the lock that copies objects (`publish.go` 278-285).

**The change** (`internal/observe`):
- **`KindSpec.Consumers []Consumer`,** each `{Name, Demand func(ctx) (bool, error)}`. A kind with consumers has no `Probe` and a fixed `Version`. Its discovery loop asks every consumer, and the kind is:
  - **served** when any consumer returns true;
  - **absent** only when every consumer returns false;
  - **a discovery gap** (today's probe error: Unreadable, or Stale over a last good state) when none returns true and any errs.
- **Per-consumer demand is held** on the kind and copied into the capture under the same lock, as `Capture.Demand[kind][consumer]` = yes, no or unknown, beside the objects. It is also published on `KindCoverage.Consumers` ("opened by admission").
- **No borrowed presence:** a part that reads a shared kind declares its consumer (`PartSpec.Consumer`). Its evaluator treats the kind as its own evidence only when its own demand is yes:
  - **no:** the kind is not this part's;
  - **unknown:** the kind is unread for this part.

  The Gateway `routes` part keeps its own reading: its counted kinds are absent without Gateway API, so it stays absent even while admission holds `services` open.
- **Stopping:** a kind stops only on the transition to every consumer's no. A consumer that flips to unknown never stops a running generation: the last good state stands, with the gap named.

**The shared kinds** move to one declaration, `healthsrc.SharedNetworkKinds(d)`, added once in `fleet/conn.go`:
- **services:** a typed watch, one projection for every reader:
  - identity and ownerRefs (Gateway);
  - `type`, whether `clusterIP` is None, `externalName`, ports (name, port, protocol), whether a selector is set, and `publishNotReadyAddresses`.

  S4 adds `loadBalancerClass` and the load-balancer status when it lands.
- **endpointslices:** today's projection plus each slice's port names, and each endpoint's `serving` and `terminating`.
- **Consumers:**
  - `gateway`: Gateway API served (today's probe);
  - `admission`: the admissionregistration webhook kinds served, so in practice on every supported cluster.

**The cost, said** (§7): every cluster now watches Services and EndpointSlices, not only Gateway API ones.
- **Rejected:** a demand proven from data ("a webhook names a Service"). It would need a consumer to read another kind's model before acquisition, and almost every real cluster has a Service-backed webhook anyway, so it saves little.
- **Also rejected:** watching only the webhook Services' slices with a label selector. That makes the shared kind's selector one consumer's, which C3 forbids.

**Tests:**
- the three-state matrix (each consumer yes, no or unknown; served, absent or gap);
- no borrowed presence (Gateway parts stay absent on a cluster without Gateway API while admission holds `services`);
- the demand and the objects come from one capture;
- the stop transition;
- the changed `gateway_test` case: slices are watched without Gateway API when admission demands them;
- projection equality for every field a rule reads.

## 3. What Klyx shows

**An "Admission" section** (registry group `admission`, always present: the API is core), with three tabs:
- **Webhooks:** one row per webhook, broken first.
  - **Columns:** the verdict, the configuration and webhook name, validating or mutating, failure policy, backend (`ns/svc:port`, or "URL"), and ready endpoints.
  - **The inspector:**
    - the verdict;
    - **the backend chain, drawn:** configuration → webhook → Service:port → endpoints (ready, terminating-only or none), each link with its state;
    - **the evidence limits** as a block, every time: ready endpoints do not prove the API server reaches them, trusts their TLS, or that admission succeeds;
    - **what it matches,** labelled as such: its rules, whether namespace and object selectors are set, the match condition count, scope, `matchPolicy`, and "rules alone do not say which objects it blocks";
    - timeout, side effects, `reinvocationPolicy` (mutating), and whether a `caBundle` is set (never its content).
- **Policies:** one row per Validating or Mutating AdmissionPolicy, broken first.
  - **Columns:** the verdict, the name, validating or mutating, failure policy, bindings and `paramKind`.
  - **The inspector:**
    - the verdict;
    - its bindings, each with `validationActions` (validating), `paramRef` and its not-found action;
    - the expression counts (validations or mutations, variables, match conditions, audit annotations) and the type-checking warnings;
    - **a param check run when it opens** (§6), as of that read.
- **Bindings:** every binding, as facts: its policy (found, or not found and so without effect), its param reference, and its actions.

**Needs you and notifications:**
- A fail-closed webhook with an unavailable backend, and a fail-closed policy whose `paramKind` is not served, reach Needs you.
- Notifications are eligible: the evidence is the cluster's own.

## 4. The source contract (C1)

| Part | Acquisition | Counted entity, identity | Dependencies and what their gaps do | Clock | Notifications |
|---|---|---|---|---|---|
| `admission/webhooks` | observer watches, projected: both webhook configuration kinds (v1), cluster-scoped | one webhook: its configuration's UID plus the webhook's name, which is unique within a configuration | `services`, `endpointslices` (C3, consumer `admission`). Unread or stale reads "backend unverified" (unknown), never unavailable. A Service is "missing" only from a complete read | none: every change is a watch event | yes |
| `admission/policies` | observer watches, projected: both policy kinds and both binding kinds (v1) | one policy: its UID | the bindings kinds: unread means "binding unverified" (unknown), never "not bound". `FactServedKinds` (§6): unknown means "paramKind unverified" | none (the served-kinds fact refreshes on the capability re-probe) | yes |
| bindings (rows, no verdict) | the same watches | one binding: its UID | the policy kinds: a missing policy from a complete read only | none | no |

**Version support:**
- A cluster that serves only some kinds reads the rest as absent per kind, as today. Mutating policies are v1 from 1.36: an older cluster reads them absent.
- `v1beta1` policy kinds are not read; that limit is said.

## 5. The webhook rule (first match wins)

"Fail-closed" means `failurePolicy` Fail or unset (the v1 default). Each unavailable reason reads "Backend unavailable: <reason>":
- fail-closed: **fault**, "matching requests may fail";
- fail-open (Ignore): **degraded**, "matching requests skip this webhook".

1. **URL backend:** unknown, "Not inspected: a URL backend has no Service to inspect". The URL shows as its origin only (the URL rule).
2. **Services unread for this consumer** (demand not yes, or the kind unreadable or checking): unknown, "Backend unverified".
3. **The Service is not in the read:**
   - from a complete read: unavailable, "Service <ns/name> not found";
   - from a stale read: unknown, "not in the last good read".
4. **Headless Service:** unavailable, "headless: the API server cannot route to it".
5. **ExternalName Service:** unknown, "outside the cluster, not inspected".
6. **Another unsupported type:** unavailable.
7. **No Service port equals the webhook's port:** unavailable, "no Service port <p>".
8. **EndpointSlices unread or checking:** unknown, "endpoints unverified". Unread is never empty.
9. **Counting endpoints** for the Service port (slices labelled with the Service's name, port names matching the Service port's name):
   - none ready, and serving-terminating ones: degraded, "only terminating endpoints";
   - none at all: unavailable, "no ready endpoints". A selectorless Service is counted the same way, from its own slices; the inspector says that something other than the Service controller manages them.
10. **Otherwise:** ok, "Backend ready (N endpoints)", with the limits said in the inspector.

The verdict never names objects it blocks: it says "matching requests", and the inspector says what the rules alone cannot.

## 6. The policy rule (first match wins)

1. **Bindings unread:** unknown, "bindings unverified".
2. **No binding names it:** paused, "Not bound: no effect".
3. **paramKind is set:**
   - **the served-kinds fact is unknown:** unknown, "paramKind unverified";
   - **paramKind is not served:**
     - fail-closed: **fault**, "paramKind <Kind> is not served: matching requests are denied";
     - Ignore: **degraded**, "…: the policy is skipped".
4. **Validating policy with type-checking warnings:** degraded, "N type-checking warnings: an expression may fail when it runs". Under Fail, a failed evaluation denies.
5. **Validating policy whose status generation is behind:** progressing, "type checking pending" (the ratified staleness language).
6. **Otherwise:** ok, "Bound (N)".

**`FactServedKinds`:**
- the served group/version/kinds from the connection's discovery cache, with its read time;
- set at connect and at every capability re-probe (5 minutes);
- captured atomically with the policies;
- its age is said: "as of discovery at <time>".

**Params are not part of the verdict:**
- Whether a binding's named or selected param exists is read when the policy's inspector opens: one read per binding, as of that read.
- The inspector says when a missing param under Deny would deny matching requests.
- **Why not in the verdict:** params can be any kind, and watching every param kind is outside this slice's budget. The inspector says that the verdict does not check params.

## 7. Projections and budget

- **Webhook configuration**, per webhook (at most 64, then counted):
  - name, failure policy, side effects, timeout, `matchPolicy`, `reinvocationPolicy`, `admissionReviewVersions`;
  - `clientConfig`: the Service's namespace, name, port and whether a path is set; or the URL's origin;
  - whether a `caBundle` is set;
  - the rules (at most 8, then counted: operations, groups, versions, resources, scope);
  - whether namespace and object selectors are set, with their expression counts;
  - the match condition count.

  One configuration's projection is measured against 16 KiB, and cut with counts kept.
- **Policies:**
  - failure policy and `paramKind`;
  - a match-constraints summary (the first 8 resource rules, then counted);
  - expression counts;
  - status generation;
  - type-checking warnings (at most 4: `fieldRef` plus warning text, clipped, under the URL rule).
- **Bindings:**
  - the policy name;
  - `paramRef`: name, namespace, whether a selector is set, and the not-found action;
  - validation actions;
  - a match-resources summary.
- **Budget per cluster:**
  - **new watches:** six admission kinds, plus `services` and `endpointslices` where Gateway API is absent;
  - **retained:** the admission models are small (tens of objects); Services and EndpointSlices are as large as on a Gateway API cluster today;
  - **measured:** the load test reports the full-evaluation time before and after, against the known 60 ms vs 50 ms finding.

## 8. Sensitive data

- **Never carried:**
  - a `caBundle`: whether it is set only;
  - a webhook URL beyond its origin: the URL rule;
  - selector values: counts only;
  - CEL expressions: counts only.
- **Type-checking warnings:** their text quotes expression fragments. They are shown clipped under the URL rule. AI gets the count and the `fieldRef` only.
- **AI allowlists** for both parts: names, kinds, roles, failure policies, backend states, counts. The bindings rows are facts: they reach AI with no verdict, through S2b's central `FactRow` gate.

## 9. Tests, lab, parity, smoke

- **Unit (`internal/admission`, pure):**
  - every webhook rule row, at both failure policies;
  - port mapping by number and name;
  - ready, terminating-only and none;
  - a selectorless Service;
  - unread versus empty;
  - every policy rule row;
  - the projections' bounds and needles: a URL with user info, a `caBundle`, a selector value.
- **observe (C3):** §2's cases.
- **healthsrc:**
  - both parts through the Observer;
  - a UID replacement (a configuration deleted and created again);
  - a webhook renamed within its configuration;
  - a Service appearing late (from unavailable to ready without a new configuration event).
- **appbridge:** the DTOs, the allowlists, the param read in the inspector, chat GVR mapping.
- **Frontend:**
  - the lens's tabs and broken-first order;
  - the backend chain drawing;
  - the limits block;
  - binding facts;
  - nav, attention, reveal, console, smoke `TITLES`.
- **Parity:**
  - a healthy baseline: an Ignore webhook for a resource that does not exist, backed by a Service over a ready stand-in pod, and a bound validating policy whose expression is `true` on a resource no one writes;
  - the oracle counts both parts.
- **The lab (`lab-s3a`, kind-klyx).** Every webhook targets a group and resource that do not exist (`lab.klyx.dev/widgets`), so nothing in the lab is ever admitted through them:
  - fail-closed with a missing Service;
  - fail-closed with no ready endpoints;
  - fail-open with an unmapped port;
  - headless;
  - ExternalName;
  - a URL with user info carrying the needle;
  - healthy;
  - an unbound policy;
  - a policy whose `paramKind` is not served;
  - a policy with a type-checking warning;
  - a healthy binding;
  - a dangling binding;
  - a bound mutating policy.
- **Native smoke:** the Webhooks tab broken first, the backend chain, the limits block, a policy's verdict, and no needle on screen.
- **Live captures:** night and day at 1440 and 1024.

## 10. Not in S3a

- **S3b:** attributing a write error to its webhook or policy, with candidates labelled.
- **Mutation attribution:** never claimed.
- **Webhook and policy metrics** (`apiserver_admission_webhook_*`): an S8 candidate.
- **Static-manifest admission and managed-platform admission:** invisible to the API, and said.
- **Verbs:** none. Admission configuration is desired state, authored in Git.
