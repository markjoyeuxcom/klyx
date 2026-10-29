#!/bin/zsh
# Removes the S8a lab from kind-klyx.
source ${0:A:h}/../../lib.sh && target
t 120 kube delete namespace klyx-s8-lab --ignore-not-found
