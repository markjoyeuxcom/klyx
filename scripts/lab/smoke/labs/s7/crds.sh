#!/bin/zsh
# Coverage S7: Gateway API v1.6.3 experimental-channel CRDs on kind-klyx, from
# the Go module cache (checksummed by the Go proxy), inspected before use: CRDs
# only, no conversion webhooks; the safe-upgrades ValidatingAdmissionPolicy is
# not applied (it refuses experimental CRDs over the lab's standard ones).
set -euo pipefail
source ${0:A:h}/../../lib.sh && target
M=$(gomod sigs.k8s.io/gateway-api@v1.6.3)/config/crd/experimental
for f in $M/gateway.networking.k8s.io_{backendtlspolicies,gatewayclasses,gateways,grpcroutes,httproutes,listenersets,referencegrants,tcproutes,tlsroutes,udproutes}.yaml \
         $M/gateway.networking.x-k8s.io_{xbackends,xbackendtrafficpolicies,xmeshes}.yaml; do
  t 60 kube apply --server-side --force-conflicts --field-manager=klyx-lab -f $f
done
t 60 kube wait --for=condition=Established --timeout=50s crd/listenersets.gateway.networking.k8s.io crd/tlsroutes.gateway.networking.k8s.io crd/tcproutes.gateway.networking.k8s.io crd/udproutes.gateway.networking.k8s.io crd/xmeshes.gateway.networking.x-k8s.io
