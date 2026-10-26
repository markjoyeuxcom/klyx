#!/usr/bin/env bash
# klyx-eval.sh - the AI eval's lab (docs/superpowers/specs/2026-10-22-klyx-ai-eval.md)
# on the kind cluster $KLYX_EVAL_KIND (default klyx):
#
#   scripts/lab/klyx-eval.sh init       make the eval directory and mark the cluster as the eval's
#   scripts/lab/klyx-eval.sh install    the controllers the scenarios need, each pinned by digest: Flux's
#                                       source and kustomize controllers (E1), KEDA 2.21.0 (E3), MetalLB 0.16.1 (E4)
#   scripts/lab/klyx-eval.sh setup [s]  plant the scenarios (default all)
#   scripts/lab/klyx-eval.sh teardown [s]   remove the scenarios' own objects (never the install's)
#   scripts/lab/klyx-eval.sh run [s]    run the eval build on the configured provider: KLYX_EVAL_BIN, KLYX_EVAL_RUNS
#   scripts/lab/klyx-eval.sh selftest   every status and mark on the scripted model (fake-model.mjs), no key
#   scripts/lab/klyx-eval.sh check      the exact-target check alone
#   scripts/lab/klyx-eval.sh a3 [case]  AI A3 §7's lab on E1 with the scripted model: plan, undo-only,
#                                       resume-between (default all); KLYX_EVAL_BIN. Klyx's own steps
#                                       write to the lab; each case restores it
#   scripts/lab/klyx-eval.sh a3-write resume|restore   the A3 lab's own writes: resume a1-web and request
#                                       a reconcile; or that, then E1's plant again (its setup)
#
# The eval directory ($KLYX_EVAL_DIR) holds the marker, the eval's own
# kubeconfig (never ~/.kube/config), the ownership token and the eval's
# fleet.yaml; reports go under its out/. Every write first passes owned()
# (eval/lib.sh). A run with a real provider is the owner's to start: it
# reads the provider's key as the app does and writes none.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
lab=$here/eval
source "$lab/lib.sh"

cmd=${1:-}
shift || true
all=(e1-flux-crashloop e2-healthy-control e3-keda-no-target e4-lb-pool-exhausted)
flux_sha256=bedc8c249ed50ff008544c96e9c904084ed472216aeeed57f3bc6528cc003ecd
keda_url=https://github.com/kedacore/keda/releases/download/v2.21.0/keda-2.21.0.yaml
keda_sha256=b43c89ffeef81722d7e2dd2c079d74789767a0f89cae1336cff784994814f6d7
metallb_url=https://raw.githubusercontent.com/metallb/metallb/v0.16.1/config/manifests/metallb-native.yaml
metallb_sha256=bf25feebb7582ca7df845efd52ffbc2960d6cbf4cfc972f47fded9f788b67f0b

# fetch <url> <sha256> <name>: a release manifest, into the eval directory,
# used only when it matches its pinned digest
fetch() {
	local f=$dir/dl/$3
	mkdir -p "$dir/dl"
	[[ -f $f ]] || { curl -fsSL "$1" -o "$f.part" && mv "$f.part" "$f"; } || die "could not fetch $1"
	[[ $(shasum -a 256 "$f" | awk '{print $1}') == "$2" ]] || {
		rm -f "$f"
		die "$3 does not match its pinned digest"
	}
	echo "$f"
}
repo=https://github.com/markjoyeuxcom/klyx-lab-a1
model_port=${KLYX_EVAL_MODEL_PORT:-18791}

scenarios() { if (($#)); then printf '%s\n' "$@"; else printf '%s\n' "${all[@]}"; fi; }

# config writes an eval fleet.yaml: the lab cluster through the eval's
# kubeconfig, the scenarios' fixture repository, and the provider block
# given (the scripted model by default).
config() {
	local file=$1 model=$2 git=${3:-yes}
	{
		echo "clusters:"
		echo "  - name: lab"
		echo "    context: $ctx"
		echo "    kubeconfig: $kubeconfig"
		echo "    environment: dev"
		if [[ $git == yes ]]; then
			echo "git:"
			echo "  repos:"
			echo "    - url: $repo"
		fi
		echo "ai:"
		echo "  provider: openaiCompatible"
		echo "  baseUrl: http://127.0.0.1:$model_port"
		echo "  model: $model"
		echo "notifications:"
		echo "  enabled: false"
	} >"$file"
	chmod 600 "$file"
}

case $cmd in
init)
	kind get clusters 2>/dev/null | grep -qx "$name" || die "kind has no cluster $name"
	if [[ -d $dir && -n $(ls -A "$dir" 2>/dev/null) && ! -f $dirmark ]]; then
		die "refusing: $dir is not empty and not an eval directory"
	fi
	mkdir -p "$dir" && chmod 700 "$dir"
	kind get kubeconfig --name "$name" >"$kubeconfig.part" && chmod 600 "$kubeconfig.part" && mv "$kubeconfig.part" "$kubeconfig"
	existing=$(k -n kube-system get configmap klyx-eval-owner -o jsonpath='{.data.token}' 2>/dev/null || true)
	if [[ -n $existing ]]; then
		[[ -s $state && $existing == "$(cat "$state")" ]] || die "refusing: $ctx already carries another eval directory's marker"
	else
		umask 077 && openssl rand -hex 16 >"$state"
		k -n kube-system create configmap klyx-eval-owner --from-literal=token="$(cat "$state")" >/dev/null
		k -n kube-system label configmap klyx-eval-owner klyx.dev/lab=eval >/dev/null
	fi
	touch "$dirmark"
	[[ -f $dir/fleet.yaml ]] || config "$dir/fleet.yaml" eval-grounded
	owned
	echo "klyx-eval: $dir is the eval directory for $ctx"
	;;
install)
	owned
	for crd in kustomizations.kustomize.toolkit.fluxcd.io gitrepositories.source.toolkit.fluxcd.io; do
		k get crd "$crd" >/dev/null 2>&1 || die "Flux's CRD $crd is not installed on $ctx; install Flux's CRDs first"
	done
	got=$(shasum -a 256 "$lab/flux-controllers.yaml" | awk '{print $1}')
	[[ $got == "$flux_sha256" ]] || die "flux-controllers.yaml does not match its pinned digest"
	k apply -f "$lab/flux-controllers.yaml" >/dev/null
	k -n klyx-a1-flux rollout status deploy/source-controller --timeout=180s
	k -n klyx-a1-flux rollout status deploy/kustomize-controller --timeout=180s
	keda=$(fetch "$keda_url" "$keda_sha256" keda-2.21.0.yaml)
	k apply --server-side --force-conflicts -f "$keda" >/dev/null
	for d in keda-operator keda-admission keda-metrics-apiserver; do k -n keda rollout status deploy/$d --timeout=180s; done
	metallb=$(fetch "$metallb_url" "$metallb_sha256" metallb-native-0.16.1.yaml)
	k apply -f "$metallb" >/dev/null
	k -n metallb-system rollout status deployment/controller --timeout=180s
	k -n metallb-system rollout status daemonset/speaker --timeout=180s
	;;
setup | teardown)
	owned
	for s in $(scenarios "$@"); do
		[[ -x $lab/$s/$cmd.sh ]] || die "no scenario $s"
		"$lab/$s/$cmd.sh"
	done
	;;
run)
	owned
	bin=${KLYX_EVAL_BIN:?the eval build: go build -tags eval -o <bin> ./cmd/klyx}
	out=$dir/out/$(date +%Y%m%dT%H%M%S)
	list=$(scenarios "$@" | paste -sd, -)
	KLYX_CONFIG=$dir/fleet.yaml KLYX_EVAL_LAB=$lab KLYX_EVAL_OUT=$out KLYX_EVAL_SCENARIOS=$list KLYX_EVAL_KIND=$name "$bin"
	echo "klyx-eval: report in $out"
	;;
selftest)
	owned
	bin=${KLYX_EVAL_BIN:?the eval build: go build -tags eval -o <bin> ./cmd/klyx}
	for s in e2-healthy-control e3-keda-no-target e4-lb-pool-exhausted; do "$lab/$s/setup.sh"; done
	k -n klyx-eval create configmap eval-witness --from-literal=value=one --dry-run=client -o yaml |
		k label --local -f - "$owner_key=$(owner_id)" -o yaml | k apply -f - >/dev/null
	log=$dir/selftest-model.log
	node "$lab/fake-model.mjs" "$model_port" >"$log" 2>&1 &
	model=$!
	trap 'kill $model 2>/dev/null || true' EXIT
	sleep 1
	root=$dir/out/selftest-$(date +%Y%m%dT%H%M%S)
	mkdir -p "$root"
	one() { # one <case> <model> <lab root> <scenarios> [git]
		local c=$1 m=$2 r=$3 s=$4 g=${5:-yes}
		config "$dir/fleet-$c.yaml" "$m" "$g"
		KLYX_CONFIG=$dir/fleet-$c.yaml KLYX_EVAL_LAB=$r KLYX_EVAL_OUT=$root/$c KLYX_EVAL_SCENARIOS=$s KLYX_EVAL_RUNS=1 KLYX_EVAL_KIND=$name \
			"$bin" >"$root/$c.log" 2>&1 || echo "klyx-eval: $c exited $?" >&2
	}
	one grounded eval-grounded "$lab" e1-flux-crashloop,e2-healthy-control
	one negated eval-negated "$lab" e1-flux-crashloop
	one at-once eval-at-once "$lab" e1-flux-crashloop
	one current-log eval-current-log "$lab" e1-flux-crashloop
	one no-report eval-no-report "$lab" e1-flux-crashloop
	one empty-then-fail eval-empty-then-fail "$lab" e2-healthy-control no
	one unsupported eval-unsupported "$lab" e2-healthy-control no
	one no-cause-proven eval-no-cause-proven "$lab" e2-healthy-control no
	one snapshot-block eval-snapshot-block "$lab" e1-flux-crashloop
	one snapshot-none eval-snapshot-none "$lab" e1-flux-crashloop
	one snapshot-malformed eval-snapshot-malformed "$lab" e1-flux-crashloop
	one grounded-e3 eval-grounded "$lab" e3-keda-no-target no
	one grounded-e4 eval-grounded "$lab" e4-lb-pool-exhausted no
	one lookalike-e3 eval-lookalike "$lab" e3-keda-no-target no
	one lookalike-e4 eval-lookalike "$lab" e4-lb-pool-exhausted no
	one broken-plant eval-grounded "$lab/selftest" s1-broken-plant no
	one no-observer-part eval-grounded "$lab/selftest" s2-no-observer-part no
	# drift: the witness changes while the scripted model waits
	k -n klyx-eval patch configmap eval-witness --type merge -p '{"data":{"value":"one"}}' >/dev/null
	one drift eval-drift "$lab/selftest" s3-drift no &
	drift=$!
	wait_for 240 "the scripted model to wait" grep -q 'drift: waiting' "$log"
	k -n klyx-eval patch configmap eval-witness --type merge -p '{"data":{"value":"two"}}' >/dev/null
	wait "$drift" || true
	python3 -I "$lab/selftest.py" "$root"
	;;
check)
	owned
	echo "klyx-eval: $ctx is the eval's ($dir)"
	;;
a3)
	owned
	bin=${KLYX_EVAL_BIN:?the eval build: go build -tags eval -o <bin> ./cmd/klyx}
	"$lab/e1-flux-crashloop/setup.sh"
	log=$dir/a3-model.log
	node "$lab/fake-model.mjs" "$model_port" >"$log" 2>&1 &
	model=$!
	trap 'kill $model 2>/dev/null || true' EXIT
	sleep 1
	root=$dir/out/a3-$(date +%Y%m%dT%H%M%S)
	mkdir -p "$root"
	failed=0
	for c in ${@:-plan undo-only resume-between}; do
		m=a3-plan
		[[ $c == undo-only ]] && m=a3-undo-only
		config "$dir/fleet-a3-$c.yaml" "$m"
		KLYX_CONFIG=$dir/fleet-a3-$c.yaml KLYX_EVAL_DIR=$dir KLYX_EVAL_LAB=$lab KLYX_EVAL_OUT=$root KLYX_EVAL_A3=$c KLYX_EVAL_KIND=$name \
			"$bin" >"$root/$c.log" 2>&1 || {
			echo "klyx-eval: A3 $c exited $?" >&2
			failed=1
		}
		[[ -f $root/a3-$c.md ]] && cat "$root/a3-$c.md"
	done
	echo "klyx-eval: A3 records in $root"
	exit $failed
	;;
a3-write)
	owned
	case ${1:-} in
	resume | restore)
		# only a1-web, and only while it is this eval's (lib.sh claim)
		owner=$(label_of "$owner_key" -n klyx-a1-flux kustomization a1-web) || exit 1
		[[ $owner == "$(owner_id)" ]] || die "refusing: a1-web is not this eval's"
		token=$(date +%s)
		k -n klyx-a1-flux patch kustomization a1-web --type merge -p '{"spec":{"suspend":false}}' >/dev/null
		k -n klyx-a1-flux annotate --overwrite kustomization a1-web reconcile.fluxcd.io/requestedAt="$token" >/dev/null
		echo "klyx-eval: a1-web resumed"
		# a plan's roll back and the resume leave Flux's history moved on:
		# E1's setup plants it again, steady then crash. It reads a1-web's
		# status, so the resume's reconcile comes first: until Flux handles
		# it the status is the plan's, and a reconcile right after a roll
		# back can record a success at the crash commit (the A3 lab)
		if [[ $1 == restore ]]; then
			handled() { [[ $(k -n klyx-a1-flux get kustomization a1-web -o jsonpath='{.status.lastHandledReconcileAt}') == "$token" ]]; }
			wait_for 300 "a1-web to handle the restore's reconcile" handled
			"$lab/e1-flux-crashloop/setup.sh"
		fi
		;;
	*) die "a3-write: no write ${1:-}" ;;
	esac
	;;
*)
	sed -n '2,20p' "$0"
	exit 2
	;;
esac
