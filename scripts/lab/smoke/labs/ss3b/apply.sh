#!/bin/zsh
# The Settings S3b lab on kind-klyx (S3b §9's native walk): a Prometheus,
# pinned, in its own labelled namespace, scraping itself, every series
# relabelled cluster=kind-klyx so a member label scopes it. Its Service
# matches none of discovery's names or selectors, so the lab cluster's own
# metrics are read as before; only an installation names it.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 120 kube "$@"; }
NS=klyx-ss3b-lab
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace $NS klyx.dev/lab=ss3b --overwrite >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: ConfigMap
metadata: {name: lab-prometheus, namespace: $NS, labels: {klyx.dev/lab: ss3b}}
data:
  prometheus.yml: |
    global: {scrape_interval: 15s}
    scrape_configs:
      - job_name: lab
        static_configs: [{targets: ['localhost:9090']}]
        relabel_configs:
          - {target_label: cluster, replacement: kind-klyx}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: lab-prometheus, namespace: $NS, labels: {klyx.dev/lab: ss3b}}
spec:
  replicas: 1
  selector: {matchLabels: {app: lab-prometheus}}
  template:
    metadata: {labels: {app: lab-prometheus, klyx.dev/lab: ss3b}}
    spec:
      terminationGracePeriodSeconds: 0
      containers:
        - name: prometheus
          image: quay.io/prometheus/prometheus:v2.53.0
          imagePullPolicy: IfNotPresent
          args: [--config.file=/etc/prometheus/prometheus.yml, --storage.tsdb.path=/prometheus, --storage.tsdb.retention.time=2h]
          ports: [{name: web, containerPort: 9090}]
          readinessProbe: {httpGet: {path: /-/ready, port: web}, periodSeconds: 5}
          volumeMounts: [{name: config, mountPath: /etc/prometheus}, {name: data, mountPath: /prometheus}]
      volumes: [{name: config, configMap: {name: lab-prometheus}}, {name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: lab-prometheus, namespace: $NS, labels: {klyx.dev/lab: ss3b}}
spec:
  selector: {app: lab-prometheus}
  ports: [{name: web, port: 9090, targetPort: web}]
YAML
k -n $NS rollout status deploy/lab-prometheus --timeout=90s >/dev/null
echo "lab-ss3b: Service $NS/lab-prometheus:web"
