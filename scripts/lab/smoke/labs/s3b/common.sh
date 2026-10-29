# Shared helpers for the S3b lab on kind-klyx (sourced).
source ${${(%):-%x}:A:h}/../../lib.sh
NS=klyx-s3b-lab
# the lab's own TLS pair, generated at its first apply, in the state
# directory
CERTS=$dir/s3b-certs
k() { t 120 kube "$@"; }
until_ok() {
  local what=$1; shift
  local i=0
  until "$@" >/dev/null 2>&1; do
    i=$((i+1)); if [ $i -gt 90 ]; then echo "TIMEOUT waiting for $what"; return 1; fi
    perl -e 'select(undef,undef,undef,2)'
  done
  echo "ok $what"
}
