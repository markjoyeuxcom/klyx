#!/bin/zsh
# Coverage S1b Stage 0 on kind-klyx: a Kustomization whose current
# generation fails while status.observedGeneration lags (the controllers
# advance it only on success). Klyx must read it as a fault, not Syncing.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
k -n flux-system delete kustomization failing-current --ignore-not-found >/dev/null
k apply -f - <<'EOF'
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: failing-current, namespace: flux-system}
spec: {interval: 10m, path: ./apps/v1, prune: false, sourceRef: {kind: GitRepository, name: platform}}
EOF
k -n flux-system patch kustomization failing-current --type=merge -p '{"spec":{"path":"./apps/v2"}}' >/dev/null
k -n flux-system patch kustomization failing-current --subresource=status --type=merge -p "{\"status\":{\"observedGeneration\":1,\"lastAppliedRevision\":\"main@sha1:9a8b7c6\",\"conditions\":[{\"type\":\"Ready\",\"status\":\"False\",\"reason\":\"BuildFailed\",\"message\":\"kustomize build failed: accumulating resources: ./apps/v2: no such file or directory\",\"observedGeneration\":2,\"lastTransitionTime\":\"$NOW\"}]}}" >/dev/null
k -n flux-system get kustomization failing-current -o jsonpath='{.metadata.generation} {.status.observedGeneration}{"\n"}'
