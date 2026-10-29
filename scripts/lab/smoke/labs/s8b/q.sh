#!/bin/zsh
# q.sh <path-with-query> [extra curl args...]: GET from the lab Loki (tenant
# "lab") inside the cluster, through the pusher pod.
source ${0:A:h}/../../lib.sh && target
p=$1; shift
t 60 kube -n klyx-s8b-lab exec pusher -- curl -s -G -H 'X-Scope-OrgID: lab' "$@" "http://loki:3100$p"
