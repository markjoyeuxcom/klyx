#!/bin/zsh
# Pushes the S8b lab's lines to Loki (tenant "lab") through the pusher pod -
# lab setup through Loki's own API; Klyx never writes to Loki.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 120 kube "$@"; }
NS=klyx-s8b-lab
source $dir/s8b-identities.env
OUT=$dir/s8b-out; mkdir -p $OUT
python3 -I ${0:A:h}/bodies.py $OUT $(date +%s) $DB1 $DB2 $WEB $WEBUID $GONE $DB2CREATED
post() { # $1 path, $2 body file
  k -n $NS exec -i pusher -- curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H 'X-Scope-OrgID: lab' --data-binary @- "http://loki:3100$1" < $2
}
for s in sd alloy; do r=$(post /loki/api/v1/push $OUT/$s.json); [ "$r" = 204 ] || { echo "push $s: $r"; exit 1; }; done
r=$(post /otlp/v1/logs $OUT/otel.json); [ "$r" = 204 ] || { echo "push otel: $r"; exit 1; }
echo "pushed sd, alloy and otel lines for history-db-0 (two generations), $WEB and history-gone"
