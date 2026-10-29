#!/bin/zsh
# Before each smoke mode (S4 §8): "fresh" is re-created, so its "within the
# grace" assertion never ages past ten minutes, and the exhausted Services
# are poked so MetalLB re-emits their AllocationFailed events (events expire
# after an hour).
set -euo pipefail
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
k -n lab-s4 delete svc fresh --ignore-not-found --wait=true >/dev/null
k apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Service
metadata: {name: fresh, namespace: lab-s4, labels: {klyx.dev/lab: s4}}
spec:
  type: LoadBalancer
  loadBalancerClass: lab.klyx.dev/none
  selector: {app: lab-s4-none}
  ports: [{name: http, port: 80, targetPort: 8080, protocol: TCP}]
EOF
t=$(date +%s)
for s in one-b svclb-demo; do k -n lab-s4 annotate svc $s klyx.dev/poke=$t --overwrite >/dev/null; done
echo "lab-s4 pending refreshed"
