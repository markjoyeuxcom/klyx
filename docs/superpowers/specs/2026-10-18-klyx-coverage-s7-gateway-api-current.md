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
