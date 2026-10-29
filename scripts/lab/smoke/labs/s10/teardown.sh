#!/bin/zsh
# Removes the S10 lab from kind-klyx: the lab's pools and classes (the held
# finalizer released first), the three class CRDs, and the nodeClassRef the
# lab gave the pre-S10 stub pool general-purpose-spot.
source ${0:A:h}/../../lib.sh && target
k() { t 180 kube "$@"; }
k delete nodepools.karpenter.sh -l klyx.dev/lab=s10 --ignore-not-found
k patch ec2nodeclasses.karpenter.k8s.aws lab-terminating --type=json -p '[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
k delete ec2nodeclasses.karpenter.k8s.aws,aksnodeclasses.karpenter.azure.com,nodeclasses.eks.amazonaws.com -l klyx.dev/lab=s10 --ignore-not-found
k patch nodepools.karpenter.sh general-purpose-spot --type=json -p '[{"op":"remove","path":"/spec/template/spec/nodeClassRef"}]' 2>/dev/null || true
k delete crd ec2nodeclasses.karpenter.k8s.aws aksnodeclasses.karpenter.azure.com nodeclasses.eks.amazonaws.com --ignore-not-found
