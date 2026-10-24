# lib.sh - shared by klyx-eval.sh and the scenarios' scripts (AI eval design
# §3, docs/superpowers/specs/2026-10-22-klyx-ai-eval.md). Every write goes to
# exactly one target: the kind cluster the eval directory names, recognised
# by kind's own record of its loopback API server and by the ownership
# marker init wrote. The eval's kubeconfig is its own file, never
# ~/.kube/config.

name=${KLYX_EVAL_KIND:-klyx}
ctx=kind-$name
dir=${KLYX_EVAL_DIR:-${TMPDIR:-/tmp}/klyx-eval}
dirmark=$dir/.klyx-eval
kubeconfig=$dir/kubeconfig
state=$dir/owner-token

die() { echo "klyx-eval: $*" >&2; exit 1; }
k() { kubectl --kubeconfig "$kubeconfig" --context "$ctx" "$@"; }

# owned: the exact-target check, before any write. The context's API
# server equals kind's own record, on loopback, and the cluster's ownership
# marker matches the directory's token: the eval build's preflight checks
# the same.
owned() {
	[[ -f $dirmark ]] || die "refusing: $dir is not an eval directory (no $dirmark); run init"
	[[ -f $kubeconfig ]] || die "no eval kubeconfig at $kubeconfig"
	[[ -s $state ]] || die "no ownership token at $state"
	local have want token
	have=$(kubectl --kubeconfig "$kubeconfig" config view --minify --context "$ctx" -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null) ||
		die "context $ctx not in $kubeconfig"
	want=$(kind get kubeconfig --name "$name" 2>/dev/null | awk '/server:/ {print $2; exit}')
	[[ -n $want && $have == "$want" ]] || die "context $ctx points at '$have', kind's $name is at '$want'"
	[[ $have =~ ^https://(127\.0\.0\.1|localhost|\[::1\]): ]] || die "API server $have is not loopback"
	token=$(k -n kube-system get configmap klyx-eval-owner -o jsonpath='{.data.token}' 2>/dev/null) ||
		die "no ownership marker on $ctx"
	[[ -n $token && $token == "$(cat "$state")" ]] || die "ownership marker on $ctx does not match $state"
}

# wait_for: poll a check until it holds or the timeout passes.
wait_for() {
	local timeout=$1 what=$2
	shift 2
	local end=$((SECONDS + timeout))
	until "$@"; do
		((SECONDS < end)) || die "timed out after ${timeout}s waiting for $what"
		sleep 5
	done
}
