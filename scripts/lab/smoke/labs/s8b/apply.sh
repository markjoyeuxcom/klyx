#!/bin/zsh
# Coverage S8b lab on kind-klyx: a real Loki 3.7.8 (single binary, multi-
# tenant, filesystem store in an emptyDir), ClusterIP only, in klyx-s8b-lab,
# labelled klyx.dev/lab=s8b.
#   loki          tenant "lab"; restarted on every apply so the store is fresh
#   pusher        curl, the lab's way to push (Klyx never writes to Loki)
#   history-db    StatefulSet: its pod is deleted on every apply, so
#                 history-db-0 has two generations (two UIDs, one name)
#   history-web   Deployment: one live pod
#   history-gone  a bare pod deleted after its UID is read: gone for good
# push.sh writes each identity's lines in three shipper shapes (one cluster
# label value each) - see FINDINGS.md.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 180 kube "$@"; }
NS=klyx-s8b-lab
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace $NS klyx.dev/lab=s8b --overwrite >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: ConfigMap
metadata: {name: loki-config, namespace: $NS, labels: {klyx.dev/lab: s8b}}
data:
  loki.yaml: |
    auth_enabled: true
    server: {http_listen_port: 3100, grpc_listen_port: 9095, log_level: warn}
    common:
      path_prefix: /loki
      storage: {filesystem: {chunks_directory: /loki/chunks, rules_directory: /loki/rules}}
      replication_factor: 1
      ring: {kvstore: {store: inmemory}}
    schema_config:
      configs:
        - {from: "2024-01-01", store: tsdb, object_store: filesystem, schema: v13, index: {prefix: index_, period: 24h}}
    limits_config:
      allow_structured_metadata: true
      reject_old_samples: false
    runtime_config: {file: /etc/loki/runtime.yaml}
    pattern_ingester: {enabled: false}
    analytics: {reporting_enabled: false}
  runtime.yaml: |
    overrides: {}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: loki, namespace: $NS, labels: {klyx.dev/lab: s8b}}
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: loki}}
  template:
    metadata: {labels: {app: loki, klyx.dev/lab: s8b}}
    spec:
      containers:
        - name: loki
          image: grafana/loki:3.7.8
          args: [-config.file=/etc/loki/loki.yaml]
          ports: [{name: http, containerPort: 3100}]
          readinessProbe: {httpGet: {path: /ready, port: 3100}, periodSeconds: 3}
          volumeMounts: [{name: config, mountPath: /etc/loki}, {name: data, mountPath: /loki}]
      volumes:
        - {name: config, configMap: {name: loki-config}}
        - {name: data, emptyDir: {}}
---
apiVersion: v1
kind: Service
metadata: {name: loki, namespace: $NS, labels: {klyx.dev/lab: s8b}}
spec:
  type: ClusterIP
  selector: {app: loki}
  ports: [{name: http, port: 3100, targetPort: 3100}]
---
apiVersion: v1
kind: Pod
metadata: {name: pusher, namespace: $NS, labels: {app: pusher, klyx.dev/lab: s8b}}
spec:
  containers: [{name: curl, image: curlimages/curl:8.11.1, command: [sleep, infinity]}]
---
apiVersion: v1
kind: Service
metadata: {name: history-db, namespace: $NS, labels: {klyx.dev/lab: s8b}}
spec: {clusterIP: None, selector: {app: history-db}, ports: [{port: 80}]}
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: history-db, namespace: $NS, labels: {klyx.dev/lab: s8b}}
spec:
  serviceName: history-db
  replicas: 1
  selector: {matchLabels: {app: history-db}}
  template:
    metadata: {labels: {app: history-db, klyx.dev/lab: s8b}}
    spec:
      terminationGracePeriodSeconds: 0
      containers: [{name: db, image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: history-web, namespace: $NS, labels: {klyx.dev/lab: s8b}}
spec:
  replicas: 1
  selector: {matchLabels: {app: history-web}}
  template:
    metadata: {labels: {app: history-web, klyx.dev/lab: s8b}}
    spec:
      terminationGracePeriodSeconds: 0
      containers: [{name: web, image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent}]
YAML
# a fresh store per apply: lines are written relative to now, never behind
k -n $NS rollout restart deploy/loki >/dev/null
k -n $NS rollout status deploy/loki --timeout=180s >/dev/null
k -n $NS rollout status sts/history-db --timeout=120s >/dev/null
k -n $NS rollout status deploy/history-web --timeout=120s >/dev/null
k -n $NS wait --for=condition=Ready pod/pusher --timeout=120s >/dev/null
# history-db-0: read generation one's UID, delete it, read generation two's
DB1=$(k -n $NS get pod history-db-0 -o jsonpath='{.metadata.uid}')
k -n $NS delete pod history-db-0 --wait=true >/dev/null
for i in {1..60}; do DB2=$(k -n $NS get pod history-db-0 -o jsonpath='{.metadata.uid}' 2>/dev/null || true); [ -n "$DB2" ] && [ "$DB2" != "$DB1" ] && break; sleep 1; done
k -n $NS wait --for=condition=Ready pod/history-db-0 --timeout=120s >/dev/null
# generation two's lines follow its creation, as real lines do: a minute of
# them, pushed after the minute has passed (Loki takes no future lines)
DB2C=$(k -n $NS get pod history-db-0 -o jsonpath='{.metadata.creationTimestamp}')
DB2CREATED=$(python3 -I -c 'import sys,datetime; print(int(datetime.datetime.strptime(sys.argv[1], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc).timestamp()))' $DB2C)
sleep 60
WEB=$(k -n $NS get pods -l app=history-web -o jsonpath='{.items[0].metadata.name}')
WEBUID=$(k -n $NS get pod $WEB -o jsonpath='{.metadata.uid}')
# history-gone: a bare pod, its UID read, then deleted for good
k -n $NS delete pod history-gone --ignore-not-found --wait=true >/dev/null
k -n $NS run history-gone --image=registry.k8s.io/pause:3.10 --image-pull-policy=IfNotPresent --labels=klyx.dev/lab=s8b,app=history-gone >/dev/null
k -n $NS wait --for=condition=Ready pod/history-gone --timeout=120s >/dev/null
GONE=$(k -n $NS get pod history-gone -o jsonpath='{.metadata.uid}')
k -n $NS delete pod history-gone --grace-period=0 --wait=true >/dev/null
print -r -- "DB1=$DB1 DB2=$DB2 DB2CREATED=$DB2CREATED WEB=$WEB WEBUID=$WEBUID GONE=$GONE" > $dir/s8b-identities.env
zsh ${0:A:h}/push.sh
