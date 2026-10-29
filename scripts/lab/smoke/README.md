# The native smoke's labs and walk

The native smoke (The Line slice 12b, `internal/smoke`, the `smoke` build tag) walks a real Klyx window against the kind cluster `klyx` and records every step as JSON. The labs here plant what its steps read: one directory per coverage slice (`labs/s1b` to `labs/s11b`, Settings S3b's `labs/s3b` and `labs/ss3b`) and the expiring certificates (`labs/certs`). Each lab's header says what it plants and why; `FINDINGS.md` beside a lab records what the lab taught about the tool it stands up.

```sh
scripts/lab/smoke/labs.sh apply          # every lab, in order (or name some: labs.sh apply s4 s9)
scripts/lab/smoke/walk.sh                # build the smoke bundle and walk it four ways
scripts/lab/smoke/walk.sh --one          # night at 1440x900 alone
scripts/lab/smoke/labs.sh teardown s9    # a lab's own objects, where it has a teardown
scripts/lab/smoke/labs.sh check          # the exact-target check alone
```

Apply the labs within the hour before a walk: some assertions age (S4's grace window, S2a's recent backup, the certificates' horizon). A walk prints one line per way - its verdict, the commit it ran, clean or dirty, steps passed of all, and the steps that failed - and whether it left the smoke fleet as it began.

## Isolation

- **One target.** Every write goes to the kind cluster `klyx` (`KLYX_SMOKE_KIND` names another), through a kubeconfig that `kind get kubeconfig` writes into the state directory. `~/.kube/config` is never read or changed, so no current context matters. Before any write, `lib.sh` checks that the context's API server is kind's own record of the cluster, on loopback.
- **One state directory.** `$KLYX_SMOKE_DIR` (default `${TMPDIR:-/tmp}/klyx-smoke`) is the smoke's only once it holds `.klyx-smoke-dir`: `labs.sh apply` refuses a non-empty directory without it. It holds the kubeconfig, the pinned downloads (`dl/`), what labs generate (keys, pushed bodies, object ids), the logs, the bundle, its fleet and the walks. Nothing is written into the repository.
- **Pinned inputs.** Upstream manifests (Velero, source-watcher, the Karpenter providers, MetalLB, Cilium's pool CRD, KEDA) are fetched once and used only when they match their sha256 in the lab; Gateway API's CRDs come from the module cache at the version `go.mod` pins.
- **The app sees the lab alone.** The walk runs the bundle with `KUBECONFIG` set to the state directory's kubeconfig. The smoke fleet (`fleet.yaml`, copied fresh before each walk and compared after) names two AKS contexts that kubeconfig does not hold, so the walk reads clusters that cannot connect; its AI steps read the scripted model (`../eval/fake-model.mjs`, `a3-plan`) on `127.0.0.1:18792`, which `walk.sh` starts unless one already answers there.

## What the labs assume

The labs overlay the kind cluster the earlier smokes built, and do not create it: Flux and Argo CD with their applications, the namespaces `shop`, `team-a`, `team-b`, `bulk`, `infra` and `helm-demo` with their workloads and release, and the AI eval's E1 scenario (`../klyx-eval.sh setup e1-flux-crashloop`). Rebuilding that base from nothing is not scripted yet.

## Requirements

kind, kubectl, helm, Go and Node (the frontend's `node_modules`), `curl`, `shasum`, `perl`, `python3`, and on macOS `codesign` for the ad-hoc signed bundle. The walk generates the Wails bindings first (they are per checkout); set `WAILS3` to the `wails3` binary or install the version `go.mod` pins: `go install github.com/wailsapp/wails/v3/cmd/wails3@<version>`.
