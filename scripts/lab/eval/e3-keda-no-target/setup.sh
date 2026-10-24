#!/usr/bin/env bash
# E3 keda-no-target's plant (eval design §2), in the eval's own namespace:
# a ScaledObject whose scaleTargetRef names a Deployment that does not
# exist, and two lookalikes that fail with the same condition reason for
# another cause: bad-auth (a TriggerAuthentication naming a missing Secret)
# and ghost-auth (an authenticationRef to a TriggerAuthentication that does
# not exist). Needs KEDA (klyx-eval.sh install).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
k get crd scaledobjects.keda.sh >/dev/null 2>&1 || die "KEDA is not installed on $ctx (klyx-eval.sh install)"
NS=klyx-eval-e3
claim namespace $NS
for o in "scaledobject no-target" "scaledobject bad-auth" "scaledobject ghost-auth" "triggerauthentication missing-secret" "deployment bad-auth" "deployment ghost-auth"; do
	claim -n $NS $o
done
own="klyx.dev/lab: eval, klyx.dev/eval-owner: \"$(owner_id)\""
dep() {
	cat <<YAML
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: $1, namespace: $NS, labels: {$own}}
spec:
  selector: {matchLabels: {app: $1}}
  template:
    metadata: {labels: {app: $1}}
    spec: {containers: [{name: app, image: registry.k8s.io/pause:3.10, resources: {requests: {cpu: 5m, memory: 8Mi}}}]}
YAML
}
trigger='{type: prometheus, metadata: {serverAddress: "http://nothing-answers.klyx-eval-e3.svc:9090", query: "vector(1)", threshold: "1", authModes: bearer}'
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Namespace
metadata: {name: $NS, labels: {$own}}
$(dep bad-auth)
$(dep ghost-auth)
---
apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata: {name: missing-secret, namespace: $NS, labels: {$own}}
spec:
  secretTargetRef: [{parameter: bearerToken, name: no-such-secret, key: token}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: no-target, namespace: $NS, labels: {$own}}
spec:
  scaleTargetRef: {name: no-such-deployment}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [{type: cron, metadata: {timezone: UTC, start: "0 0 * * *", end: "59 23 * * *", desiredReplicas: "2"}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: bad-auth, namespace: $NS, labels: {$own}}
spec:
  scaleTargetRef: {name: bad-auth}
  pollingInterval: 30
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$trigger, authenticationRef: {name: missing-secret}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: ghost-auth, namespace: $NS, labels: {$own}}
spec:
  scaleTargetRef: {name: ghost-auth}
  pollingInterval: 30
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$trigger, authenticationRef: {name: no-such-auth}}]
YAML
reason() { k -n $NS get scaledobject "$1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}' 2>/dev/null; }
planted() { [[ $(reason no-target) == ScaledObjectCheckFailed && $(reason bad-auth) == ScaledObjectCheckFailed && $(reason ghost-auth) == ScaledObjectCheckFailed ]]; }
wait_for 180 "the three ScaledObjects' checks failing" planted
echo "e3: planted"
