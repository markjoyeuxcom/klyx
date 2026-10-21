# Klyx v0.15.1

v0.15.1 fixes what the owner's first checks of v0.15.0 found on their own clusters. Codex reviewed the batch.

## The YAML editor: who owns the field you changed

- **An apply conflict names each field's manager,** read from the server's own answer, and its advice follows the kind of manager:
  - a Flux or Argo CD controller's field changes in Git, since a forced apply would be reverted on the next reconcile;
  - a Kubernetes controller's field changes in the object that drives it. Klyx names that object from the owner references: a Pod's ReplicaSet and its Deployment, or a Job's CronJob. Each step is checked by UID: a replaced object is never taken for the original. An "Open Deployment ns/name" button takes you there;
  - anyone else's changes with the tool that set it.

  It used to blame "another controller (e.g. Flux/Argo)" and promise a revert whoever the manager was. Klyx still never forces another manager's fields.
- **A Pod's editor says up front** that its spec cannot change after creation, apart from a few fields such as its containers' images, `activeDeadlineSeconds`, added tolerations and removed scheduling gates. Labels and annotations can change.

## Alerts on a shared Alertmanager

- **Alerts that share a name are told apart.** Each row says up to two labels whose values differ, `cluster` and the other where-from labels first. Four Watchdog rows from four clusters read as four clusters. A label Klyx did not keep (past the 64 kept per alert) is said as not shown, never as absent.
- **With no match set,** alerts that carry several values of a cluster label (`cluster`, `cluster_name`, `k8s_cluster` or `tenant`) get a notice. Every alert on that Alertmanager is read as this cluster's and counts in Needs you. The notice names the setting: `alertmanager.match` for the cluster in `fleet.yaml`.

## Tests

- Two frontend tests that could pass or fail on timing alone now wait for what they assert: Log history's Copy LogQL (it failed in v0.15.0's CI) and the expiry horizon.

## Known limitations

- Everything in `docs/release-notes-0.15.0.md` still applies.
- The match for a shared Alertmanager or Loki is set in `fleet.yaml`; Settings > External endpoints does not edit it yet.
