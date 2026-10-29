#!/bin/zsh
# release.sh: deleting one-a releases the one address of lab-s4-one, which
# MetalLB gives to one-b and its pool status counts show; then the order is
# restored (one-a holds it, one-b is refused again).
set -euo pipefail
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
st() { k -n metallb-system get ipaddresspools lab-s4-one -o jsonpath='{.status}'; echo; }
echo "before:"; st; k -n lab-s4 get svc one-a one-b -o 'custom-columns=NAME:.metadata.name,IP:.status.loadBalancer.ingress[0].ip'
k -n lab-s4 get svc one-a -o yaml > ${0:A:h}/one-a.yaml
k -n lab-s4 delete svc one-a --wait=true >/dev/null
for i in {1..60}; do [[ -n $(k -n lab-s4 get svc one-b -o jsonpath='{.status.loadBalancer.ingress[0].ip}') ]] && break; sleep 1; done
echo "after release:"; st; k -n lab-s4 get svc one-b -o 'custom-columns=NAME:.metadata.name,IP:.status.loadBalancer.ingress[0].ip'
# restore: a released address goes to whichever Service waits for it
# (MetalLB retries the pending ones), so the order is rebuilt from scratch
k -n lab-s4 delete svc one-a one-b svclb-demo --ignore-not-found --wait=true >/dev/null
zsh ${0:A:h}/apply.sh >/dev/null
echo "restored:"; st; k -n lab-s4 get svc one-a one-b svclb-demo -o 'custom-columns=NAME:.metadata.name,IP:.status.loadBalancer.ingress[0].ip'
