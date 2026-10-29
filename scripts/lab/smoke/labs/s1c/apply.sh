#!/bin/zsh
# Coverage S1c lab fixtures on kind-klyx: source composition and Flux
# notifications with status written through the status subresource (no
# controller runs), stand-ins for source-watcher and notification-
# controller, and Events whose text must never reach the screen. Every
# protected value carries the needle S1cLABneedle. Every write targets
# kind-klyx only.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
N=S1cLABneedle
NS=team-a
HEX=$(printf 'a1%.0s' {1..32})
SHA=$(printf 'c4%.0s' {1..20})

# the ArtifactGenerator CRD, pinned source-watcher v2.2.4 (read before use)
# source-watcher v2.2.4's ArtifactGenerator CRD, pinned by its digest
k apply -f $(fetch https://raw.githubusercontent.com/fluxcd/source-watcher/v2.2.4/config/crd/bases/source.extensions.fluxcd.io_artifactgenerators.yaml 1f8df07b14c8ed84f747fd11913ed34e7f212d24872ae94c96fd4010bb4cc504 source-watcher-v2.2.4-artifactgenerators.yaml) >/dev/null
k wait --for condition=established --timeout=60s crd/artifactgenerators.source.extensions.fluxcd.io >/dev/null

for c in source-watcher notification-controller; do
k apply -f - >/dev/null <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: {name: $c, namespace: flux-system, labels: {app.kubernetes.io/component: $c}}
spec:
  replicas: 1
  selector: {matchLabels: {app: $c}}
  template:
    metadata: {labels: {app: $c}}
    spec:
      containers: [{name: manager, image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent}]
YAML
done

k apply -f - >/dev/null <<YAML
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata: {name: apps, namespace: $NS}
spec:
  sources: [{alias: fleet, kind: GitRepository, name: images-fleet}]
  artifacts: [{name: apps-a, copy: [{from: "@fleet/apps/**", to: "@artifact/"}]}]
---
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata: {name: broken, namespace: $NS}
spec:
  sources: [{alias: fleet, kind: GitRepository, name: images-fleet}]
  artifacts: [{name: broken-a, copy: [{from: "@fleet/broken/**", to: "@artifact/"}]}]
---
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata: {name: paused, namespace: $NS, annotations: {source.extensions.fluxcd.io/reconcile: disabled}}
spec:
  sources: [{alias: fleet, kind: GitRepository, name: images-fleet}]
  artifacts: [{name: paused-a, copy: [{from: "@fleet/paused/**", to: "@artifact/"}]}]
---
apiVersion: notification.toolkit.fluxcd.io/v1
kind: Receiver
metadata: {name: github, namespace: $NS}
spec: {type: github, secretRef: {name: webhook-token}, events: [push], resources: [{kind: GitRepository, name: images-fleet}]}
---
apiVersion: notification.toolkit.fluxcd.io/v1
kind: Receiver
metadata: {name: broken, namespace: $NS}
spec: {type: generic, secretRef: {name: missing-token}, resources: [{kind: GitRepository, name: images-fleet}]}
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata: {name: slack, namespace: $NS}
spec: {type: slack, channel: alerts, address: "https://hooks.slack.com/services/T0/B0/$N", secretRef: {name: slack-url}}
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata: {name: paused, namespace: $NS}
spec: {type: generic, address: "https://hooks.example.com/$N", suspend: true}
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata: {name: on-call, namespace: $NS}
spec: {providerRef: {name: slack}, eventSeverity: error, eventSources: [{kind: Kustomization, name: "*"}], eventMetadata: {cluster: kind-klyx}}
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata: {name: orphan, namespace: $NS}
spec: {providerRef: {name: gone}, eventSources: [{kind: GitRepository, name: "*"}]}
YAML

AGUID=$(k -n $NS get artifactgenerators.source.extensions.fluxcd.io apps -o jsonpath='{.metadata.uid}')
k apply -f - >/dev/null <<YAML
apiVersion: source.toolkit.fluxcd.io/v1
kind: ExternalArtifact
metadata: {name: apps-a, namespace: $NS, labels: {source.extensions.fluxcd.io/generator: "$AGUID"}}
spec: {sourceRef: {apiVersion: source.extensions.fluxcd.io/v1beta1, kind: ArtifactGenerator, name: apps}}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: ExternalArtifact
metadata: {name: signed, namespace: $NS}
spec: {sourceRef: {apiVersion: ci.example/v1, kind: Pipeline, name: build}}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: ExternalArtifact
metadata: {name: empty, namespace: $NS, labels: {source.extensions.fluxcd.io/generator: "00000000-dead-beef-0000-000000000000"}}
spec: {}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: artifact-apps, namespace: $NS}
spec: {interval: 10m, path: ./, prune: false, sourceRef: {kind: ExternalArtifact, name: apps-a}}
YAML

gen() { k -n $NS get "$1" "$2" -o jsonpath='{.metadata.generation}'; }
cond() { # type status reason message observedGeneration
  print -r -- "{\"type\":\"$1\",\"status\":\"$2\",\"reason\":\"$3\",\"message\":\"$4\",\"observedGeneration\":$5,\"lastTransitionTime\":\"$NOW\"}"
}
st() { k -n $NS patch "$1" "$2" --subresource=status --type=merge -p "{\"status\":$3}" >/dev/null; }

AG=artifactgenerators.source.extensions.fluxcd.io
st $AG apps "{\"conditions\":[$(cond Ready True Succeeded "reconciliation succeeded, generated 1 artifact(s)" $(gen $AG apps))],\"inventory\":[{\"name\":\"apps-a\",\"namespace\":\"$NS\",\"digest\":\"sha256:$HEX\",\"filename\":\"$HEX.tar.gz\"}]}"
st $AG broken "{\"conditions\":[$(cond Ready False SourceFetchFailed "fetch sources failed: failed to download archive: GET https://store.example/signed/$N giving up after 1 attempt(s)" $(gen $AG broken))]}"
st $AG paused "{\"conditions\":[$(cond Ready True ReconciliationDisabled "reconciliation is disabled" $(gen $AG paused))]}"

EA=externalartifacts.source.toolkit.fluxcd.io
st $EA apps-a "{\"artifact\":{\"revision\":\"latest@sha256:$HEX\",\"digest\":\"sha256:$HEX\",\"path\":\"$HEX.tar.gz\",\"url\":\"http://source-watcher.flux-system.svc.cluster.local./externalartifact/$NS/apps-a/$HEX.tar.gz\",\"lastUpdateTime\":\"$NOW\",\"size\":2048,\"metadata\":{\"org.opencontainers.image.revision\":\"main@sha1:$SHA\",\"note\":\"$N\"}},\"conditions\":[$(cond Ready True Succeeded "Artifact is ready" 1)]}"
st $EA signed "{\"artifact\":{\"revision\":\"build-42@sha256:$HEX\",\"digest\":\"sha256:$HEX\",\"path\":\"a.tgz\",\"url\":\"https://store.example/t/$N/a.tgz?sig=$N\",\"lastUpdateTime\":\"$NOW\"},\"conditions\":[$(cond Ready True Succeeded "ok" 1)]}"

KS=kustomizations.kustomize.toolkit.fluxcd.io
st $KS artifact-apps "{\"observedGeneration\":$(gen $KS artifact-apps),\"lastAppliedRevision\":\"latest@sha256:$HEX\",\"lastAttemptedRevision\":\"latest@sha256:$HEX\",\"conditions\":[$(cond Ready True ReconciliationSucceeded "Applied revision: latest@sha256:$HEX" $(gen $KS artifact-apps))]}"

RC=receivers.notification.toolkit.fluxcd.io
st $RC github "{\"observedGeneration\":$(gen $RC github),\"webhookPath\":\"/hook/$N\",\"conditions\":[$(cond Ready True Succeeded "Receiver initialized for path: /hook/$N" $(gen $RC github))]}"
st $RC broken "{\"observedGeneration\":$(gen $RC broken),\"conditions\":[$(cond Ready False TokenNotFound "unable to read token from secret '$NS/missing-token' error: $N" $(gen $RC broken))]}"

# events whose text must never reach the screen
ALUID=$(k -n $NS get alerts.notification.toolkit.fluxcd.io on-call -o jsonpath='{.metadata.uid}')
RCUID=$(k -n $NS get $RC github -o jsonpath='{.metadata.uid}')
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Event
metadata: {name: on-call.s1c-dispatch, namespace: $NS}
involvedObject: {apiVersion: notification.toolkit.fluxcd.io/v1beta3, kind: Alert, name: on-call, namespace: $NS, uid: "$ALUID"}
reason: NotificationDispatchFailed
message: "failed to send to https://hooks.slack.com/services/T0/B0/$N: 404 body $N"
type: Warning
count: 2
firstTimestamp: "$NOW"
lastTimestamp: "$NOW"
source: {component: notification-controller}
---
apiVersion: v1
kind: Event
metadata: {name: github.s1c-ready, namespace: $NS}
involvedObject: {apiVersion: notification.toolkit.fluxcd.io/v1, kind: Receiver, name: github, namespace: $NS, uid: "$RCUID"}
reason: Succeeded
message: "Receiver initialized for path: /hook/$N"
type: Normal
count: 1
firstTimestamp: "$NOW"
lastTimestamp: "$NOW"
source: {component: notification-controller}
YAML
echo "lab-s1c: applied on kind-klyx"
