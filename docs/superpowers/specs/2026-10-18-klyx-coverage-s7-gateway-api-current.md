# Klyx coverage S7: Gateway API, current

Status: design, for Codex's review before code. Roadmap: `2026-10-16-klyx-coverage-roadmap.md` §2 S7, under the shared contracts C1 (source contract) and C3 (shared Observer kinds).

Today Klyx reads Gateway API as HTTPRoute only, from end to end:
- a route attaches to a Gateway by one `parentRef` (the first match wins, and further section names collapse into one lane);
- listeners carry no status;
- `allowedRoutes`, ReferenceGrants and ListenerSets are never read;
- route identity is `namespace/name` with no kind;
- the on-demand reads pick a group-level preferred version.

S7 makes the reading current:
- **ListenerSets** as first-class parents.
- **Listener status** for Gateways and ListenerSets.
- **Routes generalised to every standard kind** (HTTPRoute, GRPCRoute, TLSRoute, TCPRoute, UDPRoute), each attachment kept separately.
- **Evidence for the hard cases:** cross-namespace grants, served versions per resource, and a controller that does not declare a feature.
- **The experimental group** `gateway.networking.x-k8s.io` recognised.
- **Metrics** kept protocol-qualified.

It builds in two stages:
- **S7a:** versions, listener status, ListenerSets.
- **S7b:** route kinds, grants, features, the experimental group, metrics.

## 1. The API, read from the source

Read from `sigs.k8s.io/gateway-api` v1.4.1, v1.5.1 and v1.6.3: `apis/v1`, `apisx/v1alpha1`, `pkg/features` and `config/crd`. Also read: the Envoy Gateway release notes.

**Served versions per resource.** The `config/crd` YAML of each channel, as (served, storage):

| Resource | v1.4 | v1.5 | v1.6 |
|---|---|---|---|
| gateways, httproutes, gatewayclasses | v1 (+ v1beta1) | same | same |
| grpcroutes | v1 | v1 | v1 |
| referencegrants | v1beta1 | v1 served, v1beta1 storage | same |
| listenersets (`gateway.networking.k8s.io`) | none | v1, standard | v1, standard |
| xlistenersets (`gateway.networking.x-k8s.io`) | v1alpha1, experimental | gone | gone |
| tlsroutes | v1alpha2 and v1alpha3, experimental | v1, standard; alphas experimental only | same |
| tcproutes, udproutes | v1alpha2, experimental | v1alpha2, experimental | v1, standard; v1alpha2 experimental only |
| x-k8s experimental | XListenerSet, XBackendTrafficPolicy, XMesh | XBackendTrafficPolicy, XMesh | XBackend, XBackendTrafficPolicy, XMesh |

**ListenerSet** (v1, identical in v1.5.1 and v1.6.3):
- **`spec.parentRef`** `{group?, kind? (default Gateway), name, namespace?}` names one Gateway.
- **`spec.listeners`** has 1 to 64 entries, each `{name, hostname?, port, protocol, tls?, allowedRoutes? (default: Same namespace)}`. Names are unique within the ListenerSet, not across the Gateway.
- **The parent treats its listeners as one merged list**, in precedence order: the Gateway's own, then ListenerSets by creation time (oldest first), then by `namespace/name`.
- **`status.conditions`:**
  - Accepted, with reasons Accepted, NotAllowed, ParentNotAccepted, ListenersNotValid, Invalid and Pending;
  - Programmed;
  - both default to Unknown/Pending.
- **`status.listeners[]`** carries `{name, supportedKinds, attachedRoutes, conditions}` with these conditions:
  - Accepted (UnsupportedProtocol, TooManyListeners);
  - Conflicted (HostnameConflict, ProtocolConflict, ListenerConflict);
  - ResolvedRefs (InvalidCertificateRef, InvalidRouteKinds, RefNotPermitted);
  - Programmed (PortUnavailable, and others).
- **ReferenceGrants are not inherited.** A grant to a Gateway does not cover its ListenerSets, nor the reverse; a ListenerSet may reference its own namespace without one.

**The Gateway side** (v1.5+; the field is present in v1.4's types too):
- **`spec.allowedListeners.namespaces.from`** is All, Selector, Same or None. The default is None: no ListenerSets.
- **`status.attachedListenerSets`** counts the ListenerSets that are selected, reference this Gateway, and are Accepted.
- **A Gateway listener's own status** (`status.listeners[]`) has the same shape as a ListenerSet listener's.

**Routes:**
- **Every route kind** shares `spec.parentRefs` and `status.parents[]` (`RouteStatus`).
- **A route attaches to a ListenerSet** with `kind: ListenerSet`; `sectionName` names one of its listeners.
- **The kinds differ only in their rules:**
  - HTTPRoute: hostnames and matches;
  - GRPCRoute: hostnames and service/method matches;
  - TLSRoute v1: 1 to 1024 hostnames, required, and exactly one rule of backendRefs. The older shapes differ (revision 1): v1alpha3 has 1 to 16 hostnames and exactly one rule, and v1alpha2 has 0 to 16 hostnames and 1 to 16 rules. Conversion between these versions passes fields through, so an object admitted through v1alpha2 can be read through v1 with no hostnames and several rules;
  - TCPRoute and UDPRoute v1: rules of 1 to 16 backendRefs, with no hostnames and no matches.

**Supported features.** A GatewayClass `status.supportedFeatures[]` lists feature names such as "Gateway", "ListenerSet", "HTTPRoute", "GRPCRoute", "TLSRoute", "TLSRouteModeTerminate", "TCPRoute" and "UDPRoute" (`pkg/features`).

**Envoy Gateway:**
- 1.8.x reconciles ListenerSet: 1.8.2 applies listener precedence to hostname conflicts.
- 1.9.0 reconciles TCPRoute and UDPRoute at v1, and requires the v1.6 CRDs.

## 2. Versions: a probe per resource (S7a)

**Every Gateway API resource Klyx reads gets its own candidate list**, resolved by the existing per-resource probe (`crdServed` for Observer kinds, `servedResourceGVR` for on-demand reads), never the group's preferred version:

| Resource | Candidates, in order |
|---|---|
| gateways, gatewayclasses, httproutes | v1, v1beta1 |
| grpcroutes | v1 |
| listenersets | v1 |
| tlsroutes | v1, v1alpha3, v1alpha2 |
| tcproutes, udproutes | v1, v1alpha2 |
| referencegrants | v1, v1beta1 |

The on-demand reads (`gwGVR`, the rule metrics) move to the same per-resource resolution. A mixed installation (the experimental channel serves tlsroutes at v1 and both alphas) reads the first served candidate. The fields Klyx reads have the same shape across these versions.

The version read is named in the inspector ("read as tlsroutes.v1alpha2").

## 3. Listener status (S7a)

**A listener carries** its status (`supportedKinds`, `attachedRoutes`, conditions) and its `allowedRoutes`, for a Gateway's own listeners and a ListenerSet's.

**The Gateway verdict gains listener evidence.** Gateway Accepted and Programmed stay the headline (`GatewayRule`, unchanged order). With the Gateway itself healthy:
- a listener with Programmed False, ResolvedRefs False or Accepted False reads **Degraded**, naming the listener and its reason (for example "listener https: InvalidCertificateRef");
- a listener with Conflicted True also reads **Degraded**: it lost precedence and serves nothing.

A listener's conditions are judged only when their `observedGeneration` matches the Gateway's generation. Older ones are shown, never judged.

## 4. ListenerSets (S7a)

**A new part, `gateway/listenersets`:**
- It counts ListenerSet UIDs.
- It depends on gateways (the parent) and namespaces (for the selector).
- It is an Observer watch of `listenersets` (v1).

**Its verdict** comes from the ListenerSet's own conditions, in the Gateway's order:
- **Accepted** False is a **Fault**, named with its reason (NotAllowed, ParentNotAccepted, ListenersNotValid, Invalid).
- **Programmed** False is a **Fault**.
- Unknown or Pending reads as `GatewayRule` reads a Gateway's today: the same states, and the same handling of stale and absent conditions.
- With the ListenerSet healthy, its listener conditions apply as in §3, as **Degraded**.
- Stale conditions (an `observedGeneration` below the generation) take the Syncing presentation.

**Attachment is never flattened.** A ListenerSet's listeners keep their own identity: the ListenerSet UID plus the listener name.

**Precedence.** The Gateway inspector lists the Gateway's own listeners, then each ListenerSet's, in the merge's precedence order:
- creation time;
- then `namespace/name`.

Each listener shows the controller's own conditions. Klyx never decides a conflict itself: it shows the order and the controller's Conflicted reason.

**The parent link** is the `parentRef` (namespace defaulting to the ListenerSet's).
- **Absence is proven only by a complete, current read** (revision 1, §8): a ListenerSet whose parent Gateway is absent from a Live, complete, untruncated gateways model reads "parent Gateway not found" as a Fault, and its listeners join nothing.
- **Any other read** (forbidden, stale, checking, or truncated by the budget) makes the parent "not established": Unknown, never "not found".
- **The ListenerSet's own conditions still decide** when they prove a failure: an Accepted False from the controller is a Fault whatever the parent read.

**Klyx's own allowance check is evidence, never the verdict.** Klyx evaluates the parent's `allowedListeners` against the ListenerSet's namespace:
- **None** (the default): allows nothing.
- **Same:** the Gateway's namespace only.
- **All:** every namespace.
- **Selector:** the namespace's labels, which the namespaces projection must now keep (§8).

The controller's Accepted condition stays authoritative:
- when it agrees, Klyx's check explains it ("the Gateway allows ListenerSets from Same; this one is in another namespace");
- when it disagrees, both are said ("the controller accepted it although allowedListeners is None"), and the verdict is still the controller's.

**The Gateway's `attachedListenerSets`** is shown beside the ListenerSets Klyx found. A difference is said, never reconciled.

## 5. Routes, generalised (S7b)

**The kinds.** HTTPRoute, GRPCRoute, TLSRoute, TCPRoute and UDPRoute are watched, each as its own Observer kind with §2's candidates. The `routes` part counts all of them.

**Identity** is `kind/namespace/name` with the UID. This fixes today's `namespace/name` key, which would collide between kinds.

**Attachments, one per parentRef entry** (revision 1):
- **Identity:** the full normalised parentRef tuple: group, kind, namespace (defaulted to the route's), name, `sectionName` and `port`, each kept even when empty. It is the attachment's identity throughout:
  - the reduction's parent;
  - the DTOs;
  - pin and reveal;
  - the topology lane key.

  The display label may be shorter ("gw-a · https" or "gw-a · :8443").
- **Port-only and section-only references:** two parentRefs to one Gateway differing only by `port` (the experimental channel permits it), or only by `sectionName`, are two attachments, each with its own parent status.
- **The topology** draws one lane per attachment. Today they collapse into one.

**The parent status is the expected controller's** (revision 1). `status.parents[]` is keyed by the parentRef *and* `controllerName`. A Gateway recreated with another class can leave the retired controller's Accepted True beside the current controller's False.
- **The expected controller** is the parent Gateway's GatewayClass `spec.controllerName`; for a ListenerSet, its parent Gateway's class's.
- **Klyx judges only the entry** whose parentRef matches and whose `controllerName` is the expected one.
- **Other controllers' entries** for the same reference are shown as "an entry from another controller (‹name›), not judged".
- **When the expected controller cannot be established** (the class or the Gateway unread, stale or truncated, or a class whose `controllerName` is absent), the attachment is Unknown ("the controller judging this parent is not established"). Several entries are listed, never chosen between.
- **GatewayClasses** join the routes and listenersets parts' dependencies.

**A route's verdict** reduces its attachments with `observe.ReduceAttachments` (unchanged). The parent is the attachment identity above.

**Per attachment:**
- **Parent missing:** only on a complete, current read of the parent's kind (§8). Any other read makes the parent "not established", Unknown. A ListenerSet parent that exists but is not Accepted reads "parent ListenerSet not accepted".
- **The parent status:** Accepted, then ResolvedRefs, by reason, as today.
- **The section:** with `sectionName`, the named listener must exist in that parent, whether the Gateway's own listener or the ListenerSet's.
- **Backends, qualified by port and protocol** (revision 1), for every route kind. Today's resolver counts every EndpointSlice of a Service whatever its port, so a UDP/53 backend on a Service that also serves TCP/80 reads available from TCP endpoints. A backendRef `(Service, port)` now resolves:
  1. **The Service port, by number and transport** (revision 2). Kubernetes keys a Service's ports by `(port, protocol)`, so TCP/53 and UDP/53 can both exist. The transport comes from the route kind: UDP for UDPRoute, TCP for HTTPRoute, GRPCRoute, TLSRoute and TCPRoute. Klyx selects the one ServicePort whose `port` equals the backendRef's and whose protocol (defaulting to TCP) is that transport, never the first numeric match.
     - None found: "Service has no ‹transport› port N", a backend issue, said only on a complete Services read.
     - A same-number port of another protocol is named beside it ("port 53 exists for TCP only").
  2. **Its name.**
  3. **The EndpointSlices** of that Service whose ports include an entry with that name *and* that protocol. A named Service port matches a slice port by name and protocol; an unnamed one matches the slice's single unnamed port of that protocol.
  4. **The count:** only endpoints in those slices count.

  Slices whose ports cannot be matched are not counted. An incomplete or unread EndpointSlices model leaves the backend Unknown, as today. This changes HTTPRoute's backend verdict for multi-port Services, where it was optimistic.
- **Rules:** every returned rule is kept, whatever the version's schema allows (TLSRoute's alpha shapes, §1). Rules are matched by index, and by `name` where the kind's rule carries one.

**Rule shape per kind:**

| Kind | Hostnames | Matches | What the topology and data path show per rule |
|---|---|---|---|
| HTTPRoute | yes | path and method (unchanged) | method and path, or "every request" |
| GRPCRoute | yes | service and method | the service and method, or "every call" |
| TLSRoute | as returned: required in v1 and v1alpha3, optional in v1alpha2 (none reads "any SNI") | none | "SNI ‹hostnames›" per returned rule; never assumes one rule |
| TCPRoute, UDPRoute | none | none | "every connection on ‹listener› ‹port›" |

Fingerprints (`RuleFingerprints`) cover the kind and each kind's match fields. L4 rules fingerprint their backends only.

## 6. The hard cases' evidence (S7b)

Each piece of evidence explains the controller's status and never replaces it.

**allowedRoutes, per listener:**
- **Namespaces:** Same, All or Selector, against the route's namespace labels. A selector is evaluated only on a complete, current namespaces read; otherwise "namespace labels not established" (revision 1).
- **Kinds:** the listener's `status.supportedKinds` when reported, otherwise its declared `allowedRoutes.kinds`, otherwise the protocol's conventional kinds:
  - HTTP and HTTPS: HTTPRoute, GRPCRoute;
  - TLS: TLSRoute;
  - TCP: TCPRoute;
  - UDP: UDPRoute.

  The conventional kinds are said as a convention, because the mapping is the implementation's.
- **Disagreement:** a route whose parent status reads NotAllowedByListeners or NoMatchingParent gets Klyx's own check beside it. Where they differ, both are said.

**ReferenceGrants** (a new Observer kind, `referencegrants`, small):
- A backendRef, or a listener certificateRef, in another namespace is explained by the grants that would allow it:
  - grant namespace = the target's namespace;
  - `from` = the route's (or Gateway's, or ListenerSet's) group, kind and namespace;
  - `to` = the target's group and kind, and its name when set.
- A grant to a Gateway is not counted for its ListenerSets (§1).
- The controller's ResolvedRefs (RefNotPermitted) stays authoritative.
- **A disagreement is said only on complete evidence** (revision 1): "ResolvedRefs True, but no ReferenceGrant allows it" needs a complete, current, untruncated referencegrants read. Otherwise the grant evidence is "not established", and nothing disagrees.

**Supported features.** When the parent's GatewayClass lists `status.supportedFeatures`:
- a ListenerSet, or a route of a kind the class does not list (by §1's names), carries "the class's controller does not declare support for TLSRoute";
- a TLS listener in Terminate mode with no "TLSRouteModeTerminate" likewise.

This is evidence beside the verdict, which stays the status's. A class that lists no features says nothing.

## 7. The experimental group, recognised (S7b)

**Capability detection lists the served `gateway.networking.x-k8s.io` kinds** (XListenerSet, XBackendTrafficPolicy, XMesh, XBackend, or whatever discovery returns).

**The Gateway lens shows them in one line:** "experimental Gateway API kinds served: XMesh, XBackendTrafficPolicy; Klyx does not read them".

An XListenerSet adds "Gateway API 1.5 promoted it to ListenerSet (`gateway.networking.k8s.io/v1`)".

Nothing in the experimental group is watched or judged.

## 8. The source contract (C1) and the budget

**Parts:**

| Part | Acquisition | Counted entity | Dependencies | Notifications |
|---|---|---|---|---|
| `gateway/gateways` (changed) | watch, as today | Gateway UID | gatewayclasses | faults, as today |
| `gateway/listenersets` (new) | watch | ListenerSet UID | gateways, gatewayclasses, namespaces | faults |
| `gateway/routes` (changed) | watch, five route kinds | the kind-qualified route UID | gateways, gatewayclasses, listenersets, services, endpointslices, referencegrants, namespaces | as today |

**Projections** (revision 1: purpose-built, never the whole object). Each keeps the identity and comparison facts whole, and caps only display lists, after classification and fingerprinting over the whole input:
- **Gateways:**
  - spec: class, listeners (name, hostname, port, protocol, TLS mode and certificateRef identities, allowedRoutes whole) and allowedListeners whole;
  - status: conditions, listeners' status (conditions, supportedKinds, attachedRoutes), addresses (capped at 8 for display) and attachedListenerSets.
- **ListenerSets:** parentRef, listeners as above, status conditions and listeners' status.
- **Routes:**
  - parentRefs whole;
  - hostnames (capped at 16 for display, with the count);
  - per rule: name, backendRefs whole (group, kind, namespace, name, port, weight), and matches (capped at 8 for display; fingerprinted whole at projection);
  - status.parents whole (parentRef, controllerName, conditions with messages clipped to 512 bytes).
- **GatewayClasses:** controllerName, conditions and supportedFeatures names.
- **ReferenceGrants:** `from` and `to` whole.
- **Namespaces:** now keep their labels, for the selectors.
- **Equality:** raw-against-projected equality is tested for every field the rules read.

**Dependency completeness: only a complete, current read proves absence** (revision 1). A negative claim needs a Live, complete and untruncated read of the kind it is about:
- "parent not found", "no listener named X";
- "no ReferenceGrant allows it";
- "the namespace's labels do not match".

Every other read state makes the evidence "not established" (Unknown), never negative: not served, forbidden, checking, stale (the last good model) or truncated by the budget. A failure the controller itself reports in status (an Accepted or ResolvedRefs False) is still judged whatever the dependencies' state, because it is proven by the object read.

**Clocks:** none. Every Gateway API rule is status-driven, as the Gateway and route rules are today.

**C3 (shared kinds):**
- Services and EndpointSlices keep their gateway consumer.
- `namespaces` gains no new gate: it is already watched.
- `referencegrants` opens only when gateways are served.

**Budget** (revision 1; units, revision 2): per cluster, each kind admitted in key order (namespace/name) within its budget. A projection's size is its JSON-encoded length, as the Velero and Flux Operator kinds measure it (`Size`), not its heap footprint. Past its budget a kind reads truncated: its counts become lower bounds, and every join through it is incomplete, so not established.

| Kind | Budget | MaxObject | Roughly holds |
|---|---|---|---|
| gatewayclasses | 64 KiB | 4 KiB | 50 classes |
| gateways | 256 KiB | 16 KiB | 100 Gateways of 10 listeners |
| listenersets | 256 KiB | 16 KiB | 150 ListenerSets |
| httproutes | 1 MiB | 16 KiB | 1,000 routes of about 1 KiB |
| grpcroutes, tlsroutes | 256 KiB each | 16 KiB | 250 routes each |
| tcproutes, udproutes | 128 KiB each | 8 KiB | 250 routes each |
| referencegrants | 128 KiB | 4 KiB | 400 grants |
| namespaces (labels added) | 256 KiB | 4 KiB | 1,000 namespaces with labels |

- **Admission totals:** about 2.7 MiB of encoded projections per cluster at every budget's limit, and about 16 MiB across the six-cluster fleet. These are encoded admission totals, not a heap claim. Typical clusters hold a few kilobytes.
- **Retained versus transient:** the budget bounds the retained model. List pages during a relist are transient and not budgeted.
- **The load test** (healthsrc `TestLoad`) is extended with a Gateway fixture at about 500 routes and 50 ListenerSets per cluster, and a full-budget case filling every Gateway API kind to its budget. It records the actual retained Observer heap, steady and peak, separately from the encoded totals, before and after. The as-built notes report the measured heap, not the admission totals.
- **A sizing test** measures the projection of a maximal route, ListenerSet and Gateway against their MaxObject.

Clusters without these CRDs read Absent, at no cost.

## 9. What Klyx shows

**Gateways view:**
- **Inspector, Listeners block:** each listener with protocol, port and hostname, its attached-route count, supported kinds and conditions.
- **A ListenerSets block** in precedence order, each with its own status and listeners, and the `allowedListeners` evidence.
- **The list's listener column** counts the Gateway's listeners plus those of its accepted ListenerSets.

**ListenerSets** are a third view beside Gateways and Routes, listed broken first. Their inspector shows the parent, its listeners and the allowance evidence.

**Routes view:**
- a kind column and a kind filter;
- "HTTPRoute" becomes the row's kind;
- an L4 route shows its listener and port instead of paths.

**Topology and data path:**
- one lane per attachment, keyed by the full attachment identity (§5: the route, and the parent with its section and port);
- L4 lanes and rails draw route, backends and rule (§5's table), with no match step.

**Metrics** stay protocol-qualified:
- HTTP rule metrics show only for HTTPRoute lanes;
- GRPCRoute, TLSRoute, TCPRoute and UDPRoute say "HTTP request metrics do not apply to ‹kind›";
- route history's live gate names the HTTPRoute kind, as today.

Connection metrics for L4 are not in S7.

## 10. Tests, lab, parity, smoke

**Unit:**
- the version candidates per resource;
- listener status parsing and the Gateway listener rule;
- ListenerSet verdicts and precedence;
- `allowedListeners` (None, Same, All, Selector), agreeing with the controller and disagreeing;
- route attachments per parentRef (Gateway and ListenerSet parents; two sections, and two port-only references, to one Gateway);
- parent status by the expected controller:
  - a recreated Gateway with the retired controller's Accepted True beside the current one's False;
  - the class unread;
  - several entries;
- completeness: complete absence, forbidden, stale and truncated reads of the parent, grant and namespace kinds, each never negative, while controller-reported failures are still judged;
- port- and protocol-qualified backends:
  - a Service with UDP/53 and TCP/80 whose only ready endpoints are TCP: the UDP backend is not available;
  - a Service with TCP/53 (dns-tcp) and UDP/53 (dns-udp), in both ServicePort orders, with only TCP endpoints: a UDPRoute backend is not available, and a TCPRoute's is;
  - the converse, only UDP endpoints;
- TLSRoute's v1alpha2 shape: no hostnames and several rules, read through v1;
- the budget: a truncated route kind makes its joins not established, and the sizing test;
- each route kind's rule shape and fingerprint;
- `allowedRoutes` kinds (supportedKinds, declared, conventional) and namespaces;
- ReferenceGrants, including non-inheritance for ListenerSets;
- supported features;
- the experimental group;
- kind-qualified identity: an HTTPRoute and a TLSRoute with the same name;
- projection equality for every kind.

**The lab:**
- Gateway API v1.6.3's experimental-channel CRDs are installed on kind-klyx. They come from the module cache's `config/crd/experimental`, already checksummed by the Go proxy, and are inspected before applying.
- This upgrades the lab's v1.2.1 standard CRDs. The served versions stay compatible: v1 is kept, and v1beta1 is still served.
- The fixtures live in `klyx-gateway-lab`, with status written through the status subresource and a stand-in controller name. The roadmap's real-component list does not include a gateway controller (C6). They are:
  - a Gateway allowing ListenerSets from Same;
  - an accepted ListenerSet with a listener that conflicts on hostname;
  - a NotAllowed ListenerSet from another namespace;
  - an HTTPRoute and a TLSRoute with the same name;
  - a TCPRoute and a UDPRoute;
  - a cross-namespace backend with and without its ReferenceGrant;
  - a GatewayClass whose `supportedFeatures` omits UDPRoute;
  - an XMesh served by the experimental channel.

**Parity:**
- `klyx-parity.sh` copies the new CRDs.
- `parity/fixture.sh` adds a ListenerSet and an L4 route under `parity-gw`.
- The oracle gains the `gateway/listenersets` part, and the routes part counts every kind.

**Native smoke:**
- the ListenerSet view (the NotAllowed one first);
- a Gateway inspector's ListenerSets block in precedence order;
- the Routes view's kind filter showing a TCPRoute;
- an L4 data path with no match step.

**Captures:** night and day at 1440 and 1024.

## 11. Not in S7

- **Reading the experimental group's kinds:** recognised only.
- **L4 connection metrics, and gRPC per-method metrics:** later, protocol-qualified.
- **Mesh and GAMMA routing** (parentRefs to Services).
- **BackendTLSPolicy evaluation:** it is already a decoded policy.
- **Policy attachment to ListenerSets:** Envoy Gateway 1.9 targets them. Policies stay on today's Gateway, HTTPRoute and Service kinds.
- **Verbs:** none.

## 12. Staging

- **S7a:** §2, §3, §4, and §8's listenersets part and namespace labels; the Gateways and ListenerSets views; the lab's CRD upgrade and the ListenerSet fixtures; parity for ListenerSets.
- **S7b:** §5, §6 and §7, §8's route kinds and referencegrants, and §9's routes, topology and metrics; the L4, grant and feature fixtures; parity for the L4 route.

Each stage has its own gates and implementation review.

## 13. Revision 1: Codex's design review of 7fc7f82

| # | Finding | Where answered |
|---|---|---|
| 1 | Route status needs controller ownership as well as parent identity | §5: the expected controller is the parent's GatewayClass `controllerName` (via the parent Gateway for a ListenerSet); only its entry is judged; other controllers' entries are shown, not judged; an unestablished controller is Unknown. GatewayClasses join the dependencies (§8). |
| 2 | Model absence is not evidence | §8: only a complete, current, untruncated read proves absence (parent, listener, grant, namespace labels); any other read is "not established". Controller-reported failures are still judged. §4, §5 and §6 apply it; §10 tests each read state. |
| 3 | Today's backend aggregation cannot establish L4 availability | §5: backends resolve through the Service port, its protocol and name, to the EndpointSlices whose ports match. This applies to every route kind, and changes HTTPRoute for multi-port Services. §10 tests the UDP/53 with TCP/80 case. |
| 4 | The attachment identity dropped the port | §5: the full normalised parentRef tuple, port included, is the attachment identity throughout (reduction, DTOs, pin and reveal, lane key). §9 and §10 add the port-only case. |
| 5 | TLSRoute's alpha shapes differ | §1: the v1alpha2 and v1alpha3 shapes, and pass-through conversion. §5: every returned rule is kept, and hostnames are version-aware. §10 adds the v1alpha2 fixture. |
| 6 | The retained-model budget was not specified | §8: purpose-built projections; numeric per-kind budgets and MaxObject; truncation making joins not established; the fleet worst case; retained versus transient; the load test and a sizing test. |

Codex found the per-resource version table, the GRPCRoute addition and the S7a/S7b split reasonable.

## 14. Revision 2: Codex's re-review of aaff54f

Five of revision 1's answers were accepted, with one residual P2 and one P3:

| # | Finding | Where answered |
|---|---|---|
| 1 (P2) | Service-port selection by number alone is ambiguous for a valid same-number TCP/UDP Service | §5: the ServicePort is selected by number *and* the route kind's transport (UDP for UDPRoute, TCP otherwise), never the first numeric match; slices match by name and protocol. §10: both ServicePort orders, with only TCP endpoints and the converse. |
| (P3) | Budget units and heap wording | §8: sizes are JSON-encoded projection lengths; the totals are encoded admission totals, not heap; the load test's full-budget case records the actual retained heap, which the as-built notes report. |

**Accepted at e51e03c** (Codex, design revision 2, handoff 0b0adb79, 2026-10-18). This accepts the design only; S7a and S7b keep their separate implementation reviews and gates.

## 15. As built: S7a

Built on `feat/coverage` from b982518, commits d13e11e to 98b805e. S7b (§5, §6, §7, the route kinds and grants) is not built.

### 15.1 Versions (§2)

- **The candidates** live in `gwapi.Versions` and are the table in §2.
- **The Observer probe** is `crdServedAmong` (`internal/fleet/crdprobe.go`). It reads the first candidate whose resource list names the resource. A candidate discovery cannot read is uncertainty: when no readable candidate serves the resource, the first unreadable candidate is tried, as `crdServed` does. Absence needs a complete answer. A resource served only at a version outside the candidates is a named gap ("served at v2; Klyx reads v1 or v1beta1"), never a list read at a version Klyx does not parse; so is an unreadable non-candidate when no candidate serves.
- **The on-demand reads** (`gwGVR` for the topology, the rule metrics and the GatewayClass read) use `servedResourceAmong` over the same core. They return an error naming why nothing was read. Discovery that cannot answer at all still tries the first candidate, as `servedVersion` does.
- **The healthsrc kinds** take the probe through `NativeDeps.ServedAmong`. Without it (older test harnesses), `Served`'s answer is checked against the candidates.

### 15.2 Listener status (§3)

- **Parsing:** a listener carries its TLS mode, its `allowedRoutes` as written (namespaces with the selector read through the API's own `LabelSelector`, an unreadable selector flagged, and the kinds), and its `status.listeners` entry by name (supported kinds, attached routes, every condition in the order reported). A name reported twice keeps its first entry.
- **The rule** is `gwapi.GatewayVerdict`: `GatewayRule` unchanged, then, with the Gateway itself OK, `ListenerIssues`. Each listener's first failing current condition, in the order that explains it best: Accepted False, Conflicted True, ResolvedRefs False, Programmed False. A condition observed for an older generation is shown, never judged (`condState`'s staleness). The verdict is Degraded, labelled "listener ‹name›: ‹reason›" with "(+N more)", the reason and message in `Reason`, the condition as `Cond`, and no `Since` (it is the worst of several listeners).
- **Every surface uses it:** the Observer's gateways part, the on-demand Gateway list DTO and the topology's Gateway node.

### 15.3 ListenerSets (§4)

- **The part** `gateway/listenersets` counts ListenerSet UIDs; its dependencies are gateways, gatewayclasses and namespaces (§8's table; the namespaces kind is the core kinds'). Its ledger is on.
- **The verdict** (`gwapi.ListenerSetRule`): the set's own conditions first. Accepted False is a Fault named with its reason ("not accepted: NotAllowed"); Programmed False is a Fault; pending, absent and stale read as `GatewayRule` reads them. A controller-reported Fault is judged whatever the parent's read. Then the parent:
  - absent from a Live, complete, untruncated gateways read (`Capture.Complete`): Fault "parent Gateway not found";
  - absent from any other read: Unknown "parent Gateway not established", unless the set's own conditions already read Degraded;
  - a reference to another kind: Unknown "parent is a ‹Kind›, not a Gateway".

  With the set healthy and its parent found, its listeners' conditions apply as a Gateway's do (Degraded).
- **The allowance** (`gwapi.ListenerSetAllowance`) is evidence on the row, never the verdict: None (the default, and when `allowedListeners` is unset), Same, All, or Selector against the namespace's labels. The labels are known only when the namespace is in a complete namespaces read. A value Klyx does not read, or an unreadable selector, is said and not established. It is compared with the controller's current Accepted condition: accepted while Klyx reads "no", or NotAllowed while Klyx reads "yes", is a disagreement the text says, prefixed with the controller's side.
- **Precedence** (`gwapi.SortByPrecedence`): creation time, then namespace/name, among the ListenerSets naming the same parent (group, kind, namespace and name). The row carries its place, the count, and whether the ListenerSets read was complete (a truncated read can miss one that ranks earlier).

### 15.4 Projections and the budget (§8): two measured changes

1. **MaxObject for a Gateway and a ListenerSet is 64 KiB, not 16 KiB.** The design's figures assumed a listener without its status. Measured (`TestProjectionSizing`), a listener with a controller's status (four conditions, two supported kinds, a certificate, an allowedRoutes) is about 1.15 KiB encoded: a ten-listener Gateway is 12.0 KiB, and 16 KiB would have refused anything past about 13 listeners. The API allows 64. A 64-listener object is 73.4 KiB whole and 59.6 KiB once tight. The tight rebuild clips the object's own condition messages to 256 bytes and drops its listeners' messages (what the rules read is kept).
2. **The projection is typed.** `gwapi.Project` builds the API-shaped projection (the kept fields in the API's own encoding, credential shapes scrubbed from messages before the 1 KiB clip), measures its JSON-encoded length as `Size` (the budget's measure, as the design states), then keeps a typed, parsed `gwapi.Object`: identity in an `ObjectMeta` (name, namespace, UID, generation, creation time, resource version, labels: what the refs and the fleet index read), plus the `GatewayRef`, `ListenerSet` or `GatewayClass` the rules read. Evaluators no longer parse unstructured maps on every evaluation.

**The budgets**, in encoded bytes, are revised with the measured sizes:

| Kind | Budget | MaxObject | Measured to hold |
|---|---|---|---|
| gatewayclasses | 64 KiB | 4 KiB | 172 classes |
| gateways | 1 MiB | 64 KiB | 99 ten-listener Gateways |
| listenersets | 512 KiB | 64 KiB | 210 two-listener ListenerSets |
| namespaces (labels added) | 256 KiB | 4 KiB | 1,583 namespaces with five labels |
| httproutes | unbudgeted until S7b | | |

**The full-budget case** (`TestGatewayAPIObserverHeap`): every S7a kind filled past its budget (200 classes, 200 ten-listener Gateways, 600 two-listener ListenerSets, 3,000 namespaces), read through the real kinds, parts and rows with the gateways and listenersets rows subscribed. The running Observer holds **3.06 MiB of heap for 1.81 MiB of encoded budget: 1.69 heap bytes per budget byte**. S1a measured 5.55 for its unstructured projections, which is why the typed projection was chosen.

**The namespace count stays exact past the budget.** `Summary.Namespaces` counts the retained namespaces plus the ones the budget left out (the Observer's exact `notRetained`). The full-budget case reads 3,000 with 1,417 left out.

**The fleet index reads a truncated kind as partial** (`fleetindex.StatePartial`). Budgeting namespaces and Gateways, two default index kinds, exposed that `VisitKind` finishes over a truncated model and the index read only its coverage, so ⌘K would have shown a subset as the whole kind. The same gap existed since S1a for any budgeted kind configured in `index.kinds`. A live kind the Observer truncated now reads "partly indexed (N kept within Klyx's budget; more exist)". Its entries stay searchable. Namespaces now keep their labels for the index too.

### 15.5 Views (§9)

- **The Gateway lens** offers a third view, ListenerSets, beside Gateways and Routes. It is shown unless the ListenerSet kind is confirmed absent. It lists broken first, with columns for status, parent (and its state when not found), the allowance (a disagreement in amber) and listeners.
- **Its inspector** shows:
  - the verdict, with a detail from the evidence;
  - the parent block (its kind, its state, and its place in the merge: "2nd of 3 ListenerSets naming this parent, after the Gateway's own listeners", qualified when the read is incomplete);
  - the allowance, said to be Klyx's own reading and not the verdict;
  - the listeners with their status;
  - "read as listenersets.v1" in the identity line.
- **The Gateway inspector's Listeners block** shows each listener's endpoint, TLS mode, allowedRoutes (with its default named) and status. Failing conditions are named in amber with the controller's reason and message; current healthy ones are listed compactly; conditions from an older generation are shown as "observed at generation N, shown, not judged".
- **A ListenerSets block** lists the sets naming the Gateway in merge order, each with its verdict and listeners, beside the Gateway's `allowedListeners` and "N found naming this Gateway, M accepted · the controller reports K attached". A difference is said ("they differ"), never reconciled. The block is omitted for a Gateway that allows none, has none and reports none.
- **The Gateways list's listener column** adds "+N from ListenerSets" for the listeners of currently accepted sets.
- **ListenerSets reach** reveal (Top links select them), Explain (an allowlist for the part, and listener status for Gateways), chat, health notifications, Describe and the generic browser.
- **A reveal race, fixed:** a view mounting with a pending reveal let the first-broken auto-selection override it in the same commit. The ListenerSets view and the Gateways list (when revealed from another view) now count a consumed reveal as their one-shot selection.

### 15.6 The lab, parity, smoke and captures (§10)

- **The lab** (`scratchpad/lab-s7`):
  - `crds.sh` applies Gateway API v1.6.3's experimental-channel CRDs from the module cache to kind-klyx, server-side. They were inspected first: CRDs only, no conversion webhooks, and every stored version (`referencegrants` v1beta1 among them) still served. The bundle's `safe-upgrades` ValidatingAdmissionPolicy is not applied: it refuses experimental CRDs over the lab's standard ones.
  - `apply.sh` writes the S7a fixtures in `klyx-gateway-lab` and `klyx-gateway-other`, with status through the status subresource under the stand-in controller `lab.klyx.dev/gateway-controller`:
    - `lab-gw` allows Same and its controller reports 2 attached;
    - ListenerSets created a second apart: `ls-first` (healthy), `ls-other` (another namespace, NotAllowed) and `ls-conflict` (Conflicted HostnameConflict);
    - `lab-gw-cert` has a listener with ResolvedRefs False InvalidCertificateRef.
- **Parity:**
  - `klyx-parity.sh` copies `listenersets` and now applies every copied CRD server-side: the 1.6 HTTPRoute CRD is 635 KB, over client-side apply's 256 KiB annotation.
  - `fixture.sh` adds an accepted ListenerSet under `parity-gw` (now `allowedListeners: Same`) and case 5d, a NotAllowed ListenerSet.
  - The Go oracle and the frontend stage read the new part.
- **Captures:** `scratchpad/cap-s7a`, night and day at 1440 and 1024: the Gateways view, `lab-gw-cert`, `lab-gw`'s listeners and ListenerSets, the ListenerSets view and each set's inspector. No sideways scroll, and every expected text present.
- **Not in S7a** (S7b, as staged): route kinds and attachments, ReferenceGrants, supported features, the experimental group, and the routes budget.
- **Noted for S7b:** v1.6.3 serves BackendTLSPolicy at v1 only, while the frontend's `POLICY_REFS` and the generic browser's registry still name v1alpha3.

### 15.7 The load test (§8)

`TestLoad`'s large cluster now carries 500 routes and 50 ListenerSets. Its Gateways and ListenerSets have listener status, and its namespaces have labels; each small cluster has 50 routes and 5 ListenerSets.

The same fixture was run on the code before S7a (b982518, where the ListenerSets sit unwatched in the fake API) and after, twice each, on an idle machine:

| | before | after |
|---|---|---|
| first complete summary, large | 1.075s | 1.073s |
| Observers' retained heap, steady | 60.8 MB, 60.8 MB | 61.0 MB, 61.0 MB |
| Observers' retained heap, peak in the recovery storm | 84.9 MB, 74.2 MB | 76.9 MB, 73.4 MB |
| full evaluation, large (max of 5) | 70.7 ms, 96.2 ms | 79.5 ms, 63.0 ms |
| every kind live after the API recovered | 8.48s, 8.51s | 8.59s, 8.49s |

S7a adds 0.2 MB of steady heap at this scale, and no change to full evaluation beyond the run-to-run spread. Full evaluation misses its 50 ms budget before and after alike: the open owner question, unchanged by S7a. The full-budget case is §15.4's.

### 15.8 Gates

At 60161d9, clean:
- `go test -race ./...`: pass.
- golangci-lint v2.14.0: 0 issues.
- vitest: 2,222 passed. tsc: clean. eslint: 0 errors (3 earlier warnings, in `LogsPane`).
- Parity: Go stage PASS, run 00e39b7890c5e18f with case 5d; frontend stage pass.
- Native smoke: 176/176 in each of night and day at 1440 and 1024, the labs re-applied first.
- Mutations: 12 caught of 12. They covered the exact namespace count, not-established read as not-found, older listener conditions judged, the order explaining a conflict, discovery's order in place of the candidates', Same allowing every namespace, labels from an incomplete read, a truncated index kind read live, newest-first precedence, a projection dropping listener status, a reveal overridden by auto-selection, and the listener column counting unaccepted ListenerSets.
- Captures: 32 clean (§15.6).

### 15.9 On a real cluster: AKS with Envoy Gateway

The owner authorised the AKS dev cluster `aks-dev-sc-b0b59a671015` on 2026-10-18 (Kubernetes 1.36.4, one 2-vCPU node, Azure CNI powered by Cilium).

**The baseline** (before anything was installed): the S7a build connected watch-backed, read 1/1 nodes, 17 pods and 4 namespaces, and named every tool that was not installed. It surfaced three AKS facts worth a later decision:
- `aks-managed-deny-endpointslice-wireserver-ip`, an AKS-managed ValidatingAdmissionPolicy, carries a CEL type-checking warning (it compares a list with `null`) on 1.36. Klyx reports it correctly ("may fail when it runs", look at), but every AKS 1.36 cluster will show it and the owner cannot change it.
- AKS's three control-plane webhooks are called by URL (`ccp-webhook...svc.cluster.local`), with no Service to check. They read unknown, which leaves Admission permanently unverified on AKS.
- The resource meters read "no Prometheus Service found": AKS's managed Prometheus is outside the cluster.

**Envoy Gateway v1.9.2** (its chart pulled and inspected first) installs Gateway API v1.6.1's experimental CRDs. The pod webhook is scoped to its own namespace with `failurePolicy: Ignore`. Envoy Gateway is installed as helm release `eg` in `envoy-gateway-system`.

**The fixtures** (`scratchpad/lab-aks-s7`, labelled `klyx.dev/lab=aks-s7`, with `teardown.sh`) mirror the kind lab with a real controller writing every status. A GatewayClass `klyx-eg` points at an EnvoyProxy that makes every proxy Service ClusterIP: no Azure load balancer or public IP. They are:
- `lab-gw`, allowing Same, with an echo backend and an HTTPRoute;
- ListenerSets `ls-first`, `ls-other` (another namespace) and `ls-conflict` (the same hostname and port as `ls-first`);
- `lab-gw-cert`, with a missing certificate.

**What the real controller wrote, and how Klyx read it:**
- `ls-other`: Accepted False NotAllowed. Klyx reads "not accepted: NotAllowed", and its own allowance reading (Same, another namespace) agrees.
- `ls-conflict`: Envoy Gateway does not accept the set at all. It reports Accepted and Programmed False ListenersNotValid, and the listener Accepted False, Conflicted True and Programmed False, all HostnameConflict with one message. Klyx reads a Fault "not accepted: ListenersNotValid". The stand-in in the kind lab had it accepted.
- `lab-gw`: attachedListenerSets 1. Klyx reads "3 found naming this Gateway, 1 accepted · the controller reports 1 attached".
- `lab-gw-cert`: Accepted True, Programmed False AddressNotAssigned (no proxy for a Gateway with no valid listener), and the listener ResolvedRefs False InvalidCertificateRef. Klyx reads "not programmed", and the listener's issue is named.
- Envoy Gateway stamps `observedGeneration` on every listener condition, so Klyx judges them.
- The GatewayClass reports no `supportedFeatures`: S7b's feature evidence will say nothing for Envoy Gateway 1.9.2.
- **One change from the real data:** Envoy Gateway writes the same message on three conditions of a conflicted listener, and the inspector repeated it three times. Failing conditions with one reason and message are now said once ("Accepted False, Conflicted True, Programmed False - HostnameConflict: ...").

Captures: `scratchpad/cap-aks0` (the baseline), `cap-aks-s7a` and `cap-aks-s7a-r1` (night 1440, day 1024).

## 16. Revision 1: Codex's implementation review of 0ad4ccc

Codex returned REQUEST CHANGES: one P1 and four P2s. It accepted every deviation in §15: the 64 KiB limits and budgets, typed projections, the partial index state, the exact namespace count, the kept last-observed Degraded, the lab without the safe-upgrades policy, and the one-shot reveal. It noted that 1.69 heap bytes per budget byte is a calibration for that fixture, not a universal bound: the fake client already holds the source strings.

| # | Finding | Answer |
|---|---|---|
| 1 (P1) | Listener condition messages reached the Gateway list, topology and ListenerSet row DTOs verbatim, and the projection used the credential heuristic, which keeps user info with an encoded separator (`alice:secret%40host`) | `gwapi.SafeMessage` applies the accepted URL rule (`urlscrub.Text`), then the heuristic. It runs at parse (`condOf`, so every Gateway API condition: Gateways, listeners, ListenerSets, route parents), in the projection before the clip, and again at `condDTO` and the listener DTO. Tests cover plain and encoded user info, serialized DTOs (built through the parser and directly), the verdict's reason, and a credential the clip would cut through. |
| 2 (P2) | Truncated live reads were treated as complete, and a stale ListenerSet read fed a live Gateway's counts | A truncated ListenerSet or Gateway read is current but not complete. Pins keep an evicted selection unverified (never removed); empty states say what the budget left out; counts carry the lower-bound mark; a capped notice names it. Merged listener counts come only from a current ListenerSets read, marked "or more" when truncated. The Gateway inspector dates a stale read and claims a difference only between complete, current readings. |
| 3 (P2) | `Capture.Complete` is true for a confirmed absence, so an unserved Gateways API made "parent not found"; a stale parent lent its old allowance to a disagreement | `Capture.Current` names the Live sources. `gwapi.ParentRead{Absent, Current, Complete}`: only a complete, current read of a served kind proves a parent missing; an absent kind leaves it not established; a parent found only in a stale read is found but not current, and establishes no allowance or disagreement. Namespace labels need a complete, current read. A parent found in a current but truncated read keeps its allowance: a retained object in a live model is updated by the watch, so its configuration is current. |
| 4 (P2) | A pinned old Gateway joined its replacement's new ListenerSets by name | A Gateway that is removed, replaced or unverified joins no live ListenerSet: its block says it is not the current one of its name. |
| 5 (P2) | The ListenerSet detail said "accepted and programmed" for pending or absent programming, and for a historical rejection | The detail follows the rule's order and covers programming absent and pending, and last-observed negatives ("Accepted was False for generation 1 ...; whether it is accepted now is unverified"). "Accepted and programmed" is said only when both conditions establish it. |

Codex's probes all pass on the revision: its three Go probes (`-overlay`) and its eight mounted UI cases. They are adopted as permanent tests, with sharper assertions.

**Gates at c7ee257, clean:**
- Go: `go test -race ./...` pass; golangci-lint 0 issues.
- Frontend: vitest 2,233 passed; tsc clean; eslint 0 errors (the 3 earlier warnings).
- Parity: Go stage PASS (run e7a771d9ff9780f9); frontend stage pass.
- Native smoke: 176/176 in each of night and day at 1440 and 1024.
- Mutations: 10 of 10 caught on the revision:
  - the heuristic without the URL rule;
  - clipping before scrubbing;
  - a DTO trusting its condition;
  - an absent kind proving a parent missing;
  - a stale parent setting the allowance;
  - a truncated read counted complete;
  - a replaced Gateway joining;
  - a stale read merging counts;
  - no grouping;
  - absent programming read as programmed.
- Captures: the kind lab (32) and AKS (16), clean.

## 17. Revision 2: Codex's re-review of dbba713

Codex resolved the message safety, the parent reads and the pins, and accepted keeping the allowance of a current parent found in a truncated read: it is a whole retained object, and namespace-selector proof stays gated. Two residual P2s:

| # | Finding | Answer |
|---|---|---|
| 1 | "All programmed" used the Gateway read's coverage without its truncation; a stale Gateway's `attachedListenerSets` disagreed with a current ListenerSets read | "All programmed" needs the truncation-aware complete read. A stale Gateway's count is said as last read ("the controller reported 0 attached when the Gateways were last read") and disagrees with nothing. A retained, current Gateway's count still stands beside other Gateways left out. |
| 2 | A current Programmed False beside an Accepted True from an older generation was explained as an unreported generation | The deciding condition's own freshness decides: "Accepted, but not programmed - Invalid. Its acceptance was last reported for generation 1." |

Codex's three new mounted probes pass unchanged and are adopted as tests. Three mutations, one per guard, are caught.

**Gates at c833250** (frontend-only), clean:
- vitest: 2,236 passed. tsc: clean. eslint: 0 errors.
- Native smoke: 176/176 in each of night and day at 1440 and 1024, the labs re-applied first.

**Accepted at 2d86b13** (Codex, implementation revision 2, handoff db4f5e4f, 2026-10-18). This accepts S7a; S7b keeps its own review and gates.

## 18. As built: S7b

Built on `feat/coverage` in six stages: cbd2923 (the model), ee8e6d1 (the evidence), 5a421a0 (the projections), 00cfce3 (the Observer's routes part), 0259398 (the on-demand topology) and a1c4682 (the lens). The gates, lab, parity, smoke and captures follow.

### 18.1 Routes, attachments and the controller (§5)

- **One model for the five kinds.** `gwapi.Route` reads HTTPRoute, GRPCRoute, TLSRoute, TCPRoute and UDPRoute alike: every parentRef with its defaults, every rule as returned (TLSRoute's alpha shapes kept), matches by kind (HTTP path and method, gRPC service and method), backends, and `status.parents` with their `controllerName`.
- **The attachment identity** is the normalised reference `group/kind/namespace/name#section:port`, with every field kept even when empty. The reduction's parent, the DTOs, pins and the topology's lane key all use it. The label is shorter: "infra/eg · https", "ListenerSet infra/extra · a:8443".
- **The expected controller** is the parent Gateway's class's `controllerName` (a ListenerSet goes through its Gateway). Only that controller's entry is judged. Other controllers' entries are shown as "an entry from another controller (‹name›), not judged". A class that is unread, stale, truncated or missing, or one with no `controllerName`, leaves the attachment Unknown: "the controller judging this parent is not established", with the reason.
- **Backends by port and transport.** A backend resolves through the one ServicePort whose number is the backendRef's and whose protocol (TCP when unset) is the route's transport: UDP for a UDPRoute, TCP for every other kind. Only the EndpointSlices carrying that port's name and protocol count. A Service without the port is the new `no-port` state, a Fault: "no UDP port 53", with "port 53 exists for TCP only" when another protocol has the number. The no-endpoints text names the port: "The backend Service has no endpoints for its UDP port 53."
- **Membership.** A route counts when it has a Gateway or ListenerSet parent, even a missing one: a Fault on a complete, current read, Unknown otherwise. A route whose only parents are kinds Klyx does not read (mesh, GAMMA) is not counted.
- **Fingerprints.** HTTPRoute's are unchanged, so measured traffic keeps joining. The other kinds hash their kind into each rule's fingerprint.

### 18.2 The evidence (§6)

Built as designed, evidence beside the verdict and never replacing it:
- **allowedRoutes per target listener.** Namespaces are Same, All or Selector; a selector is judged only on a complete, current namespaces read. Kinds come from `supportedKinds`, then the declared kinds, then the protocol's convention, which is said as a convention.
- **ReferenceGrants**, with non-inheritance (a Gateway's grant is not its ListenerSets'). A disagreement such as "no ReferenceGrant allows it" is said only on a complete, current, untruncated read.
- **Declared features**, when the class lists any.

On the lab, the evidence reads like this:
- "listener web: routes from klyx-gateway-lab only (Same); this one is in klyx-gateway-other";
- "Service klyx-gateway-other/other-db from HTTPRoute klyx-gateway-lab: no ReferenceGrant in klyx-gateway-other allows it";
- "the class's controller does not declare support for UDPRoute".

### 18.3 The experimental group (§7)

Capability detection reads the resources of every served `gateway.networking.x-k8s.io` version and lists their kinds, deduplicated and sorted, with subresources left out. A version that cannot be read is a named gap, and the list may then be partial. The Gateways view says it in one neutral line: "Experimental Gateway API kinds served: XBackend, XBackendTrafficPolicy, XMesh; Klyx does not read them." An XListenerSet adds its promotion to ListenerSet. Nothing in the group is watched or judged.

### 18.4 Projections, the budget and the heap (§8)

- **Projections and limits.** A route keeps its parentRefs, backendRefs and `status.parents` whole. It caps hostnames at 16 and each rule's matches at 8 for display; the counts and fingerprints are taken over the whole object. Condition messages are made safe, then clipped at 512 bytes. The limits are as designed: 16 KiB for HTTP, gRPC and TLS routes, 8 KiB for TCP and UDP routes, 4 KiB for a ReferenceGrant. A 16-rule HTTPRoute with 4 backends and 8 matches a rule, and 4 parents, measures 15.4 KiB.
- **The full-budget case** (`TestGatewayAPIObserverHeap`, extended) fills every budgeted kind past its budget: the S7a kinds and namespaces, plus 1,500 HTTPRoutes, 400 GRPCRoutes, 400 TLSRoutes, 300 TCPRoutes, 300 UDPRoutes (four rules of two backends each) and 800 ReferenceGrants. The route kinds retain 723, 203, 256, 132 and 132 routes, and 333 grants are kept. The running Observer holds:

  | Heap held | Against | Heap bytes per budget byte |
  |---|---|---|
  | 8.11 MiB with the Routes view closed | 3.69 MiB of encoded budget | 2.20 |
  | 11.31 MiB with the routes rows subscribed | the same | 3.07 |

  S7a's kinds alone were 1.69. The typed route keeps its parsed rules and per-backend state, which cost more per encoded byte than a Gateway's listeners. The 3.2 MiB of route rows are held only while the Routes view is open. These are measurements of this full-budget fixture's shapes (four-rule routes of two backends), not a bound: other shapes at the same encoded budget hold more or less heap.

### 18.5 The on-demand topology (§9)

- **One lane per attachment.** `GetGatewayTopology` reads all five served route kinds and draws one lane per attachment landing on the Gateway, directly or through one of its ListenerSets. Each lane carries:
  - the attachment's verdict, the controllers and the evidence;
  - the rule names;
  - the version the route was read at, so "Open TLSRoute" opens the served version.
- **Live backends.** Backends resolve live by port and transport, and a Service without the port is `no-port`.
- **What the routes claim.** A failed route-kind list leaves the routes incomplete and names the kind. So do unreadable ListenerSets when a route attaches through one: whether it lands here is unknown, never silently short. A failed GatewayClass or ReferenceGrant read warns, and its lanes' controllers or grants are then not established.
- **Policies.** A policy's route target matches only the lane of its own kind; Envoy Gateway's policies target every route kind.
- **Two bridge fixes.** gRPC matches reach the rules' own match lists, and each backend keeps its transport in the DTO. Both had been set before the lists they belonged to were built; a new bridge test holds them.

### 18.6 Views (§9)

- **Routes view.**
  - Identity is `kind/namespace/name`, so an HTTPRoute and a TLSRoute of one name are two rows.
  - It has a Kind column and a kind filter. The worst parent shows by its label, with the identity in its tooltip.
  - Each attachment shows the controller judging it (or why none is established), other controllers' entries and the evidence.
  - A reveal names its kind by resource.
  - Open topology goes to the Gateway the worst attachment lands on and selects that attachment's lane.
- **Topology.**
  - Lanes are keyed by route and attachment, and a kind filter appears when more than one kind is present.
  - The header counts routes, and attachments when they differ ("2 routes · 3 attachments").
  - A route identity from the Routes view or the palette resolves to that route's worst lane.
  - Each lane says its kind, where it attaches and what it matches.
  - Through a ListenerSet, the Gateway's listeners are not the section's evidence: no "listener missing", and Listener TLS points to the ListenerSet.
- **Data path.** Each rule says what it matches in its kind's words:
  - "every request" or method and path;
  - "pkg.Catalog/Get" or "every call";
  - "SNI shop.example" or "any SNI";
  - "every connection on pg :5432".

  L4 rules have no match step.
- **Metrics.** HTTP request metrics are read and shown for HTTPRoute lanes only. Every other kind says "HTTP request metrics do not apply to ‹kind›" in the lane, the data path and the inspector, and the topology does not request the route-level read for it.
- **Elsewhere.**
  - The palette lists one command per lane.
  - ⌘K's index and the console open GRPCRoutes, TLSRoutes, TCPRoutes, UDPRoutes and ListenerSets in the lens.
  - Describe, the registry and resource refs know the L4 kinds.
  - BackendTLSPolicy opens at v1: Gateway API 1.4 made it GA, and 1.6 serves nothing else.
- **What is not done.** Registry entries still pin one version per kind. A cluster serving only an older version of a kind (say TLSRoute v1alpha2) reads it as not served in the generic browser. Resolving served versions there is a cross-cutting change, left out of S7b.

### 18.7 The lab, parity, smoke and captures (§10)

- **The kind lab** (`lab-s7/apply-s7b.sh`) uses the stand-in controller and selectorless Services with hand-written EndpointSlices. Its fixtures:
  - one Gateway with HTTP, TLS passthrough, TCP and UDP listeners;
  - an HTTPRoute and a TLSRoute named `shop`, and a GRPCRoute;
  - two TCPRoutes, one to a port the Service lacks;
  - a UDPRoute whose Service serves TCP/53 and UDP/53 with ready endpoints on TCP only, under a class that omits UDPRoute;
  - cross-namespace backends with and without their grant;
  - a route from a namespace the listener does not allow;
  - a route with two parents.

  Gateway API 1.6.3's experimental CRDs were already installed by S7a, including XMesh.
- **Parity** copies the TLS, TCP and UDP route CRDs. parity-gw gains a TCP listener and a healthy TCPRoute. Case 5e adds a TCPRoute whose backend has no TCP port 6432, a counted fault. The oracle's routes part counts all five kinds. **The gate passes**: the Go stage in 143s, then the frontend stage's two tests.
- **Native smoke** gains two steps: the Routes view's kind filter keeps the TCPRoutes, and a TCPRoute's topology lane draws a data path whose rule reads "every connection on pg :5432", with no match step and no HTTP metrics.
- **Captures** (`pw/s7b.mjs`), night and day at 1440 and 1024, 13 each, every one with its expected text and no horizontal scroll. They cover:
  - the experimental line;
  - the Routes view and its TCP filter;
  - the UDP, denied-grant, not-allowed and two-parent inspectors;
  - lab-gw-l4's topology, with the pg-old, dns, TLS and gRPC inspectors and the kind filter.

### 18.8 The load test (§8)

`TestLoad`'s routes are now mixed: four in ten are gRPC, TLS, TCP or UDP routes, one kind each, and each cluster has ReferenceGrants. The same fixture was run on the S7a code (2d86b13) and on S7b, four times each, on an idle machine:

| | before (S7a) | after (S7b) |
|---|---|---|
| first complete summary, large | 1.07-1.08s | 1.07s |
| Observers' retained heap, steady | 59.9 MB | 59.5-59.6 MB |
| recovery peak / steady | 1.37x, 1.56x, 1.25x, 1.40x | 1.61x, 1.34x, 1.42x, 1.21x |
| full evaluation, large (max of 5) | 79.5-87.9 ms | 69.4-93.9 ms |
| every kind live after the API recovered | 8.21-8.54s | 8.56-8.72s |

- **No regression.** S7b changes neither the steady heap nor full evaluation beyond the run-to-run spread.
- **Recovery peak.** Its 1.5x budget is missed once in four runs on both sides: GC pacing, not S7b.
- **Full evaluation** misses 50 ms before and after alike; it remains the open owner question.

### 18.9 On a real cluster: AKS with Envoy Gateway

`lab-aks-s7/apply-s7b.sh` (authorised 2026-10-18, ClusterIP only, labelled `aks-s7` and in the lab namespaces, so `teardown.sh` removes it) puts the S7b kinds behind Envoy Gateway v1.9.2. Envoy Gateway's own statuses confirm the design:
- **It selects ports by protocol too.** A UDPRoute to a TCP-only Service reads ResolvedRefs False PortNotFound "UDP Port 53 not found on Service klyx-gw-lab/dns-tcponly". A TCPRoute to a missing port reads "TCP Port 6432 not found".
- **The denied cross-namespace backend** reads ResolvedRefs False RefNotPermitted.
- **The route from another namespace** reads Accepted False NotAllowedByListeners.
- **Listener kinds.** Each listener reports `supportedKinds` by protocol: HTTPRoute and GRPCRoute on HTTP, and one kind each on TLS, TCP and UDP.

Klyx against these statuses, as captured (`pw/s7b-aks.mjs`): 48 captures, night and day at 1440 and 1024, each with its expected text and no horizontal scroll.
- **The Routes view** counts 11 routes across all five kinds.
- **dns-tcponly's inspector** shows the controller's verdict and Klyx's own check side by side, and they agree: "References not resolved - PortNotFound: UDP Port 53 not found on Service klyx-gw-lab/dns-tcponly" and "Backend Service klyx-gw-lab/dns-tcponly has no UDP port 53 - port 53 exists for TCP only".
- **The data path** reads "every connection on dns :53" and draws the failing backend step "no UDP port 53".

**Gates at 4bc0632:**
- `go test -race ./internal/...`: pass. golangci-lint: 0 issues.
- vitest: 2,256 passed. tsc: clean. eslint: 0 errors.
- Parity: pass, with case 5e.
- Native smoke: 181/181 in each of night and day at 1440 and 1024, the labs re-applied first.
- Mutations: 5 Go and 10 frontend, all caught.
- Captures: 52 on the kind lab and 48 on AKS, all clean.

## 19. Revision 1: Codex's implementation review of c72224e

Five P2 findings, each with a reproduction; every probe is adopted as a test.

| # | Finding | Answer |
|---|---|---|
| 1 | Retained stale dependencies were used as current proof: a stale Gateway, GatewayClass or ListenerSet established the expected controller and its status was judged; a stale Service's port resolved a healthy backend; a stale grant read "allowed" and then disagreed with a current RefNotPermitted | `AttachmentParent.Current`: a parent established through a read that is not current (the Gateways read; for a ListenerSet the ListenerSets read too) is last observed. It establishes no controller ("the Gateways read is not current: Gateway infra/eg is last observed"), no allowedRoutes or feature evidence, and a stale ListenerSet's own refusal is not a Fault. A class from a read that is not current establishes no controller and no features. A stale Service is "unreadable: the Services read is not current". A grant from a stale read is "allowed it when last read", never "yes", so nothing disagrees. A current object in a capped read is still whole and counts. |
| 2 | The Routes view ignored the route kinds' budget: a wholly omitted kind read "No routes attached", a route left out read "No longer attached ... both read in full", reveal likewise, counts unqualified | A truncated route kind makes the membership incomplete: counts carry "+", a cap notice names the kinds and how many were left out ("Capped: 1 HTTPRoute not retained ... a route left out is unverified, never detached"), an empty capped read is "No routes kept within Klyx's budget.", a pinned route left out is unverified ("Klyx's budget left routes out of the read"), and a reveal into a capped read says "not among the routes Klyx keeps within its budget - unverified". |
| 3 | An explicit lane whose attachment disappeared moved to another attachment of the route | A lane key resolves only to itself; only a bare route identity resolves to its worst lane. The missing attachment keeps its last evidence and reads "No longer attached". |
| 4 | Port-only parentRefs scoped the inspector and data path to every listener | `targetListeners` filters by the reference's section and port, each when set, as the Go model does (none through a ListenerSet). |
| 5 | The allowed-kinds check compared kinds without their group | Listener kinds are group-qualified (an empty group is the Gateway API's); a foreign group's HTTPRoute admits nothing of the Gateway API, and the explanation names it ("example.test/HTTPRoute"). |

Codex's judgements, adopted: a bare route identity resolving to its worst lane is sound; on-demand class and grant failures as warnings are sound; ListenerSet-only listener evidence is sound; the registry's single version per kind stays a disclosed limitation. Section 18.4's heap figures are now qualified as this fixture's measurements.

Mutations, one per fix: 8 Go (stale Gateway, class, ListenerSet, ListenerSet refusal, Service, grant, allowance; the kind group) and 3 frontend (explicit lane, port scope, capped membership), all caught.

## 20. Revision 2: Codex's re-review of 59dbe25

Codex verified the five findings resolved, and confirmed with a new probe that a current object retained in a capped read still proves its facts. Two residual P2s; both probes are adopted as tests:

| # | Finding | Answer |
|---|---|---|
| 1 | The combined parent-currentness gate hid a ListenerSet's own fresh refusal when only its Gateway's read was stale or capped | `AttachmentParent.ListenerSetCurrent` is the ListenerSets read's own currentness. A ListenerSet's refusal is judged on it alone; the chain's `Current` still gates the controller, allowance and feature joins. |
| 2 | An explicit `group: ""` (the core group) collapsed into the omitted default (the Gateway API's) | `parseRouteKinds` applies the default only when the key is absent, so presence is kept at parsing; `containsKind` compares the group as parsed. The explanation says `core/HTTPRoute`, and the Gateway inspector's listener kinds name a foreign or core group the same way. |

Mutations: the combined gate, no ListenerSet currentness, the empty group defaulted and the frontend kind text are all caught.

## 21. Revision 3: Codex's re-review of e29383a

Codex verified both residuals resolved. One more P2: `ListenerSetCurrent` was set only when the ListenerSet's own parent reference named a Gateway, so a current ListenerSet whose parent names another kind or group lost its own fresh refusal. It is now set whenever the ListenerSet is found, from the ListenerSets read alone. The stale-read reason follows it, and the chain's `Current`, controller and allowance gates are unchanged. Codex's probe is adopted (`routes_current_r2_test.go`), and the mutation that ties the flag to the parent's kind is caught. Go only: race and lint are clean. The smoke is not re-run, since the lab has no such ListenerSet and the frontend is unchanged.
