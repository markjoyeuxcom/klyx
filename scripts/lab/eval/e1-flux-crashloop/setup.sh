#!/usr/bin/env bash
# E1 flux-crashloop's plant (eval design §2): the A1 lab's Kustomization
# a1-web, applied at the steady commit, then attempted at the crash commit,
# whose MODE makes web's container exit at start. Flux's health check then
# times out. The fixture repository's commits are pinned here and in
# scenario.yaml. Needs Flux's controllers in klyx-a1-flux (klyx-eval.sh
# install). Idempotent: a plant already in place is left as it is. The
# objects are the eval's (lib.sh claim): the A1 lab's existing ones only by
# explicit adoption.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
claim namespace klyx-a1-app
claim -n klyx-a1-flux gitrepository a1-fleet
claim -n klyx-a1-flux kustomization a1-web

repo=https://github.com/markjoyeuxcom/klyx-lab-a1
steady=cde214ee840d9cb3f05d4f6b4b2e3bd3702900b3
crash=a9b3c819bc2d9de4231862daa21fe0c97122b980

ks() { k -n klyx-a1-flux get kustomization a1-web -o jsonpath="$1" 2>/dev/null; }
# status.history, not Ready: Ready reads Unknown while Flux retries
planted() {
	[[ $(ks '{.status.lastAttemptedRevision}') == "main@sha1:$crash" &&
		$(ks '{.status.lastAppliedRevision}') == "main@sha1:$steady" &&
		$(ks '{.status.history[0].metadata.revision}') == "main@sha1:$crash" &&
		$(ks '{.status.history[0].lastReconciledStatus}') == HealthCheckFailed ]]
}
applied_steady() {
	[[ $(ks '{.status.lastAppliedRevision}') == "main@sha1:$steady" && $(ks '{.status.conditions[?(@.type=="Ready")].status}') == True ]]
}

if planted; then
	echo "e1: planted"
	exit 0
fi

source_at() {
	k apply -f - <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: klyx-a1-app
  labels:
    klyx.dev/lab: a1
    klyx.dev/eval-owner: "$(owner_id)"
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: a1-fleet
  namespace: klyx-a1-flux
  labels:
    klyx.dev/lab: a1
    klyx.dev/eval-owner: "$(owner_id)"
spec:
  interval: 1m
  url: $repo
  ref:
    branch: main
    commit: $1
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: a1-web
  namespace: klyx-a1-flux
  labels:
    klyx.dev/lab: a1
    klyx.dev/eval-owner: "$(owner_id)"
spec:
  interval: 2m
  sourceRef:
    kind: GitRepository
    name: a1-fleet
  path: ./apps/web
  targetNamespace: klyx-a1-app
  prune: true
  wait: true
  timeout: 90s
YAML
}

source_at "$steady"
k -n klyx-a1-flux annotate --overwrite kustomization a1-web reconcile.fluxcd.io/requestedAt="$(date +%s)" >/dev/null
wait_for 240 "a1-web applied at the steady commit" applied_steady
source_at "$crash"
k -n klyx-a1-flux annotate --overwrite gitrepository a1-fleet reconcile.fluxcd.io/requestedAt="$(date +%s)" >/dev/null
k -n klyx-a1-flux annotate --overwrite kustomization a1-web reconcile.fluxcd.io/requestedAt="$(date +%s)" >/dev/null
wait_for 300 "a1-web's health check failing at the crash commit" planted
echo "e1: planted"
