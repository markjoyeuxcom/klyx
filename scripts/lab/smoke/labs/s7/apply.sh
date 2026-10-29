#!/bin/zsh
# Coverage S7a lab on kind-klyx: ListenerSets and listener status (design
# §10). No gateway controller runs (C6: the real-component list has none), so
# a stand-in class names the controller lab.klyx.dev/gateway-controller and
# every status is written through the status subresource, as a controller
# would write it. Needs the v1.6.3 experimental CRDs (crds.sh).
#   lab-gw           allows ListenerSets from Same; its own listener healthy;
#                    the controller reports 2 attached
#   ls-first         klyx-gateway-lab, accepted, healthy        (1st in merge)
#   ls-other         klyx-gateway-other, NotAllowed             (2nd)
#   ls-conflict      klyx-gateway-lab, accepted, its listener lost on
#                    hostname (Conflicted HostnameConflict)     (3rd)
#   lab-gw-cert      a Gateway whose https listener reports
#                    ResolvedRefs False InvalidCertificateRef
# Re-running recreates the ListenerSets, so their creation order holds.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 90 kube "$@"; }
NS=klyx-gateway-lab
OTHER=klyx-gateway-other
CTL=lab.klyx.dev/gateway-controller
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
cond() { # type status reason [observedGeneration]
  print -r -- "{\"type\":\"$1\",\"status\":\"$2\",\"reason\":\"$3\",\"message\":\"$4\",\"observedGeneration\":${5:-1},\"lastTransitionTime\":\"$NOW\"}"
}
status() { # kind name namespace json
  local -a ns=()
  [[ -n $3 ]] && ns=(-n $3)
  k patch $1 $2 $ns --subresource=status --type=merge -p "$4" >/dev/null
}

k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Namespace
metadata: {name: $NS, labels: {team: lab, klyx.dev/lab: s7}}
---
apiVersion: v1
kind: Namespace
metadata: {name: $OTHER, labels: {team: other, klyx.dev/lab: s7}}
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: klyx-lab-gc, labels: {klyx.dev/lab: s7}}
spec: {controllerName: $CTL}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: lab-gw, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  gatewayClassName: klyx-lab-gc
  allowedListeners: {namespaces: {from: Same}}
  listeners:
  - {name: http, protocol: HTTP, port: 80, hostname: "*.lab.example"}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: lab-gw-cert, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  gatewayClassName: klyx-lab-gc
  listeners:
  - name: https
    protocol: HTTPS
    port: 443
    hostname: secure.lab.example
    tls: {mode: Terminate, certificateRefs: [{name: lab-missing-cert}]}
YAML

status gatewayclass klyx-lab-gc "" "{\"status\":{\"conditions\":[$(cond Accepted True Accepted 'stand-in controller')],
  \"supportedFeatures\":[{\"name\":\"Gateway\"},{\"name\":\"HTTPRoute\"},{\"name\":\"ListenerSet\"},{\"name\":\"TLSRoute\"},{\"name\":\"TCPRoute\"}]}}"
kinds='[{"group":"gateway.networking.k8s.io","kind":"HTTPRoute"}]'
healthy="[$(cond Accepted True Accepted ''),$(cond Conflicted False NoConflicts ''),$(cond ResolvedRefs True ResolvedRefs ''),$(cond Programmed True Programmed '')]"
status gateway lab-gw $NS "{\"status\":{\"conditions\":[$(cond Accepted True Accepted ''),$(cond Programmed True Programmed '')],
  \"addresses\":[{\"type\":\"IPAddress\",\"value\":\"192.0.2.70\"}],\"attachedListenerSets\":2,
  \"listeners\":[{\"name\":\"http\",\"attachedRoutes\":0,\"supportedKinds\":$kinds,\"conditions\":$healthy}]}}"
status gateway lab-gw-cert $NS "{\"status\":{\"conditions\":[$(cond Accepted True Accepted ''),$(cond Programmed True Programmed '')],
  \"addresses\":[{\"type\":\"IPAddress\",\"value\":\"192.0.2.71\"}],
  \"listeners\":[{\"name\":\"https\",\"attachedRoutes\":0,\"supportedKinds\":$kinds,\"conditions\":[$(cond Accepted True Accepted ''),$(cond Conflicted False NoConflicts ''),$(cond ResolvedRefs False InvalidCertificateRef 'Secret klyx-gateway-lab/lab-missing-cert does not exist'),$(cond Programmed False Invalid 'certificate unresolved')]}]}}"

# the ListenerSets, recreated one second apart: the merge order is their
# creation time
k delete listenerset -n $NS --all --wait=true >/dev/null 2>&1 || true
k delete listenerset -n $OTHER --all --wait=true >/dev/null 2>&1 || true
lset() { # namespace name hostname
  k apply -f - >/dev/null <<YAML
apiVersion: gateway.networking.k8s.io/v1
kind: ListenerSet
metadata: {name: $2, namespace: $1, labels: {klyx.dev/lab: s7}}
spec:
  parentRef: {name: lab-gw, namespace: $NS}
  listeners:
  - {name: extra, protocol: HTTP, port: 8080, hostname: $3}
YAML
}
lset $NS ls-first a.lab.example
sleep 1.2
lset $OTHER ls-other b.lab.example
sleep 1.2
lset $NS ls-conflict a.lab.example

status listenerset ls-first $NS "{\"status\":{\"conditions\":[$(cond Accepted True Accepted ''),$(cond Programmed True Programmed '')],
  \"listeners\":[{\"name\":\"extra\",\"attachedRoutes\":0,\"supportedKinds\":$kinds,\"conditions\":$healthy}]}}"
status listenerset ls-other $OTHER "{\"status\":{\"conditions\":[$(cond Accepted False NotAllowed 'lab-gw allows ListenerSets from Same only'),$(cond Programmed False Invalid 'not accepted')]}}"
status listenerset ls-conflict $NS "{\"status\":{\"conditions\":[$(cond Accepted True ListenersNotValid 'a listener is conflicted'),$(cond Programmed True Programmed '')],
  \"listeners\":[{\"name\":\"extra\",\"attachedRoutes\":0,\"supportedKinds\":$kinds,\"conditions\":[$(cond Accepted True Accepted ''),$(cond Conflicted True HostnameConflict 'a.lab.example:8080 is claimed by klyx-gateway-lab/ls-first, which ranks earlier'),$(cond ResolvedRefs True ResolvedRefs ''),$(cond Programmed False Invalid 'conflicted')]}]}}"
echo "lab-s7 applied ($ctx)"
