#!/bin/zsh
# Removes the Settings S3b lab: its namespace, labelled klyx.dev/lab=ss3b.
source ${0:A:h}/../../lib.sh && target
t 120 kube delete namespace klyx-ss3b-lab --ignore-not-found
