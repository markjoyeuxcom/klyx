# lib.sh - shared by the native smoke's labs and its walk (sourced, zsh).
#
# Every write goes to exactly one target: the kind cluster klyx (or
# $KLYX_SMOKE_KIND), through a kubeconfig of its own that kind writes into
# the state directory - never ~/.kube/config, so no current context is
# read or changed - and only once that kubeconfig's API server is kind's
# own record of the cluster, on loopback. Upstream manifests are fetched
# once into the state directory and used only when they match their
# pinned digest. Nothing a lab generates (keys, fixtures' outputs, object
# ids) is written into the repository.

SMOKE_ROOT=${SMOKE_ROOT:-${${(%):-%x}:A:h}}
name=${KLYX_SMOKE_KIND:-klyx}
ctx=kind-$name
dir=${KLYX_SMOKE_DIR:-${TMPDIR:-/tmp}/klyx-smoke}
dirmark=$dir/.klyx-smoke-dir
kubeconfig=$dir/kubeconfig

die() { print -r -- "klyx-smoke: $*" >&2; exit 1; }

# t <seconds> <command...>: the command in its own process group, killed
# after the deadline (Go binaries ignore SIGALRM); 124 on a timeout. kube
# and kubehelm are spelled out, since the timeout execs a program
t() {
	local secs=$1; shift
	case $1 in
	kube) shift; set -- kubectl --kubeconfig $kubeconfig --context $ctx "$@" ;;
	kubehelm) shift; set -- helm --kubeconfig $kubeconfig --kube-context $ctx "$@" ;;
	esac
	perl $SMOKE_ROOT/timeout.pl $secs "$@"
}

# prepare: the state directory, its marker, and kind's kubeconfig for the
# cluster, checked as the exact target before any lab writes
prepare() {
	if [[ -d $dir && ! -f $dirmark && -n $(ls -A $dir 2>/dev/null) ]]; then
		die "refusing: $dir is not empty and is not the smoke's state directory (no $dirmark)"
	fi
	mkdir -p $dir && touch $dirmark
	kind get kubeconfig --name $name > $kubeconfig.part 2>/dev/null || { rm -f $kubeconfig.part; die "no kind cluster named $name"; }
	mv $kubeconfig.part $kubeconfig && chmod 600 $kubeconfig
	target
}

# target: the kubeconfig's context is kind's cluster, at kind's own
# loopback API server
target() {
	[[ -f $dirmark && -f $kubeconfig ]] || die "run labs.sh first: no state directory at $dir"
	local have want
	have=$(kubectl --kubeconfig $kubeconfig config view --minify --context $ctx -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null) || die "context $ctx not in $kubeconfig"
	want=$(kind get kubeconfig --name $name 2>/dev/null | awk '/server:/ {print $2; exit}')
	[[ -n $want && $have == "$want" ]] || die "context $ctx points at '$have', kind's $name is at '$want'"
	[[ $have == https://127.0.0.1:* || $have == https://localhost:* ]] || die "context $ctx is not on loopback: $have"
}

# kube and kubehelm: kubectl and helm on the target alone; kubesh is
# kube spelled out for an sh -c string
kube() { kubectl --kubeconfig $kubeconfig --context $ctx "$@"; }
kubehelm() { helm --kubeconfig $kubeconfig --kube-context $ctx "$@"; }
kubesh="kubectl --kubeconfig '$kubeconfig' --context '$ctx'"

# fetch <url> <sha256> <file>: a pinned upstream file in the state
# directory, fetched once, used only when it matches its digest; prints
# its path
fetch() {
	local out=$dir/dl/$3
	if [[ ! -f $out ]]; then
		mkdir -p $dir/dl
		curl -fsSL --max-time 120 -o $out.part "$1" || { rm -f $out.part; die "could not fetch $1"; }
		mv $out.part $out
	fi
	[[ $(shasum -a 256 $out | cut -d' ' -f1) == "$2" ]] || { rm -f $out; die "$1 does not match its pinned digest"; }
	print -r -- $out
}

# chart <oci-ref> <version> <sha256>: a Helm chart pulled once into the
# state directory, used only when its archive matches its pinned digest;
# prints its path
chart() {
	local out=$dir/dl/${1:t}-$2.tgz
	if [[ ! -f $out ]]; then
		mkdir -p $dir/dl
		t 180 helm pull $1 --version $2 -d $dir/dl >/dev/null || die "could not pull $1 $2"
	fi
	[[ $(shasum -a 256 $out | cut -d' ' -f1) == "$3" ]] || { rm -f $out; die "$1 $2 does not match its pinned digest"; }
	print -r -- $out
}

# gomod <module@version>: a Go module's directory, downloaded through the
# Go proxy and its checksum database; prints its path
gomod() {
	(cd $SMOKE_ROOT && go mod download -json "$1") | python3 -I -c 'import json, sys; print(json.load(sys.stdin)["Dir"])'
}
