#!/usr/bin/env bash
# E2 healthy-control's plant (eval design §2): a Deployment of pause that
# runs, in the eval's own namespace.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
claim namespace klyx-eval
claim -n klyx-eval deployment steady
k apply -f - <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: klyx-eval
  labels:
    klyx.dev/lab: eval
    klyx.dev/eval-owner: "$(owner_id)"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: steady
  namespace: klyx-eval
  labels:
    klyx.dev/lab: eval
    klyx.dev/eval-owner: "$(owner_id)"
spec:
  replicas: 1
  selector:
    matchLabels: {app: steady}
  template:
    metadata:
      labels: {app: steady, klyx.dev/lab: eval}
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.10
        resources:
          requests: {cpu: 5m, memory: 8Mi}
          limits: {memory: 32Mi}
YAML
k -n klyx-eval rollout status deploy/steady --timeout=180s
echo "e2: planted"
