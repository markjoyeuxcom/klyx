#!/bin/zsh
# Restores the sample device plugin; it re-registers and the node reads 2/2.
set -e
source ${0:A:h}/common.sh && target
k -n $NS patch daemonset sample-device-plugin --type=json -p '[{"op":"remove","path":"/spec/template/spec/nodeSelector"}]' >/dev/null
waitfor "example.com/resource capacity after the restore" '.status.capacity.example\.com/resource' 2
waitfor "example.com/resource allocatable after the restore" '.status.allocatable.example\.com/resource' 2
