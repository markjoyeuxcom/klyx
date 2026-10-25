# Klyx Settings slice 0: correctness before the redesign

Opened 2026-10-25, on `feat/settings-s0` (from `feat/ai-a3-lab`, which also changes Settings). The owner asked for a review of the Settings page and its coverage (2026-10-25), alongside AI roadmap decisions 2 and 4 (§4). Slice 0 fixes what the review found wrong; the redesign follows in its own spec, reviewed before it is built.

## 1. The review

Claude and Codex reviewed Settings independently at `feat/ai-a3-lab` 6639a55, then cross-checked each other's findings. The reports are in the scratchpad, untracked: `claude-settings-review.md`, `codex-settings-review.md`, `settings-review-crosscheck.md` and `codex-settings-crosscheck.md`. Neither found a P1. In summary:

- **What Settings is today:** a credential and approval console with a fleet-file inventory, rather than a configuration editor. Cluster identity, placement, protection and endpoints can be set only in the file; so can the Line's source and the metadata index's budgets.
- **What was wrong** (fixed here, §2):
  - A fleet.yaml that did not load started Klyx with no clusters, saying so only in the log.
  - Unknown keys were dropped silently.
  - The writers replaced a symlinked file.
  - The notifications save overwrote hand edits.
  - A source-less Loki read "discovered".
  - Several AI panel races and labels were wrong.
- **What is missing** (the redesign, §3): a shared observability source with its host member, a description of every setting's effect, file-versus-running state with an Apply model, section navigation, and A2's settings.
- **`git.repos[].remoteBases` does nothing.** The cache diff refuses remote bases unconditionally, so it must not be offered as a working permission.

## 2. Slice 0 as built

**The fleet file:**
1. **An existing fleet.yaml that does not load is said.** `main` keeps the load error (a missing file is still a first run), and `ConfigService` carries it:
   - Settings opens with a danger banner (the error, then "fix the file in an editor, then restart Klyx: Settings cannot repair it");
   - adding clusters is refused until the file loads;
   - the fleet's empty state names the error.
2. **Unknown keys are refused.** One decoder (`config.decode`) serves both `Load` and a snapshot:
   - An unknown key below the top level is a load error naming its place, its line and, within two edits, the key it likely means: `clusters[0] (a).alertmanager: "matches" at line 5 (did you mean "match"?)`.
   - An unknown top-level key is a warning.
   - `tags` and `match` take any key; the legacy spellings still load; merge keys are checked against the struct they merge into.
3. **An error is shown without a pasted value.** A long, unbroken value echoed by a YAML type error or a validation error's `(got "...")` is withheld; the path, key names and short values stay.
4. **The writers keep a symlink and the file's mode.** All five fleet-file writers (add clusters, the AI, Git and notifications blocks, the metrics-token move) go through one policy:
   - the file a symlink names is the one replaced, beside itself, keeping its permissions;
   - a new file is created 0600, and the token move keeps its temporary file 0600 while the token is in it;
   - a link that does not resolve, or that is repointed during the write, is refused.
5. **The notifications save never overwrites a hand edit.** The save carries the block Klyx last read or wrote, and refuses a file whose block has changed since.

**Disk and running state (the cross-check's minimum):**
1. **The Fleet rows are the running clusters, and each says how fleet.yaml now differs:** protection (in which direction), context, kubeconfig, group, environment and tags, all applied at a restart.
   - Clusters in the file but not running are listed.
   - A file that does not load now is said, while the fleet keeps running.
   - Endpoints are not compared: External endpoints reads them from the file, and says that Test or a credential action adopts an edited one.
   - "Re-scan" is now "Re-scan kubeconfig contexts", and the sidebar badge never reads the file.

**Endpoints:**
1. **A row shows when the certificate is not verified, and the endpoint's scope:** its match, or "no match: everything it holds is read as this cluster's".
2. **A Service's scheme and path are shown.**
3. **A Loki block with neither a URL nor a Service** says Klyx does not discover Loki, warns at load, and is not advertised as log history. An Alertmanager's match-only block is discovery, unchanged.

**The AI panel:**
1. **A typed key is cleared when the provider, origin or key source changes:** it never moves to another provider.
2. **A save that returns after a newer edit keeps that edit,** and it shows as unsaved.
3. **A failed reread after a change says so, with Retry,** and "In use" reads "In use, as last read". The same holds in External endpoints and Git.
4. **The TLS option is described as what it does:** it skips certificate verification for any host, and "certificate not verified" shows beside the destination in use.
5. **Protected-cluster egress is described truthfully:** one permission for chat, Explain, Suggest-fix and investigations, which follows whichever provider and origin are in use. It is not A2's consent.
6. **Every AI text names all four uses,** and "this Mac" is now "this machine".

**The wording:** the five places Klyx writes the file, with comments inside a rewritten block not kept; the Git removal's wording.

**A behaviour change to note:** a fleet.yaml with an unknown nested key that loaded before now does not, and Klyx says which key. The file was being misread; now it is refused, with the fix named.

**Tests:**
- Go:
  - unknown keys (nine cases);
  - the snapshot decode;
  - the redaction and a missing file;
  - every writer's symlink and mode;
  - a broken and a repointed link;
  - the load error and the add refusal;
  - drift, file-only clusters and a file that does not load now;
  - the endpoint rows;
  - the notifications hand edit at the writer and the service;
  - a source-less Loki's capability.
- Frontend:
  - a typed key never moves;
  - a pending save keeps a newer edit;
  - a failed reread qualifies what is in use;
  - TLS in use;
  - the endpoint rows and their reread;
  - Git's reread;
  - the Settings banners and drift;
  - the fleet's empty state.

**Gates:** `go test -race ./internal/...`, vet, golangci-lint (with the eval tag too), `tsc`, eslint, and 2481 frontend tests. One run hit the known `internal/observe` rekey flake, which passes on rerun; this slice does not touch that package. A mutation pass caught 43 of 44 mutants, once four were rewritten to compile and a test was added for the metrics-token move's repointed link. The 44th was equivalent: a check for a missing file that the read after it already made, now removed.

### 2.1 Codex's review and the fixes (2026-10-25)

Codex requested changes: six P2, all residual cases of the slice's own fixes. It judged refusing unknown nested keys right. Its check of 90 configurations in the repository that loaded before found no unexpected rejection: the seven now refused are this slice's own negative tests. The release notes must call the refusal out. Each finding is addressed:
1. **An edit made during Save's readback survives it.** Only the latest read's reply lands, and it replaces the draft only when nothing was edited while it was on its way.
2. **Every use of an alias is checked against the schema it is used in.** An alias read as a free map, or as another block, certified nothing about a cluster it was merged into; the walk now stops only at a cycle.
3. **Only a plain `<<` merges.** A quoted `'<<'` is a string key the decoder ignores, so it is now an unknown field rather than a merge the validator trusted.
4. **A permission change during a write is refused** like a content change, for every writer; the metrics-token move already refused one.
5. **A long echoed value is withheld spaces and all,** so `Bearer <key>` pasted into an enum field never reaches the screen. Decoding errors are redacted where they are made, so every consumer gets them redacted: Settings, the panels, a writer's refusal and the startup log. A key of 25 or more characters, which no field name is, is named by its length.
6. **A fleet.yaml symlink whose target is gone is an error,** said in Settings and on the empty fleet. Only an absent path is a first run.

**Tests:** Codex's six probes as regressions, and two more found by the mutation pass's reasoning: an older readback landing after a newer one, and a YAML type error echoing a pasted key (yaml.v3 shortens such values itself; Klyx's redaction there is a second guard).

**Gates:** as before, with 2483 frontend tests. A mutation pass over the fixes caught 10 of 10. The native smoke passed 210/210 four ways on 829c55e from a clean tree, the slice before these fixes; the fixes are covered by the tests above.

### 2.2 Codex's re-review (2026-10-25)

Codex found the six fixes sound, with one residual P2. A long pasted key was described by its length only on the nested unknown-field path; three other diagnostics still named it in full:
- the top-level warning;
- an invalid `match` label;
- YAML's own duplicate-key error.

Every diagnostic that names a key the file holds now uses one rule (`describeKey`): quoted, or, at 25 characters or more (longer than any field or label Klyx reads), by its length. YAML's duplicate-key error is rewritten the same way. A regression covers all three, in Load's and ReadSnapshot's strings and in Settings' serialized payload, with a short key still named. A mutation pass caught 4 of 4.

## 3. Next: the redesign spec

A spec for the redesign, reviewed by Codex before it is built:
- section navigation, deep links, search and ⌘,;
- a field contract for every setting: what it changes, its scope, its default, its effective value and source, when it takes effect, what Test and Save do;
- file-versus-running state, with an Apply model;
- cluster identity and placement editing;
- Observability sources: a source, its host member and each member's scope, for Alertmanager, Loki and metrics;
- AI profiles for interactive and proactive use;
- A2's consent, limits, retention and audit;
- a Data and storage section.

## 4. The owner's decisions (2026-10-25)

- **AI roadmap decision 2:** unattended egress for A2 is allowed and opt-in. In Settings the user chooses which model is the default for proactive runs (local or a provider).
- **Decision 4:** report retention (how many reports, for how long) is set by the user in Settings.
- **Every setting carries a description of its effect.**
- **Alertmanager:** detect it in the running cluster, or point at a specific one. In a fleet, a group of clusters may share one, and whichever cluster is viewed knows which member of its group hosts it.
