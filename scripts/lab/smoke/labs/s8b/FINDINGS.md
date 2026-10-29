# S8b lab findings (Loki 3.7.8 on kind-klyx, 2026-10-19)

The lab: one Loki 3.7.8 single binary in klyx-s8b-lab (auth_enabled, tenant
"lab", filesystem store in an emptyDir, schema v13), ClusterIP only. push.sh
writes each pod identity's lines in three shipper shapes, one cluster label
value each:
  sd     Kubernetes SD / Promtail style: {cluster=kind-klyx-sd, namespace, pod,
         container, job, stream}; no pod UID
  alloy  k8s-monitoring 4.5.3 defaults (Alloy, pod logs via the kubernetes
         API): index {cluster, k8s_cluster_name, namespace, container, job,
         service_name, service_namespace}; structured metadata {pod,
         service_instance_id}; no pod UID
  otel   real OTLP ingestion (/otlp/v1/logs) with resource attributes
         k8s.cluster.name=kind-klyx, k8s.namespace.name, k8s.pod.name,
         k8s.pod.uid, k8s.container.name, service.name
Identities: history-db-0 in two generations (two UIDs, one name; gen1 3h-2h
ago, gen2 the last 50 min), the history-web pod (2h, plus 3,000 chatty OTel
lines in the last 25 min), and history-gone (a bare pod deleted for good;
lines 90-30 min ago).

1. Index labels per shape, from /loki/api/v1/labels?query={<cluster label>}:
     sd     cluster container job namespace pod service_name stream
     alloy  cluster container job k8s_cluster_name namespace service_name service_namespace
     otel   k8s_cluster_name k8s_container_name k8s_namespace_name k8s_pod_name service_name
   - Loki adds service_name itself (limits discover_service_name): the sd
     push carried none.
   - k8s-monitoring 4.5.3 moves pod to structured metadata by default
     (feature-pod-logs-via-kubernetes-api values.yaml structuredMetadata:
     {k8s.pod.name, pod, service.instance.id}); its Loki destination adds
     clusterLabels [cluster, k8s.cluster.name].
   - OTLP: k8s.pod.name is a default index label (Loki's docs recommend
     demoting it); k8s.pod.uid is structured metadata, k8s_pod_uid.
2. X-Loki-Response-Encoding-Flags: categorize-labels returns
   data.encodingFlags ["categorize-labels"], streams with their index labels
   only, and each value as [ns, line, {structuredMetadata: {...}}] - here
   detected_level on every line (Loki's discover_log_levels), pod and
   service_instance_id (alloy), k8s_pod_uid, scope_name, severity_text
   (otel). Without the flag, structured metadata is merged into the stream
   labels and the result splits per value (2 streams for one sd stream by
   detected_level).
3. Generations: a name selector returns both generations of history-db-0
   (81 lines: gen1 31, gen2 50) for sd and otel alike. The UID filter
   {k8s_cluster_name="kind-klyx", k8s_namespace_name="klyx-s8b-lab"} |
   k8s_pod_uid="<uid>" returns gen1's 31, gen2's 50, and the gone pod's 31.
4. A label filter on a key the lines do not carry returns nothing - success,
   zero streams (alloy | k8s_pod_uid="..."). "No lines" there is not
   evidence. A probe | k8s_pod_uid!="" with limit 1 tells the shapes apart
   (otel 1 line, alloy 0).
5. Limits (defaults): limit > max_entries_limit_per_query (5000) is HTTP 400
   "max entries limit per query exceeded ... (6000 > 5000)", plain text, not
   JSON; a range over max_query_length is HTTP 400 "the query time range
   exceeds the limit (query length: 800h0m0s, limit: 30d1h)". limit counts
   entries across streams (5000 returned 3172 of the otel cluster's 3172).
   data.stats.summary.totalEntriesReturned is present.
6. Paging: start is inclusive and end exclusive (a backward page ending at
   the previous page's oldest timestamp starts strictly before it). Lines
   sharing that nanosecond at the boundary need end+1ns and a dedupe.
7. Through the API server's service proxy
   (/api/v1/namespaces/klyx-s8b-lab/services/http:loki:http/proxy/...):
   X-Scope-OrgID is forwarded. No tenant: HTTP 401 "no org id". An unknown
   tenant: HTTP 200 {"status":"success"} with no data at all - a wrong tenant
   reads exactly like "no logs". /loki/api/v1/status/buildinfo needs no
   tenant: {"version":"3.7.8",...}.
8. OTLP push answers HTTP 204 (not 200); /loki/api/v1/push answers 204.
9. A label filter naming both pod spellings works on structured metadata:
   {cluster="kind-klyx-alloy", namespace="klyx-s8b-lab"} |
   k8s_pod_name="history-db-0" or pod="history-db-0" returns the pod's 81
   lines (both generations; no UID in the alloy shape), with a container
   index label too, and history-gone's 31. The absent spelling widens
   nothing.
10. Mixed shapes under ONE match (Codex's design review of 6438d43): with
    sd and alloy both under cluster=kind-klyx-mixed, the labels union lists
    pod (sd indexes it), and {cluster=..., namespace=..., pod="P"} returns
    only the sd streams - alloy's pod is structured metadata, so its streams
    are dropped before any filter. Disjoint branches with empty matchers
    keep every stream exactly once:
      pod indexed:          {M, namespace="N", pod="P"}                     -> sd only
      k8s_pod_name indexed: {M, NS="N", k8s_pod_name="P", pod=""}           -> otel only
      neither indexed:      {M, NS="N", pod="", k8s_pod_name=""} | pod="P" or k8s_pod_name="P" -> alloy only
    (an empty matcher selects streams without that label). A label filter
    applies to index labels too ({cluster="kind-klyx-sd", namespace=N} |
    pod="P" returns the sd pod's lines), so | k8s_pod_uid="U" works in every
    branch (otel branch: gen2's 50 of 71).
11. Saturation: history-burst has 1,200 distinct lines at one nanosecond T
    (two streams, 600 each) and 50 older lines. A backward page of 1,000
    returns 1,000 at T; the next page with end=T+1 returns the same 1,000 -
    no progress. limit=5000 with end=T+1 returns all 1,250; end=T (skipping
    T) returns the 50 older lines.
