#!/usr/bin/env bash
# klyx-parity.sh - the 1c slice 6 coverage-parity fixture, on its OWN kind
# cluster (docs/superpowers/specs/2026-10-12-klyx-health-1c-slice6-parity.md):
#
#   scripts/lab/klyx-parity.sh up              create kind-klyx-parity, mark it ours, apply the baseline
#   scripts/lab/klyx-parity.sh case <name>     apply one case on the baseline
#   scripts/lab/klyx-parity.sh revert <name>   restore the baseline after that case
#   scripts/lab/klyx-parity.sh rbac <deny>     the restricted Observer identity (cases 7, 8)
#   scripts/lab/klyx-parity.sh sa-kubeconfig   a one-hour token kubeconfig for that identity
#   scripts/lab/klyx-parity.sh restart-apiserver   end every open watch (case 8)
#   scripts/lab/klyx-parity.sh check           the exact-target check alone
#   scripts/lab/klyx-parity.sh down            delete the cluster and the script's own files
#
# Isolation (spec §1): every write goes to exactly one target - the kind
# cluster klyx-parity this script created, recognised by its ownership
# marker and its loopback API server. Its kubeconfig is a separate file
# ($KLYX_PARITY_KUBECONFIG), never ~/.kube/config, so no current-context
# changes. The CRD source (kind-klyx) is only ever read.
#
# The state directory ($KLYX_PARITY_DIR) is the script's only when it holds
# the script's directory marker: up refuses a non-empty directory without
# it, down refuses without it (cluster or not), and down removes only the
# script's named files - never anything else in the directory.
set -euo pipefail

cmd=${1:-}
arg=${2:-}
name=klyx-parity
ctx=kind-$name
dir=${KLYX_PARITY_DIR:-${TMPDIR:-/tmp}/klyx-parity}
dirmark=$dir/.klyx-parity-dir
kubeconfig=$dir/kubeconfig
state=$dir/owner
own_files=("$kubeconfig" "$state" "$dir/sa-kubeconfig" "$dirmark")
src=${KLYX_PARITY_CRD_SOURCE:-kind-klyx}
here=$(cd "$(dirname "$0")" && pwd)
fixtures=$here/parity

die() { echo "klyx-parity: $*" >&2; exit 1; }
k() { kubectl --kubeconfig "$kubeconfig" --context "$ctx" "$@"; }

exists() { kind get clusters 2>/dev/null | grep -qx "$name"; }

# owned: the exact-target check, before any write (spec §1). The context
# name, its API server (equal to kind's own record, on loopback) and the
# ownership marker must all match.
owned() {
	[[ -f $dirmark ]] || die "refusing: $dir is not a klyx-parity directory (no $dirmark)"
	[[ -f $kubeconfig ]] || die "no parity kubeconfig at $kubeconfig"
	[[ -f $state ]] || die "no ownership state at $state"
	local have want token
	have=$(kubectl --kubeconfig "$kubeconfig" config view --minify --context "$ctx" -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null) ||
		die "context $ctx not in $kubeconfig"
	want=$(kind get kubeconfig --name "$name" 2>/dev/null | awk '/server:/ {print $2; exit}')
	[[ -n $want && $have == "$want" ]] || die "context $ctx points at '$have', kind's $name is at '$want'"
	[[ $have =~ ^https://(127\.0\.0\.1|localhost|\[::1\]): ]] || die "API server $have is not loopback"
	token=$(k -n kube-system get configmap klyx-parity-owner -o jsonpath='{.data.token}' 2>/dev/null) ||
		die "no ownership marker on $ctx"
	[[ -n $token && $token == "$(cat "$state")" ]] || die "ownership marker on $ctx does not match $state"
}

# The CRDs the Observer reads, copied from the read-only source with
# server-set fields stripped (no network). Applied server-side: Gateway API
# 1.6's HTTPRoute CRD is over client-side apply's 256 KiB annotation.
crds=(
	kustomizations.kustomize.toolkit.fluxcd.io helmreleases.helm.toolkit.fluxcd.io
	gitrepositories.source.toolkit.fluxcd.io ocirepositories.source.toolkit.fluxcd.io buckets.source.toolkit.fluxcd.io
	helmrepositories.source.toolkit.fluxcd.io helmcharts.source.toolkit.fluxcd.io
	applications.argoproj.io applicationsets.argoproj.io appprojects.argoproj.io
	certificates.cert-manager.io certificaterequests.cert-manager.io issuers.cert-manager.io clusterissuers.cert-manager.io
	orders.acme.cert-manager.io challenges.acme.cert-manager.io
	externalsecrets.external-secrets.io secretstores.external-secrets.io clustersecretstores.external-secrets.io
	gatewayclasses.gateway.networking.k8s.io gateways.gateway.networking.k8s.io httproutes.gateway.networking.k8s.io
	grpcroutes.gateway.networking.k8s.io referencegrants.gateway.networking.k8s.io listenersets.gateway.networking.k8s.io
	tlsroutes.gateway.networking.k8s.io tcproutes.gateway.networking.k8s.io udproutes.gateway.networking.k8s.io
	nodepools.karpenter.sh nodeclaims.karpenter.sh
	fluxinstances.fluxcd.controlplane.io fluxreports.fluxcd.controlplane.io
	resourcesets.fluxcd.controlplane.io resourcesetinputproviders.fluxcd.controlplane.io
	imagerepositories.image.toolkit.fluxcd.io imagepolicies.image.toolkit.fluxcd.io imageupdateautomations.image.toolkit.fluxcd.io
	artifactgenerators.source.extensions.fluxcd.io externalartifacts.source.toolkit.fluxcd.io receivers.notification.toolkit.fluxcd.io
	schedules.velero.io backupstoragelocations.velero.io backups.velero.io
	ipaddresspools.metallb.io l2advertisements.metallb.io bgpadvertisements.metallb.io configurationstates.metallb.io
	ciliumloadbalancerippools.cilium.io
)

copy_crds() {
	local n
	for n in "${crds[@]}"; do
		kubectl --context "$src" get crd "$n" -o json >/dev/null 2>&1 || die "CRD $n is missing on $src"
	done
	for n in "${crds[@]}"; do
		kubectl --context "$src" get crd "$n" -o json |
			jq 'del(.status, .metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.generation, .metadata.managedFields, .metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"])'
	done | k apply --server-side --force-conflicts --field-manager=klyx-parity -f - >/dev/null
	k wait --for condition=established --timeout=60s "${crds[@]/#/crd/}" >/dev/null
}

# status <kind> <ns|-> <name> <json>: write an object's status through the
# status subresource (nothing rewrites it: there are no controllers).
status() {
	local kind=$1 ns=$2 obj=$3 body=$4
	if [[ $ns == - ]]; then
		k patch "$kind" "$obj" --subresource=status --type=merge -p "{\"status\":$body}" >/dev/null
	else
		k -n "$ns" patch "$kind" "$obj" --subresource=status --type=merge -p "{\"status\":$body}" >/dev/null
	fi
}

up() {
	# the state directory: new, empty, or already the script's
	if [[ -d $dir && ! -f $dirmark && -n $(ls -A "$dir") ]]; then
		die "refusing: $dir is not empty and not a klyx-parity directory"
	fi
	mkdir -p "$dir"
	chmod 700 "$dir"
	touch "$dirmark"
	if exists; then
		owned # an existing cluster is only ever reused when it is ours
	else
		[[ ! -f $state ]] || rm -f "$state"
		local image
		image=${KLYX_PARITY_NODE_IMAGE:-$(docker inspect klyx-control-plane --format '{{.Config.Image}}')}
		kind create cluster --name "$name" --image "$image" --kubeconfig "$kubeconfig" --wait 180s
		chmod 600 "$kubeconfig"
		(umask 077; openssl rand -hex 16 >"$state")
		k -n kube-system create configmap klyx-parity-owner --from-literal=token="$(cat "$state")" >/dev/null
		owned
	fi
	copy_crds
	baseline
}

down() {
	[[ -f $dirmark ]] || die "refusing: $dir is not a klyx-parity directory (no $dirmark)"
	if exists; then
		owned
		kind delete cluster --name "$name" --kubeconfig "$kubeconfig"
	fi
	rm -f "${own_files[@]}"
	rmdir "$dir" 2>/dev/null || true # only when nothing else is there
}

# shellcheck source=parity/fixture.sh
. "$fixtures/fixture.sh"

case $cmd in
up) up ;;
down) down ;;
check) owned && echo "klyx-parity: $ctx is ours" ;;
case) owned; [[ -n $arg ]] || die "case needs a name"; apply_case "$arg" ;;
revert) owned; [[ -n $arg ]] || die "revert needs a name"; revert_case "$arg" ;;
rbac) owned; [[ -n $arg ]] || die "rbac needs a deny list (or none)"; rbac "$arg" ;;
sa-kubeconfig) owned; sa_kubeconfig ;;
restart-apiserver) owned; restart_apiserver ;;
*) die "usage: $0 up|case <name>|revert <name>|rbac <deny,...|none>|sa-kubeconfig|restart-apiserver|check|down" ;;
esac
