#!/bin/zsh
# Coverage S8a lab on kind-klyx: two real Alertmanagers (0.34.1), ClusterIP
# only, in klyx-s8-lab, labelled klyx.dev/lab=s8a.
#   alertmanager            route prefix /: an inhibition rule
#                           (KlyxLabCritical inhibits KlyxLabWarning of the
#                           same namespace) and an always-on mute time
#                           interval for klyx_lab_mute="true", and a
#                           partly muted route pair for klyx_lab_partmute
#   alertmanager-prefixed   route prefix /alertmanager (serviceRef.path)
#   alerted-web             one pause pod, the subject of the lab's pod and
#                           deployment alerts
# alerts.sh posts the synthetic alerts and the silence (lab setup through
# Alertmanager's own API - Klyx never posts).
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 120 kube "$@"; }
NS=klyx-s8-lab
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace $NS klyx.dev/lab=s8a --overwrite >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: ConfigMap
metadata: {name: alertmanager-config, namespace: $NS, labels: {klyx.dev/lab: s8a}}
data:
  alertmanager.yml: |
    route:
      receiver: lab
      group_by: [alertname]
      routes:
        - matchers: ['klyx_lab_mute="true"']
          receiver: lab-muted
          mute_time_intervals: [always]
        # partly muted: one route muted, a second (continue) not, so the
        # alert's receivers are [lab-muted, lab] and only one is muted
        - matchers: ['klyx_lab_partmute="true"']
          receiver: lab-muted
          mute_time_intervals: [always]
          continue: true
        - matchers: ['klyx_lab_partmute="true"']
          receiver: lab
    receivers:
      - name: lab
      # the mute stage runs in a receiver's notify pipeline, which a receiver
      # with no integration does not have; this one is always muted, so the
      # discard-port webhook is never called
      - name: lab-muted
        webhook_configs:
          - url: http://127.0.0.1:9/
            send_resolved: false
    inhibit_rules:
      - source_matchers: ['alertname="KlyxLabCritical"']
        target_matchers: ['alertname="KlyxLabWarning"']
        equal: [namespace]
    time_intervals:
      - name: always
        time_intervals:
          - times:
              - start_time: "00:00"
                end_time: "24:00"
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: alertmanager, namespace: $NS, labels: {klyx.dev/lab: s8a}}
spec:
  replicas: 1
  selector: {matchLabels: {app: alertmanager}}
  template:
    metadata: {labels: {app: alertmanager, klyx.dev/lab: s8a}, annotations: {klyx.dev/config-rev: "3"}}
    spec:
      containers:
        - name: alertmanager
          image: quay.io/prometheus/alertmanager:v0.34.1
          args: [--config.file=/etc/am/alertmanager.yml, --storage.path=/data, --cluster.listen-address=]
          ports: [{name: web, containerPort: 9093}]
          volumeMounts: [{name: config, mountPath: /etc/am}, {name: data, mountPath: /data}]
          readinessProbe: {httpGet: {path: /-/ready, port: web}}
      volumes: [{name: config, configMap: {name: alertmanager-config}}, {name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: alertmanager, namespace: $NS, labels: {klyx.dev/lab: s8a}}
spec:
  selector: {app: alertmanager}
  ports: [{name: web, port: 9093, targetPort: web}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: alertmanager-prefixed, namespace: $NS, labels: {klyx.dev/lab: s8a}}
spec:
  replicas: 1
  selector: {matchLabels: {app: alertmanager-prefixed}}
  template:
    metadata: {labels: {app: alertmanager-prefixed, klyx.dev/lab: s8a}, annotations: {klyx.dev/config-rev: "3"}}
    spec:
      containers:
        - name: alertmanager
          image: quay.io/prometheus/alertmanager:v0.34.1
          args: [--config.file=/etc/am/alertmanager.yml, --storage.path=/data, --cluster.listen-address=, --web.route-prefix=/alertmanager]
          ports: [{name: web, containerPort: 9093}]
          volumeMounts: [{name: config, mountPath: /etc/am}, {name: data, mountPath: /data}]
          readinessProbe: {httpGet: {path: /alertmanager/-/ready, port: web}}
      volumes: [{name: config, configMap: {name: alertmanager-config}}, {name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: alertmanager-prefixed, namespace: $NS, labels: {klyx.dev/lab: s8a}}
spec:
  selector: {app: alertmanager-prefixed}
  ports: [{name: web, port: 9093, targetPort: web}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: alerted-web, namespace: $NS, labels: {klyx.dev/lab: s8a}}
spec:
  replicas: 1
  selector: {matchLabels: {app: alerted-web}}
  template:
    metadata: {labels: {app: alerted-web, klyx.dev/lab: s8a}}
    spec:
      containers: [{name: web, image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent}]
YAML
k -n $NS rollout status deploy/alertmanager --timeout=180s >/dev/null
k -n $NS rollout status deploy/alertmanager-prefixed --timeout=180s >/dev/null
k -n $NS rollout status deploy/alerted-web --timeout=120s >/dev/null
zsh ${0:A:h}/alerts.sh
