#!/bin/zsh
# Removes the S9 lab from kind-klyx: the lab namespace, the cluster-scoped
# lab object, then KEDA itself (the release manifest).
source ${0:A:h}/../../lib.sh && target
k() { t 180 kube "$@"; }
k delete namespace klyx-s9-lab --ignore-not-found
k delete clustertriggerauthentications.keda.sh klyx-s9-lab --ignore-not-found
k delete -f $(fetch https://github.com/kedacore/keda/releases/download/v2.21.0/keda-2.21.0.yaml b43c89ffeef81722d7e2dd2c079d74789767a0f89cae1336cff784994814f6d7 keda-2.21.0.yaml) --ignore-not-found
