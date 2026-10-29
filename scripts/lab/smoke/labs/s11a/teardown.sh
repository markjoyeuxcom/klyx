#!/bin/zsh
# Removes the S11a lab: the namespace (plugin and pods), the status fixtures
# and the labels. The sample plugin's example.com/resource key stays on the
# node at 0: the kubelet keeps a device plugin's resource it once registered
# (S11a §1.1), and the next kubelet restart drops it.
source ${0:A:h}/common.sh && target
k delete namespace $NS --ignore-not-found --wait=true >/dev/null
k patch node $NODE --subresource=status --type=json -p '[{"op":"remove","path":"/status/capacity/nvidia.com~1gpu"},{"op":"remove","path":"/status/capacity/nvidia.com~1mig-1g.5gb"}]' >/dev/null 2>&1 || true
k label node $NODE klyx.dev/lab-s11a- nvidia.com/mig.strategy- nvidia.com/gpu.product- nvidia.com/gpu.count- nvidia.com/gpu.replicas- nvidia.com/gpu.sharing-strategy- nvidia.com/gpu.memory- \
  nvidia.com/mig-1g.5gb.product- nvidia.com/mig-1g.5gb.memory- nvidia.com/mig-1g.5gb.count- nvidia.com/mig-1g.5gb.replicas- nvidia.com/mig-1g.5gb.sharing-strategy- >/dev/null 2>&1 || true
echo "lab-s11a removed"
