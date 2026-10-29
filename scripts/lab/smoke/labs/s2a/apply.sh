#!/bin/zsh
# Coverage S2a lab fixtures on kind-klyx: Velero v1.18.4's CRDs (read before
# use), a stand-in server, and schedules, storage locations and backups with
# status written by a plain merge patch (Velero has no status subresource;
# no controller runs). lastBackup is status, so the missed and deferred
# schedules are immediate. Every protected value carries the needle
# S2aLABneedle. Idempotent: each apply refreshes the regular run. Every write
# targets kind-klyx only.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
N=S2aLABneedle
NS=velero
# the unscheduled backup must be younger than Velero's 24-hour "recent"
# window (S2a §4.3); apply keeps an existing object's creation time, so it
# is recreated
k -n $NS delete backups.velero.io pre-upgrade --ignore-not-found >/dev/null 2>&1 || true
# Velero v1.18.4's CRDs, each pinned by its digest
velero_crd() {
	case $1 in
	backups) fetch https://raw.githubusercontent.com/vmware-tanzu/velero/v1.18.4/config/crd/v1/bases/velero.io_backups.yaml 38107fa5fad6bff25794489d12c8a898eed4d9d89ed49d00ea90369bc4e206b1 velero-v1.18.4-backups.yaml ;;
	backupstoragelocations) fetch https://raw.githubusercontent.com/vmware-tanzu/velero/v1.18.4/config/crd/v1/bases/velero.io_backupstoragelocations.yaml bd6e8fb1169d6e9299f49090f86cb9aa263a9e67314013ba231e93ba74485eba velero-v1.18.4-backupstoragelocations.yaml ;;
	restores) fetch https://raw.githubusercontent.com/vmware-tanzu/velero/v1.18.4/config/crd/v1/bases/velero.io_restores.yaml 91eab7251df2936fcfa7bed32736c3949e1f70bbe4dcadde2e5dc2d299613042 velero-v1.18.4-restores.yaml ;;
	schedules) fetch https://raw.githubusercontent.com/vmware-tanzu/velero/v1.18.4/config/crd/v1/bases/velero.io_schedules.yaml d2276c19d93b63ed848fb492a0f34a2ac1e162938bed499a824acf356b5860c9 velero-v1.18.4-schedules.yaml ;;
	volumesnapshotlocations) fetch https://raw.githubusercontent.com/vmware-tanzu/velero/v1.18.4/config/crd/v1/bases/velero.io_volumesnapshotlocations.yaml ec1b87c3f1b83053e97e01edf3aa059dff2bcb73c9d8a89bc08a5e8644777849 velero-v1.18.4-volumesnapshotlocations.yaml ;;
	esac
}
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
STAMP=$(date -u +%Y%m%d%H%M%S)
RUN="${STAMP:0:4}-${STAMP:4:2}-${STAMP:6:2}T${STAMP:8:2}:${STAMP:10:2}:${STAMP:12:2}Z"
CA=$(printf -- "-----BEGIN CERTIFICATE----- %s" "$N" | base64)
OLD=$(date -u -v-3d +%Y-%m-%dT01:00:00Z)
OLDSTART=$(date -u -v-3d +%Y-%m-%dT01:00:05Z)
if [[ $(date -u +%H) -ge 3 ]]; then NIGHT=$(date -u +%Y-%m-%dT03:00:00Z); NIGHTNAME=$(date -u +%Y%m%d030000)
else NIGHT=$(date -u -v-1d +%Y-%m-%dT03:00:00Z); NIGHTNAME=$(date -u -v-1d +%Y%m%d030000); fi

for c in schedules backups backupstoragelocations restores volumesnapshotlocations; do
  k apply -f $(velero_crd $c) >/dev/null
done
k wait --for condition=established --timeout=60s crd/schedules.velero.io crd/backups.velero.io crd/backupstoragelocations.velero.io crd/restores.velero.io >/dev/null

k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k -n $NS delete schedules.velero.io hourly --ignore-not-found >/dev/null
k -n $NS delete backups.velero.io -l velero.io/schedule-name=hourly --ignore-not-found >/dev/null
k -n $NS delete backups.velero.io -l velero.io/schedule-name=regular --ignore-not-found >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: {name: velero, namespace: $NS, labels: {component: velero}}
spec:
  replicas: 1
  selector: {matchLabels: {deploy: velero}}
  template:
    metadata: {labels: {deploy: velero, component: velero}}
    spec:
      containers: [{name: velero, image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent}]
YAML

k apply -f - >/dev/null <<YAML
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata: {name: default, namespace: $NS}
spec:
  provider: aws
  default: true
  objectStorage: {bucket: lab-backups, prefix: kind-klyx, caCert: "$CA"}
  credential: {name: cloud-credentials, key: cloud}
  config: {region: eu-west-1, s3Url: "https://lab:$N@minio.lab.svc:9000/path", kmsKeyId: "$N"}
---
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata: {name: dr, namespace: $NS}
spec:
  provider: azure
  objectStorage: {bucket: dr-container}
  config: {resourceGroup: dr, storageAccount: drlab}
---
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata: {name: minio, namespace: $NS}
spec:
  provider: aws
  accessMode: ReadOnly
  objectStorage: {bucket: restore-only}
---
apiVersion: velero.io/v1
kind: Schedule
metadata: {name: regular, namespace: $NS}
spec:
  schedule: "@every 6h"
  template:
    storageLocation: default
    ttl: 72h0m0s
    includedNamespaces: [team-a, team-b]
    snapshotVolumes: false
    hooks:
      resources:
      - name: db
        pre: [{exec: {container: db, command: ["/bin/sh", "-c", "pg_dump --password=$N > /backup/db.sql"]}}]
---
apiVersion: velero.io/v1
kind: Schedule
metadata: {name: nightly-failed, namespace: $NS}
spec: {schedule: "0 3 * * *", template: {storageLocation: default}}
---
apiVersion: velero.io/v1
kind: Schedule
metadata: {name: daily-missed, namespace: $NS}
spec: {schedule: "0 1 * * *", template: {storageLocation: default}}
---
apiVersion: velero.io/v1
kind: Schedule
metadata: {name: deferred, namespace: $NS}
spec: {schedule: "0 1 * * *", template: {storageLocation: default}}
---
apiVersion: velero.io/v1
kind: Schedule
metadata: {name: dom-dow, namespace: $NS}
spec: {schedule: "0 3 1 * 0", template: {storageLocation: default}}
---
apiVersion: velero.io/v1
kind: Schedule
metadata: {name: paused, namespace: $NS}
spec: {schedule: "0 4 * * *", paused: true, template: {storageLocation: dr}}
---
apiVersion: velero.io/v1
kind: Backup
metadata: {name: regular-$STAMP, namespace: $NS, labels: {velero.io/schedule-name: regular, velero.io/storage-location: default}}
spec: {storageLocation: default}
---
apiVersion: velero.io/v1
kind: Backup
metadata: {name: nightly-failed-$NIGHTNAME, namespace: $NS, labels: {velero.io/schedule-name: nightly-failed, velero.io/storage-location: default}}
spec: {storageLocation: default}
---
apiVersion: velero.io/v1
kind: Backup
metadata: {name: deferred-20260101010000, namespace: $NS, labels: {velero.io/schedule-name: deferred}}
spec: {storageLocation: default}
---
apiVersion: velero.io/v1
kind: Backup
metadata: {name: regular-20260101000000, namespace: $NS, labels: {velero.io/schedule-name: regular}}
spec: {storageLocation: default}
---
apiVersion: velero.io/v1
kind: Backup
metadata: {name: pre-upgrade, namespace: $NS}
spec:
  storageLocation: dr
  hooks: {resources: [{name: x, pre: [{exec: {command: ["sh", "-c", "echo $N"]}}]}]}
---
apiVersion: velero.io/v1
kind: Restore
metadata: {name: drill, namespace: $NS}
spec:
  backupName: pre-upgrade
  hooks:
    resources:
    - name: init
      postHooks: [{init: {initContainers: [{name: c, image: busybox, env: [{name: PASSWORD, value: "$N"}]}]}}]
YAML

vs() { k -n $NS patch "$1" "$2" --type=merge -p "{\"status\":$3}" >/dev/null; }
vs backupstoragelocations.velero.io default "{\"phase\":\"Available\",\"lastValidationTime\":\"$NOW\",\"lastSyncedTime\":\"$NOW\"}"
vs backupstoragelocations.velero.io dr "{\"phase\":\"Unavailable\",\"lastValidationTime\":\"$NOW\",\"message\":\"BackupStorageLocation \\\"dr\\\" is unavailable: rpc error: GET https://drlab.blob.core.windows.net/dr-container?sv=2021-08-06&sig=$N&se=2026: 403\"}"
vs backupstoragelocations.velero.io minio "{\"phase\":\"Unavailable\",\"lastValidationTime\":\"$NOW\",\"message\":\"BackupStorageLocation \\\"minio\\\" is unavailable: invalid configuration value: $N AccessDenied\"}"
vs schedules.velero.io regular "{\"phase\":\"Enabled\",\"lastBackup\":\"$RUN\"}"
vs schedules.velero.io nightly-failed "{\"phase\":\"Enabled\",\"lastBackup\":\"$NIGHT\"}"
vs schedules.velero.io daily-missed "{\"phase\":\"Enabled\",\"lastBackup\":\"$OLD\"}"
vs schedules.velero.io deferred "{\"phase\":\"Enabled\",\"lastBackup\":\"$OLD\"}"
vs schedules.velero.io dom-dow "{\"phase\":\"Enabled\"}"
vs schedules.velero.io paused "{\"phase\":\"Enabled\"}"
vs backups.velero.io regular-$STAMP "{\"phase\":\"Completed\",\"startTimestamp\":\"$NOW\",\"completionTimestamp\":\"$NOW\",\"expiration\":\"2026-12-31T00:00:00Z\"}"
vs backups.velero.io nightly-failed-$NIGHTNAME "{\"phase\":\"Failed\",\"startTimestamp\":\"$NOW\",\"failureReason\":\"backup execution failed: Authorization: Bearer $N AccessDenied\"}"
vs backups.velero.io deferred-20260101010000 "{\"phase\":\"InProgress\",\"startTimestamp\":\"$NOW\"}"
vs backups.velero.io regular-20260101000000 "{\"phase\":\"Failed\",\"startTimestamp\":\"$OLDSTART\",\"failureReason\":\"backup execution failed: synced $N\"}"
vs backups.velero.io pre-upgrade "{\"phase\":\"FailedValidation\",\"validationErrors\":[\"backup can't be created because BackupStorageLocation dr is in Unavailable status.\",\"$N then https://x.example/?token=$N\"],\"expiration\":\"2026-10-01T00:00:00Z\"}"
vs restores.velero.io drill "{\"phase\":\"Failed\",\"failureReason\":\"restore execution failed: $N\"}"
echo "lab-s2a applied at $NOW (regular run regular-$STAMP, nightly run nightly-failed-$NIGHTNAME)"
