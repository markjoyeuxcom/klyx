# Klyx Settings S1: sections, the registry and its coverage

Opened 2026-10-25, on `feat/settings-s0`, after the redesign was accepted and its owner decisions confirmed (`2026-10-25-klyx-settings-redesign.md` §20, §16). S1 is the redesign's first slice (§14). It builds the structure every later slice fills:
- the sections, their addresses, search and ⌘,;
- the registry, with every setting's contract and disposition;
- the coverage tests that make an undescribed setting a failure;
- Diagnostics' read-only parts.

S1 adds no new write and changes no setting's behaviour. This note is for Codex's review before it is built.

## 1. Addressing and navigation

**The route** gains an optional section and anchor:

```ts
| { name: "settings"; section?: SettingsSection; anchor?: string }
type SettingsSection = "fleet" | "observability" | "ai" | "git" | "notifications" | "appearance" | "data" | "diagnostics";
```

- `openSettings(section?, anchor?)` replaces the argument-less call. With no section, Settings opens Fleet, or the section the session last showed (persisted with the route, as `klyx-route` already restores routes).
- History records each section change, so the arrows move between sections.
- An anchor is a setting's registry id. The section scrolls to it and focuses its control, or its read-only row.
- Every existing link to Settings names its section:

| Link | Opens |
|---|---|
| "Set up AI" in Explain, chat, investigations | `ai` |
| the fleet's empty state, kubeconfig badges | `fleet` |
| "approve in Settings > Git" | `git`, at the repository |
| endpoint credential prompts | `observability` |
| the palette's "Settings" | the last section |

**⌘,** (mod+,) opens Settings from anywhere the shell owns keys, as the other shell shortcuts do; a dialog or the palette owns its own keys first.

**The rail** is the lens index's slot and component (`LensIndex`): when the route is Settings, the index shows the sections instead of lenses. It keeps the same width, the same mod+\ toggle and the same keys, and each entry is a button with `aria-current`. A search field sits at the top of the rail (§3).

**The page** shows one section at a time, under the section's sentence (redesign §1.1). S1's sentences are factual and short:

| Section | Its sentence in S1 |
|---|---|
| Fleet | "12 clusters running, 4 protected; fleet.yaml agrees", or what differs |
| Observability | "4 clusters read alerts; 2 read log history", from the configured and discovered endpoints |
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

**The snapshot:**
- A golden file, `cmd/klyx/frontend/src/settings/registry.snapshot.json`, is written by `go test ./internal/settings -update`.
- A Go test fails when it differs from the registry.
- The frontend tests and the search index import it, so the frontend never renders an id the registry lacks.

## 3. Coverage: an undescribed setting fails a test

Four Go tests derive what must be covered, each from its own source, and fail on anything without an entry. A fifth, in the frontend, fails on any control without one:

| Test | Derives | From |
|---|---|---|
| **schema** | every YAML path of `config.Config`: fields by their yaml tags, lists as `[]`, maps as their own leaf, recursively | reflection over the types, the same walk as slice 0's key check |
| **environment** | every `os.Getenv("...")` literal in product code (the eval, smoke and lab builds' variables are listed, with disposition `fixed`, under a "not product settings" note) | a source scan of `internal/` and `cmd/klyx/*.go`, excluding tests and build-tagged harness files |
| **stores** | every `localStorage`/`sessionStorage` key literal in the frontend | a source scan of `cmd/klyx/frontend/src`, excluding tests |
| **bounds** | the fixed ceilings the redesign names (chat and investigation budgets, conversation slots, Git cache caps, rows and alerts bounds) | a list, each read from its Go constant, so a changed constant changes the shown value |
| **controls** (frontend) | every element with `data-setting` in the rendered sections, under each provider's states | mounted renders of each section with fixtures |

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

It carries `data-setting=<id>` and `id=setting-<id>` for the anchor. The rows grow to fit (redesign §12).

## 5. Diagnostics (read-only)

**`SettingsService.Diagnostics()`** returns:
- **The fleet file:**
  - its path, and whether `KLYX_CONFIG` set it;
  - its load state: loaded, the startup load error (slice 0), or the file not loading now;
  - its warnings;
  - a summary of slice 0's drift.
- **Tools:** for git, kubectl, helm and flux:
  - the path Klyx would run, its version, and whether a `KLYX_*_PATH` override chose it;
  - unavailable, with its reason.

  Versions are read through the existing `clibin` resolution with `--version`-style calls, run once per Settings session with a short timeout, and cached. A tool's absence or failure is said, never an empty row.
- **The launch environment:**
  - the names of the proxy variables in use (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and their lower-case forms), never their values;
  - a note that a GUI launch may not see shell exports, which matters for an AI key read from the environment.
- **The fixed bounds:** each ceiling with its value, from its Go constant (§3).

Nothing in Diagnostics writes, and nothing is sent anywhere.

## 6. What S1 does not do

- No new write, and no setting's behaviour changes: the panels' behaviour is slice 0's.
- No Apply model, no cluster editing, no installations or profiles: those are slices F, S2, S3a and S4.
- No Data and storage actions: those are S6.

## 7. Tests

**Go:**
- the four coverage tests (§3);
- the snapshot test;
- Diagnostics: a missing tool, an override, the proxy names without values, the load states.

**Frontend:**
- the controls coverage test (§3);
- the route with section and anchor, its persistence and history;
- ⌘,;
- the rail, with `aria-current` and its keys;
- search by label and by effect, opening the anchor and focusing its control;
- each existing deep link opening its section;
- `SettingField`: the label association, the effect's `aria-describedby`, the chips and the read-only row;
- each section's sentence.

**Fixtures:** night and day, 200% zoom, a narrow window, long names and URLs, keyboard only.

**The native smoke:** every section opens by its address; search finds "protection" by its effect and opens Fleet at it; ⌘, opens Settings.
