#!/bin/zsh
# Removes the S8b lab from kind-klyx.
source ${0:A:h}/../../lib.sh && target
t 120 kube delete namespace klyx-s8b-lab --ignore-not-found
