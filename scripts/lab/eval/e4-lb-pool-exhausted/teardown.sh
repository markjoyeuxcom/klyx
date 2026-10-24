#!/usr/bin/env bash
# Removes E4's namespace and its pool, each only when it carries this
# eval's owner label (lib.sh).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
release namespace klyx-eval-e4
release -n metallb-system ipaddresspool klyx-eval-one
echo "e4: removed what was the eval's"
