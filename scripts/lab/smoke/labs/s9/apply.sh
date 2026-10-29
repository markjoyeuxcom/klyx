#!/bin/zsh
# Coverage S9 lab on kind-klyx: a real KEDA 2.21.0 (the release manifest:
# operator, metrics apiserver, admission webhooks - failurePolicy Ignore,
# KEDA kinds only - and the external.metrics APIService) in keda, and the
# lab's objects in klyx-s9-lab, labelled klyx.dev/lab=s9, ClusterIP only:
#   active-web     cron trigger active all day: Active=True, default HPA name
#   idle-api       cron window 03:00-03:01 UTC: Active=False, healthy idle
#   custom-name    horizontalPodAutoscalerConfig.name: a custom HPA name
#   adopted        an existing HPA, transferred with the ownership annotation
#   paused         autoscaling.keda.sh/paused: "true"
#   paused-at-one  autoscaling.keda.sh/paused-replicas: "1"
#   failing        prometheus at an address nothing answers, with fallback
#   bad-auth       TriggerAuthentication naming a Secret that does not exist
#   no-target      a ScaledObject whose Deployment does not exist
#   sts            a StatefulSet target
#   wl-identity    TriggerAuthentication podIdentity aws, identityOwner workload
#   cluster-auth   ClusterTriggerAuthentication from a Secret in keda
#   batch          ScaledJob, cron trigger active all day
#   ghost-auth     authenticationRef to a TriggerAuthentication that does not exist
#   one-way        autoscaling.keda.sh/paused-scale-in: "true" (a directional pause)
#   hpa-gone       a plain HPA whose Deployment does not exist
#   plain-api      a plain HPA on cpu, with no metrics-server in the lab
#   twice          two plain HPAs on one Deployment
#   shared         a ScaledObject's target that a plain HPA also scales
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 180 kube "$@"; }
k apply --server-side --force-conflicts -f $(fetch https://github.com/kedacore/keda/releases/download/v2.21.0/keda-2.21.0.yaml b43c89ffeef81722d7e2dd2c079d74789767a0f89cae1336cff784994814f6d7 keda-2.21.0.yaml) >/dev/null
k label namespace keda klyx.dev/lab=s9 --overwrite >/dev/null
k -n keda rollout status deploy/keda-operator --timeout=170s >/dev/null
k -n keda rollout status deploy/keda-admission --timeout=170s >/dev/null
k -n keda rollout status deploy/keda-metrics-apiserver --timeout=170s >/dev/null
NS=klyx-s9-lab
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace $NS klyx.dev/lab=s9 --overwrite >/dev/null
dep() { cat <<YAML
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: $1, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  selector: {matchLabels: {app: $1}}
  template:
    metadata: {labels: {app: $1, klyx.dev/lab: s9}}
    spec:
      serviceAccountName: ${2:-default}
      containers: [{name: app, image: registry.k8s.io/pause:3.10, resources: {requests: {cpu: 10m, memory: 16Mi}}}]
YAML
}
always='{type: cron, metadata: {timezone: UTC, start: "0 0 * * *", end: "59 23 * * *", desiredReplicas: "2"}}'
k apply --server-side --force-conflicts --field-manager=klyx-lab -f - >/dev/null <<YAML
apiVersion: v1
kind: ServiceAccount
metadata: {name: wl-identity, namespace: $NS, labels: {klyx.dev/lab: s9}, annotations: {eks.amazonaws.com/role-arn: "arn:aws:iam::111122223333:role/klyx-lab-keda"}}
$(dep active-web)
$(dep idle-api)
$(dep custom-name)
$(dep adopted)
$(dep paused)
$(dep paused-at-one)
$(dep failing)
$(dep bad-auth)
$(dep wl-identity wl-identity)
$(dep cluster-auth)
$(dep ghost-auth)
$(dep one-way)
$(dep plain-api)
$(dep twice)
$(dep shared)
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: sts, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  serviceName: sts
  selector: {matchLabels: {app: sts}}
  template:
    metadata: {labels: {app: sts, klyx.dev/lab: s9}}
    spec: {containers: [{name: app, image: registry.k8s.io/pause:3.10}]}
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: hpa-gone, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: no-such-app}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 80}}}]
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: plain-api, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: plain-api}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 80}}}]
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: twice-a, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: twice}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 80}}}]
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: twice-b, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: twice}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 80}}}]
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: adopted-hpa, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: adopted}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 80}}}]
YAML
k apply --server-side --force-conflicts --field-manager=klyx-lab -f - >/dev/null <<YAML
apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata: {name: missing-secret, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  secretTargetRef: [{parameter: bearerToken, name: no-such-secret, key: token}]
---
apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata: {name: aws-workload, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  podIdentity: {provider: aws, identityOwner: workload}
---
apiVersion: v1
kind: Secret
metadata: {name: klyx-s9-lab-token, namespace: keda, labels: {klyx.dev/lab: s9}}
stringData: {token: lab-not-a-secret}
---
apiVersion: keda.sh/v1alpha1
kind: ClusterTriggerAuthentication
metadata: {name: klyx-s9-lab, labels: {klyx.dev/lab: s9}}
spec:
  secretTargetRef: [{parameter: bearerToken, name: klyx-s9-lab-token, key: token}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: active-web, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: active-web}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: idle-api, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: idle-api}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [{type: cron, metadata: {timezone: UTC, start: "0 3 * * *", end: "1 3 * * *", desiredReplicas: "2"}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: custom-name, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: custom-name}
  advanced: {horizontalPodAutoscalerConfig: {name: web-autoscaler}}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: adopted, namespace: $NS, labels: {klyx.dev/lab: s9}, annotations: {scaledobject.keda.sh/transfer-hpa-ownership: "true"}}
spec:
  scaleTargetRef: {name: adopted}
  advanced: {horizontalPodAutoscalerConfig: {name: adopted-hpa}}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: paused, namespace: $NS, labels: {klyx.dev/lab: s9}, annotations: {autoscaling.keda.sh/paused: "true"}}
spec:
  scaleTargetRef: {name: paused}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: paused-at-one, namespace: $NS, labels: {klyx.dev/lab: s9}, annotations: {autoscaling.keda.sh/paused-replicas: "1"}}
spec:
  scaleTargetRef: {name: paused-at-one}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: failing, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: failing}
  pollingInterval: 10
  minReplicaCount: 1
  maxReplicaCount: 3
  fallback: {failureThreshold: 2, replicas: 2}
  triggers: [{type: prometheus, metadata: {serverAddress: "http://nothing-answers.klyx-s9-lab.svc:9090", query: "vector(1)", threshold: "1"}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: bad-auth, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: bad-auth}
  pollingInterval: 10
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [{type: prometheus, metadata: {serverAddress: "http://nothing-answers.klyx-s9-lab.svc:9090", query: "vector(1)", threshold: "1", authModes: bearer}, authenticationRef: {name: missing-secret}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: no-target, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: no-such-deployment}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: sts, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: StatefulSet, name: sts}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: wl-identity, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: wl-identity}
  pollingInterval: 30
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [{type: aws-sqs-queue, metadata: {queueURL: "https://sqs.eu-west-1.amazonaws.com/111122223333/klyx-lab", queueLength: "5", awsRegion: eu-west-1}, authenticationRef: {name: aws-workload}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: cluster-auth, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: cluster-auth}
  pollingInterval: 30
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [{type: prometheus, metadata: {serverAddress: "http://nothing-answers.klyx-s9-lab.svc:9090", query: "vector(1)", threshold: "1", authModes: bearer}, authenticationRef: {name: klyx-s9-lab, kind: ClusterTriggerAuthentication}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: ghost-auth, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: ghost-auth}
  pollingInterval: 30
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [{type: prometheus, metadata: {serverAddress: "http://nothing-answers.klyx-s9-lab.svc:9090", query: "vector(1)", threshold: "1", authModes: bearer}, authenticationRef: {name: no-such-auth}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: one-way, namespace: $NS, labels: {klyx.dev/lab: s9}, annotations: {autoscaling.keda.sh/paused-scale-in: "true"}}
spec:
  scaleTargetRef: {name: one-way}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledJob
metadata: {name: batch, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  pollingInterval: 30
  maxReplicaCount: 1
  successfulJobsHistoryLimit: 2
  failedJobsHistoryLimit: 2
  jobTargetRef:
    backoffLimit: 0
    template:
      metadata: {labels: {klyx.dev/lab: s9}}
      spec:
        restartPolicy: Never
        containers: [{name: work, image: busybox:1.37, command: [sh, -c, "sleep 5"]}]
  triggers: [$always]
YAML
# the webhook refuses a ScaledObject whose target a plain HPA already scales
# (and refuses its updates): the ScaledObject first, the HPA after, both
# create-only
k create -f - >/dev/null 2>&1 <<YAML || true
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: shared, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {name: shared}
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers: [$always]
YAML
k create -f - >/dev/null 2>&1 <<YAML || true
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: shared-plain, namespace: $NS, labels: {klyx.dev/lab: s9}}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: shared}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 80}}}]
YAML
echo applied
