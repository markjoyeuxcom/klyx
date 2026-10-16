# Klyx extensions: research and recommendation

Owner question, 2026-10-16: "research if we can add plugin/extension feature".

**Answer:** yes, but not the way Headlamp and Lens do it. Those tools run plugin code inside the app with full trust. In Klyx that would give third-party code every write path and every credential-backed read the app has. I recommend declarative "packs" that Klyx evaluates itself: CRD health and references, links, and pinned commands. Code plugins would come only later, only sandboxed, and only after a spike.

## 1. What the others do

The research read Headlamp `main` 959ce358 (v0.46.0), FreeLens `main` 3295db4e (now `2.0.0-0`), Octant (archived) and the k9s docs. Each claim below has its source in the research report.

| Tool | Model | What a plugin can reach | Trust model |
|---|---|---|---|
| Headlamp | JS bundles run with `new Function` in the shared renderer; 36 `register*` APIs (routes, sidebar, detail sections, table columns, app bar, themes, cluster providers, projects) | The k8s API through Headlamp's proxy with the user's credentials, the token get/set hooks, arbitrary `fetch`, and the preload's `desktopApi` channels | Its docs say "same JavaScript context as the main application", so there is no JS sandbox. **Command execution** is tightly controlled: exact argv-prefix grants from the build manifest, the plugin's SHA-256 checked before a capability token is issued, staged executables, and user consent. There is a curated Artifact Hub catalog, and installs go through a native dialog |
| FreeLens | npm-packaged `LensRendererExtension`/`LensMainExtension` (cluster pages, object menus and detail items, status bar, catalog, preferences); v2 just broke the v1 API | Node and Electron with `contextIsolation:false`; its own API spec says "Extensions run with full privileges" | Integrity checks only. The code says that "says nothing about authenticity" |
| Lens Desktop | Extensions removed from the current product ("on the roadmap, no ETA", Mirantis, 2025-02) | - | - |
| k9s | `plugins.yaml`: shortcut, scopes, command and args with variables (`$NAMESPACE`, `$NAME`, `$CONTEXT`, `$COL-*`, `$INPUT_*`), `confirm`, `dangerous`, `background`; about 53 community files | Whatever the command does, as the user | The user's own file; `confirm` and `dangerous` plus read-only mode |
| Octant | Go plugins through hashicorp go-plugin (gRPC subprocesses) and JS in goja | Full CRUD through the host client, and an unrestricted HTTP client | Process isolation only. Archived 2023-01 |
| Kubernetes Dashboard | Plugin system removed in v7.0.0 ("outdated and not working") | - | - |

**Two lessons.**
- **Nobody ships a JavaScript sandbox for plugins.** Where hardening exists (Headlamp), it guards command execution, not the plugin code.
- **Plugin ecosystems are short-lived** (Lens, Dashboard, Octant, Monokle). The format that lasted is the plainest: k9s's YAML shell-outs.

**The threat is current.**
- Registry-borne malware has targeted exactly the credentials Klyx holds: the Shai-Hulud npm worm (CISA, 2025-09) and later variants that harvest kubeconfigs, GlassWorm in VS Code and OpenVSX, and the Cyberhaven Chrome extension takeover.
- A tool holding six production AKS kubeconfigs is a high-value target, and an npm-style marketplace with auto-update is the attack path in every one of those incidents.

## 2. What Klyx can and cannot host today

**In-webview code would own everything.**
- The frontend reaches 38 Go services through Wails bindings: applies, deletes, rollbacks, drains, Secret reveal, exec, AI egress.
- The protected-cluster guard is "a guard against mistakes, not a security boundary" (`internal/appbridge/protectguard.go`): any script that can call bindings can `ArmProtected` and then act.
- So a Headlamp-style plugin in Klyx's webview would hold all of it.

**Even a sandboxed iframe is unproven.**
- Wails v3 alpha2.103 serves every bound call at `/wails/runtime` with no check on Origin, frame or method (`pkg/application/messageprocessor.go`, `transport_http.go`).
- Whether an opaque-origin `sandbox="allow-scripts"` iframe can reach that endpoint on WKWebView, WebView2 and WebKitGTK is not known. It needs a spike, and maybe an origin or nonce check in the transport (an upstream change).

**The seams that fit, already built:**
- `internal/observe` (`KindSpec`) and `internal/health` turn kinds into verdicts that every attention surface reads: Needs you, the fleet spine, the mast.
- The generic browser already draws any CRD's printer columns from the server's tables (slice 15), and the frontend has a condition polarity registry (`chrome/conditions.ts`).
- `internal/execenv` pins a child process to the cluster's kubeconfig; `internal/procrun` contains it (macOS and Linux; Windows waits for its own contract).
- The command console and the inspector's More menu are where verbs live; ConfirmDialog with the typed interlock gates them.
- `claude-builds.json` is a working pattern for "approve this exact thing on this machine, and re-approve if it changes".

## 3. Recommendation: Klyx packs

A pack is a YAML file the user puts beside `fleet.yaml`, in `packs/`. Klyx reads it and evaluates it with its own code; a pack never executes code inside Klyx. Packs fill the long tail of CRDs and site-specific links and commands. They never replace first-class support (design principle 2: GitOps, Gateway API, cert-manager and ESO stay built in).

**P1. Kind packs and links** (read-only; nothing executes):
- **Health for a CRD** Klyx does not know (Crossplane, CloudNativePG, KEDA, Strimzi, Kyverno, Azure Service Operator):
  - which conditions count and their polarity;
  - staleness through `observedGeneration`;
  - optional rules over the object.

  The kind joins the observer as a generic `KindSpec`, so its faults reach Needs you and the fleet like any built-in kind. Every such verdict names its pack ("rule from pack cnpg"), never presented as Klyx's own judgement.
- **References:** a field path to another object (`spec.bootstrap.initdb.secret.name` → Secret) becomes a link in the inspector, and a reverse lookup where cheap.
- **Links:** URL templates per kind or label, opened in the browser. Examples are a Grafana dashboard per workload, a runbook per alert label, or the Azure portal for a node pool.
- **Rule language:** start with declarations (conditions, polarity, thresholds) and no new dependency. Add CEL (cel-go, the language Kubernetes uses for CRD validation and admission policy: bounded, non-Turing-complete) only when declarations prove too weak.

**P2. Actions** (k9s's model on Klyx's pinned path):
- **What an action is:** a verb on a kind or object, shown in the More menu and the console, made of a command plus argv templates (`{{.cluster}}`, `{{.namespace}}`, `{{.name}}`, `{{.kind}}`).
- **How it runs:**
  - never through a shell, so a label or a name can never inject;
  - with `KUBECONFIG` pinned by `execenv`, contained by `procrun`;
  - output goes to a pane, and an interactive command uses the exec terminal.
- `dangerous: true` routes through ConfirmDialog, and through the typed interlock on protected clusters.
- Installed kubectl plugins (krew) are reachable this way, with no extra contract.
- **Trust:**
  - each pack file is approved by its SHA-256 on this machine, with the exact commands it can run listed at approval;
  - a changed file needs approval again;
  - the record sits beside `fleet.yaml`, like `claude-builds.json`.
- **Windows:** actions wait for `procrun`'s Windows containment.

**Distribution:** local files only. There is no catalog and no auto-update, because those are the attack path in every incident above. Klyx can ship a small set of reviewed packs in its own repo, which also dogfoods the format.

**Later, only on demand:**
- **Compute plugins in WASM** (wazero; pure Go, memory-isolated, no files, network or syscalls unless granted), for logic declarations cannot express. The costs:
  - wazero's tested matrix leaves out macOS Intel and Windows ARM64, both Klyx targets;
  - the compiler mode needs the JIT entitlement on macOS;
  - authors must build to WASM.
- **Sandboxed UI panels**, only after the iframe spike above proves isolation on all three engines.

**Not recommended:**
- **In-webview JS plugins:** they get full privilege over every binding.
- **hashicorp go-plugin subprocesses:** they run as the user with full OS access, which is crash isolation, not a sandbox.
- **Go's `plugin` package:** no Windows support, and plugins must match the exact toolchain.
- **A marketplace.**

## 4. An example pack

```yaml
apiVersion: klyx.dev/v1alpha1
kind: Pack
metadata:
  name: cnpg
kinds:
  - group: postgresql.cnpg.io
    kind: Cluster
    health:
      conditions:
        - { type: Ready, polarity: positive }
      generation: observed          # dims and says Syncing… while behind
      thresholds:
        - field: status.readyInstances
          below: spec.instances
          verdict: attention
          reason: "{{.status.readyInstances}} of {{.spec.instances}} instances ready"
    refs:
      - { path: spec.superuserSecret.name, kind: Secret }
links:
  - scope: { kinds: [apps/Deployment, apps/StatefulSet] }
    title: Grafana
    url: "https://grafana.example/d/workload?var-namespace={{.namespace}}&var-workload={{.name}}"
actions:
  - scope: { kinds: [postgresql.cnpg.io/Cluster] }
    title: Promote a replica
    command: kubectl
    args: [cnpg, promote, "{{.name}}", "{{.input.instance}}", -n, "{{.namespace}}"]
    inputs: [{ name: instance, label: Instance to promote }]
    dangerous: true
```

## 5. Open questions for the owner

1. **Where it is felt first.** Is the long tail of CRDs (health in Needs you) or site links (Grafana, runbooks) the more valuable start?
2. **Where packs live.** Should a pack be per fleet or per cluster (k9s allows per context), or both, with the cluster winning?
3. **How a pack verdict is seen.** Should a pack's faults count in the fleet's Needs you by default, or only after the user opts the pack in?
4. **CEL.** Is a new dependency acceptable when declarations run out?
