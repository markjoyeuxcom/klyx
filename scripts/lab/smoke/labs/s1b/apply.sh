#!/bin/zsh
# Coverage S1b lab fixtures on kind-klyx: Flux image automation with status
# written through the status subresource (no controller runs), stand-ins for
# both image controllers, and a push event bound to the automation. Every
# write targets kind-klyx only.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
OLD=2026-10-15T08:00:00Z

for c in image-reflector-controller image-automation-controller; do
k apply -f - <<EOF
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
EOF
done

k apply -f - <<'EOF'
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageRepository
metadata: {name: app, namespace: team-a}
spec: {image: ghcr.io/example/app, interval: 1h, provider: generic, secretRef: {name: ghcr-pull}}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageRepository
metadata: {name: private, namespace: team-a}
spec: {image: registry.example.com/team-a/private, interval: 5m, secretRef: {name: registry-creds}}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageRepository
metadata: {name: stale-scan, namespace: team-b}
spec: {image: ghcr.io/example/worker, interval: 10m}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImagePolicy
metadata: {name: app, namespace: team-a, labels: {automation: team-a}}
spec: {imageRepositoryRef: {name: app}, policy: {semver: {range: ">=1.0.0"}}}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImagePolicy
metadata: {name: app-prerelease, namespace: team-a, labels: {automation: team-a}}
spec: {imageRepositoryRef: {name: app}, policy: {semver: {range: ">=2.0.0-rc.0"}}, filterTags: {pattern: '^v(?P<version>.*)-rc\..*$', extract: '$version'}}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImagePolicy
metadata: {name: api, namespace: team-a, labels: {automation: team-a}}
spec: {imageRepositoryRef: {name: api-images}, policy: {alphabetical: {order: asc}}}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageUpdateAutomation
metadata: {name: team-a-images, namespace: team-a, labels: {kustomize.toolkit.fluxcd.io/name: team-a-shared, kustomize.toolkit.fluxcd.io/namespace: flux-system}}
spec:
  interval: 30m
  sourceRef: {kind: GitRepository, name: images-fleet}
  git:
    checkout: {ref: {branch: main}}
    push: {branch: auto}
    commit: {author: {name: fluxbot, email: fluxbot@example.com}, messageTemplate: "Update images (lab)"}
  policySelector: {matchLabels: {automation: team-a}}
  update: {strategy: Setters, path: ./apps}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageUpdateAutomation
metadata: {name: team-b-images, namespace: team-b}
spec:
  interval: 30m
  suspend: true
  sourceRef: {kind: GitRepository, name: repo}
  git: {push: {branch: main}, commit: {author: {name: fluxbot, email: fluxbot@example.com}}}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata: {name: images-fleet, namespace: team-a}
spec: {interval: 5m, url: https://git.example.com/team-a/fleet.git, ref: {branch: main}}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: images-apps, namespace: team-a}
spec: {interval: 10m, path: ./apps, prune: false, sourceRef: {kind: GitRepository, name: images-fleet}}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: team-a-legacy, namespace: team-a}
spec: {interval: 10m, path: ./legacy, prune: false, sourceRef: {kind: GitRepository, name: images-fleet}}
EOF

cond() { # type status reason message observedGeneration time
  print -r -- "{\"type\":\"$1\",\"status\":\"$2\",\"reason\":\"$3\",\"message\":\"$4\",\"observedGeneration\":$5,\"lastTransitionTime\":\"${6:-$NOW}\"}"
}
st() { k patch "$1" "$2" -n "$3" --subresource=status --type=merge -p "$4" >/dev/null; }

st imagerepository app team-a "{\"status\":{\"observedGeneration\":1,\"canonicalImageName\":\"ghcr.io/example/app\",\"conditions\":[$(cond Ready True Succeeded 'successful scan: found 14 tags' 1)],
  \"lastScanResult\":{\"revision\":\"sha256:5e1f\",\"tagCount\":14,\"scanTime\":\"$NOW\",\"latestTags\":[\"1.4.2\",\"1.4.1\",\"1.4.0\",\"1.3.9\",\"2.0.0-rc.1\"]}}}"
st imagerepository private team-a "{\"status\":{\"observedGeneration\":1,\"conditions\":[$(cond Ready False AuthenticationFailed 'failed to configure authentication options: secret team-a/registry-creds not found' 1)]}}"
st imagerepository stale-scan team-b "{\"status\":{\"observedGeneration\":1,\"canonicalImageName\":\"ghcr.io/example/worker\",\"conditions\":[$(cond Ready True Succeeded 'successful scan: found 6 tags' 1 $OLD)],
  \"lastScanResult\":{\"revision\":\"sha256:9a0b\",\"tagCount\":6,\"scanTime\":\"$OLD\",\"latestTags\":[\"0.9.1\",\"0.9.0\"]}}}"
st imagepolicy app team-a "{\"status\":{\"observedGeneration\":1,\"conditions\":[$(cond Ready True Succeeded "Latest image tag for ghcr.io/example/app resolved to 1.4.2" 1)],
  \"latestRef\":{\"name\":\"ghcr.io/example/app\",\"tag\":\"1.4.2\",\"digest\":\"sha256:0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c\"},\"observedPreviousRef\":{\"name\":\"ghcr.io/example/app\",\"tag\":\"1.4.1\"}}}"
st imagepolicy app-prerelease team-a "{\"status\":{\"observedGeneration\":1,\"conditions\":[$(cond Ready False Failure 'cannot determine latest tag for policy: version list argument cannot be empty' 1)],
  \"latestRef\":{\"name\":\"ghcr.io/example/app\",\"tag\":\"2.0.0-rc.1\"}}}"
st imagepolicy api team-a "{\"status\":{\"observedGeneration\":1,\"conditions\":[$(cond Ready False DependencyNotReady 'retrying in 30s: referenced ImageRepository team-a/api-images not found' 1)]}}"
G=$(k -n team-a get imageupdateautomation team-a-images -o jsonpath='{.metadata.generation}')
st imageupdateautomation team-a-images team-a "{\"status\":{\"observedGeneration\":$G,\"conditions\":[$(cond Ready True Succeeded 'repository up-to-date' $G)],
  \"lastAutomationRunTime\":\"$NOW\",\"lastPushCommit\":\"4f2c9e1\",\"lastPushTime\":\"$NOW\",\"observedSourceRevision\":\"main@sha1:4f2c9e1\",
  \"observedPolicies\":{\"app\":{\"name\":\"ghcr.io/example/app\",\"tag\":\"1.4.1\"},\"app-prerelease\":{\"name\":\"ghcr.io/example/app\",\"tag\":\"2.0.0-rc.1\"}}}}"
st imageupdateautomation team-b-images team-b "{\"status\":{\"observedGeneration\":1,\"conditions\":[$(cond Ready True Succeeded 'no change since last reconciliation' 1)]}}"
st gitrepository images-fleet team-a "{\"status\":{\"observedGeneration\":1,\"artifact\":{\"revision\":\"main@sha1:4f2c9e1\",\"digest\":\"sha256:aa\",\"path\":\"gitrepository/team-a/images-fleet/4f2c9e1.tar.gz\",\"url\":\"http://source-controller.flux-system.svc.cluster.local./gitrepository/team-a/images-fleet/4f2c9e1.tar.gz\",\"lastUpdateTime\":\"$NOW\"},\"conditions\":[$(cond Ready True Succeeded "stored artifact for revision 'main@sha1:4f2c9e1'" 1)]}}"
st kustomization images-apps team-a "{\"status\":{\"observedGeneration\":1,\"lastAppliedRevision\":\"main@sha1:4f2c9e1\",\"conditions\":[$(cond Ready True ReconciliationSucceeded 'Applied revision: main@sha1:4f2c9e1' 1)]}}"
st kustomization team-a-legacy team-a "{\"status\":{\"observedGeneration\":1,\"lastAppliedRevision\":\"main@sha1:1a2b3c4\",\"conditions\":[$(cond Ready True ReconciliationSucceeded 'Applied revision: main@sha1:1a2b3c4' 1)]}}"

# the controller's push event, bound to the automation by UID
UID_AUTO=$(k -n team-a get imageupdateautomation team-a-images -o jsonpath='{.metadata.uid}')
k -n team-a delete event team-a-images.push --ignore-not-found >/dev/null
k apply -f - <<EOF
apiVersion: v1
kind: Event
metadata: {name: team-a-images.push, namespace: team-a}
involvedObject: {apiVersion: image.toolkit.fluxcd.io/v1, kind: ImageUpdateAutomation, name: team-a-images, namespace: team-a, uid: $UID_AUTO}
reason: Succeeded
type: Normal
message: "pushed commit '4f2c9e1' to branch 'auto'\nUpdate images (lab)"
source: {component: image-automation-controller}
firstTimestamp: "$NOW"
lastTimestamp: "$NOW"
count: 1
EOF
echo "s1b fixtures applied"
