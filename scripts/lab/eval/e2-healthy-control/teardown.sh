#!/usr/bin/env bash
# Removes E2's namespace, and with it everything the scenario created.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
k delete namespace klyx-eval --ignore-not-found --wait=true --timeout=180s
echo "e2: removed"
