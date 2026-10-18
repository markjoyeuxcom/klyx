# Coverage S3a: admission backend availability and policy inventory, with C3

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S3a, and its shared contract C3, which lands first. S3b (proven attribution of a write error) is a separate note. Revision 1 answers Codex's design review of a74bd2d (§11), revision 2 its re-review of 2827d4b (§12).

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

**The physical gate** (`internal/observe`):
- **`KindSpec.Consumers []Consumer`,** each `{Name, Demand func(ctx) (bool, error)}`. A kind with consumers has no `Probe` and a fixed `Version`. Its discovery loop asks every consumer, and the kind is:
  - **served** when any consumer returns true;
  - **absent** only when every consumer returns false;
  - **a discovery gap** (today's probe error: Unreadable, or Stale over a last good state) when none returns true and any errs.
- **Stopping:** a kind stops only on the transition to every consumer's no. A consumer flipping to unknown never stops a running generation.

**The per-consumer view** (revision 1: eligibility is applied everywhere, not only in evaluator branches):
- **The demand vector** (each consumer's yes, no or unknown) is held on the kind. A change in it marks dirty every part whose consumer changed, even while the aggregate gate stays yes and the physical generation runs on.
- **Each part declares its consumer** (`PartSpec.Consumer`). `captureLocked` builds that part's view of a shared kind from that consumer's demand, under the same lock as the objects:
  - **yes:** the kind as it is (Objects, Complete, Window);
  - **no:** the kind is absent for this part (Complete, no Objects), exactly as an absent kind reads today;
  - **unknown:** the kind is unread for this part (no Objects, not Complete).
- **The same view everywhere:** the part's published `KindCoverage` (absent, or unreadable with the class `demand`), its "part absent" rule, and the ledger's continuity, which reads an absent dependency as continuous. A part never sees another consumer's evidence as its own.
- **Published:** the demand vector on the kind's coverage (`Consumers`), for the inspector's "opened by".

**The shared kinds** move to one declaration, `healthsrc.SharedNetworkKinds(d)`, added once in `fleet/conn.go`:
- **services:** a typed watch, one projection for every reader:
  - identity and ownerRefs (Gateway);
  - `type`, whether `clusterIP` is None, `externalName`;
  - ports: name, port and protocol;
  - whether a selector is set, and `publishNotReadyAddresses`.

  S4 adds `loadBalancerClass` and the load-balancer status when it lands.
- **endpointslices:** identity and the service-name label, plus:
  - each slice's ports: name (nil kept distinct from ""), port (nil kept distinct) and protocol;
  - each endpoint's `ready`, `serving` and `terminating` (nil kept distinct), whether it has an address, and its target ref.
- **Consumers:**
  - `gateway`: Gateway API served (today's probe);
  - `admission`: the admissionregistration webhook kinds served, so in practice on every supported cluster.

**The cost, said** (§7): every cluster now watches Services and EndpointSlices, not only Gateway API ones.
- **Rejected:** a demand proven from data ("a webhook names a Service"). It would need a consumer to read another kind's model before acquisition, and almost every real cluster has a Service-backed webhook anyway.
- **Also rejected:** watching only the webhook Services' slices with a label selector. That makes the shared kind's selector one consumer's.
- Codex agreed with both rejections in the design review.

**Tests:**
- the three-state matrix (each consumer yes, no or unknown; served, absent or gap);
- **one consumer's demand going from yes to unknown, or to no, while another stays yes:** the physical generation is unchanged, the first consumer's parts read unread or absent, they are marked dirty, and their coverage and ledger follow;
- no borrowed presence (Gateway parts stay absent on a cluster without Gateway API while admission holds `services`);
- the demand and the objects come from one capture;
- the stop transition;
- the changed `gateway_test` case: slices are watched without Gateway API when admission demands them;
- projection equality for every field a rule reads, including nil ports and nil conditions.

## 3. What Klyx shows

**An "Admission" section** (registry group `admission`, always present: the API is core), with three tabs:
- **Webhooks:** one row per webhook configuration (revision 1: the counted entity is the configuration, §4), broken first.
  - **Columns:** the verdict (its worst webhook's), the name, validating or mutating, the webhook count, and how many of its webhooks have an unavailable or unverified backend.
  - **The inspector:**
    - the verdict;
    - **one block per webhook,** each with its own state and the backend chain drawn: configuration → webhook → Service:port → endpoints (usable, terminating-only or none), each link with its state;
    - **the evidence limits** as a block, every time:
      - ready endpoints do not prove the API server reaches them, trusts their TLS, or that admission succeeds;
      - Klyx cannot see the API server's routing mode (ClusterIP or endpoint routing), so some states depend on it and say so;
    - **what each webhook matches,** labelled as such: its rules, whether namespace and object selectors are set, the match condition count, scope, `matchPolicy`, and "rules alone do not say which objects it blocks";
    - timeout, side effects, `reinvocationPolicy` (mutating), and whether a `caBundle` is set (never its content).
- **Policies:** one row per Validating or Mutating AdmissionPolicy, broken first.
  - **Columns:** the verdict, the name, validating or mutating, failure policy, bindings and `paramKind`.
  - **The inspector:**
    - the verdict;
    - its bindings, each with `validationActions` (validating), `paramRef` and its not-found action;
    - the expression counts (validations or mutations, variables, match conditions, audit annotations);
    - **the type-checking warnings:** their count and the fields they are on, never their text (§8), marked historical when the status is behind;
    - **a param check run when it opens** (§6), as of that read.
- **Bindings:** every binding, as facts: its policy (found, or not found and so without effect), its param reference, and its actions.

**Needs you and notifications:**
- A configuration with a fail-closed webhook whose backend is unavailable, and a fail-closed policy whose `paramKind` is proven not served, reach Needs you.
- Notifications are eligible: the evidence is the cluster's own.

## 4. The source contract (C1)

| Part | Acquisition | Counted entity, identity | Dependencies and what their gaps do | Clock | Notifications |
|---|---|---|---|---|---|
| `admission/webhooks` | observer watches, projected: both webhook configuration kinds (v1), cluster-scoped | one webhook configuration: its UID, through rows, Top and reveal, pinning, Explain and ledger membership like any object. Its webhooks are its parts, keyed by name inside it, never separate members | `services`, `endpointslices` (C3, consumer `admission`). Unread, stale or demand-unknown reads "backend unverified" (unknown), never unavailable. A Service is "missing" only from a complete read | none: every change is a watch event | yes |
| `admission/policies` | observer watches, projected: both policy kinds and both binding kinds (v1) | one policy: its UID | the bindings kinds: unread means "bindings unverified" (unknown), never "not bound". `FactParamKinds` (§6): unknown means "paramKind unverified" | none (the param-kind fact refreshes on policy change and on the capability re-probe) | yes |
| bindings (fact rows, no verdict, S2b's `FactRow` gate) | the same watches | one binding: its UID | the policy kinds: a missing policy from a complete read only | none | no |

**A configuration's truncation** (§7): when a configuration's webhooks are cut to fit, the cut ones are unread.
- A configuration whose read webhooks are all ok, with some cut, is unknown: "N webhooks not read". It is never ok.
- A proven fault among the read ones stands, with "+N not read".
- The configuration stays the ledger member throughout: a cut webhook never reads as departed, because webhooks are not members.

**Version support:**
- A cluster that serves only some kinds reads the rest as absent per kind, as today. Mutating policies are v1 from 1.36: an older cluster reads them absent.
- `v1beta1` policy kinds are not read; that limit is said.

## 5. The webhook rule

**Each webhook's backend state** (first match wins). Klyx does not observe the API server's routing mode, ClusterIP (the default) or endpoint routing (`--enable-aggregator-routing`), so a state that differs between them is unknown and says so (revision 1):
1. **URL backend:** unknown, "Not inspected: a URL backend has no Service to inspect". The URL shows as its origin only.
2. **Services unread for this part** (demand not yes, the kind unreadable or checking): unknown, "backend unverified".
3. **The Service is not in the read:**
   - from a complete read: unavailable, "Service <ns/name> not found";
   - from a stale read: unknown, "not in the last good read".
4. **Headless Service:** unknown, "headless: ClusterIP routing cannot reach it, endpoint routing can; the routing mode is not observable".
5. **ExternalName Service:** unknown, "ExternalName: ClusterIP routing leaves the cluster and endpoint routing rejects it; not inspected".
6. **A type other than ClusterIP, NodePort, LoadBalancer or ExternalName:** unavailable (both modes reject it).
7. **No Service port with the webhook's port number:** unavailable, "no Service port <p>" (both modes).
8. **The Service port's protocol is not TCP:** unknown, "the Service port is <protocol>; a webhook is called over TCP".
9. **EndpointSlices unread or checking:** unknown, "endpoints unverified". Unread is never empty.
10. **Usable endpoints**, judged per routing mode (revision 2: the two modes check different things):
    - **Endpoint routing** (`RandomEndpoint`, `proxy.go` 117-144) can use an endpoint when:
      - its slice is labelled with the Service's name;
      - its slice carries a port whose name is present and equals the Service port's name ("" for an unnamed port), and whose number is present. **The slice port's protocol is not checked;**
      - the endpoint is ready (nil is ready) and has an address.
    - **ClusterIP routing** (the proxy) can use an endpoint when it meets the same conditions and the slice port's protocol also equals the Service port's. When no endpoint is ready, it also falls back to serving-terminating ones.

    **The outcome:**
    - **Usable in both modes:** ok, "Backend ready (N endpoints)", with the limits said in the inspector.
    - **Usable in one mode only:** unknown, "usable under <mode> routing only; the routing mode is not observable". This covers a protocol mismatch that only ClusterIP routing rejects.
    - **None usable in either mode, but serving-terminating ones carry the port:** degraded, "only terminating endpoints". ClusterIP routing may fall back to them; endpoint routing does not.
    - **Otherwise:** unavailable, "no usable endpoints", with the reason: no slices, no slice carries the port by name, the port has no number, or no ready endpoint.
    - **A selectorless Service** is counted the same way from its own slices; the inspector says that something other than the Service controller manages them.

**Unavailable**, by failure policy (fail-closed means Fail or unset, the v1 default):
- fail-closed: **fault**, "Backend unavailable: <reason>; matching requests may fail";
- fail-open: **degraded**, "…; matching requests skip this webhook".

**The configuration's verdict** is its worst webhook's, in this order:
1. fault;
2. degraded;
3. unknown (unverified, not inspected, routing-dependent, or webhooks not read because of the cut, §4);
4. ok.

Its reason names the deciding webhook and "+N more". The verdict never names objects it blocks: it says "matching requests", and the inspector says what the rules alone cannot.

## 6. The policy rule

**For a policy** (first match wins):
1. **Bindings unread:** unknown, "bindings unverified".
2. **No binding names it:** paused, "Not bound: no effect".
3. **`paramKind` is set.** The param-kind fact (below) for its exact group, version and kind:
   - **unknown:** unknown, "paramKind unverified".
   - **proven not served:**
     - fail-closed: **fault**, "paramKind <Kind> is not served: matching requests are denied";
     - Ignore: **degraded**, "…: the policy is skipped".

     This is a policy-level configuration error ("failed to find resource referenced by paramKind", `policy_source.go` 407-416). It applies through every binding whatever its `paramRef`, and for validating policies whatever its `validationActions`: configuration errors take the dispatcher's separate path.
   - **A binding whose `paramRef` sets a namespace for a cluster-scoped `paramKind`:** a binding-level configuration error. It is a fault under Fail and degraded under Ignore, naming the binding. The fact carries the kind's scope.
4. **A validating policy whose status generation is behind:** progressing, "type checking pending". Any warnings are shown as historical, never as the verdict (revision 1).
5. **A validating policy with current type-checking warnings:** degraded, "N type-checking warnings: an expression may fail when it runs". The consequence is qualified by its bindings' actions, since a failed evaluation becomes a deny decision under Fail and the binding's actions apply it (`validating/dispatcher.go` 230-260):
   - fail-closed with a Deny binding: "a failure denies requests matched by Deny bindings";
   - fail-closed with only Warn or Audit bindings: "a failure is warned or audited, not denied";
   - Ignore: "a failure is ignored".
6. **Otherwise:** ok, "Bound (N)".

Mutating policies have no status, so rows 4 and 5 do not apply.

**`FactParamKinds`** (revision 1: positive and negative evidence only from a completed discovery):
- **Its source:** the connection follows both policy kinds through the Observer's `WatchKind`, as it does pods and nodes. It collects the exact group, version and kind each `paramKind` names.
- **For each,** it runs discovery of that exact group and version (cached, with a TTL), and records the result with its read time:
  - **served:** the kind is in that group and version's resources. The record carries the kind's scope.
  - **not served:** that group and version was discovered without the kind, or returned NotFound.
  - **unknown:** discovery failed (an aggregated group unavailable, a timeout, forbidden), or has not run yet.

  A cold or incomplete discovery is unknown, never "not served". A non-preferred served version is served.
- **Its freshness:** refreshed when the set changes and at each capability re-probe (5 minutes), and captured atomically by the policies part. The inspector says "as of discovery at <time>".

**Params are not in the verdict.** Whether a binding's param exists is read when the policy's inspector opens, existence only:
- **The read:** metadata-only get or list, never a param's values. One read per binding, capped, and as of that read.
- **The cases, each said:**
  - **no `paramRef`:** the binding evaluates with no params, and nothing is checked;
  - **a namespaced `paramKind` with no `paramRef.namespace`:** "depends on the request's namespace: not checked". The API server looks in the admitted request's namespace (`policy_dispatcher.go` 299-317);
  - **an explicit namespace, or a cluster-scoped kind:** a name is got, a selector listed (limit 1). Each reads found, not found, or not readable (forbidden or an error, which is never "not found");
  - **not found** (a missing name, or a selector that matches nothing): with `parameterNotFoundAction: Allow`, "this binding is skipped". With `Deny`, it is a configuration error, so the policy's failure policy decides (`types.go` 591-596, `policy_dispatcher.go` 173-185 and 212-229):
    - fail-closed: "a request this binding matches is denied (no params found)";
    - Ignore: "this binding is skipped (no params found; the policy ignores configuration errors)".

    This holds whatever a validating binding's actions: configuration errors take their own path (revision 2).
- **Why not in the verdict:** params can be any kind, and watching every param kind is outside this slice's budget. The inspector says the verdict does not check params.

## 7. Projections and budget

- **A webhook configuration:**
  - per webhook, at most 64 webhooks, then counted:
    - name, failure policy, side effects, timeout, `matchPolicy`, `reinvocationPolicy`, `admissionReviewVersions`;
    - `clientConfig`: the Service's namespace, name, port and whether a path is set; or the URL's origin and whether a path is set;
    - whether a `caBundle` is set;
    - the rules: at most 8, then counted (operations, groups, versions, resources, scope);
    - whether namespace and object selectors are set, with their expression counts;
    - the match condition count.
  - **Measured** against 16 KiB, and cut from the tail with counts kept. Cut webhooks are unread (§4).
- **Policies:**
  - failure policy, `paramKind`, and a match-constraints summary (the first 8 resource rules, then counted);
  - expression counts and the status generation;
  - type-checking warnings: their count and validated `fieldRef`s (at most 8, then counted), never their text (§8).
- **Bindings:**
  - the policy name;
  - `paramRef`: name, namespace, whether a selector is set, and the not-found action;
  - validation actions;
  - a match-resources summary.
- **Services and EndpointSlices:** §2's projections, unbudgeted as today.
- **Budget per cluster:**
  - **new watches:** six admission kinds, plus `services` and `endpointslices` where Gateway API is absent;
  - **retained:** the admission models are small (tens of objects); Services and EndpointSlices are as large as on a Gateway API cluster today;
  - **measured:** the load test reports the full-evaluation time before and after, against the known 60 ms vs 50 ms finding.

## 8. Sensitive data

**Never carried by Klyx's admission rows and inspectors, AI Explain or chat:**
- a `caBundle`: whether it is set only;
- a webhook URL beyond its origin: the URL rule, and whether a path is set;
- selector values: counts only;
- **CEL expressions:** counts only;
- **type-checking warning text** (revision 1). The type checker copies the CEL diagnostic into the warning (`validating/typechecking.go` 115-133), which quotes expression source, literals included.

**What crosses for a warning:**
- the count;
- each `fieldRef` that matches the type checker's own form (`spec.(validations|auditAnnotations|matchConditions|variables)[i].(expression|messageExpression|valueExpression)`), else "(unrecognised field)";
- Klyx's fixed wording, "type-checking warning on <fieldRef>".

**The existing paths change the same way:**
- **Instance facts** (`crd/facts_admission3.go`), which AI Explain and the chat projection read today: count and validated `fieldRef`s instead of the warning text.
- **`facts_admission.go`'s webhook URL:** the origin, and whether a path is set.

**The generic YAML and Describe views** still show the object as the API returns it, as for every kind but Secrets. The note says so, and Klyx's own summaries never quote it.

**A literal-needle regression at the serialised boundary:** an expression and a warning carrying a credential-shaped literal with no URL. The needle must not appear in:
- the rows DTO;
- the instance facts;
- the AI Explain evidence;
- the chat projection.

**AI allowlists** for both parts: names, kinds, roles, failure policies, backend states and counts. The bindings rows are facts, gated by S2b's `FactRow`.

## 9. Tests, lab, parity, smoke

- **Unit (`internal/admission`, pure):**
  - **every webhook state, at both failure policies,** including:
    - headless, ExternalName and non-TCP as unknown;
    - a manually managed slice whose port matches by name and number but not protocol, which reads unknown (endpoint routing would use it; ClusterIP routing would not);
    - a slice port with the right name and no number;
    - a selectorless Service's slices;
    - only terminating endpoints;
    - unread versus empty;
  - **the configuration's verdict:**
    - two webhooks in one configuration, one faulty;
    - a webhook renamed within its configuration;
    - a truncated tail with a faulty webhook cut, which reads unknown, never ok;
  - **every policy row:**
    - status behind, with historical warnings;
    - warnings under Deny, Warn or Audit only, and Ignore;
    - a namespace set for a cluster-scoped `paramKind`;
  - **the projections' bounds and needles:** a URL with user info (synthetic: the API rejects it), a `caBundle`, a selector value, an expression literal, and a warning's text.
- **The param-kind fact:**
  - served at a non-preferred version;
  - an unreadable aggregated group, which reads unknown;
  - cold discovery, which reads unknown;
  - not served.
- **The param check:**
  - a missing named param and an empty selector result, each under Deny with Fail, Deny with Ignore, and Allow;
  - no `paramRef`;
  - cluster-scoped;
  - request-namespace dependent;
  - name and selector;
  - capped or forbidden;
  - Deny versus Allow;
  - metadata only, never values.
- **observe (C3):** §2's cases.
- **healthsrc:**
  - both parts through the Observer;
  - a configuration replaced under its name (a new UID);
  - a Service appearing late (from unavailable to ready without a configuration event);
  - Explain, reveal and pinning of a configuration row;
  - ledger membership across a cut.
- **appbridge:**
  - the DTOs and allowlists;
  - the literal-needle regression at the serialised boundary (§8);
  - the param read;
  - chat GVR mapping.
- **Frontend:**
  - the lens's tabs and broken-first order;
  - the per-webhook backend chains;
  - the limits block, with the routing-mode limit;
  - historical warnings;
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
  - a URL whose path carries the needle (allowed by the API; user info is not, `util/webhook/validation.go` 39-59);
  - healthy;
  - an unbound policy;
  - a policy whose `paramKind` is not served;
  - a policy whose expression carries the needle and draws a type-checking warning;
  - a healthy binding;
  - a dangling binding;
  - a bound mutating policy.
- **Native smoke:**
  - the Webhooks tab broken first;
  - a configuration's backend chains;
  - the limits block;
  - a policy's verdict;
  - no needle on screen.
- **Live captures:** night and day at 1440 and 1024.

## 10. Not in S3a

- **S3b:** attributing a write error to its webhook or policy, with candidates labelled.
- **Mutation attribution:** never claimed.
- **Webhook and policy metrics** (`apiserver_admission_webhook_*`): an S8 candidate.
- **Static-manifest admission and managed-platform admission:** invisible to the API, and said.
- **Verbs:** none. Admission configuration is desired state, authored in Git.

## 11. Revision 1: Codex's design review of a74bd2d

| # | Finding | Where answered |
|---|---|---|
| 1 (P1) | Warning text quotes expression source, so a literal could cross | §8: count, a validated `fieldRef` and fixed wording only; `facts_admission3.go` changes too; a literal-needle regression at the serialised boundary |
| 2 | Headless is reachable under endpoint routing | §5: routing-mode-dependent states are unknown and say so; the limit names the unobserved mode |
| 3 | A port name alone is not usable-port evidence | §2 keeps port numbers, nil and protocol; §5 defines a usable endpoint as endpoint routing requires; non-TCP is unknown |
| 4 | Stale warnings preceded the generation fence; the deny wording ignored validation actions | §6: freshness first, with historical warnings; the consequence is qualified by Deny, Warn or Audit and Ignore; configuration errors take their own path |
| 5 | The discovery cache is lazy: absence there is not "not served" | §6 `FactParamKinds`: exact group-version discovery with per-kind served, not served or unknown, and read times; cold or failed is unknown |
| 6 | The param check depends on the request's namespace | §6: no `paramRef.namespace` for a namespaced kind is "not checked"; every case said; metadata only |
| 7 | A per-webhook row had no identity through rows, Explain and the ledger | §3, §4: the counted entity is the configuration (one UID); webhooks are its parts; truncation reads unknown, never ok, and never departs a member |
| 8 (P3) | A URL with user info cannot be created | §9: user info in synthetic tests; the lab's needle is in the URL path |
| - | C3 guidance | §2: the demand vector dirties its parts; the per-consumer view applies to Objects, Complete, coverage and ledger; the yes-to-unknown or no test with the generation unchanged |

## 12. Revision 2: Codex's re-review of 2827d4b

| # | Finding | Where answered |
|---|---|---|
| 1 | Endpoint routing does not check the slice port's protocol, so a protocol-only mismatch is routing-dependent | §5 row 10: eligibility per mode; usable in one mode only reads unknown; a test beside the non-TCP Service port |
| 2 | Deny on a missing param denies only under Fail; Ignore skips | §6: the not-found wording by failure policy, for a name and a selector, independent of validation actions; tests for each |

## 13. As built (585f6f6 to e0457a1)

**C3** (`internal/observe`, 585f6f6; the shared kinds, 94aa907):
- **The gate:** `KindSpec.Consumers` and `Version`. A kind with consumers runs `consumersProbe` in its discovery loop:
  - served when any is yes;
  - absent when all are no;
  - otherwise a probe error naming each unknown consumer.
- **Dirtying:** the demand vector is held on the kind, and a change in it marks the kind's parts dirty even while the generation runs on.
- **The per-consumer view:** `PartSpec.Consumer` makes `captureLocked` rebuild the part's view of a shared kind:
  - **yes:** as it is;
  - **no:** Absent and complete, with no objects;
  - **unknown:** Unreadable with class `demand`, or Checking before the first probe.

  The view covers objects, completeness, windows, coverage and the ledger's source identity. `New` panics on a part that names a consumer the kind lacks.
- **`healthsrc.SharedNetworkKinds`:** declared once in `fleet/conn.go`, with consumers `gateway` and `admission`.
  - **Services** become a typed watch. They are projected to a `*corev1.Service`, so `meta.Accessor` readers (the fleet index) keep working.
  - **EndpointSlices** keep port names, numbers and protocols (nil kept) and the three conditions. An address is a marker (`(address)`), never the address.
  - **The Gateway routes part** reads them through `ConsumerGateway`.

**S3a:**
- **`internal/admission`** (46753ab): the projections and both rules, as §5 and §6. `ConfigVerdict` takes the worst webhook in the order fault, degraded, unknown, ok, with "+N more"; a cut reads unknown.
- **`healthsrc.Admission`** (715f511): the webhooks part (configurations, through the admission consumer) and the policies part (policies with their verdicts, and every binding as a `FactRow` naming its policy's state).
- **`FactParamKinds`** (`fleet/admissionparams.go`):
  - **source:** the connection follows both policy kinds with `WatchKind`;
  - **discovery:** each referenced group/version is discovered exactly through the discovery cache, recording served (with scope and resource), not served (NotFound, or the kind absent), or unknown (any other failure);
  - **refresh:** the fact is set on every resolution, so its read time stays current: on policy changes and on every health sync.
- **The instance facts** (`crd/facts_admission*.go`):
  - a warning's validated field instead of its text;
  - a webhook URL's origin with "(path set)";
  - a Service path as "(path set)".
- **The chat projection** (6fba312): a finding of the build. The default CRD branch sent the spec's strings to depth 4, CEL expressions included, and the warning text in status. Policies and bindings now have their own cases, built from the admission projections: counts and presence only.
- **`AdmissionService.CheckParams`** (`fleet/admissioncheck.go`): it reads the bindings fresh, so a selector is the current one, used for the read and never returned. The reads are metadata get or list (limit 1), capped at 20 bindings. Forbidden is "unreadable", never "not found".
- **The lens** (ff5bbd7, e0457a1):
  - **`AdmissionView`:** Webhooks, Policies and Bindings tabs;
  - **`BackendChain`:** each webhook's stations, sharing the width;
  - **the limits block**, including the routing mode and static manifests;
  - **historical warnings**;
  - **the param check:** fenced by cluster and policy UID.
- **A deviation from §3: the capability.** §3 said "always present: the API is core". The build detects it instead (`capability.Admission`: `admissionregistration.k8s.io/v1` served, carried to `ClusterDTO.admissionPresent` and the re-probe). The attention task and registry group are gated on it. That keeps capability detection over assumption, and keeps an always-on task out of every fixture that predates it.
- **The parity frontend stage** now skips listed facts (a binding, an uncounted Backup), which the oracle does not count.

**Found by the live captures** (kind-klyx, lab-s3a): the backend chain overflowed the inspector at 1440; and "1 usable endpoints". Both fixed in e0457a1.

**The load test** (`healthsrc/load_test.go`) assembled the Gateway task without the shared kinds after 94aa907. It is opt-in, so the gate missed it. It now builds typed Services, the shared kinds and the admission task, with webhook configurations and bound policies at scale.

**Gates:**
- **The parity gate:** passes at e0457a1 (Go stage and frontend stage; the parity-test fixes in 71c2f10).
  - The first run failed: the Go stage required a rule on every row, as the frontend stage did, and a binding's row is a fact.
  - A first fix that read a generic `Binding` field also skipped FluxInstance rows, which carry an unrelated `Binding`. The skip is per part (`admission/policies`, `velero/backups`).
- **The four-way native smoke** passes 159/159 in night-1440, night-1024, day-1440 and day-1024, run within the hour after re-applying lab-s1b, lab-s1c, lab-s2b and lab-s3a.
- **The rest:**
  - `go test -race` on admission, healthsrc, appbridge, fleet, observe, crd and capability: ok;
  - golangci-lint v2.14.0: 0 issues;
  - the frontend suite: 2181 passed.
- **The live captures** (scratchpad cap-s3a, all four themes and widths): no needle on screen and no sideways scroll.
- **The load test** (§7), large cluster, two runs each:
  - **full evaluation:** 66.1 and 72.1 ms before (5be4ec7, the known 50 ms miss) against 72.9 and 78.4 ms after, about +6 ms (9%);
  - **steady retained heap:** +0.5 MB on the large cluster, +1.1 MB across six;
  - **recovery-storm peak:** 64.0 and 86.3 MB before, 79.1 and 80.7 MB after; noise, no regression.
