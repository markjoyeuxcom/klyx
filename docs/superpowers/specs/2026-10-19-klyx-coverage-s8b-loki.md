# Coverage S8b: log history, from Loki

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S8b. It follows C5 (`2026-10-18-klyx-coverage-c5-external-credentials.md`) and S8a (`2026-10-19-klyx-coverage-s8a-alertmanager.md`), whose transport, `match`, `serviceRef.path` and source identity it reuses.

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

Klyx never assumes a shape. Each history read starts with **one labels read**: `/loki/api/v1/labels` with `query={match}` (no query without `match`) over the read's own range.
- It answers which names are index labels for this cluster's streams in that range. That is an index read, never a scan of lines.
- `{"status":"success"}` with no `data` (an unknown tenant reads the same, lab finding 7) is said as "no streams for this cluster's match in the range on tenant T". It is never "no logs for this pod".

**The selector, from the labels read:**
1. **The stream selector:**
   - opens with `match`;
   - adds the namespace: `k8s_namespace_name` when indexed, else `namespace`;
   - adds the pod: `k8s_pod_name` or `pod` when indexed;
   - adds the container (when one is chosen): `k8s_container_name` or `container` when indexed;
   - adds the UID, `k8s_pod_uid`, when it is an index label (rare, but then it selects streams).
2. **What is not indexed** becomes a label filter on structured metadata, placed before any parser. The pod is `| k8s_pod_name="P" or pod="P"`, the container likewise.
   - Klyx names both spellings because it cannot list structured metadata keys without scanning lines.
   - A filter on a key the lines lack matches nothing (lab finding 4), so naming both never widens the selection.
3. **No namespace label indexed:** history is not offered for this Loki. The reason is "no namespace label in this cluster's streams (looked for k8s_namespace_name, namespace)". A selector of `match` alone would select the whole cluster.

The values are Kubernetes names, which LogQL's string quoting carries without escaping trouble. Klyx escapes them with Go's `strconv.Quote` all the same.

## 4. The pod's identity: UID when the lines prove it, else a stated weaker selector

**The main read selects by name**, inside the read's range. It asks with `X-Loki-Response-Encoding-Flags: categorize-labels`, so each entry carries its structured metadata apart from its stream (lab finding 2).

**The entries returned decide the strength:**
- **Every returned entry carries `k8s_pod_uid`.**
  - Klyx re-reads with `| k8s_pod_uid="<uid>"` added: the **UID selector**. That is one more read, only when UIDs exist. Its lines are said to be this pod's: "selected by this pod's UID (k8s_pod_uid)".
  - Generations of one name separate: history-db-0's gen1 and gen2 read 31 and 50 lines, the name read 81 (lab finding 3).
- **Some carry it and some do not** (a namespace shipped two ways): the name selector stands, and is said as such: "some lines carry a pod UID, some do not; selected by name".
- **Under the UID selector,** older lines a shipper wrote without the UID are not shown. The strength sentence says so: "lines without a pod UID are not shown".
  - "Select by name instead" re-reads with the name selector and its own sentence. It is the person's explicit choice, never a silent fallback.
- **None carries it:** the **name selector**, said with its bound:
  - **With the pod's lifetime known** (created time from the row; for a gone pod, the time Klyx last observed it, or now): "selected by namespace and name, from this pod's creation". A pod's name is unique in its namespace at any moment, so a same-name pod can only appear before its creation or after its removal.
    - The range starts at the creation time.
    - For a gone pod the end is open (Klyx does not know the deletion time), and the line says: "a later pod with this name would match after this one was removed".
  - **No lifetime known** (`PinnedGone` has no created time): "selected by namespace and name only: every pod with this name in the range".
- **No entry at all:** "no lines for this selector in the range". The selector is shown. Absence is never evidence that the pod logged nothing: the shipper may not have collected it, or retention may have passed.

**The pod's UID is the one Klyx holds:**
- the live pod's;
- or the removed inspector's last row;
- or `PinnedGone`'s UID.

A history view is UID-pinned in the UI, as every inspector is. A same-name replacement's live logs never answer a gone pod's history.

## 5. The read

- **The range:**
  - presets of 1 hour, 6 hours, 24 hours and 7 days, plus "since this pod was created" when the created time is known;
  - the default is since creation, clipped to 7 days, else 24 hours;
  - there is no custom range picker in S8b.
- **The cost.**
  - When the pod is an index label, the read selects its streams only.
  - When it is structured metadata (the k8s-monitoring default), Loki reads every line of the namespace's streams in the range to filter them.
  - Klyx therefore narrows with the container when one is chosen and defaults to the pod's lifetime. A Loki timeout or limit is shown in Loki's words, with "narrow the range or choose a container".
- **Each page is one `query_range`:**
  - backward, with the stream selector and filters;
  - `limit` 1,000 entries (Loki's default maximum is 5,000; lab finding 5);
  - `start` and `end` in nanoseconds;
  - categorize-labels.
- **The first page is the newest.** "Load older" reads the next page.
  - Its `end` is the oldest timestamp held plus one nanosecond, since `end` is exclusive (lab finding 6).
  - Entries already held are dropped by (stream labels, timestamp, line) before they join.
- **A page shorter than its limit** means the range is exhausted, and the view says "start of the range reached". A full page offers "Load older". Nothing claims completeness the read did not prove.
- **The view holds at most 10,000 lines** (`LogsPane`'s cap). Past it, the oldest held lines are dropped and the view says so, as the live view does.
- **Rendering:**
  - Lines show with their timestamp (RFC 3339, milliseconds), so `logline.ts` dims it.
  - Streams merge by timestamp; equal timestamps keep their stream order.
  - A line over 64 KiB is clipped and marked.
  - A container column shows when no container is chosen and the streams name several.
- **Non-2xx responses:**
  - A 400 (Loki's limits) is shown with Loki's own text, redacted (lab finding 5: the body is plain text).
  - A 401 without a tenant reads "Loki requires a tenant; set loki.tenant".
  - Any other status names it.
- **The response is parsed strictly.**
  - `status` must be `success`.
  - `resultType` must be `streams`.
  - Each value must be `[ns-string, line, {structuredMetadata?, parsed?}]`, or the two-element form when the server ignored the flag. Without the flag's echo (`encodingFlags`), structured metadata is merged into the stream labels, and Klyx reads the UID from there.
  - Anything else fails the page whole, and `null` is not an empty list (S8a revision 1).

**Text and redaction:**
- The operator's own log view stays honest and unscrubbed (C5).
- One exception, as S8a's display values: the endpoint's own credential, in its exact and quoted spellings, is redacted from lines and labels. A response can only echo the credential it was sent.
- No line goes to AI in S8b: chat's `get_pod_logs` stays on the kube API.

## 6. The source contract

- **History is on demand, not an Observer snapshot.** There is no polling, no part and no counting. A history view is one source read at a time.
- **Each read carries the source identity it resolved:** how | location | tenant | sorted match (S8a §4). The view shows the source line.
- **A configuration or credential change while pages are held** ends the view's lineage. The next page re-reads from the newest, never mixing two sources' lines. The credential fence is C5's: a read across a revision fails.
- **Settings' Test** reads `/loki/api/v1/status/buildinfo` (no tenant needed), then a labels read in this cluster's `match` over the last hour. It reports:
  - "Available: Loki 3.7.8 at <source>; this cluster's streams carry N index labels (namespace: k8s_namespace_name)"; or
  - "answered, but no streams match this cluster's match in the last hour on tenant T (check tenant and match)".
  - It never reads lines.

## 7. What the operator sees

- **The logs pane gains a mode: Live (the kube API, as today) | History (Loki).**
  - History is offered when `logHistory` is present.
  - The range sits beside the container, and the selector line sits above the lines, for example:
    - `{k8s_cluster_name="kind-klyx", k8s_namespace_name="klyx-s8b-lab", k8s_pod_name="history-db-0"} | k8s_pod_uid="1b28…"` · selected by this pod's UID · Service klyx-s8b-lab/loki:http · tenant lab
  - Copy LogQL copies the exact query, so the person can paste it into Grafana.
  - Follow, previous and tail are Live's controls; History has the range, Load older and Refresh (the newest page again).
- **Gone pods:**
  - The removed-pod inspector's Logs action becomes "Log history" when `logHistory` is present. It opens History with the last row's UID, namespace, name, created time and containers.
  - `PinnedGone` gains the same action, with "all containers" and no created time (§4).
  - A replaced pod's history opens the gone one's UID, never the replacement's.
- **The pop-out window** carries History as the pane does.
- **The words:**
  - "history from Loki";
  - the strength sentence of §4;
  - "start of the range reached" or "Load older";
  - a failed read in its own words, with the last pages kept and marked "not refreshed".

## 8. Bounds

- **Per response:** 8 MiB (endpointhttp's cap, Loki's kind limit). A page over it fails with "the Loki response exceeded 8 MiB; narrow the range or choose a container".
- **Per page:** 1,000 entries. Per view: 10,000 lines.
- **Time:** a 30-second deadline per read. The range is at most 7 days in the UI (Loki's default maximum is 30 days 1 hour).
- **Concurrency:** one history read per view at a time (a new request cancels the old), at most two per cluster and four fleet-wide.
- **Memory:** a page decodes into at most 1,000 lines of up to 64 KiB displayed. The 8 MiB body bounds the decode.
- **The labels read** adds one index request per page set (§3), never one per line.

## 9. The wiring

- **`internal/loki` (pure):**
  - `Shape` from the labels read;
  - `Selector` (stream selector, filters, the UID variant, and the LogQL string);
  - `ParseStreams` (categorize-labels, and the two-element fallback);
  - `Merge` (dedupe and order);
  - `Strength` (the §4 decision from a page's entries).
- **`fleet` `lokiState`:**
  - resolves the endpoint or the service proxy with `endpointAuth` and Loki limits;
  - holds the source identity;
  - limits reads;
  - `ReadLogHistory(ctx, req)` returns one page with its selector, strength, source line and range;
  - `LokiStatus(ctx)` serves Settings.
- **`appbridge` `LogHistoryService`:**
  - `ReadPodLogHistory(cluster, podRef{uid, ns, name, created, container}, range, cursor)` returns a `LogHistoryPageDTO`: lines with timestamp, container and stream, plus selector, strength, source, a next cursor or none, and an error;
  - it is request/response, not an event stream: a page is bounded and small.
- **`EndpointsService`:** Test for Loki, Set through the connection (as S8a), and `logHistory` on the cluster DTO.
- **The frontend:**
  - `LogsPane` gains its mode, the range, the selector line, Load older and Refresh;
  - the removed-pod inspector and `PinnedGone` gain "Log history";
  - Settings' Loki Test.

## 10. The lab (built; `lab-s8b/`)

On `kind-klyx`, in `klyx-s8b-lab`:
- **Setup:** ClusterIP only, labelled `klyx.dev/lab=s8b`, with a teardown script.
- **Loki 3.7.8** single binary: multi-tenant, tenant `lab`, an emptyDir store re-created on every apply, so lines are always written relative to now.
- **A curl pod** pushes the lines (lab setup through Loki's own API; Klyx never writes to Loki).
- **Pod identities:** `history-db-0` in two generations, a live `history-web` pod with 3,000 chatty lines, and `history-gone`, deleted for good.
- **Each identity is written in the three shapes of §3,** one cluster label value each: `kind-klyx-sd`, `kind-klyx-alloy`, and `kind-klyx` (OTLP through `/otlp/v1/logs`).
- **The smoke fleet** reads `klyx-s8b-lab/loki:http` with `tenant: lab` and `match: {k8s_cluster_name: kind-klyx}`.
- **The captures** switch `match` to the SD and Alloy clusters to show the weaker selectors.

## 11. Tests

- **Pure:**
  - `Shape` for the three shapes, a missing namespace label, and a `match` key not indexed;
  - `Selector` for each shape, with and without a container, and the UID variant, with quoting;
  - `ParseStreams` for categorize-labels, the two-element fallback, `null`, a non-streams result, a bad timestamp and a non-success status;
  - `Merge` for dedupe at a page boundary sharing a nanosecond, order, and the 10,000 cap;
  - `Strength` for all with a UID, mixed, none, and none with and without a lifetime.
- **Transport:**
  - both paths with the tenant header;
  - the 8 MiB cap;
  - a 400 plain-text body shown redacted;
  - 401 with no tenant;
  - the credential fence across a revision;
  - redaction of the exact credential from lines and labels.
- **Source:**
  - a configuration change between pages ends the lineage;
  - an unknown tenant's empty labels answer is said as such, never "no logs".
- **The frontend:**
  - the mode switch;
  - the selector line and strength sentence for each strength;
  - Load older and the start-of-range state;
  - the cap notice;
  - the removed-pod and `PinnedGone` entries;
  - a replaced pod opening the gone UID;
  - a failure keeping pages, marked.
- **The lab:**
  - the smoke opens History for `history-db-0` and sees 50 lines, gen2 only, under the UID selector;
  - with the Alloy cluster's `match`, the captures show the name selector's sentence;
  - the gone pod's history is captured from the removed inspector, by deleting a lab pod under a running server-mode app.
- **Mutations:**
  - the UID decision (all / some / none);
  - `end` + 1 ns and the dedupe;
  - the shape's namespace requirement;
  - the not-indexed filter placement;
  - the categorize-labels header;
  - the null and status checks;
  - the redaction;
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
