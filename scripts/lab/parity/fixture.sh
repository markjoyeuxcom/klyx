# shellcheck shell=bash
# The slice 6 parity fixture's objects (sourced by klyx-parity.sh after its
# exact-target check). Every capability object carries the oracle's
# declarations, independent of Klyx's rules (spec §1):
#   klyx.dev/parity-role      fault | degraded | unknown | progressing | paused | ok
#   klyx.dev/parity-included  true | false
# Objects without them (kind's own) are ok and not included.
#
# Recipes follow the Observer's rules (no timer may flip a fixture object
# during a run): conditions carry observedGeneration and the full
# metav1.Condition fields; Certificates have a far notAfter; ExternalSecrets
# never refresh (refreshInterval "0"); NodeClaims are Ready; every Gateway
# API condition is current.

ns=parity
pause=registry.k8s.io/pause:3.10

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# gen <resource> <ns|-> <name>: the object's metadata.generation.
gen() {
	if [[ $2 == - ]]; then k get "$1" "$3" -o jsonpath='{.metadata.generation}'; else k -n "$2" get "$1" "$3" -o jsonpath='{.metadata.generation}'; fi
}

# cond <type> <status> <reason> [observedGeneration]
cond() {
	local og=""
	[[ -n ${4:-} ]] && og=",\"observedGeneration\":$4"
	printf '{"type":"%s","status":"%s","reason":"%s","message":"klyx parity fixture","lastTransitionTime":"%s"%s}' "$1" "$2" "$3" "$(now)" "$og"
}

# ready <resource> <ns|-> <name> <True|False> <reason> [extra status json, without braces]
# Writes a Ready condition and the object's observedGeneration.
ready() {
	local g
	g=$(gen "$1" "$2" "$3")
	status "$1" "$2" "$3" "{\"observedGeneration\":$g,\"conditions\":[$(cond Ready "$4" "$5" "$g")]${6:+,$6}}"
}

marks() { # marks <role> <included>
	printf 'klyx.dev/parity-role: "%s"\n    klyx.dev/parity-included: "%s"' "$1" "$2"
}

controllers() {
	local n
	for n in flux-system argocd cert-manager external-secrets karpenter "$ns"; do
		k create namespace "$n" --dry-run=client -o yaml | k apply -f - >/dev/null
	done
	k label namespace "$ns" klyx.dev/lab=parity --overwrite >/dev/null
	local spec
	for spec in flux-system/kustomize-controller cert-manager/cert-manager external-secrets/external-secrets karpenter/karpenter; do
		stand_in "${spec%%/*}" "${spec##*/}" ""
	done
	k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: argocd-application-controller, namespace: argocd}
spec:
  replicas: 1
  serviceName: argocd-application-controller
  selector: {matchLabels: {app: argocd-application-controller}}
  template:
    metadata: {labels: {app: argocd-application-controller}}
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: pause, image: $pause, imagePullPolicy: Never}]
EOF
	k -n argocd rollout status statefulset/argocd-application-controller --timeout=120s >/dev/null
}

# stand_in <ns> <name> <role-if-down|"">: a pause Deployment under a
# controller's conventional name. Recreate, so taking it down removes the
# old pod before the new one stays Pending (spec §1).
stand_in() {
	local selector="" role=ok
	if [[ -n $3 ]]; then
		selector="nodeSelector: {klyx.dev/parity: unschedulable}"
		role=$3
	fi
	k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $2
  namespace: $1
  annotations:
    $(marks "$role" true)
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: $2}}
  template:
    metadata:
      labels: {app: $2}
      annotations:
        klyx.dev/parity-role: "$role"
    spec:
      terminationGracePeriodSeconds: 1
      $selector
      containers: [{name: pause, image: $pause, imagePullPolicy: Never}]
EOF
	if [[ -z $3 ]]; then
		k -n "$1" rollout status "deployment/$2" --timeout=120s >/dev/null
		settled_pods "$1" "$2" Running
	else
		settled_pods "$1" "$2" Pending
	fi
}

# settled_pods <ns> <app> <phase>: wait until the app has exactly one pod,
# in that phase, and nothing terminating (the Recreate transition is over).
settled_pods() {
	local i got
	for ((i = 0; i < 120; i++)); do
		got=$(k -n "$1" get pods -l "app=$2" -o jsonpath='{range .items[*]}{.status.phase}{.metadata.deletionTimestamp}{"\n"}{end}')
		[[ $got == "$3" ]] && return
		sleep 1
	done
	die "$1/$2 did not settle to one $3 pod (got: $got)"
}

flux_objects() {
	k apply -f - >/dev/null <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata: {name: parity-git, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {interval: 10m, url: "https://example.invalid/parity.git"}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: parity-oci, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {interval: 10m, url: "oci://example.invalid/parity"}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: Bucket
metadata: {name: parity-bucket, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {interval: 10m, bucketName: parity, endpoint: example.invalid}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata: {name: parity-helmrepo, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {interval: 10m, url: "https://example.invalid/charts"}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmChart
metadata: {name: parity-chart, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {interval: 10m, chart: parity, sourceRef: {kind: HelmRepository, name: parity-helmrepo}}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: parity-ks, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {interval: 10m, prune: false, path: ./, sourceRef: {kind: GitRepository, name: parity-git}}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: parity-hr, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {interval: 10m, chart: {spec: {chart: parity, sourceRef: {kind: HelmRepository, name: parity-helmrepo}}}}
EOF
	local r
	for r in gitrepositories.source.toolkit.fluxcd.io/parity-git ocirepositories.source.toolkit.fluxcd.io/parity-oci \
		buckets.source.toolkit.fluxcd.io/parity-bucket helmrepositories.source.toolkit.fluxcd.io/parity-helmrepo \
		helmcharts.source.toolkit.fluxcd.io/parity-chart kustomizations.kustomize.toolkit.fluxcd.io/parity-ks \
		helmreleases.helm.toolkit.fluxcd.io/parity-hr; do
		ready "${r%%/*}" "$ns" "${r##*/}" True Succeeded
	done
}

argo_objects() {
	local t
	t=$(now)
	k apply -f - >/dev/null <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: parity-app, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec:
  project: default
  destination: {server: "https://kubernetes.default.svc", namespace: $ns}
  source: {repoURL: "https://example.invalid/parity.git", path: ., targetRevision: HEAD}
status:
  sync: {status: Synced}
  health: {status: Healthy}
---
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata: {name: parity-appset, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec:
  generators: [{list: {elements: []}}]
  template:
    metadata: {name: "parity-{{name}}"}
    spec:
      project: default
      destination: {server: "https://kubernetes.default.svc", namespace: $ns}
      source: {repoURL: "https://example.invalid/parity.git", path: ., targetRevision: HEAD}
EOF
	status applicationsets.argoproj.io "$ns" parity-appset "{\"conditions\":[$(appset_cond ErrorOccurred False ApplicationSetUpToDate "$t"),$(appset_cond ParametersGenerated True ParametersGenerated "$t"),$(appset_cond ResourcesUpToDate True ApplicationSetUpToDate "$t")]}"
}

appset_cond() { printf '{"type":"%s","status":"%s","reason":"%s","message":"klyx parity fixture","lastTransitionTime":"%s"}' "$@"; }

certs_objects() {
	k apply -f - >/dev/null <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata: {name: parity-issuer, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {selfSigned: {}}
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: parity-clusterissuer, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {selfSigned: {}}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: parity-cert, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {secretName: parity-cert, dnsNames: [parity.example.invalid], issuerRef: {name: parity-issuer, kind: Issuer}}
EOF
	ready issuers.cert-manager.io "$ns" parity-issuer True IsReady
	ready clusterissuers.cert-manager.io - parity-clusterissuer True IsReady
	ready certificates.cert-manager.io "$ns" parity-cert True Ready '"notAfter":"2099-01-01T00:00:00Z"'
}

eso_objects() {
	k apply -f - >/dev/null <<EOF
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata: {name: parity-store, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {provider: {fake: {data: []}}}
---
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata: {name: parity-cstore, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {provider: {fake: {data: []}}}
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata: {name: parity-es, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec:
  refreshInterval: "0"
  secretStoreRef: {name: parity-store, kind: SecretStore}
  target: {name: parity-es}
  data: []
EOF
	ready secretstores.external-secrets.io "$ns" parity-store True Valid
	ready clustersecretstores.external-secrets.io - parity-cstore True Valid
	ready externalsecrets.external-secrets.io "$ns" parity-es True SecretSynced
}

# backend <name> <ready true|false|none>: a selector-less Service with a
# hand-made EndpointSlice (one endpoint, ready or not; or none at all).
backend() {
	local eps="[{addresses: [10.0.0.10], conditions: {ready: $2}}]"
	[[ $2 == none ]] && eps="[]"
	k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata: {name: $1, namespace: $ns}
spec: {ports: [{port: 80, protocol: TCP}]}
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: $1-1
  namespace: $ns
  labels: {kubernetes.io/service-name: $1, endpointslice.kubernetes.io/managed-by: klyx-parity}
addressType: IPv4
ports: [{port: 80, protocol: TCP}]
endpoints: $eps
EOF
}

# route <name> <backend service> <ResolvedRefs status> <reason> <role> <included>
route() {
	k apply -f - >/dev/null <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: $1
  namespace: $ns
  annotations:
    $(marks "$5" "$6")
spec:
  parentRefs: [{name: parity-gw}]
  rules: [{backendRefs: [{name: $2, port: 80}]}]
EOF
	local g
	g=$(gen httproutes.gateway.networking.k8s.io "$ns" "$1")
	status httproutes.gateway.networking.k8s.io "$ns" "$1" "{\"parents\":[{\"parentRef\":{\"group\":\"gateway.networking.k8s.io\",\"kind\":\"Gateway\",\"name\":\"parity-gw\",\"namespace\":\"$ns\"},\"controllerName\":\"example.dev/parity\",\"conditions\":[$(cond Accepted True Accepted "$g"),$(cond ResolvedRefs "$3" "$4" "$g")]}]}"
}

gateway_objects() {
	k apply -f - >/dev/null <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: parity-gc}
spec: {controllerName: example.dev/parity}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: parity-gw, namespace: $ns, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec:
  gatewayClassName: parity-gc
  listeners: [{name: http, port: 80, protocol: HTTP}]
EOF
	local g
	g=$(gen gatewayclasses.gateway.networking.k8s.io - parity-gc)
	status gatewayclasses.gateway.networking.k8s.io - parity-gc "{\"conditions\":[$(cond Accepted True Accepted "$g")]}"
	g=$(gen gateways.gateway.networking.k8s.io "$ns" parity-gw)
	status gateways.gateway.networking.k8s.io "$ns" parity-gw "{\"conditions\":[$(cond Accepted True Accepted "$g"),$(cond Programmed True Programmed "$g")]}"
	backend parity-backend true
	route parity-route parity-backend True ResolvedRefs ok false
}

karpenter_objects() {
	k apply -f - >/dev/null <<EOF
apiVersion: karpenter.sh/v1
kind: NodePool
metadata: {name: parity-pool, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {template: {spec: {}}}
---
apiVersion: karpenter.sh/v1
kind: NodeClaim
metadata: {name: parity-claim, labels: {karpenter.sh/nodepool: parity-pool}, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {}
EOF
	ready nodepools.karpenter.sh - parity-pool True Ready
	local g
	g=$(gen nodeclaims.karpenter.sh - parity-claim)
	status nodeclaims.karpenter.sh - parity-claim "{\"conditions\":[$(cond Launched True Launched "$g"),$(cond Registered True Registered "$g"),$(cond Initialized True Initialized "$g"),$(cond Ready True Ready "$g")]}"
}

baseline() {
	controllers
	flux_objects
	argo_objects
	certs_objects
	eso_objects
	gateway_objects
	karpenter_objects
	echo "klyx-parity: baseline applied on $ctx"
}

apply_case() {
	case $1 in
	1) # a failed unused Issuer
		k apply -f - >/dev/null <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: parity-issuer-failed
  namespace: $ns
  annotations:
    $(marks fault true)
spec: {selfSigned: {}}
EOF
		ready issuers.cert-manager.io "$ns" parity-issuer-failed False ErrInitIssuer ;;
	2) # a failed unused SecretStore
		k apply -f - >/dev/null <<EOF
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: parity-store-failed
  namespace: $ns
  annotations:
    $(marks fault true)
spec: {provider: {fake: {data: []}}}
EOF
		ready secretstores.external-secrets.io "$ns" parity-store-failed False InvalidProviderConfig ;;
	3) # an ApplicationSet failure (the Argo rule's role is degraded)
		k apply -f - >/dev/null <<EOF
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: parity-appset-failed
  namespace: $ns
  annotations:
    $(marks degraded true)
spec:
  generators: [{list: {elements: []}}]
  template:
    metadata: {name: "parity-failed-{{name}}"}
    spec:
      project: default
      destination: {server: "https://kubernetes.default.svc", namespace: $ns}
      source: {repoURL: "https://example.invalid/parity.git", path: ., targetRevision: HEAD}
EOF
		local t
		t=$(now)
		status applicationsets.argoproj.io "$ns" parity-appset-failed "{\"conditions\":[$(appset_cond ErrorOccurred True ApplicationGenerationFromParamsError "$t"),$(appset_cond ParametersGenerated True ParametersGenerated "$t"),$(appset_cond ResourcesUpToDate False ApplicationGenerationFromParamsError "$t")]}" ;;
	4) # a failing Flux source
		k apply -f - >/dev/null <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: parity-git-failed
  namespace: $ns
  annotations:
    $(marks fault true)
spec: {interval: 10m, url: "https://example.invalid/failed.git"}
EOF
		ready gitrepositories.source.toolkit.fluxcd.io "$ns" parity-git-failed False GitOperationFailed ;;
	5a) # a route whose backend is not found
		route parity-route-nobackend parity-missing False BackendNotFound fault true ;;
	5b) # a route whose backend has endpoints, none ready
		backend parity-unready false
		route parity-route-unready parity-unready True ResolvedRefs fault true ;;
	5c) # a route whose backend has no endpoints at all
		backend parity-empty none
		route parity-route-empty parity-empty True ResolvedRefs fault true ;;
	6) # cert-manager down, no cert-manager object fault (core: its
		# Deployment and the Pending pod are faults, declared)
		stand_in cert-manager cert-manager fault ;;
	*) die "unknown case '$1'" ;;
	esac
	echo "klyx-parity: case $1 applied"
}

revert_case() {
	case $1 in
	1) k -n "$ns" delete issuers.cert-manager.io parity-issuer-failed --wait >/dev/null ;;
	2) k -n "$ns" delete secretstores.external-secrets.io parity-store-failed --wait >/dev/null ;;
	3) k -n "$ns" delete applicationsets.argoproj.io parity-appset-failed --wait >/dev/null ;;
	4) k -n "$ns" delete gitrepositories.source.toolkit.fluxcd.io parity-git-failed --wait >/dev/null ;;
	5a) k -n "$ns" delete httproutes.gateway.networking.k8s.io parity-route-nobackend --wait >/dev/null ;;
	5b) k -n "$ns" delete httproutes.gateway.networking.k8s.io parity-route-unready --wait >/dev/null
		k -n "$ns" delete service/parity-unready endpointslice/parity-unready-1 --wait >/dev/null ;;
	5c) k -n "$ns" delete httproutes.gateway.networking.k8s.io parity-route-empty --wait >/dev/null
		k -n "$ns" delete service/parity-empty endpointslice/parity-empty-1 --wait >/dev/null ;;
	6) stand_in cert-manager cert-manager "" ;;
	*) die "unknown case '$1'" ;;
	esac
	echo "klyx-parity: case $1 reverted"
}

# rbac <deny,...|none>: the restricted Observer identity for cases 7 and 8 -
# get/list/watch on every listable resource but the denied ones (RBAC has
# no deny rule, so the grant is enumerated from discovery).
rbac() {
	local deny=",$1,"
	local rules
	rules=$(k api-resources --verbs=list,watch -o name | while read -r r; do
		[[ $deny == *",$r,"* ]] && continue
		if [[ $r == *.* ]]; then printf '%s\t%s\n' "${r#*.}" "${r%%.*}"; else printf '\t%s\n' "$r"; fi
	done | jq -Rn '[inputs | split("\t") | {g: .[0], r: .[1]}] | group_by(.g) | map({apiGroups: [.[0].g], resources: map(.r), verbs: ["get", "list", "watch"]})')
	k -n kube-system create serviceaccount klyx-parity-observer --dry-run=client -o yaml | k apply -f - >/dev/null
	jq -n --argjson rules "$rules" '{apiVersion: "rbac.authorization.k8s.io/v1", kind: "ClusterRole", metadata: {name: "klyx-parity-observer"}, rules: $rules}' | k apply -f - >/dev/null
	k create clusterrolebinding klyx-parity-observer --clusterrole=klyx-parity-observer --serviceaccount=kube-system:klyx-parity-observer --dry-run=client -o yaml | k apply -f - >/dev/null
	echo "klyx-parity: observer identity denies '${1}'"
}

# sa_kubeconfig: a one-hour TokenRequest token for the restricted identity,
# in its own kubeconfig (deleted by the gate on cleanup and by down).
sa_kubeconfig() {
	local token server ca out=$dir/sa-kubeconfig
	token=$(k -n kube-system create token klyx-parity-observer --duration=1h)
	server=$(kubectl --kubeconfig "$kubeconfig" config view --minify --context "$ctx" -o jsonpath='{.clusters[0].cluster.server}')
	ca=$(kubectl --kubeconfig "$kubeconfig" config view --minify --raw --context "$ctx" -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
	(umask 077; cat >"$out" <<EOF
apiVersion: v1
kind: Config
clusters: [{name: $ctx, cluster: {server: "$server", certificate-authority-data: "$ca"}}]
users: [{name: klyx-parity-observer, user: {token: "$token"}}]
contexts: [{name: $ctx, context: {cluster: $ctx, user: klyx-parity-observer}}]
current-context: $ctx
EOF
	)
	echo "$out"
}

# restart_apiserver: end every open watch (RBAC removal alone does not) by
# restarting the API server's static-pod container - only on the verified
# parity node container.
restart_apiserver() {
	local node=$name-control-plane
	[[ $(docker inspect "$node" --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}' 2>/dev/null) == "$name" ]] ||
		die "container $node is not the $name kind node"
	docker exec "$node" sh -c 'crictl stop $(crictl ps --name kube-apiserver -q)' >/dev/null
	local i
	for ((i = 0; i < 120; i++)); do
		k get --raw /readyz >/dev/null 2>&1 && { echo "klyx-parity: API server restarted"; return; }
		sleep 1
	done
	die "the API server did not come back within 120s"
}
