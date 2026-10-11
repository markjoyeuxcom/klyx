#!/usr/bin/env bash
# klyx-load.sh - the removable 1c §7 lab fixture on a LOCAL kind cluster:
# namespaces klyx-load-00..09 with 1000 Deployments x 5 replicas whose pods
# can never schedule (an unsatisfiable node selector) - 5000 Pending pods
# that cost the cluster no workload.
#
#   scripts/lab/klyx-load.sh up      [context]   create the fixture and wait for 5000 pods
#   scripts/lab/klyx-load.sh status  [context]   count fixture objects
#   scripts/lab/klyx-load.sh down    [context]   delete every klyx-load-* namespace
#
# It refuses any context that is not a kind cluster on a loopback API server,
# and only ever touches namespaces labelled klyx.dev/lab=load.
set -euo pipefail

cmd=${1:-}
ctx=${2:-kind-klyx}
namespaces=10
per_ns=100
replicas=5

die() { echo "klyx-load: $*" >&2; exit 1; }

guard() {
	[[ $ctx == kind-* ]] || die "refusing context '$ctx': only kind-* contexts"
	local server
	server=$(kubectl config view --context "$ctx" --minify -o jsonpath='{.clusters[0].cluster.server}')
	[[ $server =~ ^https://(127\.0\.0\.1|localhost|\[::1\]): ]] || die "refusing '$ctx': API server $server is not loopback"
}

k() { kubectl --context "$ctx" "$@"; }

manifest() {
	for ((n = 0; n < namespaces; n++)); do
		ns=$(printf 'klyx-load-%02d' "$n")
		cat <<EOF
---
apiVersion: v1
kind: Namespace
metadata:
  name: $ns
  labels: {klyx.dev/lab: load}
EOF
		for ((i = 0; i < per_ns; i++)); do
			name=$(printf 'load-%03d' "$i")
			cat <<EOF
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $name
  namespace: $ns
  labels: {klyx.dev/lab: load, app: $name}
spec:
  replicas: $replicas
  selector: {matchLabels: {app: $name}}
  template:
    metadata:
      labels: {klyx.dev/lab: load, app: $name, tier: load, team: platform, env: lab}
    spec:
      nodeSelector: {klyx.dev/load: unschedulable}
      terminationGracePeriodSeconds: 0
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.10
          resources: {requests: {cpu: 1m, memory: 1Mi}}
EOF
		done
	done
}

count_pods() { k get pods -A -l klyx.dev/lab=load --no-headers 2>/dev/null | wc -l | tr -d ' '; }

case $cmd in
up)
	guard
	manifest | k apply --server-side --field-manager klyx-load -f - >/dev/null
	want=$((namespaces * per_ns * replicas))
	echo "klyx-load: applied $((namespaces * per_ns)) Deployments in $namespaces namespaces; waiting for $want pods"
	for _ in $(seq 1 180); do
		got=$(count_pods)
		[[ $got -ge $want ]] && { echo "klyx-load: $got pods (Pending by design)"; exit 0; }
		sleep 5
	done
	die "only $(count_pods)/$want pods after 15m"
	;;
status)
	guard
	echo "namespaces: $(k get ns -l klyx.dev/lab=load --no-headers 2>/dev/null | wc -l | tr -d ' ')"
	echo "deployments: $(k get deploy -A -l klyx.dev/lab=load --no-headers 2>/dev/null | wc -l | tr -d ' ')"
	echo "pods: $(count_pods)"
	;;
down)
	guard
	k delete ns -l klyx.dev/lab=load --wait=true --timeout=10m
	echo "klyx-load: fixture removed"
	;;
*)
	sed -n '2,13p' "$0"
	exit 2
	;;
esac
