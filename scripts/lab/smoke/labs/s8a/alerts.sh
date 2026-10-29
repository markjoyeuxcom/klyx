#!/bin/zsh
# Posts the S8a lab's synthetic alerts (ending two hours out) and the silence
# with amtool inside each lab Alertmanager (lab setup through Alertmanager's
# own API - Klyx never posts). Re-posting the same labels keeps the alert's
# fingerprint and its first startsAt.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 90 kube "$@"; }
NS=klyx-s8-lab
POD=$(k -n $NS get pods -l app=alerted-web -o jsonpath='{.items[0].metadata.name}')
NODE=$(k get nodes -o jsonpath='{.items[0].metadata.name}')
ENDS=$(date -u -v+2H +%Y-%m-%dT%H:%M:%SZ)
STARTS=$(date -u -v-10M +%Y-%m-%dT%H:%M:%SZ)
script() { # $1 = the Alertmanager URL inside its pod
  local U=$1 C="cluster=kind-klyx"
  local T="--start=$STARTS --end=$ENDS --generator-url=http://prometheus.example/graph"
  cat <<SH
set -e
a() { amtool --alertmanager.url=$U alert add "\$@" $T >/dev/null; }
a alertname=KlyxLabPodCrashLooping severity=critical $C namespace=$NS pod=$POD container=web --annotation="summary=Pod $POD is crash looping (lab)"
a alertname=KlyxLabDeploymentReplicasMismatch severity=warning $C namespace=$NS deployment=alerted-web --annotation="summary=Deployment alerted-web has not matched its replicas (lab)"
a alertname=KlyxLabNodeMemoryPressure severity=warning $C node=$NODE --annotation="summary=Node $NODE is under memory pressure (lab)"
a alertname=KlyxLabNamespaceQuota severity=info $C namespace=$NS --annotation="summary=Namespace quota at 80% (lab, info)"
a alertname=KlyxLabClusterWide severity=critical $C --annotation="summary=A cluster-wide alert with no subject (lab)"
a alertname=Watchdog severity=none $C --annotation="summary=The always-firing watchdog (lab)"
a alertname=KlyxLabCritical severity=critical $C namespace=$NS --annotation="summary=The inhibiting alert (lab)"
a alertname=KlyxLabWarning severity=warning $C namespace=$NS --annotation="summary=Inhibited by KlyxLabCritical (lab)"
a alertname=KlyxLabMuted severity=warning $C namespace=$NS klyx_lab_mute=true --annotation="summary=Muted by a time interval (lab)"
a alertname=KlyxLabPartlyMuted severity=warning $C namespace=$NS klyx_lab_partmute=true --annotation="summary=Muted for one receiver, active for another (lab)"
a alertname=KlyxLabSilenced severity=critical $C namespace=$NS --annotation="summary=Silenced (lab)"
a alertname=KlyxLabGonePod severity=warning $C namespace=$NS pod=ghost-0 --annotation="summary=Names a pod that does not exist (lab)"
a alertname=KlyxLabElsewhere severity=critical cluster=elsewhere namespace=$NS --annotation="summary=Another cluster's alert (lab)"
# a fresh 24h silence on every apply: one checked "still active" can expire
# mid-smoke (it did: a 3h silence ran out at 17:16 during a 17:12 run)
for id in \$(amtool --alertmanager.url=$U silence query -q alertname=KlyxLabSilenced); do
  amtool --alertmanager.url=$U silence expire \$id >/dev/null
done
amtool --alertmanager.url=$U silence add alertname=KlyxLabSilenced --author=klyx-lab --comment="S8a lab silence" --duration=24h >/dev/null
SH
}
k -n $NS exec deploy/alertmanager -c alertmanager -- sh -c "$(script http://localhost:9093)"
k -n $NS exec deploy/alertmanager-prefixed -c alertmanager -- sh -c "$(script http://localhost:9093/alertmanager)"
echo "posted 13 alerts and a silence to each lab Alertmanager"
