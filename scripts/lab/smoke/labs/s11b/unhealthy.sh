#!/bin/zsh
# Marks lab-template's allocated device unhealthy through the example
# driver's health annotation (health.example.com/<device> on its kubelet
# plugin pod); "healthy" restores it. The kubelet then reports the device
# in the pod's allocatedResourcesStatus.
source ${0:A:h}/common.sh && target
state=${1:-unhealthy}
claim=$(k -n $NS get pod lab-template -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
dev=$(k -n $NS get resourceclaim $claim -o jsonpath='{.status.allocation.devices.results[0].device}')
plugin=$(k -n $DRIVER_NS get pods -o name | grep -i kubeletplugin | head -1)
[ -n "$dev" ] && [ -n "$plugin" ] || { echo "no device ($dev) or plugin ($plugin)"; exit 1; }
k -n $DRIVER_NS annotate $plugin "health.example.com/$dev=$state" --overwrite >/dev/null
echo "device $dev ($claim) marked $state on $plugin"
