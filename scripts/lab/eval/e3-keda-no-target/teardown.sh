#!/usr/bin/env bash
# Removes E3's namespace, and with it everything the scenario created, only
# when it carries this eval's owner label (lib.sh).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/../lib.sh"
owned
release namespace klyx-eval-e3
echo "e3: removed what was the eval's"
