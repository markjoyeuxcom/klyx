#!/usr/bin/env bash
# Removes E1's objects: the Kustomization, its source and the workload's
# namespace. Flux's controllers are the install's, and stay.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
k -n klyx-a1-flux delete kustomization a1-web --ignore-not-found --wait=true --timeout=120s
k -n klyx-a1-flux delete gitrepository a1-fleet --ignore-not-found
k delete namespace klyx-a1-app --ignore-not-found --wait=true --timeout=180s
echo "e1: removed"
