#!/usr/bin/env bash
# E4 lb-pool-exhausted's plant (eval design §2), in the eval's own
# namespace: a MetalLB pool of one address (the eval's own, in
# metallb-system), held by one-a, so MetalLB refuses one-b. Two lookalikes
# pending for other reasons: cilium-l2, of a Cilium class with no Cilium
# (with Cilium's own "out of IPs" condition written as Cilium would), and
# wrong-pool, asking for a pool that does not exist. Needs MetalLB
# (klyx-eval.sh install). MetalLB writes AllocationFailed only when it
# reconciles one-b, so an old event is renewed by nudging one-b.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
k get crd ipaddresspools.metallb.io >/dev/null 2>&1 || die "MetalLB is not installed on $ctx (klyx-eval.sh install)"
NS=klyx-eval-e4
POOL=klyx-eval-one
ADDR=${KLYX_EVAL_E4_ADDRESS:-192.168.97.230/32}
claim namespace $NS
claim -n metallb-system ipaddresspool $POOL
for s in one-a one-b cilium-l2 wrong-pool; do claim -n $NS service $s; done
own="klyx.dev/lab: eval, klyx.dev/eval-owner: \"$(owner_id)\""
svc() { # svc <name> <spec extra> <annotations>
	cat <<YAML
---
apiVersion: v1
kind: Service
metadata: {name: $1, namespace: $NS, labels: {$own}, annotations: {$3}}
spec: {type: LoadBalancer, $2 selector: {app: e4-none}, ports: [{port: 80, targetPort: 80}]}
YAML
}
for i in {1..30}; do
	k apply -f - >/dev/null <<YAML && break
apiVersion: v1
kind: Namespace
metadata: {name: $NS, labels: {$own}}
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata: {name: $POOL, namespace: metallb-system, labels: {$own}}
spec: {addresses: ["$ADDR"], autoAssign: false}
YAML
	sleep 2 # the MetalLB webhook serves once its certificate is written
done
k apply -f - >/dev/null <<YAML
$(svc one-a "" "metallb.io/address-pool: $POOL")
YAML
ip() { k -n $NS get svc "$1" -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null; }
held() { [[ -n $(ip one-a) ]]; }
wait_for 120 "one-a holding the pool's address" held
k apply -f - >/dev/null <<YAML
$(svc one-b "" "metallb.io/address-pool: $POOL")
$(svc cilium-l2 "loadBalancerClass: io.cilium/l2-announcer," "")
$(svc wrong-pool "" "metallb.io/address-pool: klyx-eval-no-such-pool")
YAML
# Cilium's own condition on its classed Service, as Cilium writes it (a
# Service has no generation, so the condition names none)
k -n $NS patch svc cilium-l2 --subresource=status --type=merge -p "{\"status\":{\"conditions\":[{\"type\":\"cilium.io/IPAMRequestSatisfied\",\"status\":\"False\",\"reason\":\"out_of_ips\",\"message\":\"No more IPs available\",\"lastTransitionTime\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}]}}" >/dev/null
# renew MetalLB's refusal of one-b
k -n $NS annotate --overwrite svc one-b klyx.dev/eval-nudge="$(date +%s)" >/dev/null
refused() {
	k -n $NS get events --field-selector involvedObject.name=one-b,reason=AllocationFailed -o jsonpath='{.items[*].message}' 2>/dev/null | grep -q 'no available IPs'
}
wait_for 120 "MetalLB refusing one-b" refused
[[ -z $(ip one-b) && -z $(ip cilium-l2) && -z $(ip wrong-pool) ]] || die "a Service that should be pending has an address"
echo "e4: planted"
