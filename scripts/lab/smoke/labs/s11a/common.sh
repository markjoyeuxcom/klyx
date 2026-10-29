# Shared helpers for the S11a lab on kind-klyx (sourced).
source ${${(%):-%x}:A:h}/../../lib.sh
NODE=klyx-control-plane
NS=klyx-s11a-lab
k() { t 120 kube "$@"; }
# field <jsonpath-in-node-status> - one figure of the node, "" when absent
field() { k get node $NODE -o "jsonpath={$1}"; }
# waitfor <what> <jsonpath> <want> - poll the node until the figure reads want (at most ~120 s)
waitfor() {
  local i=0
  while [ "$(field "$2")" != "$3" ]; do
    i=$((i+1)); if [ $i -gt 60 ]; then echo "TIMEOUT waiting for $1 = $3 (now '$(field "$2")')"; return 1; fi
    perl -e 'select(undef,undef,undef,2)'
  done
  echo "ok $1 = $3"
}
