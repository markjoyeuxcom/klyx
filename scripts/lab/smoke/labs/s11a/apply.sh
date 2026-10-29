#!/bin/zsh
# Coverage S11a lab on kind-klyx (one node, klyx-control-plane). Labelled
# klyx.dev/lab=s11a, namespaced in klyx-s11a-lab; nothing exposed.
#  - a REAL device plugin: the Kubernetes e2e sample device plugin (1.8.0,
#    multi-arch) as a DaemonSet, registering example.com/resource (2
#    devices) through the kubelet's device manager; a pod requests 1.
#  - vocabulary FIXTURES, stated as such: nvidia.com/gpu 8 and
#    nvidia.com/mig-1g.5gb 7 advertised through the node status subresource
#    (the documented method; the kubelet keeps them and mirrors
#    allocatable), with GPU Feature Discovery labels per resource (MIG mixed;
#    nvidia.com/gpu time-sliced with 4 replicas; the MIG profile unshared).
#    Pods request 3 nvidia.com/gpu and 1 nvidia.com/mig-1g.5gb.
# Re-run after any node or kubelet restart: the kubelet may zero manual
# extended resources on re-registering (S11a §1.1).
set -e
source ${0:A:h}/common.sh && target
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Namespace
metadata: {name: $NS, labels: {klyx.dev/lab: s11a}}
---
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: sample-device-plugin, namespace: $NS, labels: {klyx.dev/lab: s11a}}
spec:
  selector: {matchLabels: {app: sample-device-plugin}}
  template:
    metadata: {labels: {app: sample-device-plugin, klyx.dev/lab: s11a}}
    spec:
      tolerations: [{operator: Exists}]
      volumes:
      - {name: device-plugin, hostPath: {path: /var/lib/kubelet/device-plugins}}
      - {name: plugins-registry, hostPath: {path: /var/lib/kubelet/plugins_registry}}
      - {name: dev, hostPath: {path: /dev}}
      - {name: cdi-dir, hostPath: {path: /var/run/cdi}}
      containers:
      - name: sample-device-plugin
        image: registry.k8s.io/e2e-test-images/sample-device-plugin:1.8.0
        env: [{name: PLUGIN_SOCK_DIR, value: /var/lib/kubelet/device-plugins}]
        securityContext: {privileged: true}
        volumeMounts:
        - {name: device-plugin, mountPath: /var/lib/kubelet/device-plugins}
        - {name: plugins-registry, mountPath: /var/lib/kubelet/plugins_registry}
        - {name: dev, mountPath: /dev}
        - {name: cdi-dir, mountPath: /var/run/cdi}
YAML
# a previous stop-plugin.sh left a nodeSelector: clear it
k -n $NS patch daemonset sample-device-plugin --type=json -p '[{"op":"remove","path":"/spec/template/spec/nodeSelector"}]' >/dev/null 2>&1 || true
waitfor "example.com/resource allocatable" '.status.allocatable.example\.com/resource' 2
# vocabulary fixtures: capacity through the status subresource
k patch node $NODE --subresource=status --type=json -p '[{"op":"add","path":"/status/capacity/nvidia.com~1gpu","value":"8"},{"op":"add","path":"/status/capacity/nvidia.com~1mig-1g.5gb","value":"7"}]' >/dev/null
k label node $NODE --overwrite >/dev/null \
  klyx.dev/lab-s11a=true \
  nvidia.com/mig.strategy=mixed \
  nvidia.com/gpu.product=NVIDIA-A100-SXM4-80GB-SHARED nvidia.com/gpu.count=2 nvidia.com/gpu.replicas=4 nvidia.com/gpu.sharing-strategy=time-slicing nvidia.com/gpu.memory=81920 \
  nvidia.com/mig-1g.5gb.product=NVIDIA-A100-SXM4-80GB-MIG-1g.5gb nvidia.com/mig-1g.5gb.memory=4864 nvidia.com/mig-1g.5gb.count=7 nvidia.com/mig-1g.5gb.replicas=1 nvidia.com/mig-1g.5gb.sharing-strategy=none
waitfor "nvidia.com/gpu allocatable (mirrored by the kubelet)" '.status.allocatable.nvidia\.com/gpu' 8
waitfor "nvidia.com/mig-1g.5gb allocatable" '.status.allocatable.nvidia\.com/mig-1g\.5gb' 7
for p in "lab-train nvidia.com/gpu 3" "lab-mig nvidia.com/mig-1g.5gb 1" "lab-sample example.com/resource 1"; do
  set -- ${(z)p}
  k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: $1, namespace: $NS, labels: {klyx.dev/lab: s11a}}
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - name: app
    image: registry.k8s.io/pause:3.10
    resources: {requests: {$2: "$3"}, limits: {$2: "$3"}}
YAML
done
k -n $NS wait --for=condition=Ready pod -l klyx.dev/lab=s11a --timeout=120s >/dev/null
echo "lab-s11a applied"
