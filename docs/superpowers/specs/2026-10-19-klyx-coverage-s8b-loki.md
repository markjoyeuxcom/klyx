# Coverage S8b: log history, from Loki

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S8b. It follows C5 (`2026-10-18-klyx-coverage-c5-external-credentials.md`) and S8a (`2026-10-19-klyx-coverage-s8a-alertmanager.md`), whose transport, `match`, `serviceRef.path` and source identity it reuses. Revision 1 answers Codex's design review of 6438d43 (§14); revision 2 its re-review of c2b4e26 (§15). **Design accepted by Codex at b4218c1** (handoff 8063ebd0). **Implementation accepted by Codex at 94be5c2** (handoff aecfe757), with no remaining findings, after three revisions (§17-§19).

The roadmap's contract:
- **History for a pod beyond its previous container,** gone pods included, over a selectable time range, from the logs pane.
- **Klyx builds the LogQL from the object;** the person never types it, and the selector used is shown.
- **Without pod-UID metadata,** Klyx says which weaker selector it used, and never claims the logs belong only to that pod.

The S8 external-evidence contract applies too: identity by configured labels, no manufactured recovery, and bounded reads that never read complete when they are not.

The facts below come from Loki 3.7.8 in the S8b lab (§10; `lab-s8b/FINDINGS.md`) and from the pinned shippers' code: k8s-monitoring 4.5.3, and Loki's OTLP ingestion.

## 1. What is there today (feat/coverage-s8 after S8a)

- **The live logs path** is the kube API only.
  - `fleet.PodLogStream` opens a follow, or reads the previous container once, with `TailLines` up to 5,000 and no `SinceTime` or `Timestamps`.
  - `LogsService` streams it in batches.
  - `LogsPane` holds at most 10,000 lines and renders 2,000.
  - Its controls are the container, tail, previous, follow, wrap, level filters, search, clear and copy. It has no time control.
- **A gone pod has no logs.**
  - The live stream ends.
  - A reopen gets the API server's not-found.
  - A same-name replacement is silently read by name.
  - The removed-pod inspector disables its Logs action.
- **The `loki` endpoint is configured but never read.** It came with C5: `endpoint`, `serviceRef` (with S8a's `path`), `tenant`, `auth`, and S8a's `match`.
  - Settings shows its credential state, with Test "not implemented until S8".
  - `fleet` holds no Loki state.
- **`internal/endpointhttp`** carries C5's direct and proxied transports with per-kind limits. Every read buffers one capped body.
- **What Klyx keeps about a gone pod:**
  - the removed-pod inspector's last row: UID, namespace, name, node, created time, owner, containers;
  - `PinnedGone` (from Needs you, notifications, alerts or the console): UID, namespace and name.

## 2. Configuration: which Loki, which lines

The `loki` block keeps its shape: `endpoint` | `serviceRef` (with `path`), `tlsSkipVerify`, `auth`, `tenant`, `match`.

- **`match`** is the index labels that make a stream this cluster's, e.g. `{cluster: aks-prd-weu}`, or `{k8s_cluster_name: aks-prd-weu}` for OTLP. Its pairs open every stream selector Klyx builds.
- **A `match` key must be an index label.** Structured metadata cannot select streams, and a selector on a label no stream carries selects nothing.
  - When the labels read in `match` (§3) comes back empty, Klyx makes one more labels read without a query: the tenant's index labels in the range.
  - That second read tells two cases apart:
    - "match names cluster, which is not an index label on tenant T in the range";
    - "no streams match cluster="X" on tenant T in the range".
  - Neither is ever read as "no logs" for a pod.
- **Without `match`,** every stream in the tenant is read as this cluster's, and the selector line says so. Two clusters on one Loki without `match` already get S8a's load warning.
- **Resolution:** an `endpoint`, else a `serviceRef` through the API server's service proxy. The proxy forwards `X-Scope-OrgID` (lab finding 7).
- **No discovery in S8b.**
  - The common installations (the grafana/loki chart, a shared Mimir/LGTM Loki outside the cluster) have no resource that proves an installation.
  - The Loki Operator's `LokiStack` serves through an authenticating gateway with its own tenancy modes.
  - A Service name or label is not proof (S8a §2). Discovery stays out until a proof exists.
- **The capability `logHistory`** is present when `loki` is configured. It is carried on the cluster DTO (as S8a's `alertsPresent`). History is offered only then.

## 3. The shape: reading the labels before building a selector

The three common shippers label the same pod differently (lab finding 1):

| shape | namespace | pod | container | pod UID |
|---|---|---|---|---|
| Kubernetes SD (Promtail style) | `namespace` (index) | `pod` (index) | `container` (index) | none |
| k8s-monitoring 4.5.3 (Alloy) | `namespace` (index) | `pod` (structured metadata) | `container` (index) | none |
| OTLP (Loki's defaults) | `k8s_namespace_name` (index) | `k8s_pod_name` (index; Loki recommends demoting it) | `k8s_container_name` (index) | `k8s_pod_uid` (structured metadata) |

Klyx never assumes a shape, and never assumes one shape for all of a cluster's streams. A cluster can ship two ways under one `match`. Codex's review of 6438d43 showed, and the lab confirmed, that a selector chosen from a label union drops streams: with SD and Alloy under one `cluster`, `pod` is in the union because SD indexes it, and `{…, pod="P"}` drops every Alloy stream (lab finding 10).

**The labels read only prunes.** `/loki/api/v1/labels` with `query={match}` (no query without `match`) over the read's range lists the label names of this cluster's streams.
- Loki may consult a wider span than asked, and merges names across streams. The list is a superset for the range, never proof of what any one stream carries.
- Klyx uses it only to skip branches whose label no stream could carry, and to say why nothing was found:
  - `{"status":"success"}` with no `data` (an unknown tenant reads the same, lab finding 7) gets one more labels read without a query. That tells "match names cluster, which is not an index label on tenant T" from "no streams match cluster="X" on tenant T".
  - Neither is "no logs for this pod".
- **Neither namespace spelling in the list:** history is not offered for this Loki, with "no namespace label in this cluster's streams (looked for k8s_namespace_name, namespace)". A selector of `match` alone would select the whole cluster.

**Disjoint branches select every stream exactly once.** An empty matcher selects streams without that label (lab finding 10). For each namespace spelling in the list, at most three pod branches:

| branch | stream selector | filter |
|---|---|---|
| pod indexed | `{M, NS="N", pod="P"}` | |
| k8s_pod_name indexed | `{M, NS="N", k8s_pod_name="P", pod=""}` | |
| neither indexed | `{M, NS="N", pod="", k8s_pod_name=""}` | `\| pod="P" or k8s_pod_name="P"` |

- **Namespaces are disjoint too.** When both spellings are listed, the `k8s_namespace_name` branches add `namespace=""`, so a stream carrying both belongs to the `namespace` branches only.
- **Pruning:**
  - The indexed branches run only when their label is listed.
  - The "neither" branch always runs, since structured metadata cannot be listed without scanning lines. Where every stream indexes its pod, its selector selects nothing, at no line cost.
  - That is at most six branches, typically two.
- **A chosen container** is a label filter on every branch: `| container="C" or k8s_container_name="C"`. Label filters read index labels and structured metadata alike (lab finding 10), so one form serves every shape. Naming both spellings never widens the selection (lab finding 9).
- **The UID** is likewise a label filter on every branch: `| k8s_pod_uid="U"`, whether the shipper indexed it or not.
- **Values** are quoted with Go's `strconv.Quote`.

## 4. The pod's identity: UID when the lines prove it, else a stated weaker selector

**A page set** is the lines one history view reads, from the newest back.
- Its first page reads every branch by name, with `X-Loki-Response-Encoding-Flags: categorize-labels`, so each entry carries its structured metadata apart from its stream (lab finding 2).
- The merged entries decide the mode, once.

**The decision:**
- **Every entry of the first page carries `k8s_pod_uid`:** the page set is in **UID mode**. The first page is re-read with the UID filter on every branch, one more read set, only then. Its lines are said to be this pod's: "selected by this pod's UID (k8s_pod_uid); lines without a pod UID are not shown".
  - Generations of one name separate: history-db-0's gen1 and gen2 read apart under the UID, where the name read mixes them (lab finding 3).
- **Some carry it or none do:** the page set is in **name mode**, said with its bound. Name mode never adds a UID filter, even where the shipper indexed it.
- **"Select by name instead"** (from UID mode) and "Select by UID" (when the first page's entries all carry one) are the person's explicit choices. Each starts a new page set; it is never a silent fallback.

**The mode is locked for the page set.**
- It is bound into the page set's cursor with:
  - the branches;
  - the absolute range (its end fixed at the first read);
  - the source's lineage key and endpoint revision (§6);
  - the pod UID and the container.
- Load older keeps all of them.
  - An older page of UID-less lines never turns a UID view into a name view, nor the reverse.
  - Under UID mode those lines are filtered out, as the sentence says.
- Any change of mode, range, container or source starts a new page set from the newest. Refresh does too.

**The name sentence is always the weaker one, even for a live pod.**
- The creation bound filters by the timestamps the lines report.
- The application's, the shipper's and the cluster's clocks can disagree, so a timestamp bound narrows but never proves identity.
- The sentences:
  - **With a created time** (the row's, or the removed inspector's): "selected by namespace and name, lines timestamped from this pod's creation; another pod with this name in that time would match too". For a gone pod the end is open: "a later pod with this name would match after this one was removed".
  - **Without one** (`PinnedGone`): "selected by namespace and name only: every pod with this name in the range".
- **Some carry a UID:** "some lines carry a pod UID, some do not; selected by name".
- **No entry at all:** "no lines for this selector in the range", with the selector shown. Absence is never evidence that the pod logged nothing: the shipper may not have collected it, or retention may have passed.

**The pod's UID is the one Klyx holds:**
- the live pod's;
- or the removed inspector's last row;
- or `PinnedGone`'s UID.

A history view is UID-pinned in the UI, as every inspector is. A same-name replacement's live logs never answer a gone pod's history.

## 5. The read

- **The range:**
  - presets of 1 hour, 6 hours, 24 hours and 7 days, plus "since this pod was created" when the created time is known;
  - the default is since creation, clipped to 7 days, else 24 hours;
  - there is no custom range picker in S8b;
  - the range is absolute once a page set starts (§4).
- **The cost.**
  - The indexed branches select the pod's streams only.
  - The "neither indexed" branch makes Loki read the lines of the namespace's streams that index no pod, to filter them. For the k8s-monitoring default, that is every stream of the namespace.
  - Klyx defaults to the pod's lifetime. A Loki timeout or limit is shown in Loki's words, with "narrow the range or choose a container".
- **A page across branches:**
  - Each active branch is read backward:
    - `limit` 1,000 (Loki's default maximum is 5,000; lab finding 5);
    - `start` and the branch's own `end`, in nanoseconds;
    - categorize-labels.
  - Entries already delivered in this page set drop out first, by their dedupe key: branch, stream labels, structured metadata, timestamp and line, all raw.
  - Then the results merge newest first, and the newest 1,000 overall are kept. Removing the overlap before the cut matters: 1,000 held boundary entries plus 200 recovered older ones add those 200.
  - Each branch's `end` moves to the oldest timestamp it contributed to the kept lines, plus one nanosecond (`end` is exclusive; lab finding 6).
- **Exhaustion is read from the raw response,** never from what remains after the dedupe.
  - A branch is exhausted when its raw response held fewer entries than its limit and every one is now delivered: kept on this page, or already delivered earlier in the page set.
  - "Start of the range reached" is said only when every branch is exhausted.
- **Saturation** (lab finding 11): a branch's raw response is full, and holds no entry older than the boundary timestamp T, so it cannot advance.
  - Klyx re-reads that branch once with `limit` 5,000.
  - If it is still saturated, or the server refuses the limit (its 400, in its words), the branch stops with a marker among the lines: "at least N lines share the timestamp T on this selector; Klyx cannot establish whether more remain". N is the count read; a refused retry proves no more.
  - The person may choose "Skip past T" (`end` = T). That leaves those lines out, and the marker says so.
  - Never a silent skip, an endless repeat, or "start of the range reached".
- **What a view holds:** at most 10,000 lines and 32 MiB of line text.
  - Load older at the cap evicts from the newest end and keeps the cursor, so older pages stay reachable.
  - The view says "newer lines dropped (N held); Refresh returns to the newest".
- **Rendering:**
  - History renders every held line through a virtualized list (`chrome/VirtualList`), not Live's newest-2,000 window.
  - Load older keeps the scroll anchor on the first line held before it.
  - Lines show with their timestamp (RFC 3339, milliseconds), so `logline.ts` dims it.
  - Streams merge by timestamp; equal timestamps keep a stable branch and stream order.
  - A line over 64 KiB is clipped and marked.
  - A container column shows when no container is chosen and the streams name several.
- **Non-2xx responses:**
  - A 400 (Loki's limits) is shown with Loki's own text, as a display value (lab finding 5: the body is plain text).
  - A 401 without a tenant reads "Loki requires a tenant; set loki.tenant".
  - Any other status names it.
- **The response is parsed strictly.**
  - `status` must be `success`.
  - `resultType` must be `streams`.
  - Each value must be `[ns-string, line, {structuredMetadata?, parsed?}]`, or the two-element form when the server ignored the flag. Without the flag's echo (`encodingFlags`), structured metadata is merged into the stream labels, and Klyx reads the UID from there.
  - Anything else fails the page whole, and `null` is not an empty list (S8a revision 1).

**Text and the credential boundary:**
- **Raw values stay in process.** Raw queries, label values and identities serve the HTTP request, the matching, the UID classification, the order and the dedupe, all before any redaction or clip.
- **Log content** (lines, stream labels, structured metadata) stays honest and unscrubbed, as C5 holds for the operator's own log view. It passes only the endpoint's own credential through an exact-only redactor (`Redactor.Exact`): its exact and quoted spellings, never the general credential shapes, which would alter honest log text.
- **Everything else Klyx says** goes out as a display value with the read's redactor (S8a's `Text`: the exact spellings and the general shapes), clipped:
  - the selector line, the source, the tenant and the strength sentence;
  - the errors and Settings' text.
- **Copy LogQL** copies the query only when its display form equals the raw query.
  - Otherwise it is unavailable: "the query names a value equal to the endpoint's credential; Klyx does not copy it".
  - Klyx never offers a redacted query as the executable one, and never puts the credential on the clipboard.
- **No line goes to AI in S8b:** chat's `get_pod_logs` stays on the kube API.

## 6. The source contract

- **History is on demand, not an Observer snapshot.** There is no polling, no part and no counting.
- **Each page set carries the source it resolved:**
  - its identity (how | location | tenant | sorted match, S8a §4), kept in process;
  - its lineage key (the identity, hashed, as S8a's `SourceKey`);
  - its endpoint revision.
  - The view shows the source line as a display value.
- **Load older** sends the cursor back.
  - A lineage key or revision that no longer matches the connection's ends the page set: "the source changed; reading the newest again". It never mixes two sources' lines.
  - The credential fence is C5's: a read across a revision fails.
- **Settings' Test** reads `/loki/api/v1/status/buildinfo` (no tenant needed), then a labels read in this cluster's `match` over the last hour. It reports, as display values:
  - "Available: Loki 3.7.8 at <source>; this cluster's streams carry the namespace label k8s_namespace_name"; or
  - "answered, but no streams match this cluster's match in the last hour on tenant T (check tenant and match)".
  - It never reads lines.

## 7. What the operator sees

- **The logs pane gains a mode: Live (the kube API, as today) | History (Loki).**
  - History is offered when `logHistory` is present.
  - The range sits beside the container, and the selector line sits above the lines. It shows the branch count when there is more than one ("2 selectors"), each expandable, for example:
    - `{k8s_cluster_name="kind-klyx", k8s_namespace_name="klyx-s8b-lab", k8s_pod_name="history-db-0", pod=""} | k8s_pod_uid="1b28…"` · selected by this pod's UID · Service klyx-s8b-lab/loki:http · tenant lab
  - Copy LogQL copies each branch's query (§5's boundary).
  - Follow, previous and tail are Live's controls. History has the range, Load older, Refresh (a new page set from the newest), and the mode choice of §4.
- **Gone pods:**
  - The removed-pod inspector's Logs action becomes "Log history" when `logHistory` is present. It opens History with the last row's UID, namespace, name, created time and containers.
  - `PinnedGone` gains the same action, with "all containers" and no created time (§4).
  - A replaced pod's history opens the gone one's UID, never the replacement's.
- **The pop-out window** carries History as the pane does.
- **The words:**
  - "history from Loki";
  - the strength sentence of §4;
  - "start of the range reached", "Load older", or the saturation marker;
  - the cap notice;
  - a failed read in its own words, with the last pages kept and marked "not refreshed".

## 8. Bounds

- **Per response:** 8 MiB (endpointhttp's cap, Loki's kind limit). A page over it fails with "the Loki response exceeded 8 MiB; narrow the range or choose a container".
- **Per page:** 1,000 entries kept, from at most six branch reads (typically two), plus one 5,000-entry read per saturated branch.
- **Per view:** 10,000 lines and 32 MiB of held line text.
- **Time:** a 30-second deadline per page. The range is at most 7 days in the UI (Loki's default maximum is 30 days 1 hour).
- **Concurrency:** one page per view at a time (a new request cancels the old), at most two per cluster and four fleet-wide. A page's branch reads share its slot.
- **Memory, measured on full shapes:**
  - the heap a page retains, at six branches of 8 MiB each and with redaction expansion;
  - the bytes of its DTO;
  - a view at both caps.
  - Measured as S8a's budget test was.
- **The labels read** adds one index request per page set, two when it comes back empty, never one per line.

## 9. The wiring

- **`internal/endpointhttp`, a small extension:** C5's redirect, cap-below-decoding and revision tests stay, with new ones for each addition.
  - **Request headers on both paths** (`X-Loki-Response-Encoding-Flags`). Today `Get` takes none.
  - **A raw proxied read** returning the status and the bounded body for any status, as the direct path does. Loki's 400 diagnosis is that body.
    - **client-go cannot carry it** (Codex's re-review, pinned v0.36.2 `rest/request.go`):
      - `Do(ctx)` passes a non-2xx through `transformResponse`. When the negotiated serializer cannot decode `text/plain`, it returns only the transformed error: the body is gone before `Result.Raw`.
      - `DoRaw(ctx)` keeps the body, but its non-2xx branch overwrites a body-read error with a status error, losing C5's cap sentinel.
    - **So the raw read executes through the guarded `*http.Client` itself.**
      - `NewGuardedProxyREST` builds that client (the kube credentials and TLS from `rest.HTTPClientFor`, `SameOriginRedirect`, `safehttp.Cap`). It keeps it beside the REST client.
      - The REST client only builds the proxy URL: `Get().AbsPath(...).Param(...).URL()`, which runs nothing.
      - Klyx sends the request with its headers and reads the body through `ReadCapped` first. A body-read, cap or cancellation error takes precedence over the status. Only a complete body is paired with its status, 2xx or not.
      - The C5 credential stack and redirect guard are the same client's. No endpoint credential is ever sent on this path (C5).
    - **Codex's two probes are the transport contract:** a plain-text 400 body arrives whole, and the cap sentinel survives a 400.
- **`safehttp.Redactor.Exact`:** the exact and quoted spellings only, for log content (§5).
- **`internal/loki` (pure):**
  - `Branches` from the labels read (§3);
  - `Query` (each branch's LogQL in name or UID mode, with the container filter), in raw and display forms;
  - `ParseStreams` (categorize-labels, and the two-element fallback);
  - `Page` (the k-way merge, per-branch cursors, the dedupe, exhaustion and saturation);
  - `Mode` (the §4 decision from a first page's entries);
  - the cursor (mode, branches, absolute range, lineage key, revision, UID, container; opaque to the UI).
- **`fleet` `lokiState`:**
  - resolves the endpoint or the service proxy with `endpointAuth` and Loki limits;
  - holds the source identity;
  - limits reads;
  - `ReadLogHistory(ctx, req)` returns one page with its selector, strength, source line, range and cursor;
  - `LokiStatus(ctx)` serves Settings.
- **`appbridge` `LogHistoryService`:**
  - `ReadPodLogHistory(cluster, podRef{uid, ns, name, created, container}, range, mode, cursor)` returns a `LogHistoryPageDTO`. It carries:
    - lines with timestamp, container and stream (display values);
    - the selector line (one per branch) and the strength sentence;
    - the source;
    - Copy LogQL's availability;
    - a next cursor or "start of the range reached";
    - any saturation markers, and an error;
  - it is request/response, not an event stream: a page is bounded and small.
- **`EndpointsService`:** Test for Loki, Set through the connection (as S8a), and `logHistory` on the cluster DTO.
- **The frontend:**
  - `LogsPane` gains its mode, the range, the selector line, Load older, Refresh and the mode choice. History renders through `VirtualList`, holding at most 10,000 lines and 32 MiB, and evicts from the newest end;
  - the removed-pod inspector and `PinnedGone` gain "Log history";
  - Settings' Loki Test.

## 10. The lab (built; `lab-s8b/`)

On `kind-klyx`, in `klyx-s8b-lab`:
- **Setup:** ClusterIP only, labelled `klyx.dev/lab=s8b`, with a teardown script.
- **Loki 3.7.8** single binary: multi-tenant, tenant `lab`, an emptyDir store re-created on every apply, so lines are always written relative to now.
- **A curl pod** pushes the lines (lab setup through Loki's own API; Klyx never writes to Loki).
- **Pod identities:** `history-db-0` in two generations, a live `history-web` pod with 3,000 chatty lines, and `history-gone`, deleted for good.
- **Each identity is written in the three shapes of §3,** one cluster label value each: `kind-klyx-sd`, `kind-klyx-alloy`, and `kind-klyx` (OTLP through `/otlp/v1/logs`).
- **The three shapes are also written under ONE match:**
  - `kind-klyx-mixed`: SD and Alloy under `cluster`, Alloy and OTLP under `k8s_cluster_name`;
  - `history-db-0`'s generations in each (lab finding 10).
- **`history-burst`** carries 1,200 lines at one nanosecond across two streams, with older lines behind them (lab finding 11).
- **The smoke fleet** reads `klyx-s8b-lab/loki:http` with `tenant: lab` and `match: {k8s_cluster_name: kind-klyx}`.
- **The captures** switch `match` to the SD and Alloy clusters to show the weaker selectors.

## 11. Tests

- **Pure:**
  - `Branches`:
    - each shape alone;
    - all three under the same match, with an indexed-pod and a metadata-pod stream sharing the spelling `pod`;
    - both namespace spellings, and the pruning;
    - a missing namespace label, and a `match` key not indexed;
  - `Query` for each branch in both modes, with and without a container, quoting, and an indexed UID left out in name mode;
  - `ParseStreams` for categorize-labels, the two-element fallback, `null`, a non-streams result, a bad timestamp and a non-success status;
  - `Page`:
    - the k-way merge across branches, per-branch cursors and the dedupe;
    - exhaustion from raw lengths, never from what remains after dedupe;
    - 1,000 or more entries at one nanosecond in one stream and across streams: one 5,000 re-read, then the explicit "at least N" marker and "Skip past T", never a loop or a false "start reached";
    - the overlap removed before the cut (1,000 held at T plus 200 older adds 200), and exhaustion counting entries already delivered;
  - `Mode`:
    - all with a UID, some, none;
    - a UID-only newest page followed by UID-less and mixed older pages, and the reverse, the mode held;
    - an explicit change starting a new page set;
  - the name sentence for a live pod, a gone pod and `PinnedGone`, always weaker.
- **Transport:**
  - both paths with the tenant header;
  - the 8 MiB cap;
  - a 400 plain-text body shown redacted;
  - 401 with no tenant;
  - the credential fence across a revision;
  - the request headers on both paths;
  - the raw proxied read:
    - a plain-text 400 body arrives whole (Codex's probe: 50 bytes, where `Do().Raw()` gave 0);
    - the cap sentinel survives a 400 (where `DoRaw` replaced it);
    - a cancellation or read error wins over the status;
    - the redirect guard and the kube credentials are kept;
  - the credential boundary, with exact and quoted needles:
    - a credential equal to a match value, to a pod name and to a label key;
    - redacted from the selector, source, strength, errors and Settings;
    - Copy LogQL unavailable whenever its display form differs;
    - log content passed through `Exact` only;
    - the raw values still selecting, classifying and deduping.
- **Source:**
  - a configuration or revision change between pages ends the page set ("the source changed");
  - an unknown tenant's empty labels answer is said as such, never "no logs".
- **The frontend:**
  - the mode switch;
  - the selector line and strength sentence for each strength;
  - Load older and the start-of-range state;
  - navigation past 2,000 and past 10,000 held lines (the newest end evicted, older pages reachable, the anchor kept);
  - the saturation marker and "Skip past T";
  - the cap notice;
  - the removed-pod and `PinnedGone` entries;
  - a replaced pod opening the gone UID;
  - a failure keeping pages, marked.
- **The lab:**
  - the smoke opens History for `history-db-0` and sees gen2 only, under the UID selector;
  - with `match` on `kind-klyx-mixed`, the captures show both branches' lines (SD and Alloy) under one view;
  - `history-burst` shows the saturation path;
  - with the Alloy cluster's `match`, the captures show the name selector's sentence;
  - the gone pod's history is captured from the removed inspector, by deleting a lab pod under a running server-mode app.
- **Mutations:**
  - the UID decision (all / some / none) and its lock;
  - the empty matchers that keep branches disjoint;
  - the "neither" branch always running;
  - `end` + 1 ns, the dedupe and exhaustion from raw lengths;
  - the saturation re-read and the marker;
  - the eviction end;
  - the shape's namespace requirement;
  - the categorize-labels header;
  - the null and status checks;
  - the exact-only log redaction and the display redaction;
  - Copy LogQL's availability;
  - the fence.

## 12. Staging

1. `internal/loki` (pure): shape, selector, parse, merge, strength, with their tests.
2. `fleet` `lokiState`, the read and Settings' status, the capability and the factory wiring.
3. `appbridge`: `LogHistoryService` and the endpoints changes.
4. The frontend: the logs pane mode, and the gone-pod entries.
5. The lab in `relabs.sh`, the smoke steps, captures and the as-built notes.

## 13. Not in S8b

- **Workload-level history** (every pod of a Deployment). The aggregate view's per-pod identity would need a selector per pod; it waits for demand.
- **Discovery** (§2).
- **UID keys other than `k8s_pod_uid`** (a relabelled `pod_uid`): a configurable key waits for a real shipper that needs it.
- **Log lines to AI.**
- **Live tail from Loki** (`/loki/api/v1/tail`): Live stays the kube API.
- **Metrics from LogQL** (rates, volumes).
- **A custom range picker.**

## 14. Revision 1: Codex's design review of 6438d43

Codex requested changes with five findings. The lab gained the cases behind them (findings 10 and 11).

1. **A label-name union cannot choose one shape for all streams.** Mixed shippers under one `match` lost streams.
   - §3 now reads the labels only to prune.
   - It selects through disjoint branches with empty matchers, at most six: each namespace spelling, then pod indexed as `pod`, as `k8s_pod_name`, or neither.
   - The container and the UID are label filters on every branch.
   - The lab shows SD, Alloy and OTLP lines of one pod under one `match` each kept exactly once.
2. **Saturated pages.**
   - Exhaustion is read from raw response lengths.
   - A branch that cannot advance past one timestamp is re-read once at 5,000, then stops with an explicit marker and a "Skip past T" choice. Never a loop, a silent skip or a false "start reached" (§5).
3. **The cap defeated backward browsing.** History holds 10,000 lines and 32 MiB, evicts from the newest end, keeps its cursor, and renders through a virtualized list, so older pages stay reachable past 2,000 and 10,000 lines (§5).
4. **Mode must be locked per page set.** The mode, branches, absolute range, lineage key and revision, UID and container are bound into the cursor. Load older keeps them; an explicit choice starts a new page set (§4).
5. **The credential boundary for Klyx's own text.**
   - Raw values stay in process.
   - Log content passes an exact-only redactor; everything Klyx says is a display value.
   - Copy LogQL is unavailable when the display query differs from the raw one (§5).

**Guidance adopted:**
- the `endpointhttp` extension (request headers; the proxied non-2xx body) and `Redactor.Exact` (§9);
- the creation bound as a timestamp filter, the name sentence always weaker;
- label presence never treated as per-stream or exact-range proof;
- a held-byte budget and full-shape memory measurement (§8).

## 15. Revision 2: Codex's re-review of c2b4e26

Codex confirmed the five findings addressed: its local model selects each of 45 index and metadata shapes exactly once through the branches. One P2 remained, with two clarifications:

1. **The proxied raw read** cannot be client-go's `Do(ctx).Raw()`, which loses a plain-text 400 body, nor `DoRaw`, which replaces the cap sentinel on a 400.
   - §9 now executes the raw read through the guarded `*http.Client` that `NewGuardedProxyREST` builds, with the REST client building the URL only.
   - The body is read capped before the status is looked at, so read, cap and cancellation errors take precedence.
   - Codex's two probes are the transport's contract tests.
2. **The overlap is removed before the newest-1,000 cut,** and exhaustion counts entries already delivered (§5).
3. **The saturation marker says "at least N"** and that Klyx cannot establish whether more remain (§5).

## 16. As built

Built on `feat/coverage-s8` after the design's acceptance at b4218c1:
- **706b1f9** - `internal/loki`, pure.
- **d6beaad** - the transport and the connection's Loki.
- **3aede5c** - `LogHistoryService`, Loki's Test and Set, and the history window.
- **af2d04d** - the frontend.
- **Stage 5** (the commit carrying this section) - the lab in `relabs.sh`, the smoke, captures, the tests the mutations asked for, and these notes.

### 16.1 What it is

- **`internal/loki`:**
  - `Branches` and `Query` (§3);
  - `ParseStreams` (§5);
  - `PageSet` with `Apply`, `Saturated`, `Stop` and `Skip`, and `ReadPage` (§5);
  - `UIDsOf` (§4).
- **A model test** selects each of 21 stream shapes under one match by exactly one branch, and another pod's by none.
- **`endpointhttp`:**
  - `Direct.GetWith` carries protocol headers;
  - `NewGuardedProxy` keeps the guarded `http.Client` beside the REST client;
  - `RawProxy` builds the proxy URL with the REST client and runs it through that client, reading the body capped before the status (§9). Codex's two probes are its contract tests, with the redirect guard and header tests.
- **`fleet`:**
  - the connection's Loki: an endpoint, or a `serviceRef` through `RawProxy`;
  - `LokiReader` (labels, query_range with categorize-labels, build info, Loki's words as display values, C5's fence);
  - per-page slots: two per cluster, four fleet-wide;
  - `LokiTest`, and the `LogHistory` capability.
- **`appbridge`:**
  - `LogHistoryService`: one page set per view, the mode and its lock (§4), the source move (Reset), the cancel of a read in flight, the exact-only line redaction and 64 KiB clip, and display selectors with Copy LogQL's rule;
  - Loki's Test and Set through the connection;
  - `OpenLogHistoryWindow`.
- **The frontend:**
  - the dock's Live | History, with History only for a gone pod;
  - `LogHistoryPane`: the controls and words of §7, the view's 10,000 lines and 32 MiB with eviction from the newest end, and every line through `VirtualList`;
  - Log history from the removed-pod inspector and `PinnedGone`;
  - the pop-out.

### 16.2 Decided while building

- **The page set lives in the backend, keyed by the view's id.**
  - Its cursor is the server's state, not a token the UI carries. A saturated boundary can hold 5,000 dedupe keys, too much to send back and forth.
  - A view holds one page set. At most 16 views are kept, idle ones dropped after 15 minutes.
  - A view whose page set expired starts again from the newest.
- **`Redactor.Exact` already existed** (C5), with exactly the exact and quoted spellings and no general shapes.
- **A first page that fails leaves no page set,** so the next request starts again rather than paging nothing.
- **A source move between pages** (lineage, revision, or a different pod, UID or container) starts a fresh page set, marked Reset. The design's "the source changed" reads in the view as "The Loki source changed; reading the newest again."
- **The Live | History switch is in the dock's header,** beside the pod's name: the dock already owns the pod's identity. `LogsPane` (the Live view) is unchanged.
- **The lab's second generation logs after its creation.** The first lab pushed generation two's lines relative to the apply, before the pod existed. A "since created" History then holds none, as it should. `apply.sh` now waits a minute after recreating `history-db-0` and pushes that minute's lines.

### 16.3 Verified

- **The lab** (§10; `lab-s8b/FINDINGS.md`, findings 1 to 11).
- **The captures** run against the server-mode build on the smoke fleet: History by UID, by name since creation, by name over 7 days, and a gone pod's history opened from the removed inspector. The capture deletes the lab's `history-web` pod under the running server.
  - **By UID:** generation two only; the selector shows the UID filter; "Select by name instead".
  - **By name since creation:** still generation two alone, bounded by its creation.
  - **By name over 7 days:** generation one appears too, with the name-only sentence.
  - **The gone pod:** "Pod removed" with "Log history", Live disabled, History by UID with Load older.

- **The captures in other shapes:** Alloy, and mixed shippers under one match, each from its own server configuration.
  - **Alloy** (`match: {cluster: kind-klyx-alloy}`): the name selector with the structured-metadata filter (`pod="history-db-0" or k8s_pod_name="history-db-0"`), no UID, generation two since its creation.
  - **Mixed** (`match: {cluster: kind-klyx-mixed}`): two selectors, the indexed-pod branch and the "neither" branch. The SD and Alloy lines of one pod interleave by time, in name mode, since the SD lines carry no UID.
- **The OTLP captures** pass in night and day at 1440 and 1024.

### 16.4 Gates

- **The native smoke:** 186 of 186 steps in each of night and day at 1440 and 1024, with the new History step and Loki's Test in Settings.
  - **History:** `history-db-0` reads 62 lines, generation two alone, "selected by this pod's UID".
  - **Loki's Test:** "Available: Loki 3.7.8 at Service klyx-s8b-lab/loki:http; this cluster's streams carry the namespace label k8s_namespace_name".
- **The parity gate:** both stages pass (S8b adds no Observer part).
- **Go:** `go test -race` passes for `loki`, `endpointhttp`, `fleet` and `appbridge`; golangci-lint reports 0 issues.
- **The frontend:** tsc is clean, eslint has 0 errors (its warnings predate S8b), and vitest passes 2,308 tests.
- **Mutations:**
  - 15 caught in stage 1 (two after added tests: saturation at T-1, "at least N" counting both sets);
  - 19 in the Go of stages 2 and 3, three after added tests: the proxied fence, the clip on a 3-byte rune, and Loki's Set through the connection;
  - 11 in the frontend, three after added tests: `PinnedGone`'s history, the gone pod's disabled Live, and History without a UID.
- **No equivalents.**

## 17. Implementation revision 1: Codex's review of b2f945c

Codex requested changes with one P1, seven P2s and one P3. Each has a repository test beside Codex's probe.

1. **[P1] A shared label amplified into the reply.**
   - The line's displayed container label is now clipped to 256 bytes, like a label value.
   - A page keeps at most `loki.PageBytes` (8 MiB) of entries: each entry's line plus a bounded allowance, never the stream's shared labels. The cursor moves only past what is kept, so the rest arrives on later pages, none lost and none twice.
   - **Measured on the admitted worst shape:** six branches each answering about 8 MiB of 60 KiB lines, with a 128 KiB label shared by every line. The page carries 135 lines in an 8.0 MiB reply, and the label is clipped.
2. **[P2] View state raced across restarts.**
   - Each view now has a persistent slot whose one mutex guards its page state across restarts.
   - A new request cancels the slot's read in flight, then waits on that mutex. A cancelled owner that never got the mutex says it was replaced.
   - The service lock guards only the slot's bookkeeping. Concurrent same-view requests run clean under `-race`.
3. **[P2] No completion fence.** After the page, first or next, the source is checked again. A source that moved refuses the page (no lines, Reset, "the Loki source changed") and drops the page set.
4. **[P2] An expired or evicted cursor restarted silently.** Any request without Restart that finds no page set to continue starts from the newest, marked Reset, so the view replaces its lines. This covers an expired, evicted or failed page set, and a moved source.
5. **[P2] A stopped marker vanished on a later page.**
   - Every stopped branch's marker, with the server's refusal, is returned on every page until it is skipped.
   - The page says `more` (a branch can still be read). A view whose only branches left are stopped offers Skip past, not an endless Load older.
6. **[P2] The view's byte cap spared the incoming page.** The cap now applies to the incoming page too, dropping from its newest end and keeping its oldest lines, where the cursor continues.
7. **[P2] The 16-view maximum grew while every view read.** A new view is refused when 16 are held and none is idle: "16 log history views are reading; close one or try again".
8. **[P2] A failed Refresh dropped the last good lines.**
   - A failed Refresh of the same target keeps the held lines and what they were read by, marked "not refreshed".
   - A changed target (range, container, mode or pod) never shows the old lines.
9. **[P3] An empty labels list skipped the fallback.** An empty list is nothing selectable, in the reader and in the service, and the second, unscoped labels read says why.

**Mutations:** 14 caught for this revision. One, keeping a failed first page's state, first survived and got its test; that test also shows a "no streams" first page cannot be paged.

## 18. Implementation revision 2: Codex's re-review of f4cce2f

Codex's probes for revision 1 all passed. Codex requested changes with three residual P2s; each has a repository test beside Codex's probe.

1. **[P2] A cancelled owner still committed its page.**
   - **Staged reads.** A page is now read on a staged copy of the page set (`PageSet.Clone`). The copy is kept only when the page is delivered: after the page is filled, under the view's mutex, the request's context must still be live and the source current.
   - **The owner fence.** A request that a newer one replaced returns "a newer request for this view replaced this one", with no lines, and keeps nothing. A request out of time says so and keeps nothing.
   - **The window the fence leaves.** An owner can keep its page an instant before the newer request cancels it, and the pane discards that reply anyway. So a next page now names the page it continues: the page set and the last of its pages the view took (`set`, `after`). Each reply carries its `set` and `page`.
   - **The service holds two positions:** the page set after its last page, and before it.
     - `after` equal to the last page continues.
     - `after` one page earlier reads the last page again (the view discarded its reply).
     - Anything else starts from the newest, marked Reset.

     The cursor moves only with what the view took.
   - **Failed pages.** A failed page, first or next, now keeps nothing: the view's page set is where it was, so its cursor still continues it. This replaces §17.4's "failed" case: a failed Refresh now keeps both the held lines and their cursor. Nothing selectable still leaves no page set.
   - Skip past now waits while a page is read, like Load older.
2. **[P2] A thrown read kept another target's lines.**
   - One rule in the pane now covers every failed read, whether an answer with an error or a call that threw.
   - Lines held of this target stay, with what they were read by and their cursor, marked "not refreshed".
   - For another target, or after the source ended the page set, nothing held is shown and the cursor is cleared.
3. **[P2] The page budget counted raw lines.**
   - **What the budget counts now** is what the bridge carries for each entry:
     - its line and container as shown: exact redaction (which can lengthen them), then the 64 KiB line clip and the 256-byte label clip;
     - JSON-escaped as the bridge's `json.Marshal` writes them (a control character, `<`, `>` and `&` take six bytes each);
     - plus 128 bytes for the line's other fields.
   - `loki.ReadPage` takes the size function. The cursor still moves only past what is kept.
   - **Measured** (`TestHistoryBudgetShown`), every line once and in order:
     - A 4-byte credential shown as `[redacted]`: 1,000 lines read as 7 MB and shown as 17.5 MB arrive in pages of 475, 475 and 50 lines, each reply at most 7.96 MiB.
     - Lines of `\x00<` (each byte escaped to six): pages of 22 lines, 7.74 MiB each.
     - Codex's probes measure 8.35 MB (475 lines) and 8.11 MB (22 lines), both under `PageBytes`.
   - `jsonStringLen` matches `json.Marshal` exactly on valid UTF-8 and is an upper bound for an invalid byte. Go 1.27's encoder writes the replacement rune (3 bytes); the count assumes `\ufffd` escaped (6).
4. The blank lines at the end of the two test files are gone.

**Probes.** Codex's two cancellation probes sent a next page without a cursor, so under this revision they start again (Reset). Adapted to send the cursor of the page they took, both pass, with the other probes, unchanged.

**Mutations:** 22 caught for this revision (16 Go, 6 frontend). Three first survived and got their tests:
- a cursor naming another page set was continued when its page number matched;
- the pane's cursor stuck at page 1;
- a clone sharing its delivered keys only shows when a staged page ends at the held boundary.

## 19. Implementation revision 3: Codex's re-review of 1230f36

Codex confirmed the three findings fixed and requested changes with one P2.

1. **[P2] A clipped line kept its whole redaction alive.**
   - **The cause.** `clipUTF8` returned a slice of the line, so the 64 KiB shown kept the full backing array behind it.
   - **Codex's probe.** One 6 MiB line of a 4-byte credential, which redaction lengthens to 15 MiB, retained 15 MiB behind each page.
   - **The fix.** The clip now copies its kept bytes (`strings.Clone`).
   - **Tests.** `TestHistoryClippedLineOwnsItsBytes` checks that the clip owns a separate array, and that the same 6 MiB line leaves 0.06 MiB of heap after the page.
   - **The other clips already copied.** The container label and the selectors go through `alertmanager.Clip`, which builds a new string, and Loki's refusal text is clipped the same way.

**Probes and mutations.** Codex's combined probes pass, including the heap probe. The mutation that restores the shared slice is caught.

**Implementation accepted by Codex at 94be5c2** (handoff aecfe757, 2026-10-19), with no remaining findings. Codex's combined review overlay passes under `-race`: the adapted earlier probes and the retained-heap case, which shows 65,536 bytes and retains 66,136 bytes. The owner's verdict is still to come.
