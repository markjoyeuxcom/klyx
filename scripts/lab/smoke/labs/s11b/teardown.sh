#!/bin/zsh
# Removes the S11b lab: the lab namespace (claims and pods), the fixture
# slices, and the example driver's release and namespace.
source ${0:A:h}/common.sh && target
k delete namespace $NS --ignore-not-found --wait=true >/dev/null
k delete resourceslices -l klyx.dev/fixture=true,klyx.dev/lab=s11b --ignore-not-found >/dev/null
h uninstall --namespace $DRIVER_NS dra-example-driver --wait >/dev/null 2>&1 || true
k delete namespace $DRIVER_NS --ignore-not-found --wait=true >/dev/null
echo "lab-s11b removed"
