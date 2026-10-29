#!/bin/zsh
# Coverage S7b lab on kind-klyx: every route kind on one Gateway, judged by
# its class's controller (design §10). As in S7a no gateway controller runs:
# the stand-in class klyx-lab-gc names lab.klyx.dev/gateway-controller, lists
# HTTPRoute, TLSRoute and TCPRoute among its features (not GRPCRoute or
# UDPRoute), and every status is written through the status subresource.
# Backends are selectorless Services with hand-written EndpointSlices, so the
# ready endpoints are exactly the ones below. Needs crds.sh and apply.sh.
#   lab-gw-l4        listeners web HTTP 80, tls TLS 443 Passthrough,
#                    pg TCP 5432, dns UDP 53; routes from Same only
#   HTTPRoute shop   web -> shop-web:80                       healthy
#   TLSRoute  shop   tls -> shop-tls:443 (same name, another kind) healthy
#   GRPCRoute api    web -> api:9000, pkg.Catalog/Get         healthy; a feature note
#   TCPRoute  pg     pg  -> pg:5432                           healthy
#   TCPRoute  pg-old pg  -> pg:6432 (the Service has no 6432) no-port fault
#   UDPRoute  dns    dns -> dns:53; the Service has dns-tcp TCP/53 and
#                    dns UDP/53, only the TCP slice has endpoints: not
#                    available; the class omits UDPRoute
#   HTTPRoute xns-granted  web -> klyx-gateway-other/other-api:80 with its
#                    ReferenceGrant                            healthy
#   HTTPRoute xns-denied   web -> klyx-gateway-other/other-db:80, no grant:
#                    ResolvedRefs False RefNotPermitted        fault
#   HTTPRoute from-other   (klyx-gateway-other) web: Accepted False
#                    NotAllowedByListeners                     fault
#   HTTPRoute two-parents  lab-gw-l4/web and lab-gw/http: two attachments
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 90 kube "$@"; }
NS=klyx-gateway-lab
OTHER=klyx-gateway-other
CTL=lab.klyx.dev/gateway-controller
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
cond() { # type status reason message [observedGeneration]
  print -r -- "{\"type\":\"$1\",\"status\":\"$2\",\"reason\":\"$3\",\"message\":\"$4\",\"observedGeneration\":${5:-1},\"lastTransitionTime\":\"$NOW\"}"
}
status() { # kind name namespace json
  k patch $1 $2 -n $3 --subresource=status --type=merge -p "$4" >/dev/null
}
ok="[$(cond Accepted True Accepted ''),$(cond ResolvedRefs True ResolvedRefs '')]"
parent() { # name [section]
  local s=""
  [[ -n $2 ]] && s=",\"sectionName\":\"$2\""
  print -r -- "{\"group\":\"gateway.networking.k8s.io\",\"kind\":\"Gateway\",\"namespace\":\"$NS\",\"name\":\"$1\"$s}"
}
routestatus() { # kind name namespace parentsJSON
  status $1 $2 $3 "{\"status\":{\"parents\":$4}}"
}
entry() { # parentJSON conditionsJSON
  print -r -- "{\"parentRef\":$1,\"controllerName\":\"$CTL\",\"conditions\":$2}"
}
svc() { # namespace name ports-yaml
  k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Service
metadata: {name: $2, namespace: $1, labels: {klyx.dev/lab: s7}}
spec:
  type: ClusterIP
  ports: $3
YAML
}
slice() { # namespace service slice port-name protocol port ready-count
  local eps="" i
  for i in $(seq 1 $7); do eps+="
- addresses: [\"10.244.250.$((RANDOM % 200 + 10))\"]
  conditions: {ready: true}"; done
  [[ -z $eps ]] && eps=" []"
  k apply -f - >/dev/null <<YAML
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: $3
  namespace: $1
  labels: {kubernetes.io/service-name: $2, endpointslice.kubernetes.io/managed-by: lab.klyx.dev, klyx.dev/lab: s7}
addressType: IPv4
ports: [{name: "$4", protocol: $5, port: $6}]
endpoints:$eps
YAML
}

k apply -f - >/dev/null <<YAML
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: lab-gw-l4, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  gatewayClassName: klyx-lab-gc
  listeners:
  - {name: web, protocol: HTTP, port: 80, hostname: "*.l4.lab.example"}
  - {name: tls, protocol: TLS, port: 443, hostname: shop.l4.lab.example, tls: {mode: Passthrough}}
  - {name: pg, protocol: TCP, port: 5432}
  - {name: dns, protocol: UDP, port: 53}
---
apiVersion: gateway.networking.k8s.io/v1
kind: ReferenceGrant
metadata: {name: lab-routes-to-other-api, namespace: $OTHER, labels: {klyx.dev/lab: s7}}
spec:
  from: [{group: gateway.networking.k8s.io, kind: HTTPRoute, namespace: $NS}]
  to: [{group: "", kind: Service, name: other-api}]
YAML

svc $NS shop-web '[{name: http, port: 80, protocol: TCP}]'
svc $NS shop-tls '[{name: https, port: 443, protocol: TCP}]'
svc $NS api '[{name: grpc, port: 9000, protocol: TCP}]'
svc $NS pg '[{name: pg, port: 5432, protocol: TCP}]'
svc $NS dns '[{name: dns-tcp, port: 53, protocol: TCP}, {name: dns, port: 53, protocol: UDP}]'
svc $OTHER other-api '[{name: http, port: 80, protocol: TCP}]'
svc $OTHER other-db '[{name: http, port: 80, protocol: TCP}]'
slice $NS shop-web shop-web-lab http TCP 80 2
slice $NS shop-tls shop-tls-lab https TCP 443 1
slice $NS api api-lab grpc TCP 9000 2
slice $NS pg pg-lab pg TCP 5432 1
slice $NS dns dns-tcp-lab dns-tcp TCP 53 3
slice $NS dns dns-udp-lab dns UDP 53 0
slice $OTHER other-api other-api-lab http TCP 80 1
slice $OTHER other-db other-db-lab http TCP 80 1

k apply -f - >/dev/null <<YAML
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: shop, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: web}]
  hostnames: [shop.l4.lab.example]
  rules: [{matches: [{path: {type: PathPrefix, value: /}}], backendRefs: [{name: shop-web, port: 80}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: TLSRoute
metadata: {name: shop, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: tls}]
  hostnames: [shop.l4.lab.example]
  rules: [{backendRefs: [{name: shop-tls, port: 443}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: GRPCRoute
metadata: {name: api, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: web}]
  hostnames: [api.l4.lab.example]
  rules: [{matches: [{method: {service: pkg.Catalog, method: Get}}], backendRefs: [{name: api, port: 9000}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: TCPRoute
metadata: {name: pg, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: pg}]
  rules: [{backendRefs: [{name: pg, port: 5432}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: TCPRoute
metadata: {name: pg-old, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: pg}]
  rules: [{backendRefs: [{name: pg, port: 6432}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: UDPRoute
metadata: {name: dns, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: dns}]
  rules: [{backendRefs: [{name: dns, port: 53}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: xns-granted, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: web}]
  hostnames: [granted.l4.lab.example]
  rules: [{backendRefs: [{name: other-api, namespace: $OTHER, port: 80}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: xns-denied, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: web}]
  hostnames: [denied.l4.lab.example]
  rules: [{backendRefs: [{name: other-db, namespace: $OTHER, port: 80}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: from-other, namespace: $OTHER, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, namespace: $NS, sectionName: web}]
  hostnames: [other.l4.lab.example]
  rules: [{backendRefs: [{name: other-api, port: 80}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: two-parents, namespace: $NS, labels: {klyx.dev/lab: s7}}
spec:
  parentRefs: [{name: lab-gw-l4, sectionName: web}, {name: lab-gw, sectionName: http}]
  hostnames: [two.l4.lab.example, two.lab.example]
  rules: [{backendRefs: [{name: shop-web, port: 80}]}]
YAML

web=$(parent lab-gw-l4 web)
routestatus httproute shop $NS "[$(entry $web $ok)]"
routestatus tlsroute shop $NS "[$(entry "$(parent lab-gw-l4 tls)" $ok)]"
routestatus grpcroute api $NS "[$(entry $web $ok)]"
routestatus tcproute pg $NS "[$(entry "$(parent lab-gw-l4 pg)" $ok)]"
routestatus tcproute pg-old $NS "[$(entry "$(parent lab-gw-l4 pg)" $ok)]"
routestatus udproute dns $NS "[$(entry "$(parent lab-gw-l4 dns)" $ok)]"
routestatus httproute xns-granted $NS "[$(entry $web $ok)]"
routestatus httproute xns-denied $NS "[$(entry $web "[$(cond Accepted True Accepted ''),$(cond ResolvedRefs False RefNotPermitted 'backendRef klyx-gateway-other/other-db is not permitted by any ReferenceGrant')]")]"
routestatus httproute from-other $OTHER "[$(entry $web "[$(cond Accepted False NotAllowedByListeners 'listener web allows routes from Same only'),$(cond ResolvedRefs True ResolvedRefs '')]")]"
routestatus httproute two-parents $NS "[$(entry $web $ok),$(entry "$(parent lab-gw http)" $ok)]"

kinds() { # kind...
  local out="" x
  for x in "$@"; do out+="${out:+,}{\"group\":\"gateway.networking.k8s.io\",\"kind\":\"$x\"}"; done
  print -r -- "[$out]"
}
healthy="[$(cond Accepted True Accepted ''),$(cond Conflicted False NoConflicts ''),$(cond ResolvedRefs True ResolvedRefs ''),$(cond Programmed True Programmed '')]"
status gateway lab-gw-l4 $NS "{\"status\":{\"conditions\":[$(cond Accepted True Accepted ''),$(cond Programmed True Programmed '')],
  \"addresses\":[{\"type\":\"IPAddress\",\"value\":\"192.0.2.72\"}],
  \"listeners\":[{\"name\":\"web\",\"attachedRoutes\":5,\"supportedKinds\":$(kinds HTTPRoute GRPCRoute),\"conditions\":$healthy},
    {\"name\":\"tls\",\"attachedRoutes\":1,\"supportedKinds\":$(kinds TLSRoute),\"conditions\":$healthy},
    {\"name\":\"pg\",\"attachedRoutes\":2,\"supportedKinds\":$(kinds TCPRoute),\"conditions\":$healthy},
    {\"name\":\"dns\",\"attachedRoutes\":1,\"supportedKinds\":$(kinds UDPRoute),\"conditions\":$healthy}]}}"
echo "lab-s7b applied ($ctx)"
