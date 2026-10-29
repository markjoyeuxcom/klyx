#!/bin/zsh
# Stops the sample device plugin for real: its DaemonSet gets a nodeSelector
# no node carries, so the controller cannot replace the pod. The kubelet then
# marks its devices unhealthy: capacity 2, allocatable 0 for 5 minutes
# (the node reads "Resources not allocatable"), then capacity 0.
set -e
source ${0:A:h}/common.sh && target
k -n $NS patch daemonset sample-device-plugin --type=merge -p '{"spec":{"template":{"spec":{"nodeSelector":{"klyx.dev/s11a-plugin":"off"}}}}}' >/dev/null
waitfor "example.com/resource allocatable after the stop" '.status.allocatable.example\.com/resource' 0
waitfor "example.com/resource capacity (still counted)" '.status.capacity.example\.com/resource' 2
