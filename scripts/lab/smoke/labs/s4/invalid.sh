#!/bin/zsh
# invalid.sh on|off: an input MetalLB's webhook admits but its reconciler
# refuses (S4 §8, revision 3): a BGPPeer whose password Secret is not
# kubernetes.io/basic-auth. The webhook clears the Secret reference before
# validating; the reconciler reads the Secret and refuses its type.
set -euo pipefail
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
case ${1:-} in
on)
	k -n metallb-system create secret generic lab-s4-bgp --from-literal=password=lab-s4-not-a-secret --dry-run=client -o yaml | k apply -f - >/dev/null
	k label -n metallb-system secret lab-s4-bgp klyx.dev/lab=s4 --overwrite >/dev/null
	k apply -f - <<'EOF'
apiVersion: metallb.io/v1beta2
kind: BGPPeer
metadata: {name: lab-s4-peer, namespace: metallb-system, labels: {klyx.dev/lab: s4}}
spec: {myASN: 64512, peerASN: 64513, peerAddress: 192.168.97.250, passwordSecret: {name: lab-s4-bgp, namespace: metallb-system}}
EOF
	;;
off)
	k -n metallb-system delete bgppeers.metallb.io lab-s4-peer --ignore-not-found
	k -n metallb-system delete secret lab-s4-bgp --ignore-not-found
	;;
*) echo "usage: invalid.sh on|off"; exit 2 ;;
esac
