#!/bin/zsh
# Coverage S2b lab fixtures on kind-klyx, over lab-s2a: a schedule with a
# proven older success (an owner reference) and a newer PartiallyFailed run
# (its N), a schedule whose only success looks synced, an expired success
# a GC-blocked location left in the API, and three Restores (completed,
# failed with the needle in its failure reason, failed validation). Status
# by merge patch; no controller runs. Every write targets kind-klyx only.
set -e
source ${0:A:h}/../../lib.sh && target
zsh ${0:A:h}/../s2a/apply.sh
k() { t 60 kube "$@"; }
N=S2aLABneedle
NS=velero
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LB=$(date -u -v-1H +%Y-%m-%dT%H:00:00Z); LBNAME=$(date -u -v-1H +%Y%m%d%H0000)
OLDSTART=$(date -u -v-25H +%Y-%m-%dT%H:00:00Z); OLDDONE=$(date -u -v-25H +%Y-%m-%dT%H:10:00Z)
SYNCSTART=$(date -u -v-30d +%Y-%m-%dT02:00:00Z); SYNCDONE=$(date -u -v-30d +%Y-%m-%dT02:20:00Z)
EXPSTART=$(date -u -v-40d +%Y-%m-%dT02:00:00Z); EXPDONE=$(date -u -v-40d +%Y-%m-%dT02:20:00Z); EXPIRED=$(date -u -v-10d +%Y-%m-%dT02:20:00Z)
FUTURE=$(date -u -v+30d +%Y-%m-%dT00:00:00Z)
REGULAR=$(k -n $NS get backups.velero.io -l velero.io/schedule-name=regular -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | grep -v 2026010100 | head -1)

k -n $NS delete backups.velero.io -l velero.io/schedule-name=partial-weekly --ignore-not-found >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: velero.io/v1
kind: Schedule
metadata: {name: partial-weekly, namespace: $NS}
spec: {schedule: "@every 24h", template: {storageLocation: default}}
---
apiVersion: velero.io/v1
kind: Schedule
metadata: {name: synced-only, namespace: $NS}
spec: {schedule: "@every 24h", template: {storageLocation: default}}
---
apiVersion: velero.io/v1
kind: Backup
metadata: {name: partial-weekly-$LBNAME, namespace: $NS, labels: {velero.io/schedule-name: partial-weekly}}
spec: {storageLocation: default}
---
apiVersion: velero.io/v1
kind: Backup
metadata: {name: synced-only-20260101020000, namespace: $NS, labels: {velero.io/schedule-name: synced-only}}
spec: {storageLocation: default}
---
apiVersion: velero.io/v1
kind: Backup
metadata: {name: manual-expired, namespace: $NS, labels: {velero.io/gc-failure: BSLReadOnly}}
spec: {storageLocation: minio}
---
apiVersion: velero.io/v1
kind: Restore
metadata: {name: restore-ok, namespace: $NS}
spec:
  backupName: $REGULAR
  includedNamespaces: [team-a]
  namespaceMapping: {team-a: team-a-restored}
  restorePVs: true
---
apiVersion: velero.io/v1
kind: Restore
metadata: {name: restore-invalid, namespace: $NS}
spec:
  scheduleName: nightly-failed
  labelSelector: {matchLabels: {app: "$N"}}
YAML
UID_PW=$(k -n $NS get schedules.velero.io partial-weekly -o jsonpath='{.metadata.uid}')
k apply -f - >/dev/null <<YAML
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: partial-weekly-older
  namespace: $NS
  labels: {velero.io/schedule-name: partial-weekly}
  ownerReferences: [{apiVersion: velero.io/v1, kind: Schedule, name: partial-weekly, uid: "$UID_PW", controller: true}]
spec: {storageLocation: default}
YAML

vs() { k -n $NS patch "$1" "$2" --type=merge -p "{\"status\":$3}" >/dev/null; }
vs schedules.velero.io partial-weekly "{\"phase\":\"Enabled\",\"lastBackup\":\"$LB\"}"
vs schedules.velero.io synced-only "{\"phase\":\"Enabled\"}"
vs backups.velero.io partial-weekly-older "{\"phase\":\"Completed\",\"startTimestamp\":\"$OLDSTART\",\"completionTimestamp\":\"$OLDDONE\",\"expiration\":\"$FUTURE\"}"
vs backups.velero.io partial-weekly-$LBNAME "{\"phase\":\"PartiallyFailed\",\"startTimestamp\":\"$LB\",\"completionTimestamp\":\"$NOW\",\"errors\":3,\"expiration\":\"$FUTURE\"}"
vs backups.velero.io synced-only-20260101020000 "{\"phase\":\"Completed\",\"startTimestamp\":\"$SYNCSTART\",\"completionTimestamp\":\"$SYNCDONE\",\"expiration\":\"$FUTURE\"}"
vs backups.velero.io manual-expired "{\"phase\":\"Completed\",\"startTimestamp\":\"$EXPSTART\",\"completionTimestamp\":\"$EXPDONE\",\"expiration\":\"$EXPIRED\"}"
vs restores.velero.io restore-ok "{\"phase\":\"Completed\",\"startTimestamp\":\"$NOW\",\"completionTimestamp\":\"$NOW\",\"progress\":{\"totalItems\":12,\"itemsRestored\":12}}"
vs restores.velero.io restore-invalid "{\"phase\":\"FailedValidation\",\"validationErrors\":[\"No completed backups found for schedule\",\"$N https://u:$N@x.example/\"]}"
echo "lab-s2b applied at $NOW (partial-weekly N partial-weekly-$LBNAME, restore-ok from $REGULAR)"
