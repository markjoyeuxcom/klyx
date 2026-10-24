#!/usr/bin/env bash
# Removes E1's objects: the Kustomization, its source and the workload's
# namespace, each only when it carries this eval's owner label (lib.sh).
# Flux's controllers are the install's, and stay.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
release -n klyx-a1-flux kustomization a1-web
release -n klyx-a1-flux gitrepository a1-fleet
release namespace klyx-a1-app
echo "e1: removed what was the eval's"
