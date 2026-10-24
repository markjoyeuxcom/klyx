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

# Scenario objects are the eval directory's by its owner label, a digest of
# its token (never the token): setup creates or claims only objects that
# carry it, and teardown deletes only those. An object that exists without
# it is refused, unless it is the A1 lab's (klyx.dev/lab=a1) and adoption
# was asked for explicitly with KLYX_EVAL_ADOPT=a1: the A1 lab's objects
# become the eval's then, and say so.
owner_key=klyx.dev/eval-owner
owner_id() { shasum -a 256 "$state" | cut -c1-16; }

# label_of <label> <get args...>: an existing object's label, or "absent"
# when the API says it does not exist. Any other failure to read it (a
# Forbidden, a timeout) establishes nothing, so it stops the script.
label_of() {
	local key=$1 out
	shift
	out=$(k get "$@" -o json --ignore-not-found 2>/dev/null) || die "could not read $*: whose it is is not established"
	if [[ -z $out ]]; then
		echo absent
		return
	fi
	python3 -I -c 'import json,sys; d=json.loads(sys.argv[2]); print(d.get("metadata",{}).get("labels",{}).get(sys.argv[1],""))' "$key" "$out"
}

# claim <get args...>: an existing object is refused unless it is ours, or
# the A1 lab's and adopted; an absent one is created by the caller
claim() {
	local owner lab
	owner=$(label_of "$owner_key" "$@") || exit 1
	[[ $owner == absent || $owner == "$(owner_id)" ]] && return 0
	lab=$(label_of klyx.dev/lab "$@") || exit 1
	if [[ ${KLYX_EVAL_ADOPT:-} == a1 && $lab == a1 ]]; then
		k label --overwrite "$@" "$owner_key=$(owner_id)" >/dev/null
		echo "klyx-eval: adopted the A1 lab's $* as this eval's"
		return 0
	fi
	die "refusing: $* exists and is not this eval's (no $owner_key=$(owner_id)); the A1 lab's objects are adopted only with KLYX_EVAL_ADOPT=a1"
}

# release <get args...>: delete an object only if it is this eval's; say
# what is left when it is not
release() {
	local owner
	owner=$(label_of "$owner_key" "$@") || exit 1
	case $owner in
	absent) ;;
	"$(owner_id)") k delete "$@" --ignore-not-found --wait=true --timeout=180s >/dev/null ;;
	*) echo "klyx-eval: left $* alone: it is not this eval's" ;;
	esac
}
