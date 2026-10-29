# Builds the S8b lab's push bodies: each pod identity's lines in three
# shipper shapes. argv: outdir now_seconds DB1 DB2 WEB WEBUID GONE
import json, sys
out, now = sys.argv[1], int(sys.argv[2])
db1, db2, web, webuid, gone = sys.argv[3:8]
db2created = int(sys.argv[8]) if len(sys.argv) > 8 else now - 61
NS = "klyx-s8b-lab"
# (pod, uid, container, generation tag, start offset s, end offset s, step s)
ids = [
    ("history-db-0", db1, "db", "gen1", -3 * 3600, -2 * 3600, 120),
    ("history-db-0", db2, "db", "gen2", db2created + 1 - now, -1, 1),  # after its creation
    (web, webuid, "web", "web", -2 * 3600, -60, 120),
    ("history-gone", gone, "history-gone", "gone", -90 * 60, -30 * 60, 120),
]
def lines(tag, scheme, start, end, step):
    t, n = now + start, 0
    while t <= now + end:
        lvl = "error" if n % 7 == 3 else "info"
        yield t, f'level={lvl} msg="{tag} {scheme} line {n}" gen={tag}'
        t += step; n += 1
ns = lambda t: str(t * 1_000_000_000)
# sd: Kubernetes SD (Promtail-style) - pod is an index label, no UID
sd = {"streams": []}
# alloy: k8s-monitoring 4.5.3 defaults - pod and service_instance_id are
# structured metadata; cluster and k8s_cluster_name from the destination
alloy = {"streams": []}
# otel: OTLP resource attributes; Loki maps k8s.pod.name to an index label
# and k8s.pod.uid to structured metadata
otel = {"resourceLogs": []}
for pod, uid, c, tag, start, end, step in ids:
    ls = list(lines(tag, "sd", start, end, step))
    sd["streams"].append({"stream": {"cluster": "kind-klyx-sd", "namespace": NS, "pod": pod, "container": c, "job": f"{NS}/{c}", "stream": "stdout"},
                          "values": [[ns(t), l] for t, l in ls]})
    ls = list(lines(tag, "alloy", start, end, step))
    alloy["streams"].append({"stream": {"cluster": "kind-klyx-alloy", "k8s_cluster_name": "kind-klyx-alloy", "namespace": NS, "container": c, "job": f"{NS}/{c}",
                                        "service_name": c, "service_namespace": NS},
                             "values": [[ns(t), l, {"pod": pod, "service_instance_id": f"{NS}.{pod}.{c}"}] for t, l in ls]})
    recs = [{"timeUnixNano": ns(t), "body": {"stringValue": l}, "severityText": "ERROR" if "level=error" in l else "INFO"} for t, l in lines(tag, "otel", start, end, step)]
    if tag == "web":  # a chatty pod: 3,000 more lines in the last 25 minutes
        recs += [{"timeUnixNano": str((now - 1500) * 1_000_000_000 + i * 500_000_000), "body": {"stringValue": f'level=info msg="web otel chatty {i}"'}, "severityText": "INFO"} for i in range(3000)]
        recs.sort(key=lambda r: int(r["timeUnixNano"]))
    attrs = {"k8s.cluster.name": "kind-klyx", "k8s.namespace.name": NS, "k8s.pod.name": pod, "k8s.pod.uid": uid, "k8s.container.name": c, "service.name": c}
    otel["resourceLogs"].append({"resource": {"attributes": [{"key": k, "value": {"stringValue": v}} for k, v in attrs.items()]},
                                 "scopeLogs": [{"scope": {"name": "klyx-lab"}, "logRecords": recs}]})
# mixed: the three shapes under ONE match (Codex's design review): sd and
# alloy share cluster=kind-klyx-mixed; alloy and otel share
# k8s_cluster_name=kind-klyx-mixed; history-db-0's two generations
for pod, uid, c, tag, start, end, step in ids[:2]:
    ls = list(lines(tag, "mixed-sd", start, end, step))
    sd["streams"].append({"stream": {"cluster": "kind-klyx-mixed", "namespace": NS, "pod": pod, "container": c, "job": f"{NS}/{c}", "stream": "stdout"},
                          "values": [[ns(t), l] for t, l in ls]})
    ls = list(lines(tag, "mixed-alloy", start, end, step))
    alloy["streams"].append({"stream": {"cluster": "kind-klyx-mixed", "k8s_cluster_name": "kind-klyx-mixed", "namespace": NS, "container": c, "job": f"{NS}/{c}",
                                        "service_name": c, "service_namespace": NS},
                             "values": [[ns(t), l, {"pod": pod, "service_instance_id": f"{NS}.{pod}.{c}"}] for t, l in ls]})
    recs = [{"timeUnixNano": ns(t), "body": {"stringValue": l}} for t, l in lines(tag, "mixed-otel", start, end, step)]
    attrs = {"k8s.cluster.name": "kind-klyx-mixed", "k8s.namespace.name": NS, "k8s.pod.name": pod, "k8s.pod.uid": uid, "k8s.container.name": c, "service.name": c}
    otel["resourceLogs"].append({"resource": {"attributes": [{"key": k, "value": {"stringValue": v}} for k, v in attrs.items()]},
                                 "scopeLogs": [{"scope": {"name": "klyx-lab"}, "logRecords": recs}]})
# burst: 1,200 distinct lines at ONE nanosecond (two streams, 600 each) for
# history-burst, with 50 older lines behind them (sd shape)
T = now - 600
for st in ("stdout", "stderr"):
    sd["streams"].append({"stream": {"cluster": "kind-klyx-sd", "namespace": NS, "pod": "history-burst", "container": "burst", "job": f"{NS}/burst", "stream": st},
                          "values": [[ns(now - 3600 + i * 60), f"older {st} {i}"] for i in range(25)] + [[ns(T), f"burst {st} {i}"] for i in range(600)]})
for name, body in (("sd", sd), ("alloy", alloy), ("otel", otel)):
    open(f"{out}/{name}.json", "w").write(json.dumps(body))
