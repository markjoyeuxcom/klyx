#!/bin/zsh
# Keeps the base lab's cert-manager fixtures' dates relative to now (their
# status is written, no cert-manager runs): web-tls inside its renewal
# window, not expired; api-internal and team-a-internal comfortably valid.
# The smoke's issuance-chain step depends on web-tls not sorting ahead of
# the failing grafana certificate as expired. kind-klyx only.
set -euo pipefail
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
at() { date -u -v"$1" +%Y-%m-%dT%H:%M:%SZ; }
dates() { # namespace name notBefore notAfter renewalTime
	k -n $1 patch certificate $2 --subresource=status --type=merge \
		-p "{\"status\":{\"notBefore\":\"$3\",\"notAfter\":\"$4\",\"renewalTime\":\"$5\"}}" >/dev/null
}
dates shop web-tls "$(at -70d)" "$(at +12d)" "$(at -2d)"
dates shop api-internal "$(at -70d)" "$(at +56d)" "$(at +26d)"
dates team-a team-a-internal "$(at -70d)" "$(at +56d)" "$(at +26d)"
echo "lab-certs refreshed"
