#!/bin/zsh
# Coverage S4 lab on kind-klyx only: a real MetalLB v0.16.1 (the native
# manifest, read before applying: sha256 bf25feeb...), its pools and an
# L2Advertisement from the kind network's range, and the scenarios S4 §8
# names. Fixtures only for what the lab cannot produce: the Cilium pool CRD
# (Cilium is not the lab's CNI) with status-written pools, a Service with
# Cilium's unsatisfied condition, and a stand-in svclb DaemonSet whose pod
# cannot get its host port.
set -euo pipefail
source ${0:A:h}/../../lib.sh && target
k() { t 120 kube "$@"; }
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# MetalLB v0.16.1's native manifest and Cilium v1.20.3's pool CRD, each
# pinned by its digest
METALLB=$(fetch https://raw.githubusercontent.com/metallb/metallb/v0.16.1/config/manifests/metallb-native.yaml bf25feebb7582ca7df845efd52ffbc2960d6cbf4cfc972f47fded9f788b67f0b metallb-v0.16.1-native.yaml)
CILIUM_POOLS=$(fetch https://raw.githubusercontent.com/cilium/cilium/v1.20.3/pkg/k8s/apis/cilium.io/client/crds/v2/ciliumloadbalancerippools.yaml 31dd3d26d987e46bc04d66ec2f9bdcf2aa2b13b7b368e515ca324961b55d65b1 cilium-v1.20.3-ciliumloadbalancerippools.yaml)

echo "== MetalLB v0.16.1"
k apply -f $METALLB >/dev/null
k -n metallb-system rollout status deployment/controller --timeout=180s
k -n metallb-system rollout status daemonset/speaker --timeout=180s
# the webhook serves once the controller has written its certificate
for i in {1..60}; do
	k -n metallb-system get endpointslices -l kubernetes.io/service-name=metallb-webhook-service -o jsonpath='{.items[*].endpoints[*].conditions.ready}' 2>/dev/null | grep -q true && break
	sleep 2
done

echo "== the Cilium pool CRD (no Cilium)"
k apply -f $CILIUM_POOLS >/dev/null
k wait --for condition=established --timeout=60s crd/ciliumloadbalancerippools.cilium.io >/dev/null

echo "== pools and advertisements"
for i in {1..30}; do
	k apply -f - >/dev/null <<'EOF' && break
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata: {name: lab-s4-main, namespace: metallb-system, labels: {klyx.dev/lab: s4, tier: gold}}
spec: {addresses: ["192.168.97.200-192.168.97.204"]}
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata: {name: lab-s4-one, namespace: metallb-system, labels: {klyx.dev/lab: s4, tier: gold}}
spec: {addresses: ["192.168.97.210/32"], autoAssign: false}
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata: {name: lab-s4-silent, namespace: metallb-system, labels: {klyx.dev/lab: s4, tier: bronze}}
spec: {addresses: ["192.168.97.220/32"], autoAssign: false}
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata: {name: lab-s4-gold, namespace: metallb-system, labels: {klyx.dev/lab: s4}}
spec:
  ipAddressPoolSelectors:
  - matchExpressions: [{key: tier, operator: In, values: [gold]}]
EOF
	sleep 2
done

echo "== services"
k create namespace lab-s4 --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace lab-s4 klyx.dev/lab=s4 --overwrite >/dev/null
svc() { # svc <name> <extra spec yaml> <annotations yaml>
	k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata:
  name: $1
  namespace: lab-s4
  labels: {klyx.dev/lab: s4}
  annotations: {$3}
spec:
  type: LoadBalancer
  selector: {app: lab-s4-none}
  ports: [{name: http, port: 80, targetPort: 8080, protocol: TCP}]
  $2
EOF
}
svc web "" ""
svc one-a "" 'metallb.io/address-pool: lab-s4-one'
for i in {1..60}; do [[ -n $(k -n lab-s4 get svc one-a -o jsonpath='{.status.loadBalancer.ingress[0].ip}') ]] && break; sleep 1; done
# exhausted: one-a holds the one address, so MetalLB refuses one-b itself
svc one-b "" 'metallb.io/address-pool: lab-s4-one'
svc silent "" 'metallb.io/address-pool: lab-s4-silent'
svc cilium-l2 "loadBalancerClass: io.cilium/l2-announcer" ""
svc svclb-demo "" 'metallb.io/address-pool: lab-s4-one'

echo "== fixtures: Cilium pools (status written; nothing rewrites them)"
k apply -f - >/dev/null <<'EOF'
apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata: {name: lab-s4-cilium-ok, labels: {klyx.dev/lab: s4}}
spec: {blocks: [{cidr: "10.250.0.0/28"}]}
---
apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata: {name: lab-s4-cilium-conflict, labels: {klyx.dev/lab: s4}}
spec: {blocks: [{cidr: "10.250.0.0/29"}]}
---
apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata: {name: lab-s4-cilium-full, labels: {klyx.dev/lab: s4}}
spec: {blocks: [{cidr: "10.250.1.0/30"}]}
---
apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata: {name: lab-s4-cilium-v6, labels: {klyx.dev/lab: s4}}
spec: {blocks: [{cidr: "fd00:250::/64"}]}
EOF
gen() { k get ciliumloadbalancerippools "$1" -o jsonpath='{.metadata.generation}'; }
c() { printf '{"type":"%s","status":"%s","reason":"%s","message":"%s","observedGeneration":%s,"lastTransitionTime":"%s"}' "$1" "$2" "$3" "$4" "$5" "$(now)"; }
counts() { # counts <pool> <total> <available> <used> [extra condition]
	local g; g=$(gen "$1")
	k patch ciliumloadbalancerippools "$1" --subresource=status --type=merge -p "{\"status\":{\"conditions\":[$(c cilium.io/IPsTotal Unknown noreason "$2" "$g"),$(c cilium.io/IPsAvailable Unknown noreason "$3" "$g"),$(c cilium.io/IPsUsed Unknown noreason "$4" "$g")${5:+,$5}]}}" >/dev/null
}
counts lab-s4-cilium-ok 16 14 2
counts lab-s4-cilium-conflict 8 8 0 "$(c cilium.io/PoolConflict True cidr_overlap "Pool conflicts since range '10.250.0.0/29' overlaps range '10.250.0.0/28' from IP Pool 'lab-s4-cilium-ok'" "$(gen lab-s4-cilium-conflict)")"
counts lab-s4-cilium-full 4 0 4
counts lab-s4-cilium-v6 18446744073709551616 18446744073709551614 2

echo "== fixtures: Cilium's unsatisfied condition on cilium-l2 (MetalLB ignores a classed Service)"
g=$(k -n lab-s4 get svc cilium-l2 -o jsonpath='{.metadata.generation}')
g=${g:-0} # a Service carries no generation; Cilium writes the same zero
k -n lab-s4 patch svc cilium-l2 --subresource=status --type=merge -p "{\"status\":{\"conditions\":[$(c cilium.io/IPAMRequestSatisfied False out_of_ips "No more IPs available" "$g")]}}" >/dev/null

echo "== fixtures: a stand-in svclb DaemonSet for svclb-demo whose pod cannot get its host port"
uid=$(k -n lab-s4 get svc svclb-demo -o jsonpath='{.metadata.uid}')
ds="svclb-svclb-demo-${uid:0:8}"
k -n kube-system delete daemonset -l klyx.dev/lab=s4 --ignore-not-found >/dev/null
k -n kube-system delete pod lab-s4-port-holder --ignore-not-found >/dev/null
k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: lab-s4-port-holder, namespace: kube-system, labels: {klyx.dev/lab: s4}}
spec:
  terminationGracePeriodSeconds: 1
  containers: [{name: pause, image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent, ports: [{containerPort: 80, hostPort: 30080}]}]
EOF
k -n kube-system wait --for=condition=Ready pod/lab-s4-port-holder --timeout=120s >/dev/null
k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: $ds, namespace: kube-system, labels: {klyx.dev/lab: s4}}
spec:
  selector: {matchLabels: {app: $ds}}
  template:
    metadata:
      labels: {app: $ds, svccontroller.k3s.cattle.io/svcname: svclb-demo, svccontroller.k3s.cattle.io/svcnamespace: lab-s4}
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: lb-tcp-80, image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent, ports: [{containerPort: 80, hostPort: 30080}]}]
EOF

zsh ${0:A:h}/pending.sh
echo "lab-s4 applied"
