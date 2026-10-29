# S9 lab findings (KEDA 2.21.0 on kind-klyx, Kubernetes 1.37.0, 2026-10-19)

Read with `lab-s9/read.sh` (JSON in `lab-s9/read/<stamp>/`), t1 ~5 s and t2 ~90 s after apply.

1. **Conditions written on every ScaledObject:** Ready, Active, Fallback, Paused, from the first reconcile. Active starts Unknown (reason None, or UnknownState after a failed check). 2.21 adds **HPAActive**, mirroring the HPA's ScalingActive (HPAActive / FailedGetExternalMetric). It is absent while there is no HPA (paused, failed check).
2. **The HPA join is explicit.**
   - `status.hpaName`: `keda-hpa-<name>`, or `advanced.horizontalPodAutoscalerConfig.name` (web-autoscaler).
   - The HPA has a single ownerReference: `keda.sh/v1alpha1` ScaledObject, controller=true, blockOwnerDeletion=true.
   - Its labels: `scaledobject.keda.sh/name=<so>`, `app.kubernetes.io/managed-by=keda-operator`, `app.kubernetes.io/part-of=<so>`, `app.kubernetes.io/version=2.21.0`.
   - An adopted HPA (`scaledobject.keda.sh/transfer-hpa-ownership: "true"` plus the name) gets the same ownerReference and labels. Its user labels are kept.
3. **The target:** `status.scaleTargetKind` is `apps/v1.Deployment` or `apps/v1.StatefulSet`; `status.scaleTargetGVKR` is {group, version, kind, resource}. Both are absent when the check failed (no-target: status holds conditions only).
4. **Pause:**
   - `autoscaling.keda.sh/paused: "true"` gives Paused=True (ScaledObjectPaused) and Ready=True "ScaledObject is paused".
   - KEDA deletes the HPA: no hpaName, no HPAActive, Active Unknown.
   - `paused-replicas: "1"` behaves the same, plus `status.pausedReplicaCount`.
5. **Failures:**
   - A spec or authentication failure at setup (TriggerAuthentication names a missing Secret) gives Ready=False, reason ScaledObjectCheckFailed. The message names the scaler's parse error ("bearer token=<empty> is required"), never the TriggerAuthentication or the Secret. No HPA is created.
   - A runtime trigger failure without fallback gives Ready=False, reason TriggerError, and Active=False ScalerNotActive. `status.health` is not written.
   - With fallback (failing): Ready=True, Fallback=True (FallbackExists), and `health {"s0-prometheus": {numberOfFailures: 5, status: Failing}}`. Active=True: the fallback replicas count as active. The HPA reads ScalingActive=True and HPAActive=True. **The HPA alone hides the failing trigger.**
   - Workload identity on AWS without credentials: Ready=False TriggerError. The cause is only in the Warning event KEDAScalerFailed: "STS: AssumeRole ... failed to refresh cached credentials".
6. **The authentications:**
   - TriggerAuthentication and ClusterTriggerAuthentication carry no conditions.
   - Their `status.scaledobjects` is a string naming the referencing ScaledObjects ("bad-auth").
   - Events: TriggerAuthenticationAdded.
   - `status.authenticationsTypes` on the ScaledObject is that string reversed: the referenced authentication names.
7. **ScaledJob:**
   - Conditions: Ready, Active, Paused (no Fallback).
   - Each Job carries a single controller ownerReference to the ScaledJob, and the labels `scaledjob.keda.sh/name=<sj>` and `app.kubernetes.io/managed-by=keda-operator`.
   - Events: KEDAJobsCreated, ScaledJobActive.
8. **Other status fields:**
   - `status.triggersActivity`: a map from metric name to {isActive}, per trigger.
   - `status.triggersTypes` and `status.authenticationsTypes`: comma-joined strings.
   - `status.lastActiveTime`.
   - `status.originalReplicaCount`: the target's replicas before KEDA took over.
9. **The webhook:**
   - It warns (it does not refuse) on `pollingInterval` when it is not relevant for scaling.
   - All of KEDA's webhooks are failurePolicy Ignore, scoped to its own kinds.
   - The release manifest registers APIService `v1beta1.external.metrics.k8s.io`, which points at `keda/keda-metrics-apiserver`.
10. **The operator:** Deployments `keda-operator`, `keda-metrics-apiserver` and `keda-admission` in `keda`. A finalizer `finalizer.keda.sh` is on every ScaledObject, ScaledJob and TriggerAuthentication.
