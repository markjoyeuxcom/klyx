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
	for spec in flux-system/kustomize-controller cert-manager/cert-manager external-secrets/external-secrets; do
		stand_in "${spec%%/*}" "${spec##*/}" ""
	done
	karpenter_controllers
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

# node_image <ref>: the pause image under another name on the parity node
# (coverage S10: Karpenter's controllers are attributed by their pods'
# images), tagged in the node's containerd - no pull, only on the verified
# parity node container.
node_image() {
	local node=$name-control-plane
	[[ $(docker inspect "$node" --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}' 2>/dev/null) == "$name" ]] ||
		die "container $node is not the $name kind node"
	docker exec "$node" ctr -n k8s.io images tag --force "$pause" "$1" >/dev/null
}

# extended_objects: coverage S11a's real device plugin, the Kubernetes e2e
# sample device plugin (1.8.0, pulled), registering example.com/resource
# (2 devices) through the parity node's kubelet, and a pod requesting 1.
# Case 11 stops it for real (its DaemonSet's nodeSelector matches nothing),
# so the kubelet reads capacity 2, allocatable 0 for 5 minutes.
ext_alloc() { k get node "$name-control-plane" -o 'jsonpath={.status.allocatable.example\.com/resource}'; }
ext_wait() { # ext_wait <allocatable>: poll the node until its allocatable reads it
	local i=0
	until [[ $(ext_alloc) == "$1" ]]; do
		i=$((i + 1))
		((i > 60)) && die "example.com/resource allocatable never read $1 (now '$(ext_alloc)')"
		sleep 2
	done
}
extended_objects() {
	k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: parity-device-plugin, namespace: $ns}
spec:
  selector: {matchLabels: {app: parity-device-plugin}}
  template:
    metadata: {labels: {app: parity-device-plugin}}
    spec:
      tolerations: [{operator: Exists}]
      volumes:
      - {name: device-plugin, hostPath: {path: /var/lib/kubelet/device-plugins}}
      - {name: plugins-registry, hostPath: {path: /var/lib/kubelet/plugins_registry}}
      - {name: cdi-dir, hostPath: {path: /var/run/cdi}}
      containers:
      - name: plugin
        image: registry.k8s.io/e2e-test-images/sample-device-plugin:1.8.0
        env: [{name: PLUGIN_SOCK_DIR, value: /var/lib/kubelet/device-plugins}]
        securityContext: {privileged: true}
        volumeMounts:
        - {name: device-plugin, mountPath: /var/lib/kubelet/device-plugins}
        - {name: plugins-registry, mountPath: /var/lib/kubelet/plugins_registry}
        - {name: cdi-dir, mountPath: /var/run/cdi}
EOF
	ext_wait 2
	k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: parity-device-user, namespace: $ns}
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - name: pause
    image: $pause
    imagePullPolicy: Never
    resources: {requests: {example.com/resource: "1"}, limits: {example.com/resource: "1"}}
EOF
	k -n "$ns" wait --for=condition=Ready pod/parity-device-user --timeout=120s >/dev/null
}

# dra_objects: coverage S11b's upstream example DRA driver (the chart pinned
# by digest, rendered for the cluster's version and applied into its own
# namespace: never installed as a Helm release, which would be a
# helm/releases member the oracle does not model), its node-local
# pool of 8 devices, one claim allocated to a running pod, and its
# DeviceClass. Case 12 adds a claim no node can satisfy; case 13 an invalid
# fixture pool (all-nodes slices under a driver name no plugin serves).
dra_objects() {
	if [[ ! -f $dra_chart || $(openssl dgst -sha256 -r "$dra_chart" | awk '{print $1}') != "$dra_chart_sha256" ]]; then
		helm pull oci://registry.k8s.io/dra-example-driver/charts/dra-example-driver --version "$dra_chart_version" -d "$dir" >/dev/null ||
			die "could not pull the example DRA driver's chart"
		local got
		got=$(openssl dgst -sha256 -r "$dra_chart" | awk '{print $1}')
		[[ $got == "$dra_chart_sha256" ]] || { rm -f "$dra_chart"; die "the DRA chart has digest $got, pinned $dra_chart_sha256"; }
	fi
	local server
	server=$(k version -o json | python3 -I -c 'import json, sys; print(json.load(sys.stdin)["serverVersion"]["gitVersion"])') ||
		die "could not read the server's version"
	k create namespace parity-dra --dry-run=client -o yaml | k apply -f - >/dev/null
	helm template dra-example-driver "$dra_chart" --namespace parity-dra --kube-version "$server" --api-versions resource.k8s.io/v1 |
		k -n parity-dra apply -f - >/dev/null || die "the example DRA driver did not apply"
	k -n parity-dra rollout status daemonset/dra-example-driver-kubeletplugin --timeout=300s >/dev/null ||
		die "the example DRA driver's kubelet plugin did not roll out"
	local i=0
	until k get resourceslices -o 'jsonpath={.items[*].spec.nodeName}' | grep -q "$name-control-plane"; do
		i=$((i + 1))
		((i > 90)) && die "the DRA driver published no slice for the node"
		sleep 2
	done
	k apply -f - >/dev/null <<EOF
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata: {name: parity-claim, namespace: $ns}
spec:
  devices:
    requests:
    - name: gpu
      exactly: {deviceClassName: gpu.example.com, allocationMode: ExactCount, count: 1}
---
apiVersion: v1
kind: Pod
metadata: {name: parity-dra-user, namespace: $ns}
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - name: pause
    image: $pause
    imagePullPolicy: Never
    resources: {claims: [{name: gpu}]}
  resourceClaims: [{name: gpu, resourceClaimName: parity-claim}]
EOF
	k -n "$ns" wait --for=condition=Ready pod/parity-dra-user --timeout=180s >/dev/null
}

# karpenter_controllers: an AWS and an Azure stand-in, labelled as the
# charts label them and running pause under each provider's controller
# image, so both are attributed (S10 §5); the parity node carries EKS Auto
# Mode's label, so the Auto Mode NodeClass is managed. The task reads
# "managed" in every case.
karpenter_controllers() {
	node_image public.ecr.aws/karpenter/controller:parity
	node_image mcr.microsoft.com/aks/karpenter/controller:parity
	k create namespace karpenter-azure --dry-run=client -o yaml | k apply -f - >/dev/null
	local spec
	for spec in karpenter/public.ecr.aws/karpenter/controller:parity karpenter-azure/mcr.microsoft.com/aks/karpenter/controller:parity; do
		k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: karpenter, namespace: ${spec%%/*}, labels: {app.kubernetes.io/name: karpenter}}
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: karpenter}}
  template:
    metadata: {labels: {app: karpenter}}
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: controller, image: ${spec#*/}, imagePullPolicy: Never}]
EOF
		k -n "${spec%%/*}" rollout status deployment/karpenter --timeout=120s >/dev/null
		settled_pods "${spec%%/*}" karpenter Running
	done
	k label nodes --all eks.amazonaws.com/compute-type=auto --overwrite >/dev/null
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
  allowedListeners: {namespaces: {from: Same}}
  listeners: [{name: http, port: 80, protocol: HTTP}, {name: tcp, port: 5432, protocol: TCP}]
EOF
	local g
	g=$(gen gatewayclasses.gateway.networking.k8s.io - parity-gc)
	status gatewayclasses.gateway.networking.k8s.io - parity-gc "{\"conditions\":[$(cond Accepted True Accepted "$g")]}"
	g=$(gen gateways.gateway.networking.k8s.io "$ns" parity-gw)
	status gateways.gateway.networking.k8s.io "$ns" parity-gw "{\"conditions\":[$(cond Accepted True Accepted "$g"),$(cond Programmed True Programmed "$g")]}"
	backend parity-backend true
	route parity-route parity-backend True ResolvedRefs ok false
	listenerset parity-ls True Accepted ok false
	tcproute parity-tcp 80 ok false
}

# tcproute <name> <backend port> <role> <included>: a TCPRoute on parity-gw's
# tcp listener to parity-backend (coverage S7b), accepted with references
# resolved; its backend resolves by port number and TCP
tcproute() {
	k apply -f - >/dev/null <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: TCPRoute
metadata:
  name: $1
  namespace: $ns
  annotations:
    $(marks "$3" "$4")
spec:
  parentRefs: [{name: parity-gw, sectionName: tcp}]
  rules: [{backendRefs: [{name: parity-backend, port: $2}]}]
EOF
	local g
	g=$(gen tcproutes.gateway.networking.k8s.io "$ns" "$1")
	status tcproutes.gateway.networking.k8s.io "$ns" "$1" "{\"parents\":[{\"parentRef\":{\"group\":\"gateway.networking.k8s.io\",\"kind\":\"Gateway\",\"name\":\"parity-gw\",\"namespace\":\"$ns\",\"sectionName\":\"tcp\"},\"controllerName\":\"example.dev/parity\",\"conditions\":[$(cond Accepted True Accepted "$g"),$(cond ResolvedRefs True ResolvedRefs "$g")]}]}"
}

# listenerset <name> <Accepted status> <reason> <role> <included>: a
# ListenerSet under parity-gw (coverage S7a), its controller's verdict
listenerset() {
	k apply -f - >/dev/null <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: ListenerSet
metadata:
  name: $1
  namespace: $ns
  annotations:
    $(marks "$4" "$5")
spec:
  parentRef: {name: parity-gw}
  listeners: [{name: extra, port: 8080, protocol: HTTP, hostname: $1.parity.example}]
EOF
	local g
	g=$(gen listenersets.gateway.networking.k8s.io "$ns" "$1")
	status listenersets.gateway.networking.k8s.io "$ns" "$1" "{\"conditions\":[$(cond Accepted "$2" "$3" "$g"),$(cond Programmed "$2" "$3" "$g")]}"
}

karpenter_objects() {
	# coverage S10: one class per provider adapter, each Ready, and pools that
	# use them (parity-pool and parity-pool-b share the EC2NodeClass: case
	# 10 fails it). Every status is written; nothing reconciles.
	k apply -f - >/dev/null <<EOF
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata: {name: parity-ec2}
spec: {role: KarpenterNodeRole-parity, amiSelectorTerms: [{alias: al2023@latest}], subnetSelectorTerms: [{tags: {karpenter.sh/discovery: parity}}], securityGroupSelectorTerms: [{tags: {karpenter.sh/discovery: parity}}]}
---
apiVersion: karpenter.azure.com/v1beta1
kind: AKSNodeClass
metadata: {name: parity-aks}
spec: {imageFamily: AzureLinux}
---
apiVersion: eks.amazonaws.com/v1
kind: NodeClass
metadata: {name: parity-auto}
spec: {role: AutoNodeRole-parity}
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata: {name: parity-pool, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {template: {spec: {nodeClassRef: {group: karpenter.k8s.aws, kind: EC2NodeClass, name: parity-ec2}}}}
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata: {name: parity-pool-b, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {template: {spec: {nodeClassRef: {group: karpenter.k8s.aws, kind: EC2NodeClass, name: parity-ec2}}}}
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata: {name: parity-pool-aks, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {template: {spec: {nodeClassRef: {group: karpenter.azure.com, kind: AKSNodeClass, name: parity-aks}}}}
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata: {name: parity-pool-auto, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {template: {spec: {nodeClassRef: {group: eks.amazonaws.com, kind: NodeClass, name: parity-auto}}}}
---
apiVersion: karpenter.sh/v1
kind: NodeClaim
metadata: {name: parity-claim, labels: {karpenter.sh/nodepool: parity-pool}, annotations: {klyx.dev/parity-role: ok, klyx.dev/parity-included: "false"}}
spec: {nodeClassRef: {group: karpenter.k8s.aws, kind: EC2NodeClass, name: parity-ec2}}
EOF
	class_ready ec2nodeclasses.karpenter.k8s.aws parity-ec2 True
	class_ready aksnodeclasses.karpenter.azure.com parity-aks True
	class_ready nodeclasses.eks.amazonaws.com parity-auto True
	local p
	for p in parity-pool parity-pool-b parity-pool-aks parity-pool-auto; do pool_ready "$p" True; done
	local g
	g=$(gen nodeclaims.karpenter.sh - parity-claim)
	status nodeclaims.karpenter.sh - parity-claim "{\"conditions\":[$(cond Launched True Launched "$g"),$(cond Registered True Registered "$g"),$(cond Initialized True Initialized "$g"),$(cond Ready True Ready "$g")]}"
}

# class_ready <resource> <name> <True|False>: a class's Ready, with a
# failing SubnetsReady for False (S10: the first False dependent).
class_ready() {
	local g
	g=$(gen "$1" - "$2")
	if [[ $3 == True ]]; then
		status "$1" - "$2" "{\"conditions\":[$(cond SubnetsReady True SubnetsReady "$g"),$(cond Ready True Ready "$g")]}"
	else
		status "$1" - "$2" "{\"conditions\":[$(cond SubnetsReady False SubnetsNotFound "$g"),$(cond Ready False UnhealthyDependents "$g")]}"
	fi
}

# pool_ready <pool> <True|False>: a pool's conditions as Karpenter's
# readiness controller writes them, NodeClassReady copying its class.
pool_ready() {
	local g
	g=$(gen nodepools.karpenter.sh - "$1")
	if [[ $2 == True ]]; then
		status nodepools.karpenter.sh - "$1" "{\"conditions\":[$(cond ValidationSucceeded True ValidationSucceeded "$g"),$(cond NodeClassReady True NodeClassReady "$g"),$(cond Ready True Ready "$g")]}"
	else
		status nodepools.karpenter.sh - "$1" "{\"conditions\":[$(cond ValidationSucceeded True ValidationSucceeded "$g"),$(cond NodeClassReady False SubnetsNotFound "$g"),$(cond Ready False UnhealthyDependents "$g")]}"
	fi
}

fluxop_objects() {
	# A flux-operator stand-in (coverage S1a): found by its label, runtime
	# namespace flux-system (RUNTIME_NAMESPACE unset), so the FluxInstance
	# there is bound and the kustomize-controller probe stays in flux-system.
	# The baseline is healthy (case B has no attention): a suspended
	# ResourceSet is paused, never attention; the operator's faults are
	# covered by the observer-path tests (internal/healthsrc/fluxop_test.go).
	k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: flux-operator
  namespace: flux-system
  labels: {app.kubernetes.io/name: flux-operator}
  annotations:
    $(marks ok true)
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: flux-operator}}
  template:
    metadata:
      labels: {app: flux-operator}
      annotations:
        klyx.dev/parity-role: "ok"
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: manager, image: $pause, imagePullPolicy: Never}]
---
apiVersion: fluxcd.controlplane.io/v1
kind: FluxInstance
metadata:
  name: flux
  namespace: flux-system
  annotations:
    $(marks ok false)
spec: {distribution: {version: "2.x", registry: ghcr.io/fluxcd}}
---
apiVersion: fluxcd.controlplane.io/v1
kind: FluxReport
metadata: {name: flux, namespace: flux-system}
spec: {distribution: {version: v2.9.6, status: Installed, entitlement: parity}}
---
apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSet
metadata:
  name: parity-rs
  namespace: $ns
  annotations:
    $(marks ok false)
spec: {resources: [{apiVersion: v1, kind: ConfigMap, metadata: {name: parity-rs-cm, namespace: $ns}}]}

---
apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSet
metadata:
  name: parity-rs-paused
  namespace: $ns
  annotations:
    fluxcd.controlplane.io/reconcile: disabled
    $(marks paused false)
spec: {resources: [{apiVersion: v1, kind: ConfigMap, metadata: {name: parity-rs-paused-cm, namespace: $ns}}]}
---
apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: parity-rsip
  namespace: $ns
  annotations:
    $(marks ok false)
spec: {type: Static, defaultValues: {env: parity}}
EOF
	k -n flux-system rollout status deployment/flux-operator --timeout=120s >/dev/null
	settled_pods flux-system flux-operator Running
	ready fluxinstances.fluxcd.controlplane.io flux-system flux True ReconciliationSucceeded
	ready fluxreports.fluxcd.controlplane.io flux-system flux True ReconciliationSucceeded
	ready resourcesets.fluxcd.controlplane.io "$ns" parity-rs True ReconciliationSucceeded
	ready resourcesets.fluxcd.controlplane.io "$ns" parity-rs-paused True ReconciliationSucceeded
	ready resourcesetinputproviders.fluxcd.controlplane.io "$ns" parity-rsip True ReconciliationSucceeded
}

images_objects() {
	# Flux image automation (coverage S1b): stand-ins for both controllers,
	# found by their component label in flux-system, so the Flux task has no
	# controller issue. The baseline is healthy (case B has no attention):
	# a recent scan and run under a long interval, a resolved policy, and a
	# suspended automation, which is paused, never attention. Faults are
	# covered by the observer-path tests (internal/healthsrc/fluximages_test.go).
	local c
	for c in image-reflector-controller image-automation-controller; do
		k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $c
  namespace: flux-system
  labels: {app.kubernetes.io/component: $c}
  annotations:
    $(marks ok true)
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: $c}}
  template:
    metadata:
      labels: {app: $c}
      annotations:
        klyx.dev/parity-role: "ok"
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: manager, image: $pause, imagePullPolicy: Never}]
EOF
		k -n flux-system rollout status "deployment/$c" --timeout=120s >/dev/null
		settled_pods flux-system "$c" Running
	done
	k apply -f - >/dev/null <<EOF
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageRepository
metadata:
  name: parity-repo
  namespace: $ns
  annotations:
    $(marks ok false)
spec: {image: ghcr.io/klyx/parity, interval: 24h}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImagePolicy
metadata:
  name: parity-policy
  namespace: $ns
  labels: {klyx.dev/parity: images}
  annotations:
    $(marks ok false)
spec: {imageRepositoryRef: {name: parity-repo}, policy: {semver: {range: ">=1.0.0"}}}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageUpdateAutomation
metadata:
  name: parity-auto
  namespace: $ns
  annotations:
    $(marks ok false)
spec:
  interval: 24h
  sourceRef: {kind: GitRepository, name: parity-git}
  git: {push: {branch: auto}, commit: {author: {name: parity, email: parity@klyx.dev}}}
  policySelector: {matchLabels: {klyx.dev/parity: images}}
  update: {strategy: Setters, path: ./}
---
apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageUpdateAutomation
metadata:
  name: parity-auto-paused
  namespace: $ns
  annotations:
    $(marks paused false)
spec:
  interval: 24h
  suspend: true
  sourceRef: {kind: GitRepository, name: parity-git}
  git: {push: {branch: auto}, commit: {author: {name: parity, email: parity@klyx.dev}}}
EOF
	local t
	t=$(now)
	ready imagerepositories.image.toolkit.fluxcd.io "$ns" parity-repo True Succeeded "\"lastScanResult\":{\"tagCount\":3,\"scanTime\":\"$t\",\"latestTags\":[\"1.2.0\",\"1.1.0\",\"1.0.0\"]},\"canonicalImageName\":\"ghcr.io/klyx/parity\""
	ready imagepolicies.image.toolkit.fluxcd.io "$ns" parity-policy True Succeeded "\"latestRef\":{\"name\":\"ghcr.io/klyx/parity\",\"tag\":\"1.2.0\"}"
	ready imageupdateautomations.image.toolkit.fluxcd.io "$ns" parity-auto True Succeeded "\"lastAutomationRunTime\":\"$t\",\"observedPolicies\":{\"parity-policy\":{\"name\":\"ghcr.io/klyx/parity\",\"tag\":\"1.2.0\"}}"
	ready imageupdateautomations.image.toolkit.fluxcd.io "$ns" parity-auto-paused True Succeeded
}

composition_objects() {
	# Source composition and Receivers (coverage S1c): stand-ins for
	# source-watcher and notification-controller, found by their component
	# label in flux-system, so the Flux task has no controller issue. The
	# baseline is healthy: a ready generator whose inventory names a ready
	# artifact that records it, and a ready Receiver. Faults are covered by
	# the observer-path tests (internal/healthsrc/fluxcomposition_test.go).
	local c
	for c in source-watcher notification-controller; do
		k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $c
  namespace: flux-system
  labels: {app.kubernetes.io/component: $c}
  annotations:
    $(marks ok true)
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: $c}}
  template:
    metadata:
      labels: {app: $c}
      annotations:
        klyx.dev/parity-role: "ok"
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: manager, image: $pause, imagePullPolicy: Never}]
EOF
		k -n flux-system rollout status "deployment/$c" --timeout=120s >/dev/null
		settled_pods flux-system "$c" Running
	done
	k apply -f - >/dev/null <<EOF
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: parity-gen
  namespace: $ns
  annotations:
    $(marks ok false)
spec:
  sources: [{alias: git, kind: GitRepository, name: parity-git}]
  artifacts: [{name: parity-art, copy: [{from: "@git/**", to: "@artifact/"}]}]
---
apiVersion: notification.toolkit.fluxcd.io/v1
kind: Receiver
metadata:
  name: parity-receiver
  namespace: $ns
  annotations:
    $(marks ok false)
spec:
  type: generic
  secretRef: {name: parity-token}
  resources: [{kind: GitRepository, name: parity-git}]
EOF
	local uid t
	uid=$(k -n "$ns" get artifactgenerators.source.extensions.fluxcd.io parity-gen -o jsonpath='{.metadata.uid}')
	k apply -f - >/dev/null <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: ExternalArtifact
metadata:
  name: parity-art
  namespace: $ns
  labels: {source.extensions.fluxcd.io/generator: "$uid"}
  annotations:
    $(marks ok false)
spec:
  sourceRef: {apiVersion: source.extensions.fluxcd.io/v1beta1, kind: ArtifactGenerator, name: parity-gen}
EOF
	t=$(now)
	ready artifactgenerators.source.extensions.fluxcd.io "$ns" parity-gen True Succeeded "\"inventory\":[{\"name\":\"parity-art\",\"namespace\":\"$ns\",\"digest\":\"sha256:ab\",\"filename\":\"a.tar.gz\"}]"
	ready externalartifacts.source.toolkit.fluxcd.io "$ns" parity-art True Succeeded "\"artifact\":{\"revision\":\"latest@sha256:ab\",\"digest\":\"sha256:ab\",\"path\":\"a.tar.gz\",\"url\":\"http://source-watcher.flux-system.svc/a.tar.gz\",\"lastUpdateTime\":\"$t\"}"
	ready receivers.notification.toolkit.fluxcd.io "$ns" parity-receiver True Succeeded "\"webhookPath\":\"/hook/parity\""
}

velero_objects() {
	# Velero (coverage S2a): a stand-in server (component=velero, a container
	# named velero) and a healthy, time-free baseline. Velero has no status
	# subresource, so status is written by a plain merge patch. A paused
	# schedule; a yearly schedule awaiting its first run (none is owed for
	# months, so this fixture is not for New Year's Eve); an Available
	# location; and a completed unscheduled backup. Every Backup of the
	# baseline is unscheduled: the oracle counts every object of a counted
	# kind, and the backups part counts only those. Faults and
	# clock-driven states are covered by internal/velero's fake-clock tests
	# and internal/healthsrc's observer-path test.
	k create namespace velero --dry-run=client -o yaml | k apply -f - >/dev/null
	k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: velero
  namespace: velero
  labels: {component: velero}
  annotations:
    $(marks ok true)
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: velero}}
  template:
    metadata:
      labels: {app: velero, deploy: velero, component: velero}
      annotations:
        klyx.dev/parity-role: "ok"
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: velero, image: $pause, imagePullPolicy: Never}]
EOF
	k -n velero rollout status deployment/velero --timeout=120s >/dev/null
	settled_pods velero velero Running
	local stamp t
	stamp=$(date -u +%Y%m%d%H%M%S)
	t="${stamp:0:4}-${stamp:4:2}-${stamp:6:2}T${stamp:8:2}:${stamp:10:2}:${stamp:12:2}Z"
	k apply -f - >/dev/null <<EOF
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: parity-paused
  namespace: velero
  annotations:
    $(marks paused false)
spec: {schedule: "0 2 * * *", paused: true, template: {storageLocation: parity-available}}
---
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: parity-yearly
  namespace: velero
  annotations:
    $(marks progressing false)
spec: {schedule: "0 0 1 1 *", template: {storageLocation: parity-available}}
---
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: parity-available
  namespace: velero
  annotations:
    $(marks ok false)
spec: {provider: aws, default: true, objectStorage: {bucket: parity}}
---
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: parity-manual
  namespace: velero
  annotations:
    $(marks ok false)
spec: {storageLocation: parity-available}
EOF
	vstatus() { k -n velero patch "$1" "$2" --type=merge -p "{\"status\":$3}" >/dev/null; }
	vstatus schedules.velero.io parity-paused '{"phase":"Enabled"}'
	vstatus schedules.velero.io parity-yearly '{"phase":"Enabled"}'
	vstatus backupstoragelocations.velero.io parity-available "{\"phase\":\"Available\",\"lastValidationTime\":\"$t\"}"
	vstatus backups.velero.io parity-manual "{\"phase\":\"Completed\",\"startTimestamp\":\"$t\",\"completionTimestamp\":\"$t\"}"
}

admission_objects() {
	# Admission (coverage S3a): a healthy baseline that admits nothing. The
	# webhook targets a group and resource that do not exist, fails open,
	# and is backed by a Service over the Velero stand-in's ready pod; the
	# policy's expression is true and its binding matches no namespace.
	# Faults and every backend and policy state are covered by
	# internal/admission's tests and the kind-klyx lab (lab-s3a).
	k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata: {name: parity-admission-hook, namespace: velero}
spec:
  selector: {app: velero}
  ports: [{name: https, port: 443, targetPort: 8443, protocol: TCP}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: parity-admission
  annotations:
    $(marks ok false)
webhooks:
- name: parity.lab.klyx.dev
  failurePolicy: Ignore
  sideEffects: None
  admissionReviewVersions: [v1]
  clientConfig: {service: {namespace: velero, name: parity-admission-hook, port: 443}}
  rules: [{operations: [CREATE], apiGroups: [lab.klyx.dev], apiVersions: [v1], resources: [widgets]}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: parity-policy
  annotations:
    $(marks ok false)
spec:
  failurePolicy: Ignore
  matchConstraints: {resourceRules: [{operations: [CREATE], apiGroups: [lab.klyx.dev], apiVersions: [v1], resources: [widgets]}]}
  validations: [{expression: "true"}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: parity-policy-b}
spec:
  policyName: parity-policy
  validationActions: [Deny]
  matchResources: {namespaceSelector: {matchLabels: {klyx.dev/parity: none}}}
EOF
}

lb_objects() {
	# Load balancing (coverage S4): a healthy baseline with no controller
	# writing. A stand-in MetalLB controller (its native labels); a pool
	# with written counts, selected by an L2Advertisement, in the namespace
	# of a Valid controller ConfigurationState; a LoadBalancer Service whose
	# address the fixture writes (kind runs no load-balancer controller, and
	# a Service of another class is ignored by every controller); and a
	# Cilium pool with current counts. Pending, exhaustion, conflict,
	# advertisement and configuration faults are covered by internal/lb's
	# tests and the kind-klyx lab (lab-s4, a real MetalLB).
	k create namespace metallb-system --dry-run=client -o yaml | k apply -f - >/dev/null
	k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller
  namespace: metallb-system
  labels: {app: metallb, component: controller}
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: metallb-stand-in}}
  template:
    metadata:
      labels: {app: metallb-stand-in}
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: controller, image: $pause, imagePullPolicy: Never}]
EOF
	k -n metallb-system rollout status deployment/controller --timeout=120s >/dev/null
	settled_pods metallb-system metallb-stand-in Running
	k apply -f - >/dev/null <<EOF
apiVersion: metallb.io/v1beta1
kind: ConfigurationState
metadata:
  name: controller
  namespace: metallb-system
  annotations:
    $(marks ok false)
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: parity-pool
  namespace: metallb-system
  labels: {tier: gold}
  annotations:
    $(marks ok false)
spec: {addresses: ["192.0.2.0/28"], autoAssign: false}
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata: {name: parity-l2, namespace: metallb-system}
spec:
  ipAddressPoolSelectors: [{matchLabels: {tier: gold}}]
---
apiVersion: v1
kind: Service
metadata:
  name: parity-lb
  namespace: $ns
  annotations:
    $(marks ok false)
spec:
  type: LoadBalancer
  loadBalancerClass: parity.klyx.dev/written
  selector: {app: parity-none}
  ports: [{name: http, port: 80, targetPort: 8080, protocol: TCP}]
---
apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata:
  name: parity-cilium
  annotations:
    $(marks ok false)
spec: {blocks: [{cidr: "198.51.100.0/28"}]}
EOF
	status configurationstates.metallb.io metallb-system controller '{"result":"Valid"}'
	status ipaddresspools.metallb.io metallb-system parity-pool '{"assignedIPv4":1,"availableIPv4":15,"assignedIPv6":0,"availableIPv6":0}'
	status services "$ns" parity-lb '{"loadBalancer":{"ingress":[{"ip":"192.0.2.1"}]}}'
	local g
	g=$(gen ciliumloadbalancerippools.cilium.io - parity-cilium)
	status ciliumloadbalancerippools.cilium.io - parity-cilium "{\"conditions\":[$(cond cilium.io/IPsTotal Unknown noreason "$g" | sed 's/klyx parity fixture/16/'),$(cond cilium.io/IPsAvailable Unknown noreason "$g" | sed 's/klyx parity fixture/16/'),$(cond cilium.io/IPsUsed Unknown noreason "$g" | sed 's/klyx parity fixture/0/')]}"
}

# alerts_objects (coverage S8a): a real Alertmanager 0.34.1 in parity-alerting,
# read by the parity connection through the service proxy with
# match cluster=kind-klyx-parity. Posted with amtool inside it: two critical
# and one warning active (counted: 3), one info active (not counted), one
# critical silenced (suppressed), and another cluster's alert that match
# must keep out. The baseline waits until every alert is processed.
am_ns=parity-alerting
alerts_objects() {
	k create namespace $am_ns --dry-run=client -o yaml | k apply -f - >/dev/null
	k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ConfigMap
metadata: {name: alertmanager-config, namespace: $am_ns}
data:
  alertmanager.yml: |
    route: {receiver: parity, group_by: [alertname]}
    receivers: [{name: parity}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: alertmanager, namespace: $am_ns}
spec:
  replicas: 1
  selector: {matchLabels: {app: alertmanager}}
  template:
    metadata: {labels: {app: alertmanager}}
    spec:
      containers:
        - name: alertmanager
          image: quay.io/prometheus/alertmanager:v0.34.1
          args: [--config.file=/etc/am/alertmanager.yml, --storage.path=/data, --cluster.listen-address=]
          ports: [{name: web, containerPort: 9093}]
          volumeMounts: [{name: config, mountPath: /etc/am}, {name: data, mountPath: /data}]
          readinessProbe: {httpGet: {path: /-/ready, port: web}}
      volumes: [{name: config, configMap: {name: alertmanager-config}}, {name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: alertmanager, namespace: $am_ns}
spec:
  selector: {app: alertmanager}
  ports: [{name: web, port: 9093, targetPort: web}]
EOF
	k -n $am_ns rollout status deploy/alertmanager --timeout=240s >/dev/null
	local ends c=cluster=kind-klyx-parity
	ends=$(date -u -v+6H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+6 hours' +%Y-%m-%dT%H:%M:%SZ)
	k -n $am_ns exec deploy/alertmanager -c alertmanager -- sh -c "
		a() { amtool --alertmanager.url=http://localhost:9093 alert add \"\$@\" --end=$ends >/dev/null; }
		a alertname=ParityCritA severity=critical $c namespace=$ns
		a alertname=ParityCritB severity=critical $c
		a alertname=ParityWarn severity=warning $c namespace=$ns
		a alertname=ParityInfo severity=info $c
		a alertname=ParitySilenced severity=critical $c
		a alertname=ParityElsewhere severity=critical cluster=elsewhere
		amtool --alertmanager.url=http://localhost:9093 silence add alertname=ParitySilenced --author=klyx-parity --comment=parity --duration=6h >/dev/null"
	local i
	for ((i = 0; i < 60; i++)); do
		if k get --raw "/api/v1/namespaces/$am_ns/services/http:alertmanager:web/proxy/api/v2/alerts?filter=cluster%3D%22kind-klyx-parity%22" |
			jq -e '(map(select(.status.state == "active")) | length) == 4 and (map(select(.status.state == "suppressed")) | length) == 1' >/dev/null 2>&1; then
			return 0
		fi
		sleep 2
	done
	die "the parity Alertmanager's alerts were not all processed"
}

# KEDA (coverage S9): the real 2.21.0 release manifest, pinned by its
# digest (klyx-parity.sh) and cached in the state directory, so the
# capability reads what it reads on any cluster - the operator by its
# labels and running image, the admission webhooks, and the external
# metrics APIService served by KEDA's own metrics server. A stand-in
# operator cannot serve that API, and its absence is a controller issue
# (S9 §2), which a healthy baseline cannot carry. The operator and the
# metrics server watch only the keda namespace, so nothing reconciles the
# fixture's scalers: their statuses are written here, as every capability
# object's is. The admission webhooks keep the release's cluster-wide scope
# and validate every fixture scaler, as they would anywhere.
keda_install() {
	local got d i
	if [[ ! -f $keda_manifest || $(openssl dgst -sha256 -r "$keda_manifest" | awk '{print $1}') != "$keda_sha256" ]]; then
		curl -fsSL --max-time 120 -o "$keda_manifest.part" "https://github.com/kedacore/keda/releases/download/v$keda_version/keda-$keda_version.yaml" ||
			die "could not fetch KEDA $keda_version's release manifest"
		got=$(openssl dgst -sha256 -r "$keda_manifest.part" | awk '{print $1}')
		[[ $got == "$keda_sha256" ]] || { rm -f "$keda_manifest.part"; die "KEDA's release manifest has digest $got, pinned $keda_sha256"; }
		mv "$keda_manifest.part" "$keda_manifest"
	fi
	k create --dry-run=client -o json -f "$keda_manifest" |
		jq 'if .kind == "Deployment" and (.metadata.name == "keda-operator" or .metadata.name == "keda-metrics-apiserver")
			then .spec.template.spec.containers |= map(.env |= map(if .name == "WATCH_NAMESPACE" then .value = "keda" else . end))
			else . end' |
		k apply --server-side --force-conflicts --field-manager=klyx-parity -f - >/dev/null
	for d in keda-operator keda-metrics-apiserver; do
		[[ $(k -n keda get deployment "$d" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="WATCH_NAMESPACE")].value}') == keda ]] ||
			die "KEDA's $d does not watch only the keda namespace"
	done
	for d in keda-operator keda-metrics-apiserver keda-admission; do
		k -n keda rollout status "deployment/$d" --timeout=300s >/dev/null
	done
	# the operator's certificate rotation writes the CA the APIService needs
	for ((i = 0; i < 90; i++)); do
		[[ $(k get apiservices.apiregistration.k8s.io v1beta1.external.metrics.k8s.io -o jsonpath='{.status.conditions[?(@.type=="Available")].status}') == True ]] && return
		sleep 2
	done
	die "KEDA's external metrics API did not become available"
}

# zero_target <name>: a Deployment at 0 replicas - KEDA's idle scale - so
# kind's HPA controller reports AbleToScale=True and ScalingActive=False
# ScalingDisabled, the same status every sync and never a metric read: no
# timer flips an HPA. The Deployment reads Scaled to 0.
zero_target() {
	k apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $1
  namespace: $ns
  annotations:
    $(marks paused false)
spec:
  replicas: 0
  selector: {matchLabels: {app: $1}}
  template:
    metadata: {labels: {app: $1}}
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: pause, image: $pause, imagePullPolicy: Never}]
EOF
}

# keda_hpa <scaledobject> <target>: the HPA KEDA creates for a
# ScaledObject - its name, labels and controller reference - since no KEDA
# reconciles the fixture's scalers.
keda_hpa() {
	local uid
	uid=$(k -n "$ns" get scaledobjects.keda.sh "$1" -o jsonpath='{.metadata.uid}')
	k apply -f - >/dev/null <<EOF
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: keda-hpa-$1
  namespace: $ns
  labels: {app.kubernetes.io/managed-by: keda-operator, app.kubernetes.io/name: keda-hpa-$1, app.kubernetes.io/part-of: $1, scaledobject.keda.sh/name: $1}
  ownerReferences: [{apiVersion: keda.sh/v1alpha1, kind: ScaledObject, name: $1, uid: "$uid", controller: true, blockOwnerDeletion: true}]
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: $2}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: External, external: {metric: {name: s0-prometheus, selector: {matchLabels: {scaledobject.keda.sh/name: $1}}}, target: {type: AverageValue, averageValue: "10"}}}]
EOF
}

# hpa_reported <name> <condition> <reason>: wait until kind's HPA
# controller has reported the HPA (it syncs each every 15s).
hpa_reported() {
	local i
	for ((i = 0; i < 60; i++)); do
		[[ $(k -n "$ns" get horizontalpodautoscalers.autoscaling "$1" -o jsonpath="{.status.conditions[?(@.type==\"$2\")].reason}" 2>/dev/null) == "$3" ]] && return
		sleep 2
	done
	die "the HPA controller did not report $ns/$1 $2 $3"
}

# so_status <name> <conditions json> [extra status json, without braces]:
# a ScaledObject's status as KEDA 2.21 writes it (no condition times).
so_status() {
	status scaledobjects.keda.sh "$ns" "$1" "{\"conditions\":$2${3:+,$3}}"
}

kcond() { printf '{"type":"%s","status":"%s","reason":"%s","message":"%s"}' "$@"; }

keda_objects() {
	# The baseline (coverage S9): a ScaledObject idle at zero with its HPA
	# and an authentication; a ScaledObject whose full pause completed (no
	# HPA); an idle ScaledJob; and a standalone HPA. Faults are case 9's.
	keda_install
	zero_target parity-keda-idle
	zero_target parity-keda-paused
	zero_target parity-hpa-target
	k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata: {name: parity-keda-secret, namespace: $ns}
stringData: {token: parity-fixture}
---
apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata: {name: parity-keda-auth, namespace: $ns}
spec:
  secretTargetRef: [{parameter: bearerToken, name: parity-keda-secret, key: token}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: parity-so-idle
  namespace: $ns
  annotations:
    $(marks ok false)
spec:
  scaleTargetRef: {name: parity-keda-idle}
  minReplicaCount: 0
  maxReplicaCount: 3
  triggers:
  - type: prometheus
    metadata: {serverAddress: "http://prometheus.$ns.svc:9090", query: "sum(rate(http_requests_total[1m]))", threshold: "10", authModes: bearer}
    authenticationRef: {name: parity-keda-auth}
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: parity-so-paused
  namespace: $ns
  annotations:
    autoscaling.keda.sh/paused: "true"
    $(marks paused false)
spec:
  scaleTargetRef: {name: parity-keda-paused}
  minReplicaCount: 0
  maxReplicaCount: 3
  triggers: [{type: cron, metadata: {timezone: UTC, start: "0 0 * * *", end: "59 23 * * *", desiredReplicas: "2"}}]
---
apiVersion: keda.sh/v1alpha1
kind: ScaledJob
metadata:
  name: parity-sj
  namespace: $ns
  annotations:
    $(marks ok false)
spec:
  maxReplicaCount: 1
  jobTargetRef:
    template:
      spec:
        restartPolicy: Never
        containers: [{name: pause, image: $pause, imagePullPolicy: Never}]
  triggers: [{type: cron, metadata: {timezone: UTC, start: "0 3 * * *", end: "1 3 * * *", desiredReplicas: "1"}}]
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: parity-hpa-plain
  namespace: $ns
  annotations:
    $(marks ok false)
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: parity-hpa-target}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 80}}}]
EOF
	keda_hpa parity-so-idle parity-keda-idle
	local gvkr='"scaleTargetGVKR":{"group":"apps","kind":"Deployment","resource":"deployments","version":"v1"},"scaleTargetKind":"apps/v1.Deployment"'
	so_status parity-so-idle "[$(kcond Ready True ScaledObjectReady "ScaledObject is defined correctly and is ready for scaling"),$(kcond Active False ScalerNotActive "Scaling is not performed because triggers are not active"),$(kcond Fallback False NoFallbackFound "No fallbacks are active on this scaled object"),{\"type\":\"Paused\",\"status\":\"False\"},$(kcond HPAActive True HPAActive "HPA is actively scaling")]" \
		"\"hpaName\":\"keda-hpa-parity-so-idle\",\"externalMetricNames\":[\"s0-prometheus\"],\"originalReplicaCount\":0,$gvkr,\"triggersTypes\":\"prometheus\",\"authenticationsTypes\":\"parity-keda-auth\",\"triggersActivity\":{\"s0-prometheus\":{\"isActive\":false}}"
	so_status parity-so-paused "[$(kcond Ready True ScaledObjectReady "ScaledObject is paused"),{\"type\":\"Active\",\"status\":\"Unknown\"},$(kcond Fallback False NoFallbackFound "No fallbacks are active on this scaled object"),$(kcond Paused True ScaledObjectPaused "ScaledObject is paused")]"
	status scaledjobs.keda.sh "$ns" parity-sj "{\"conditions\":[$(kcond Ready True ScaledJobReady "ScaledJob is defined correctly and is ready for scaling"),$(kcond Active False ScalerNotActive "Scaling is not performed because triggers are not active"),{\"type\":\"Fallback\",\"status\":\"Unknown\"},{\"type\":\"Paused\",\"status\":\"False\"}],\"triggersTypes\":\"cron\"}"
	hpa_reported keda-hpa-parity-so-idle ScalingActive ScalingDisabled
	hpa_reported parity-hpa-plain ScalingActive ScalingDisabled
}

baseline() {
	controllers
	flux_objects
	fluxop_objects
	images_objects
	composition_objects
	argo_objects
	certs_objects
	eso_objects
	gateway_objects
	karpenter_objects
	velero_objects
	admission_objects
	lb_objects
	keda_objects
	alerts_objects
	extended_objects
	dra_objects
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
	5d) # a ListenerSet its controller did not accept (coverage S7a)
		listenerset parity-ls-notallowed False NotAllowed fault true ;;
	5e) # a TCPRoute whose backend Service has no TCP port 6432 (coverage S7b)
		tcproute parity-tcp-noport 6432 fault true ;;
	6) # cert-manager down, no cert-manager object fault (core: its
		# Deployment and the Pending pod are faults, declared)
		stand_in cert-manager cert-manager fault ;;
	6b) # the Flux controller down: the GitOps tier degrades the connection
		stand_in flux-system kustomize-controller fault ;;
	9) # coverage S9: a ScaledObject on fallback (degraded) with its HPA, and
		# a standalone HPA whose target does not exist (kind's HPA controller
		# reports AbleToScale=False FailedGetScale every sync)
		zero_target parity-keda-fallback
		k apply -f - >/dev/null <<EOF
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: parity-so-fallback
  namespace: $ns
  annotations:
    $(marks degraded true)
spec:
  scaleTargetRef: {name: parity-keda-fallback}
  minReplicaCount: 0
  maxReplicaCount: 3
  fallback: {failureThreshold: 3, replicas: 2}
  triggers:
  - type: prometheus
    metadata: {serverAddress: "http://prometheus.$ns.svc:9090", query: "sum(rate(http_requests_total[1m]))", threshold: "10"}
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: parity-hpa-gone
  namespace: $ns
  annotations:
    $(marks fault true)
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: parity-missing}
  minReplicas: 1
  maxReplicas: 3
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 80}}}]
EOF
		keda_hpa parity-so-fallback parity-keda-fallback
		so_status parity-so-fallback "[$(kcond Ready True ScaledObjectReady "ScaledObject is defined correctly and is ready for scaling"),$(kcond Active False ScalerNotActive "Scaling is not performed because triggers are not active"),$(kcond Fallback True FallbackExists "At least one trigger is falling back on this scaled object"),{\"type\":\"Paused\",\"status\":\"False\"},$(kcond HPAActive True HPAActive "HPA is actively scaling")]" \
			'"hpaName":"keda-hpa-parity-so-fallback","externalMetricNames":["s0-prometheus"],"health":{"s0-prometheus":{"numberOfFailures":7,"status":"Failing"}},"scaleTargetGVKR":{"group":"apps","kind":"Deployment","resource":"deployments","version":"v1"},"scaleTargetKind":"apps/v1.Deployment","triggersTypes":"prometheus"'
		hpa_reported keda-hpa-parity-so-fallback ScalingActive ScalingDisabled
		hpa_reported parity-hpa-gone AbleToScale FailedGetScale ;;
	10) # coverage S10: the EC2NodeClass fails; its two pools light once each
		# (the class's failure, never their copies twice), the class not at all
		class_ready ec2nodeclasses.karpenter.k8s.aws parity-ec2 False
		local p
		for p in parity-pool parity-pool-b; do
			k annotate nodepools.karpenter.sh "$p" klyx.dev/parity-role=fault klyx.dev/parity-included=true --overwrite >/dev/null
			pool_ready "$p" False
		done ;;
	11) # coverage S11a: the device plugin stops; the node reads its
		# resource not allocatable (degraded, counted once) within the grace
		k -n "$ns" patch daemonset parity-device-plugin --type=merge -p '{"spec":{"template":{"spec":{"nodeSelector":{"klyx.dev/parity-plugin":"off"}}}}}' >/dev/null
		ext_wait 0
		k annotate node "$name-control-plane" klyx.dev/parity-role=degraded klyx.dev/parity-included=true --overwrite >/dev/null ;;
	12) # coverage S11b: a claim for nine devices of eight; its pod's only
		# claim, so the scheduler's reason is claim-specific (degraded), and
		# the pod is unschedulable (fault)
		k apply -f - >/dev/null <<EOF
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: parity-claim-nine
  namespace: $ns
  annotations: {klyx.dev/parity-role: degraded, klyx.dev/parity-included: "true"}
spec:
  devices:
    requests:
    - name: gpu
      exactly: {deviceClassName: gpu.example.com, allocationMode: ExactCount, count: 9}
---
apiVersion: v1
kind: Pod
metadata:
  name: parity-dra-nine
  namespace: $ns
  annotations: {klyx.dev/parity-role: fault}
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - name: pause
    image: $pause
    imagePullPolicy: Never
    resources: {claims: [{name: gpu}]}
  resourceClaims: [{name: gpu, resourceClaimName: parity-claim-nine}]
EOF
		local i=0
		until k -n "$ns" get pod parity-dra-nine -o 'jsonpath={.status.conditions[?(@.type=="PodScheduled")].reason}' | grep -q Unschedulable; do
			i=$((i + 1))
			((i > 60)) && die "parity-dra-nine never read unschedulable"
			sleep 2
		done ;;
	13) # coverage S11b: an invalid fixture pool (two slices name one device)
		local s
		for s in a b; do
			k apply -f - >/dev/null <<EOF
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  name: parity-invalid-$s
  annotations: {klyx.dev/parity-role: degraded, klyx.dev/parity-included: "true"}
spec:
  driver: fixture.klyx.dev
  pool: {name: parity-invalid, generation: 1, resourceSliceCount: 2}
  allNodes: true
  devices: [{name: dev-0}]
EOF
		done ;;
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
	5d) k -n "$ns" delete listenersets.gateway.networking.k8s.io parity-ls-notallowed --wait >/dev/null ;;
	5e) k -n "$ns" delete tcproutes.gateway.networking.k8s.io parity-tcp-noport --wait >/dev/null ;;
	6) stand_in cert-manager cert-manager "" ;;
	6b) stand_in flux-system kustomize-controller "" ;;
	9) k -n "$ns" delete scaledobjects.keda.sh parity-so-fallback --wait >/dev/null
		k -n "$ns" delete horizontalpodautoscalers.autoscaling keda-hpa-parity-so-fallback parity-hpa-gone --ignore-not-found --wait >/dev/null
		k -n "$ns" delete deployment parity-keda-fallback --wait >/dev/null ;;
	10) class_ready ec2nodeclasses.karpenter.k8s.aws parity-ec2 True
		local p
		for p in parity-pool parity-pool-b; do
			k annotate nodepools.karpenter.sh "$p" klyx.dev/parity-role=ok klyx.dev/parity-included=false --overwrite >/dev/null
			pool_ready "$p" True
		done ;;
	11) k -n "$ns" patch daemonset parity-device-plugin --type=json -p '[{"op":"remove","path":"/spec/template/spec/nodeSelector"}]' >/dev/null
		ext_wait 2
		k annotate node "$name-control-plane" klyx.dev/parity-role=ok klyx.dev/parity-included=false --overwrite >/dev/null ;;
	12) k -n "$ns" delete pod parity-dra-nine --wait >/dev/null
		k -n "$ns" delete resourceclaims.resource.k8s.io parity-claim-nine --wait >/dev/null ;;
	13) k delete resourceslices.resource.k8s.io parity-invalid-a parity-invalid-b --wait >/dev/null ;;
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
	done | jq -Rn '[inputs | split("\t") | {g: .[0], r: .[1]}] | group_by(.g) | map({apiGroups: [.[0].g], resources: map(.r), verbs: ["get", "list", "watch"]}) + [{apiGroups: [""], resources: ["services/proxy"], verbs: ["get"]}]')
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
	# Returned only once the new API server serves and the kubelet has
	# reported its pod Ready again with a higher restart count: /readyz alone
	# can answer from the old process, and the pod's mirror status reads
	# Ready=False for about ten seconds after the restart - a pod the
	# Observer then rightly reads as Progressing, which a hold check would
	# race (found in S2a's parity runs).
	local pod=kube-apiserver-$node before i rc ready
	before=$(k -n kube-system get pod "$pod" -o jsonpath='{.status.containerStatuses[0].restartCount}')
	docker exec "$node" sh -c 'crictl stop $(crictl ps --name kube-apiserver -q)' >/dev/null
	for ((i = 0; i < 120; i++)); do
		if k get --raw /readyz >/dev/null 2>&1; then
			rc=$(k -n kube-system get pod "$pod" -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null || true)
			ready=$(k -n kube-system get pod "$pod" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
			if [[ -n $rc && $rc -gt $before && $ready == True ]]; then
				echo "klyx-parity: API server restarted"
				return
			fi
		fi
		sleep 1
	done
	die "the API server did not come back, reported Ready with a new restart, within 120s"
}
