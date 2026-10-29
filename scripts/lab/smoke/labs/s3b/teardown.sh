#!/bin/zsh
# Removes everything lab-s3b made: the admission objects first (so nothing
# refuses the namespace's deletion), then the namespace.
source ${0:A:h}/common.sh && target
k delete validatingadmissionpolicybinding klyx-s3b-freeze-binding --ignore-not-found >/dev/null
k delete validatingadmissionpolicy klyx-s3b-freeze --ignore-not-found >/dev/null
k delete validatingwebhookconfiguration klyx-s3b-deny --ignore-not-found >/dev/null
k delete namespace $NS --ignore-not-found --wait=false >/dev/null
echo "lab-s3b torn down"
