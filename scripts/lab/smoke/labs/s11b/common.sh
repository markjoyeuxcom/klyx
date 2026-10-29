# Shared helpers for the S11b lab on kind-klyx (sourced).
source ${${(%):-%x}:A:h}/../../lib.sh
NODE=klyx-control-plane
DRIVER_NS=klyx-s11b-dra
NS=klyx-s11b-lab
k() { t 120 kube "$@"; }
h() { t 300 kubehelm "$@"; }
# until <what> <command...> - retry a command until it succeeds (at most ~180 s)
until_ok() {
  local what=$1; shift
  local i=0
  until "$@" >/dev/null 2>&1; do
    i=$((i+1)); if [ $i -gt 90 ]; then echo "TIMEOUT waiting for $what"; return 1; fi
    perl -e 'select(undef,undef,undef,2)'
  done
  echo "ok $what"
}
