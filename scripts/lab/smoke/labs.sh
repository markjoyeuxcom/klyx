#!/bin/zsh
# labs.sh - the native smoke's labs on the kind cluster klyx (README.md):
#
#   scripts/lab/smoke/labs.sh apply [lab...]      the state directory and its kubeconfig, then each lab (default all, in order)
#   scripts/lab/smoke/labs.sh teardown [lab...]   the labs' own objects, for each lab that has a teardown (default all)
#   scripts/lab/smoke/labs.sh check               the exact-target check alone
#
# Every write goes to kind-klyx alone, through the state directory's own
# kubeconfig, checked as the exact target first (lib.sh). Each lab's output
# is in $dir/logs/<lab>-<script>.log. The labs overlay the base kind-klyx
# the earlier smokes built; they do not create it.
source ${0:A:h}/lib.sh

# the order the smoke applies them in; a lab's scripts run in turn
labs=(s2a s1b s1c s2b s3a s4 s5 s6 s7 s8a s8b s9 s10 s11a s11b s3b ss3b certs)
typeset -A scripts=(
	s7 "apply apply-s7b"
	s11b "apply unhealthy"
	certs refresh
)

run() { # <lab> <script>: one lab script, its output in the log
	local log=$dir/logs/$1-$2.log
	if zsh $SMOKE_ROOT/labs/$1/$2.sh > $log 2>&1; then
		print -r -- "ok   $1/$2"
	else
		print -r -- "FAIL $1/$2 (see $log)"
		return 1
	fi
}

cmd=${1:-}
shift 2>/dev/null
want=(${@:-$labs})
for l in $want; do (( ${labs[(Ie)$l]} )) || die "no lab named $l (labs: $labs)"; done

case $cmd in
apply)
	prepare
	mkdir -p $dir/logs
	failed=0
	for l in $want; do for s in ${=${scripts[$l]:-apply}}; do run $l $s || failed=1; done; done
	exit $failed
	;;
teardown)
	target
	mkdir -p $dir/logs
	failed=0
	for l in $want; do [[ -f $SMOKE_ROOT/labs/$l/teardown.sh ]] && { run $l teardown || failed=1; }; done
	exit $failed
	;;
check)
	target && print -r -- "target: $ctx at $(kube config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
	;;
*)
	die "usage: labs.sh apply|teardown [lab...] | check"
	;;
esac
