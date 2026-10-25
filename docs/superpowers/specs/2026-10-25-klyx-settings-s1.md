# Klyx Settings S1: sections, the registry and its coverage

Opened 2026-10-25, on `feat/settings-s0`, after the redesign was accepted and its owner decisions confirmed (`2026-10-25-klyx-settings-redesign.md` §20, §16). S1 is the redesign's first slice (§14). It builds the structure every later slice fills:
- the sections, their addresses, search and ⌘,;
- the registry, with every setting's contract and disposition;
- the coverage tests that make an undescribed setting a failure;
- Diagnostics' read-only parts.

S1 adds no new write and changes no setting's behaviour. This note is for Codex's review before it is built; revisions 1 to 3 the same day, after that review and two re-reviews (§8 to §10); the design is accepted (§11).

## 1. Addressing and navigation

**The route** gains an optional section and anchor:

```ts
| { name: "settings"; section?: SettingsSection; anchor?: string }
type SettingsSection = "fleet" | "observability" | "ai" | "git" | "notifications" | "appearance" | "data" | "diagnostics";
```

- `openSettings(section?, anchor?)` replaces the argument-less call. With no section, Settings opens Fleet, or the section the session last showed (persisted with the route, as `klyx-route` already restores routes).
- History records each section change, so the arrows move between sections.
- An anchor is a setting's registry id, plus an instance for a repeated row: `clusters[].protected@aks-we-prd`, `git.repos[].matches@<repository key>`.
  - The section scrolls to that row and focuses its control, or its read-only row.
  - An anchor naming an instance that is no longer there opens the section at the list, saying it is gone.
  - A search result for a repeated field opens its list, with every instance's row marked.
  - Each control's DOM id and description id are made from the registry id and the instance key, so they are unique.
- Every existing link to Settings names its section:

| Link | Opens |
|---|---|
| "Set up AI" in Explain, chat, investigations | `ai` |
| the fleet's empty state, kubeconfig badges | `fleet` |
| "approve in Settings > Git" | `git`, at that repository's row |
| endpoint credential prompts | `observability`, at the cluster's endpoint |
| the palette's "Settings" | the last section |

**⌘,** (mod+,) opens Settings from anywhere the shell owns keys, through AppShell's ownership checks. A confirmation, the palette or a text editor owns its keys first. Collapsing the rail keeps the existing focus handoff.

**Leaving a section with a draft** asks to save, discard or stay. A typed credential is its own draft, never saved or discarded as part of the section's settings: the guard names it separately ("an unsaved API key"), and Save settings never stores it. This applies to switching sections, a search result, history, a deep link or leaving Settings, since a section is mounted one at a time and the AI panel's draft is local. A save that is still being read back finishes first. A key draft is never persisted in the route, history or the registry. This is the redesign's §2 rule, and S1 builds it.

**The route** is validated when restored: an unknown section or anchor opens Settings at Fleet, said. History compares and describes section routes. Returning to Settings from another route keeps its section.

**The rail** is the lens index's slot and component (`LensIndex`): when the route is Settings, the index shows the sections instead of lenses. It keeps the same width, the same mod+\ toggle and the same keys, and each entry is a button with `aria-current`. A search field sits at the top of the rail (§3).

**The page** shows one section at a time, under the section's sentence (redesign §1.1). S1's sentences are factual and short:

| Section | Its sentence in S1 |
|---|---|
| Fleet | "12 clusters running, 4 protected; fleet.yaml agrees", or what differs |
| Observability | "Alertmanager configured or found for 4 clusters; Loki configured for 2", from the configuration and discovery, which establish a source, not a successful read. Where the health summary has the alerts part's last read, its state is said with it. |
| AI | the In use line, as today |
| Git | the binary in use, as today |
| Notifications | on or off, and the scope |
| Appearance | the theme and zoom in use |
| Data and storage | what Klyx keeps, and its total size where Klyx knows it |
| Diagnostics | the fleet file's state |

The existing panels move into their sections unchanged in behaviour:

| Section | Holds in S1 |
|---|---|
| **Fleet** | the cluster table (slice 0's drift included), kubeconfig contexts, the line's comparison source (effective, file-only) |
| **Observability** | External endpoints |
| **AI** | the AI panel |
| **Git** | the Git panel |
| **Notifications** | the notifications panel |
| **Appearance** | theme, and zoom with its reset (from the existing zoom store; no new preference) |
| **Data and storage** | what Klyx keeps, read-only, and the search index's kinds and budgets (effective, file-only); actions come in S6 |
| **Diagnostics** | §5 |

## 2. The registry

**Where it lives:** the registry is Go (`internal/settings`), not TypeScript as the redesign's §3 first named it. Its coverage must be derived from the Go schema, and the backend supplies each effective value, so one source serves both. The frontend reads it through `SettingsService.Registry()`.

**What each entry holds,** as the redesign's field contract (§3):

```go
type Entry struct {
	ID          string      // stable: "ai.provider", "clusters[].protected", "env.KLYX_CONFIG", "store.klyx-theme"
	Section     string
	Label       string
	Effect      string      // what changing it does, and what it does not
	Scope       string      // fleet | cluster | endpoint | profile | process | webview
	Default     string
	Validation  string      // allowed values, units, zero, empty and unset
	TakesEffect string      // now | on Save | at reconnect | at restart | on Test or a credential action
	Test        string      // what Test checks and sends, when there is one
	Save        string      // what Save writes, and where
	Disposition string      // editable | effective-file-only | deferred | fixed
	Source      SourceKind  // schema | env | store | bound
}
```

**Text:**
- The text is product copy, written once.
- Each existing panel renders its controls through one component, `SettingField` (§4), which reads the entry by id. Labels and effects therefore come from the registry, not from the panels' current inline strings.
- The registry never holds a value; credentials appear only as their state.
- `Entry.Source` is where the entry comes from (schema, environment, store, bound). The effective value's source at run time comes from the service that owns the value, or from the webview store, with slice 0's failed-reread and drift qualifications. A fresh read of the file never stands in for what runs, for the line's source or the index.

**The snapshot:**
- A golden file, `cmd/klyx/frontend/src/settings/registry.snapshot.json`, is written by `go test ./internal/settings -update`.
- A Go test fails when it differs from the registry, or when either has a missing, surplus or duplicate id. `Registry()`'s reply is that same data.
- The frontend tests and the search index import it, so the frontend never renders an id the registry lacks.

## 3. Coverage: an undescribed setting fails a test

Four Go tests derive what must be covered, each from its own source, and fail on anything without an entry. A fifth, in the frontend, fails on any control without one:

| Test | Derives | From |
|---|---|---|
| **schema** | every YAML path of `config.Config`: fields by their yaml tags, lists as `[]`, maps as their own leaf, recursively | reflection over the types, the same walk as slice 0's key check |
| **environment** | every environment name product code reads, by declared family (§3.1) | a Go AST scan, with negative fixtures |
| **stores** | every webview storage key, and every native store beside the fleet file (§3.2) | one registered key list, and one path function, each enforced by a scan |
| **bounds** | the fixed ceilings the redesign names (chat and investigation budgets, conversation slots, Git cache caps, rows and alerts bounds) | a list, each read from its Go constant through a dependency-free leaf package (`internal/bounds`), so a changed constant changes the shown value. A new ceiling is added to the list by review: the test detects changed values, not new ceilings, and says so. |
| **controls** (frontend) | every form control a section actually renders: input, select, textarea, switch and checkbox roles, in dialogs and portals too, under each provider's state. Each must sit inside a `SettingField` whose id is registered, or carry an explicit exemption (`data-setting-exempt="action"`, `"search"`, `"nav"`). | mounted renders of each section with fixtures, and a negative fixture that inserts a raw unregistered input and fails |

### 3.1 Environment: one facade

Product code reads the environment only through `internal/envread`, and a scan rejects any other access. Coverage then follows from the type system, not from recognizing every way code might spell a read.
- **Names are opaque handles, owned by the facade.** `envread.Name` is a struct with an unexported field. Only `envread` can make a non-zero one, and it makes exactly the declared ones, as exported values (`envread.KlyxConfig`, `envread.Terminal`). A string, a conversion or a literal cannot become a Name outside `envread`. The zero Name, the only one other code can construct, is refused by every facade function before any access, as is a handle missing from the declaration table (a membership check, not trust in the type). Families, dynamic purposes and native store ids are handles of the same kind. `internal/settings` holds the descriptive metadata keyed by the handles, and a test compares the facade's declaration table with the registry, each way. The declarations, by family:
  - **product:** `KLYX_CONFIG`, and the eval, smoke and lab builds' variables, these as `fixed` under a "not product settings" note;
  - **tool overrides:** the `KLYX_*_PATH` names; each `clibin.Binary` takes an `envread.Name`, so a new override is a new declaration;
  - **library:** names Klyx passes to a library that reads them, such as `KUBECONFIG` through client-go's `RecommendedConfigPathEnvVar`, declared, and checked equal to the library's constant by a test;
  - **injected:** names read through an injected function (`TERMINAL`, `SSH_AUTH_SOCK`, `XDG_CONFIG_HOME`), whose type is `func(envread.Name) string`;
  - **dynamic:** the AI key variable, read by `envread.Dynamic(name, purpose)` with `purpose` a declared constant: its entry describes the contract, never a value;
  - **inherited:** names honoured by libraries without Klyx reading them (the proxy variables, `HOME`, `PATH`), declared as such, without claiming to enumerate what a kubeconfig exec plugin reads.
- **Walks:** a walk of the environment is `envread.Walk(family)`, where the family is a declared handle owning its prefixes, and `Walk` applies them itself. A new prefix is therefore a new declaration, never an edit at the walk site. A child process's environment is `envread.ForChild(family)`, likewise (§3.3).
- **The scan** (Go AST, product code: `internal/`, `cmd/klyx/*.go`, `cmd/klyxctl`; tests and build-tagged harness files excluded) fails on any reference, called or not, to:
  - `os.Getenv`, `os.LookupEnv`, `os.Environ`, `os.ExpandEnv`, `os.Setenv` or `os.Unsetenv`;
  - `syscall.Getenv` or `syscall.Environ`;
  - the same through an aliased import;
  - all of these outside `envread`.

  A function value, a constant name or a helper cannot bypass it, because any of them must start from one of these references.
- **Negative cases:** an undeclared or zero Name, family, dynamic purpose or native store id is refused before any access (unit tests of each facade). Synthetic Go files run through the scan, and each of these fails it:
  - a constant-named `os.Getenv`;
  - `os.LookupEnv`;
  - `os.Environ` used as a value;
  - an aliased `os` import;
  - a raw `getenv` default of `os.Getenv`.

  A walk naming a family that is not a declared handle is refused before it reads.

### 3.2 Stores: one facade each

- **Webview storage:** all `localStorage` and `sessionStorage` access goes through `src/settings/storage.ts`, whose functions take a key from the declared union (`storageKeys.ts`). They also check the key's membership in that declared table at run time, so a cast (`"x" as StorageKey`) is refused before any access. Each key has a registry entry.
  - The existing call sites move to it, `persist.ts`'s helper included.
  - A TypeScript AST test fails on any reference to `localStorage`, `sessionStorage`, `window.localStorage`, `globalThis.sessionStorage` and the like, outside that module. This covers element access by string (`window["localStorage"]`), and a reference held in a variable or passed to a helper, since obtaining the object is the reference.
  - Negative fixtures: storage passed to a helper, aliased, and accessed with brackets.
- **Native stores:** the fleet file's directory is held only by `internal/localstore`, set once at startup.
  - Every file or directory Klyx keeps beside the fleet file (`window.json`, `claude-builds.json`, `git-cache/`) comes from `localstore.Path(store)`, with `store` an opaque handle declared there. An undeclared or zero handle is refused before a path is formed.
  - A test compares the declared names with the registry's native entries, each way.
  - The paths `main.go` builds today move to it, and a Go scan fails on any `filepath.Dir` of the fleet path outside `localstore`.

### 3.3 Preserving behaviour through the facades

The facades change how code reaches the environment and stores, not what it reads, inherits or writes. Each of these existing behaviours is an acceptance requirement, with old-versus-new fixture comparisons:
- **Native tools inherit the whole environment** (`execenv.Environ`: nil inherits; extras are appended last), as do helm and flux. Their `ForChild` family is "inherit all", so an external auth or plugin variable (`AWS_PROFILE`, an exec plugin's own) still reaches kubectl. Settings describes it as inheritance and never shows values.
- **Git and the Claude Code CLI keep their allowlists:**
  - Git's owner environment and isolated per-command configuration (`gitcli/env.go`);
  - the CLI's exact names, its `LC_*` family and its fixed lockdown values (`claudecli_contract.go`).

  Their families are those allowlists, never "inherit all". Fixed child overrides stay after inherited entries, with their precedence.
- **Timing and injection:**
  - Reads happen at the same moments as today, never frozen at module start.
  - Tests keep their fake readers.
  - `Dynamic` stays bound to the AI key's explicit source.
  - The desktop PATH extension (`ApplyDesktopToolPath`) keeps its ordering and platform expansion through `envread`.
- **Webview storage:**
  - the same key strings, tiers (local or session), JSON formats, migrations and defaults;
  - the best-effort handling when storage is unavailable;
  - the session lifetime of view searches;
  - zoom's cross-window updates and the theme's change notifications.
- **Native locations:**
  - `localstore`'s root is the fleet file's lexical directory, as today, so `KLYX_CONFIG` still relocates the adjacent files;
  - it is not slice 0's resolved symlink target, which stays the fleet file writers' own: `localstore` never touches the temp-file, mode, symlink or compare-and-swap rules;
  - tests supply isolated roots without re-rooting a running app.

**Search** filters the snapshot by label and effect, case-insensitively and by word. It lists matches by section, and opens the section at the entry's anchor.

**The console** lists "Settings: <section>" for each section and "Settings: <label>" for each entry whose disposition is `editable` or `effective-file-only`.

**The dispositions at the start,** per the redesign's §3. Every current editable control is `editable`. These are the rest:

| Entry | Disposition | Section |
|---|---|---|
| `clusters[].name`, `.context`, `.kubeconfig`, `.tags`, `.group`, `.environment`, `.protected` | effective-file-only (editable in S2) | Fleet |
| `clusters[].metrics`, `.alertmanager`, `.loki` and their fields | effective-file-only (installations in S3a/S3b) | Observability |
| `line.source.*` | effective-file-only | Fleet |
| `index.*` | effective-file-only (editable in S6) | Data and storage |
| `git.repos[].remoteBases` | deferred: not supported (the cache diff refuses remote bases) | Git |
| `ai.backend`, `ai.apiKey`, `clusters[].metrics.token` | effective-file-only, legacy (migration offered where it exists today) | AI, Observability |
| `env.KLYX_CONFIG`, `env.KLYX_*_PATH`, the proxy variables, `TERMINAL` | effective-file-only, process | Diagnostics |
| webview stores (theme, zoom, layout, view preferences, Activity, palette ranking) | editable or effective, per store | Appearance, Data and storage |
| the fixed ceilings | fixed | Diagnostics |

## 4. `SettingField`

One component renders a setting, whichever control it has. Its parts:
- **The label**, associated with the control.
- **The effect**, always visible, under the control, in secondary ink, wired to the control by `aria-describedby`. It is never only a tooltip.
- **A source chip** for the effective value: "file", "default", "inferred", "discovered", "environment", "this window".
- **A takes-effect chip** when it is anything but immediate.
- **The default and the validation,** in a disclosure under the effect, reachable by keyboard.
- **A read-only row** for a file-only or fixed entry, with its effective value and where to change it ("in fleet.yaml, under clusters[].group; applies at restart"), or "fixed".

It carries `data-setting=<id>`, and its control and description ids are made from the registry id and the instance key (§1), so a repeated field's are unique. The rows grow to fit (redesign §12).

## 5. Diagnostics (read-only)

**`SettingsService.Diagnostics()`** returns:
- **The fleet file:**
  - its path, and whether `KLYX_CONFIG` set it;
  - its load state: loaded, the startup load error (slice 0), or the file not loading now;
  - its warnings;
  - a summary of slice 0's drift.
- **Tools:** for git, kubectl, helm and flux, the resolution, shown on every visit without running anything:
  - the path `clibin` resolves;
  - its source, which the resolver now returns rather than Diagnostics inferring it:
    - the override, chosen;
    - `PATH`;
    - a platform or package-manager fallback location;
    - not found, with why.

    Separately, whether an override was set but missing, so a missing override that fell back to `PATH` or to a fallback says both.

  **Versions are read only on an explicit "Check versions"**, which says that it runs these local programs:

  | Tool | argv |
  |---|---|
  | git | none: the Git service's pinned path and version are reused |
  | kubectl | `kubectl version --client --output=json` |
  | helm | `helm version --short` |
  | flux | `flux version --client` |

  Each run follows the Git binary check's contract (`gitcli.Locate`):
  - explicit argv, no shell;
  - a minimal environment (`PATH`, `HOME`, `KUBECONFIG` set to an empty temporary file, so nothing reads a kubeconfig);
  - its own process group, killed on cancel;
  - a 5-second deadline with a wait delay;
  - stdout and stderr each capped at 64 KiB;
  - the version parsed from the output and only then clipped to 120 characters with control characters removed: kubectl's JSON `clientVersion.gitVersion`, and the version field of helm's and flux's output;
  - on a platform without the reviewed process containment, versions are not checked, and Diagnostics says so; there is no plain-exec fallback;
  - a failure said with a fixed reason and the exit status, never the raw error text.

  Klyx invokes only these client-only commands, and nothing in Diagnostics changes Klyx's configuration or state; the empty kubeconfig is a scratch file, removed afterwards. Running a local tool is not a sandbox: what an installed wrapper does beyond these arguments is the wrapper's.
- **The launch environment:**
  - the names of the proxy variables that are set (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and their lower-case forms), never their values, and what honours them: the AI clients do, the observability transports deliberately do not;
  - a note that a GUI launch may not see shell exports, which matters for an AI key read from the environment.
- **The fixed bounds:** each ceiling with its value, from its Go constant (§3).

Diagnostics changes nothing of Klyx's configuration or state, and sends nothing anywhere (the version check's scratch kubeconfig aside, §5).

## 6. What S1 does not do

- No new write, and no setting's behaviour changes: the panels' behaviour is slice 0's.
- No Apply model, no cluster editing, no installations or profiles: those are slices F, S2, S3a and S4.
- No Data and storage actions: those are S6.

## 7. Tests

**Go:**
- the four coverage tests (§3);
- the snapshot test;
- Diagnostics:
  - a missing tool;
  - an override chosen, and an override set but missing;
  - the proxy names without values;
  - the load states;
  - "Check versions" with stand-in binaries that record argv and environment, fail if a kubeconfig is read, emit excessive output, spawn a child that outlives a normal exit, or hang; a cancel; kubectl's JSON parsed;
  - a fallback location found, with and without a missing override.

**Frontend:**
- the controls coverage test (§3);
- the route with section and anchor, its persistence, validation and history, and returning to Settings from another route;
- repeated rows: two repositories and two clusters with the same field, each addressed, and a vanished instance;
- leaving a section with an AI draft: save, discard and stay, and a save still being read back;
- ⌘,;
- the rail, with `aria-current` and its keys;
- search by label and by effect, opening the anchor and focusing its control;
- each existing deep link opening its section;
- `SettingField`: the label association, the effect's `aria-describedby`, the chips and the read-only row;
- each section's sentence.

**Fixtures:** night and day, 200% zoom, a narrow window, long names and URLs, keyboard only.

**The native smoke:** every section opens by its address; search finds "protection" by its effect and opens Fleet at it; ⌘, opens Settings.

## 8. Revision 1: Codex's review (2026-10-25)

Codex requested changes: three P2 and two P3. It accepted the Go registry (§2) and found the schema derivation sound for the current types (67 leaf paths).

| Finding | Change |
|---|---|
| **R1, P2:** coverage missed indirect environment reads and native stores | §3.1: declared families, with tool overrides taken from `clibin`; an AST scan of every read form, with negative fixtures. §3.2: one registered key list and wrapper for webview storage, and one path function for native stores, each enforced by a scan. |
| **R2, P2:** the controls test began from marked elements | §3: it starts from every form control a section renders, which must sit in a registered `SettingField` or carry an exemption; a negative fixture inserts a raw input. |
| **R3, P2:** version calls needed an exact, bounded, client-only contract | §5: resolution shown passively, with its true source. Versions only on an explicit "Check versions", with exact client-only argv, a minimal environment and an empty kubeconfig, containment, a deadline and output caps. Git's pinned version reused. |
| **R4, P3:** a registry id cannot address a repeated row | §1: anchors carry an instance; unique DOM ids; generic search results; a vanished instance said. |
| **R5, P3:** configured endpoints are not successful reads | §1, §5: "configured or found" wording, with the last read state where it is known; proxy names as "set". |

**Also from the review:**
- leaving a section with a draft asks to save, discard or stay (§1);
- the route is validated when restored (§1);
- registry and snapshot checks for missing, surplus and duplicate ids (§2);
- `Entry.Source` is provenance, not the effective value's source (§2);
- the bounds come through a leaf package (§3).

## 9. Revision 2: Codex's re-review (2026-10-25)

Codex ruled R2 to R5 resolved, R1 partly. One P2 and one P3 remained.

| Finding | Change |
|---|---|
| **RR1, P2:** a scan of read forms misses constants, function values, walks and storage passed to helpers | §3.1, §3.2: facades. The environment, webview storage and the fleet file's directory are reachable only through `envread`, `storage.ts` and `localstore`. Names are typed declarations, walks and child environments name a declared family, and a scan fails on any raw reference outside a facade, however it is spelled. Negative fixtures for each form Codex named. |
| **RR2, P3:** no source label for a platform fallback | §5: the resolver returns its source, with a fallback location among them and a missing override said separately. |

**Also from the re-review:**
- kubectl's version parsed from its JSON before clipping (§5);
- no plain-exec fallback where containment is not reviewed (§5);
- the honest wording of what Diagnostics writes and runs (§5);
- unique ids for repeated fields (§4);
- a typed credential kept apart from the section's draft (§1).

## 10. Revision 3: Codex's second re-review (2026-10-25)

Codex ruled the raw-reference bans, the walk families, the storage scan and the fallback source resolved. One P2 remained.

| Finding | Change |
|---|---|
| **R2-1, P2:** Go's named string types do not stop `envread.Read("UNDECLARED")` from compiling | §3.1, §3.2: opaque handles owned by each facade (a struct with an unexported field), declared there as exported values. The zero handle and any handle not in the declaration table are refused before access. Families, dynamic purposes and native store ids are handled the same way. Webview keys are checked for membership at run time, so a cast is refused. The declaration tables and the registry are compared each way. |

**Also from the re-review:**
- §3.3 records the behaviour the facades must preserve, with old-versus-new fixture comparisons: native tools inheriting the whole environment, Git's and the CLI's allowlists, read timing and injection, storage formats and lifetimes, and native locations kept apart from the fleet file writers' symlink target;
- the old "nothing writes" sentence in §5 now agrees with the version check's scratch kubeconfig.

## 11. ACCEPT (2026-10-25)

Codex accepted the S1 design at revision 3 (83d047b), after the review of 8b92319 and three re-reviews, with no remaining findings.
- **The handles:** its cross-package prototype rejects every argument form of R2-1 at compile time, and refuses zero and non-member handles before any simulated access.
- **§3.3:** a faithful record of its preservation requirements.

The accepted contracts are acceptance requirements for the build: the coverage tests, the facades and their scans, the rendered-control coverage, instance anchors, the draft guard, Diagnostics' passive resolution and contained version check, and §3.3's old-versus-new comparisons.
