#!/bin/zsh
# Coverage S6 lab on kind-klyx (v1.37, a real kubelet): in-place resize and
# ephemeral containers, the behaviour itself (roadmap C6). Every pod runs
# pause; nothing is exposed.
#   resize-done        cpu 100m/200m resized to 150m/300m: completes, its
#                      generation acknowledged
#   debug-target       runAsUser 65534, so restricted (non-root, no numeric
#                      user) can start busybox; two debug containers:
#                      dbg-failed (general, exits 3) and dbg-running
#                      (restricted, sleeps)
# Not in the lab, covered by unit, golden and projection-parity tests:
#   Infeasible: on 1.37 the API server refuses a resize past the node's
#     allocatable ("Forbidden: node didn't have enough allocatable
#     resources"), so Infeasible comes only from kubelet-side policies
#     (static CPU or memory manager) kind does not run; and the kubelet
#     owns pod status, so a status-patched stand-in would not hold.
#   Deferred: the scheduler reserves a Deferred resize's desired amount
#     (its own rule), which would starve this single node and the other
#     labs.
#   An actuation Error.
# Re-running recreates the pods (resize and ephemeral containers are
# one-way).
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 90 kube "$@"; }
NS=klyx-resize-lab
PAUSE='image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent'

k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k delete pod -n $NS --all --wait=true --timeout=60s >/dev/null 2>&1 || true
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: resize-done, namespace: $NS, labels: {klyx.dev/lab: s6}}
spec:
  terminationGracePeriodSeconds: 1
  containers: [{name: app, $PAUSE, resources: {requests: {cpu: 100m, memory: 32Mi}, limits: {cpu: 200m, memory: 64Mi}}}]
---
apiVersion: v1
kind: Pod
metadata: {name: debug-target, namespace: $NS, labels: {klyx.dev/lab: s6}}
spec:
  terminationGracePeriodSeconds: 1
  securityContext: {runAsUser: 65534, runAsGroup: 65534}
  containers: [{name: app, $PAUSE, resources: {requests: {cpu: 10m, memory: 16Mi}}}]
YAML
k wait --for=condition=Ready pod -l klyx.dev/lab=s6 -n $NS --timeout=90s >/dev/null

k patch pod resize-done -n $NS --subresource resize \
	-p '{"spec":{"containers":[{"name":"app","resources":{"requests":{"cpu":"150m"},"limits":{"cpu":"300m"}}}]}}' >/dev/null

k debug debug-target -n $NS --image=busybox:1.36 --target=app --profile=general -c dbg-failed -- sh -c 'exit 3' >/dev/null 2>&1
k debug debug-target -n $NS --image=busybox:1.36 --target=app --profile=restricted -c dbg-running -- sleep 86400 >/dev/null 2>&1

# settle: the resize actuated and acknowledged, the debug containers'
# states reported
for i in {1..30}; do
	done_cpu=$(k get pod resize-done -n $NS -o jsonpath='{.status.containerStatuses[0].resources.requests.cpu}')
	acked=$(k get pod resize-done -n $NS -o jsonpath='{.metadata.generation}/{.status.observedGeneration}')
	failed=$(k get pod debug-target -n $NS -o jsonpath='{.status.ephemeralContainerStatuses[?(@.name=="dbg-failed")].state.terminated.exitCode}')
	running=$(k get pod debug-target -n $NS -o jsonpath='{.status.ephemeralContainerStatuses[?(@.name=="dbg-running")].state.running.startedAt}')
	[ "$done_cpu" = 150m ] && [ "$acked" = 2/2 ] && [ "$failed" = 3 ] && [ -n "$running" ] && break
	sleep 2
done
echo "lab-s6: resize-done cpu=$done_cpu generation/observed=$acked, dbg-failed exit=$failed, dbg-running since=$running"
