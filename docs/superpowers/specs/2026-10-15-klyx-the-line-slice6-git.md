# The Line, slice 6: Git access

Design note for slice 6 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §7 (Git access: the binary, the transport boundary, the environment, the allowed commands, acquisition, the cache, when it fetches, failing honestly, untrusted text), §3.4 (ordered states), §3.7 (the Ledger with history), §14 decisions 5, 6, 7 and 12.
- **The parent's §7 is the contract.** This note does not restate it. It records what exists, how the slice is cut, where each piece lives, how it is tested, and the points §7 leaves open.
- **Scope:** Go, plus Settings > Git and the fleet screen's ordered states and Ledger. Nothing here writes to a cluster or to the owner's working trees.

## 0. What exists (surveyed 2026-10-15)

- **No production code runs git.** No go-git, no `GIT_*` variables. The only `git` shell-out is a lab test.
- **`internal/clibin`** resolves a CLI: an environment override, then `PATH`, then per-platform fallbacks (a macOS .app does not inherit the shell's `PATH`). It checks no version. kubectl, helm and flux use it.
- **Containment exists only for the Claude Code CLI** (`internal/ai/claudecli.go`): a process group killed on deadline or cancel, a `WaitDelay`, capped output buffers, and an environment built from scratch. The kubectl, helm and flux shell-outs inherit the parent environment and have no output cap.
- **fleet.yaml:** `Config` holds `clusters`, `ai` and `line.source` (the comparison override). There is no `git` section. The AI writer (`config.SaveAI`) replaces one top-level block under the shared write lock, refuses a concurrent edit (`replaceUnchanged`) and proves nothing else changed (`onlyAIChanged`). Its helpers hard-code `ai`, but their logic is key-agnostic.
- **The config dir** is `filepath.Dir(fleet.yaml)` (`$KLYX_CONFIG`, else `~/.config/klyx`). It holds `window.json` and `claude-builds.json`. No cache directory exists.
- **The line's Git key:** `line.NormalizeRepo` gives a scheme-less match key (`github.com/acme/fleet`). The raw `spec.url` survives only in `flux.Source.URL`. A unit keeps its `SourceName`.
- **`flux diff` (M10-f):** the UI always passes an empty path, so the Kustomization's repo-relative `spec.path` is used and does not exist locally. That is the known gap. No kustomize, `fluxcd/pkg` or go-git module is in `go.mod`.
- **Settings:** `AISettingsService` is the pattern for a panel that reads state, saves through a config writer, and reports `ActionResultDTO`.
- **Git on this machine:** Homebrew git 2.56.0 first on `PATH`, Apple Git 2.54.0 at `/usr/bin/git`; both meet the 2.45 floor. `git http-backend` is installed.

## 1. Sub-slices

| | Builds |
|---|---|
| **6a** | `internal/procrun` (§2), `internal/gitcli`: the binary and version gate, the environment and isolated config, typed argv for every allowed command, the read queries, the cache's creation and layout |
| **6b** | Acquisition (§4): the lock, reservations, staging, validation, journaled publication and recovery, size sampling. History refresh first; content acquisition with it |
| **6c** | `git.repos` in fleet.yaml and Settings > Git (§5): approval, URL mapping, sizes, "Fetch now", "Clear cache" |
| **6d** | The line with history (§6): the target T, the ordered states, "held · N behind", the Ledger's commits, "Open in Git", the fetch cadence |
| **6e** | The cache diff (§7), the parent's "6b": the in-process confined Kustomize build and the server-side dry-run diff |

Each is Codex-reviewed on its own, in this order.

## 2. Process containment (6a)

`internal/procrun` is extracted from the Claude CLI's containment and shared:
- `Run(ctx, Spec) Result`: a process group killed on deadline or cancel, `WaitDelay`, capped stdout and stderr with an overflow flag, stdin when given, an exit code.
- `Spec` carries the argv, the directory, the environment (never inherited) and the deadline.
- The Claude CLI moves onto it **in the same sub-slice, with its tests unchanged**, so the reviewed AI contract is visibly the same code path (§9, open point 1).

## 3. `internal/gitcli` (6a)

### 3.1 The binary

- Resolved through `clibin` with `KLYX_GIT_PATH`, `PATH`, then `/opt/homebrew/bin/git`, `/usr/local/bin/git`, `/usr/bin/git`.
- **The version gate:** `git version 2.56.0`, `git version 2.54.0 (Apple Git-157)` and Windows' `2.45.1.windows.1` parse; anything below 2.45, unparsable or missing turns every Git feature off with one note naming the reason. The §3.3 rules then hold everywhere.

### 3.2 The environment and isolated config

Exactly the parent's allowlist, built from scratch: `HOME`, `PATH`, `USER`, `LANG`/`LC_*`, `TMPDIR`, `SSH_AUTH_SOCK`; Klyx sets `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=<generated>`, `GIT_DIR`, `GIT_NO_LAZY_FETCH=1` (except an acquisition fetch), the pager and prompt variables, and `GIT_LITERAL_PATHSPECS=1`.
- **The generated global config** lives at `<config dir>/git-cache/gitconfig` (0600). It holds only `credential.helper` (§3.3) and `core.sshCommand=ssh -o BatchMode=yes`.
- **Every argv** carries the parent's `-c` flags, `--no-pager`, revisions after `--end-of-options` and paths after `--`.

### 3.3 The credential helper

The parent reads the owner's helper "once from their git config". That needs reads outside §7's table, run **once at startup** with the owner's own `HOME`, no isolated config, and a 10s deadline (revised after design review e48a108c: `--global` alone never sees a system helper such as `osxkeychain`):
- `git config --system --includes --get-all credential.helper`, then `git config --global --includes --get-all credential.helper`. An explicit scope never reads a repository's local config, whatever the working directory; `--includes` follows `include.*` as git itself does at run time.
- **Precedence, as git applies it:** system values first, then global (git's global scope covers `$XDG_CONFIG_HOME/git/config` and `~/.gitconfig`). An empty value clears every helper before it.
- Only the resulting helper values are kept, in order, and written to the generated config. A helper given as a shell snippet (`!...`) is refused, and Settings > Git says so.

### 3.4 Typed commands

- Each allowed command of parent §7 is one Go function that builds its own argv. No function takes a free-form argv, and no other command can be built.
- **Inputs are validated before they reach argv:** a sha is 40 or 64 hex; a path is repo-relative, with no `..` segment and no leading `/`; a ref follows §3.6.
- **Reads** (`log`, `rev-list --count`, `merge-base [--is-ancestor]`, `diff --name-status`) run against the published cache with `GIT_NO_LAZY_FETCH=1` and the 10s deadline.

### 3.6 Tracked refs (revised after design review e48a108c)

- **The source of a tracked ref** is the line's own key, never free text: the chosen key's ref, and the `line.source` override's, when that key's repo maps to an approved entry (§5.2). `git.repos` holds no refs.
- **What is fetched, by key ref kind:**
  - a Flux branch: `refs/heads/<name>`;
  - a Flux tag: `refs/tags/<name>`;
  - a Flux commit, or an Argo sha: that 40- or 64-hex object id;
  - an Argo ref only when fully qualified (`refs/heads/…`, `refs/tags/…`), taken as written.
  - Semver ranges, the default branch, `HEAD` and unqualified Argo refs are not fetched; they have no T (§6.1).
- **Validation, before any refspec is built:** the full ref must sit exactly in `refs/heads/` or `refs/tags/`, and its name must pass git's `check-ref-format` rules, implemented in Go: no `*`, `?`, `[`, `\`, `^`, `~`, `:`, space or control character; no `..`, `@{` or `//`; no component starting with `.` or ending in `.lock`; not ending in `/` or `.`; not `@`; no leading `-`.
- **The refspec** is then `+<full ref>:refs/klyx/staging/<full ref>` (a sha: `+<sha>:refs/klyx/staging/commit/<sha>`), one per tracked ref, never a pattern. A refused ref leaves that key with no T and says why.

### 3.5 The cache

- `<config dir>/git-cache/<sha256 of the approved URL>.git`, 0700, created with `init --bare --template=` and the parent's partial-clone config.
- A test proves a published cache passes `git fsck --connectivity-only` with its blobs missing, after a restart.

## 4. Acquisition (6b)

The parent's §7 contract, built as one `acquire` function with two callers (history refresh, content):
- **Lock:** one fleet-wide `sync.Mutex` plus a queue; a cancelled caller leaves the queue.
- **Reservation:** caps of 1 GiB per repo and 2 GiB in total, measured from the cache's own packs; a content acquisition is also capped at 64 MiB.
- **Validation:** `verify-pack -v` over the staging repo's own packs only; a history refresh that brings any blob is refused ("this server does not support partial clone"); a content acquisition that brings any id it did not ask for is refused.
- **Publication and recovery:** the parent's journal and recovery table, exactly. Recovery runs on start and before every acquisition.
- **Size sampling:** every 250ms; the fetch is killed past its reservation.
- **Scrubbing:** an error shown to the owner has URLs' userinfo and any `token=`-like query removed.

## 5. `git.repos` and Settings > Git (6c)

### 5.1 fleet.yaml

```yaml
git:
  repos:
    - url: git@github.com:acme/fleet.git   # executed exactly as approved
      web: https://github.com/acme/fleet   # optional: "Open in Git" for unknown hosts
      remoteBases: false                   # 6e: allow remote Kustomize bases from approved repos
```

- **The writer** generalises the AI block writer: `replaceTopLevelBlock(doc, key, block)` and `onlyKeyChanged(before, after, key)`, used by `SaveAI` and the new `SaveGit`, under the same lock and `replaceUnchanged`.
- **Nothing else ever adds to `git.repos`** (§14.5): only Settings > Git writes it.
- **No credential is ever stored** (revised after design review e48a108c; parent §7: "Klyx stores no tokens"). Approval and loading both refuse a URL that could carry one:
  - an `https` URL with any userinfo (`https://token@host/…`, `https://user:pass@host/…`), a query or a fragment;
  - an `ssh://` URL with a password (`ssh://user:pass@host/…`); an SSH user alone (`git@host:org/repo`, `ssh://git@host/…`) is allowed;
  - any other scheme (`file`, `http`, `git`, `ext`) - only `https` and `ssh` run (§7's protocol allowlist).
  - A refused entry already in fleet.yaml is ignored with a note naming it, never executed; the owner's credentials belong to the SSH agent or the credential helper.

### 5.2 Mapping cluster URLs

- A cluster's `spec.url` is only **matched**: `NormalizeRepo(spec.url) == NormalizeRepo(entry.url)` selects an approved entry. It is never executed.
- An SSH host alias in an approved URL does not normalise to the cluster's host; Settings > Git then offers the owner an explicit mapping ("this entry serves github.com/acme/fleet"), stored as `matches: [github.com/acme/fleet]` on the entry.

### 5.3 The panel

- **Each repo:** its URL as written, the cluster URLs mapped to it (per cluster), its size, its last fetch ("history as of 20:31", or "fetch failed: <scrubbed error>"), "Fetch now" and "Clear cache".
- **Unapproved:** every Git source URL read from the clusters that matches no entry, with "Approve…". The approval dialog shows the exact URL that will run, says it runs through the system git with the owner's SSH agent or credential helper, and that a helper that opens its own window is outside Klyx's control.
- **The binary line:** the git path and version, or the reason Git is off.

## 6. The line with history (6d)

### 6.1 Where it is computed

- **A `GitService`** owns the caches and the fetch cadence. It never blocks `LineService`'s worker.
- **After each LineDTO,** it computes, for the chosen key when that key is an approved Git repo:
  - **the target T:** the cache's tip of the tracked ref, for a Flux branch, tag or commit, or a fully qualified Argo ref or sha. Semver ranges, `HEAD`, the default branch and unqualified Argo refs have no T, and say why;
  - **per station in state 5** (one applied sha): at target, behind N, ahead N, diverged at <merge-base>, unrelated history, or not in fetched history;
  - **"Held · N behind"** with the parent's three evidence kinds;
  - **the Ledger's commits:** the union of `rev-list T ^<station sha>` over ordered stations, newest first, capped at 20, each with its short sha, subject (200 characters), author, time and the ordered stations that contain it. Commits only on a diverged or ahead station are listed under that station.
- **It publishes `line:git`** (a version, the LineDTO version it answers, T with its freshness, the per-station states, the Ledger commits). The frontend shows it only when its LineDTO version matches the board's, so an ordering never sits on another line's facts.

### 6.2 On the fleet screen

- **The verdict** speaks of T when every compared station is ordered: "prd · westeurope is held, 3 commits behind main"; "main at a1b9e3c · fetched 20:31" names T's freshness.
- **A cell** shows its ordered state in place of "matches/differs", with the reason when not ordered ("not in fetched history").
- **The Ledger** becomes "commits not yet everywhere", with dots for which ordered stations contain each commit, and "Open in Git" on known hosts (GitHub, GitLab, Bitbucket, Azure DevOps) or through `web`.
- **Untrusted text** (subjects, authors, paths) is rendered as plain text, capped, and never sent to an AI provider.

### 6.3 Fetch cadence

- The fleet screen calls `GitService.FetchDue()` when it mounts and every 5 minutes while mounted; "Fetch now" calls `Fetch(repo)`. The service fetches a repo only when its last attempt is at least 5 minutes old, unless asked now.
- A failed or capped fetch keeps the last good history, labelled "history as of <time>; fetch failed: <scrubbed error>".

## 7. The cache diff (6e)

The parent's contract, with the modules it needs:
- **New dependencies:** `sigs.k8s.io/kustomize/api` and `kyaml` for the in-process build, and `github.com/fluxcd/pkg/ssa` for the server-side dry-run diff `flux diff` itself uses.
- **The build:** the input set in rounds (at most 10, 5,000 files, 64 MiB), acquired as content, exported with `git archive`, loaded into an in-memory filesystem that refuses absolute paths, `..` and escaping links, and built with root-only load restrictions, no plugins, no exec functions, and a loader that refuses every remote URL (unless `remoteBases` is on for that repo, and then only from approved URLs through the same acquisition).
- **The diff:** server-side dry-run on the station's pinned connection, as `flux diff` does. SOPS files are diffed as stored and the result says so; post-build substitution is not applied and the result says "variables shown unsubstituted".
- **The UI:** the Flux inspector's "Compute diff" uses the cache when its repo is approved and fetched, and says which (cache or none) it used. The flux CLI path stays for unapproved repos.
- **Fixtures:** a generator reading `../outside.txt` fails, a nested base escaping the root fails, and a remote base is refused.

## 8. Testing

- **The transport in tests is the production one.** An `httptest` TLS server runs `git http-backend` over a fixture repo; the test's generated global config adds `http.sslCAInfo` for the test CA. File and plain-HTTP protocols stay forbidden. A fixture server with `uploadpack.allowFilter=false` proves a history refresh that receives blobs is refused.
- **6a:** the version parser (Homebrew, Apple, Windows, too old, garbage); the environment holds exactly the allowlist (a planted `GIT_EXEC_PATH`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `KUBECONFIG` and a cloud credential never reach git); every typed command's argv; validation refusing a short sha, `..`, an option-looking revision; deadlines kill the process group; output caps.
- **Refs (§3.6):** `*`, `refs/*`, `refs/heads/*`, `a:b`, `-x`, `main..dev`, `x@{1}`, `.hidden`, `x.lock`, `refs/remotes/origin/main` and a control character are refused; branch, tag, sha and fully qualified Argo refs build exactly one non-pattern refspec each.
- **The credential helper (§3.3):** with isolated system and global configs - a system-only `osxkeychain`; a global helper after a system one; an empty global value clearing the system helper; an `include.path` file honoured; a shell-snippet helper refused; a repository-local helper never read.
- **Credential-bearing URLs (§5.1):** each refused form is refused at approval and ignored at load, never executed.
- **6b:** every row of the parent's recovery table, by killing the process between journaled steps; a cap mid-fetch; cancellation in the queue; the content contract (only the listed ids, refusal above 5,000); `fsck --connectivity-only` after restart.
- **6c:** the writer changes only `git`; matching by normalised URL; an alias entry mapped explicitly; an unapproved URL never executed.
- **6d:** each ordered state and its reason; "held · N behind" for each evidence kind; the Ledger's union and its diverged/ahead listing; caps on untrusted text; a `line:git` for an older LineDTO is not shown.
- **6e:** the confinement fixtures; SOPS and substitution wording; a remote base refused, then allowed per repo.

## 9. Open points (ruled in design review e48a108c)

1. **`internal/procrun` extracted from the AI path (§2):** accepted, with the Claude CLI's tests kept.
2. **The credential helper (§3.3):** changed - system then global scopes with git's precedence, not `--global` alone.
3. **SSH aliases (§5.2)** mapped by an explicit owner-made `matches` entry: accepted.
4. **`line:git` as a separate event (§6.1)** fenced by the LineDTO version it answers: accepted.
5. **6e's three modules** (`kustomize/api`, `kyaml`, `fluxcd/pkg/ssa`): accepted.
6. **The HTTPS `git http-backend` fixture (§8):** accepted; the test CA needs no loosening of `protocol.allow`.

The review also added §3.6 (tracked refs: their source and exact validation) and the credential-bearing URL refusals in §5.1.
