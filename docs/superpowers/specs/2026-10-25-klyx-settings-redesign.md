# Klyx Settings: the redesign

Opened 2026-10-25, on `feat/settings-s0`, after slice 0 (`2026-10-25-klyx-settings-slice0.md`). The owner asked for considerable enhancements to the Settings page (2026-10-25):
- every setting described by its effect;
- AI roadmap decision 2: the user chooses the default model for unattended, opt-in proactive runs;
- decision 4: the user sets report retention;
- an Alertmanager design: detect it in the running cluster, or point at a specific one; a group of clusters may share one, and whichever cluster is viewed knows which member of its group hosts it.

This note is the design, for Codex's review before anything is built. It sequences the work in slices (§14), each built, reviewed and accepted in turn.

## 0. What exists

**From the review** (slice 0 §1; the full inventories are in the review reports):
- **Settings is one long page of seven sections:** Appearance, Fleet configuration, External endpoints, AI assist, Git history, Notifications, Kubeconfig contexts.
- **Credentials and approvals:** it edits the AI block, the notifications block and Git approvals, and stores, tests and removes endpoint credentials.
- **Clusters:** it adds clusters from kubeconfig contexts.
- **File-only:** cluster identity, placement, protection and endpoints; the Line's comparison source; the metadata index's budgets.
- **Silent or wrong** (slice 0 fixed these):
  - a fleet.yaml that did not load;
  - unknown keys;
  - symlinks;
  - hand edits under a notifications toggle;
  - a source-less Loki;
  - AI panel races and labels.
- **Missing:**
  - a shared observability source with its host member;
  - a description of each setting's effect;
  - file-versus-running state with an Apply model;
  - section navigation and search;
  - A2's settings.

**What slice 0 left in place, which this redesign builds on:**
- one strict decoder, with errors that withhold echoed values;
- one write policy for the fleet file: a symlink kept, mode kept, compare-and-swap on content, target and mode, a lock across Klyx's writers;
- each running cluster's drift from the file, said;
- the AI panel's draft generation and read sequencing.

## 1. Principles

1. **One sentence leads every section** (The Line §1.1): the effective state in words, before any control.
   - "Chat, Explain, Suggest-fix and investigations use Claude through api.anthropic.com. Proactive runs use qwen3 on this machine."
   - "Viewing aks-we-dev: alerts come from lgtm-we, hosted by aks-we-prd, filtered by cluster=aks-we-dev."
2. **No setting without its effect.** Every control carries, always visible and never on hover alone, the field contract of §3.
3. **Effective, with its source.** A value shows where it comes from: the file, a default, inferred (protection from a prd token), discovered (an Alertmanager found in the cluster), or a group's source.
4. **File and running are kept apart.** Each value says when it takes effect: now, on Apply, at reconnect, or at restart. A difference between the file and what runs is said (slice 0's drift, extended in §4).
5. **Danger keeps its shape.** Loosening protection, granting protected or unattended egress, and removing a cluster take the ▲ and the typed interlock. Tightening never does.
6. **Writes are Klyx's own five, and now more,** each through slice 0's write policy, each touching only its block or its cluster's entry. Comments inside a rewritten block are not kept, and Settings says so where it writes.
7. **The fences hold.** A change that alters what a running read or send may reach ends the old one through the existing fences (connection, settings revision, A-C2). Settings never makes a running AI turn or a verb continue under changed terms.

## 2. Structure and navigation

**The section rail** takes the lens index's slot: 232px, collapsible with mod+\, the same component and keys. The sections, in order:

| Section | Holds |
|---|---|
| **Fleet** | clusters (identity, placement, protection, connection), adding and removing them, kubeconfig contexts |
| **Observability** | metrics, Alertmanager and Loki sources, their hosts and member scopes, credentials |
| **AI** | providers and models (profiles), interactive use, proactive use (A2), protected-cluster egress, investigations |
| **Git** | approvals, aliases, web links, cache, the git binary |
| **Notifications** | OS permission, fault scope and recoveries, report notifications (A2) |
| **Appearance** | theme, zoom, layout resets, shortcuts |
| **Data and storage** | what Klyx keeps on disk and in the keychain, its size and lifetime, export and delete |
| **Diagnostics** | the fleet file's path, load state and drift; tool paths and versions; what the launch environment changes |

**Addressing:**
- The Settings route takes a section and an optional anchor: `settings/ai/proactive`, `settings/fleet/aks-we-prd`.
- Every link that today opens Settings at the top names its section ("Set up AI" goes to `settings/ai`).
- ⌘, opens Settings, and the history arrows work inside it.

**Search:** a field at the top of the rail filters every setting by its label and its effect. The command console lists "Settings: <section>" and "Settings: <setting>", from the same registry (§3).

**Saving:**
- Each section has one Save (or Apply) and a Revert for its draft, and says "unsaved", "saved", "applies at restart" or "a newer read failed".
- Leaving a section with unsaved changes asks to save or discard them.
- Immediate actions say they are immediate: a credential stored or removed, a theme chosen, a cache cleared.

## 3. The field contract

Every setting is described once, in a registry that the page, search and the console read:

| Part | Says |
|---|---|
| **Label** | what it is, in the product's vocabulary |
| **Effect** | what changing it does, in one or two sentences, including what it does not do |
| **Scope** | the fleet, a group, a cluster, a source, a profile |
| **Default** | the value when nothing is set, and why |
| **Effective** | the value in use, with its source (§1.3) |
| **Takes effect** | now, on Apply, at reconnect, at restart |
| **Test** | what Test checks and sends, when there is one |
| **Save** | what Save writes, and where |

**The registry** (`settings/registry.ts`) holds the static parts. The backend supplies the effective value and its source per setting, so the frontend never infers them. A setting with no registry entry is a test failure: the page cannot render an undescribed control.

**Fixed bounds are said, not offered.** Chat and investigation budgets, the Git cache caps and the rows and alerts bounds are safety limits. Diagnostics lists them, with their values, and nothing raises them.

## 4. File and running: the Apply model

Slice 0 says each cluster's drift. The redesign makes each section able to act on it:

| What changed | How it applies |
|---|---|
| AI, Git, notifications blocks | **now**, as today: Save writes, then the service rebuilds |
| A cluster's group, environment, tags | **now:** written, then the line rescans and summaries relabel |
| A cluster's protection, tightened | **now**, without confirmation |
| A cluster's protection, loosened (explicitly, or by an environment or name change that drops a prd token) | **now, after the typed interlock** naming the cluster |
| A cluster's context or kubeconfig | **at reconnect:** "Save and reconnect" ends that cluster's streams, forwards, AI turns and fences first, and says so in the dialog |
| An observability source, or a member's scope | **now:** the source's snapshots re-key and old pages end (S8a's re-key) |
| AI profiles, egress grants | **now**, through the existing settings revision, which ends running turns |
| Proactive (A2) consent, triggers, limits | **now:** queued work is re-checked; revoked work stops before its next read or send |

**A hand edit is never applied silently.** When the file differs from what runs, the section says what differs. It offers "Use the file's values", which applies them through the same path and confirmations as a Settings edit: a hand edit that loosens protection still takes the typed interlock. A cluster's name and the fleet file's location stay restart-only, and are said as such.

## 5. Fleet

**The section's sentence:** "12 clusters running, 4 protected; fleet.yaml agrees", or names what differs.

**Clusters** are a real table (header semantics, 40px rows): name, context, region, group, environment, protection and connection. Selecting one opens its editor:
- **name:** read-only, and the field says why. The keychain's endpoint credentials, the view preferences and saved state are keyed by it, so a rename needs their migration: a later slice (§14).
- **context and kubeconfig:** "Save and reconnect" (§4).
- **region, group, environment, tags:**
  - region and group place the cluster on the line (region first, then group);
  - environment ranks the station and feeds protection's inference;
  - each field says so.
- **protection:** explicit, inferred (with the token that infers it), or none. The effect is said: typed confirmation before destructive verbs and AI egress, which is a guard against mistakes, not authorization (RBAC still decides). Loosening takes the interlock.
- **endpoints:** links to Observability, filtered to this cluster.

**Adding:** kubeconfig contexts, as today, now inside Fleet.

**Removing a cluster:**
- it disconnects first;
- it offers to delete the cluster's keychain entries;
- it refuses while an observability source names the cluster as its host, saying which;
- it takes the interlock on a protected cluster.

**Writes:** a cluster's entry is edited as a YAML node: the changed fields only, other fields and comments kept, through slice 0's write policy. An entry Klyx cannot edit node-wise (an alias, a flow mapping) is refused with what to change by hand.

## 6. Observability sources

This is the owner's Alertmanager design (§0). It applies to Loki and metrics too: the owner's LGTM stack is one Mimir and one Loki for many clusters, with a tenant per cluster.

**A source** is a record, written once:

```yaml
sources:
  - id: lgtm-we
    kind: alertmanager           # alertmanager | loki | metrics
    name: Shared Alertmanager, West Europe
    host: aks-we-prd             # reached through this cluster's API server:
    serviceRef: {namespace: monitoring, name: alertmanager-main, port: web}
    # or discover: true (Alertmanager and metrics), or endpoint: https://...
    tenant: ops                  # X-Scope-OrgID, when the backend is multi-tenant
    memberLabel: cluster         # the label whose value names the member
    serves: [{group: we}]        # groups, or clusters by name
```

**A cluster's source**, for each kind, is resolved in this order:
1. its own block, as today: `alertmanager: {endpoint | serviceRef}`, now also `{source: <id>}` or `{off: true}`;
2. a source serving it by name;
3. a source serving its group;
4. discovery in itself (Alertmanager and metrics; Loki is never discovered);
5. none, said as such.

Two sources serving the same cluster for one kind at the same step are a load error naming both.

**A member's scope:**
- By default, its alerts, streams or series are those whose `memberLabel` equals the member's name.
- A member may override the value (`match: {cluster: stg-we}`), or the whole match.
- A source serving two or more members without a `memberLabel` is a load error. Attribution to a member is never assumed, which is the review's shared-source finding made impossible to configure.

**What each cluster says:**
- The Alerts lens header, the inspector's evidence and Observability's row read: "from lgtm-we, hosted by aks-we-prd, tenant ops, filtered by cluster=aks-we-dev".
- When the host is disconnected, members read "lgtm-we's host aks-we-prd is not connected", never "no alerts".

**Reading:**
- One read per source per interval, through the host's pinned connection (or the endpoint), with the members' values as the Alertmanager filter, split by member before any lens, count or AI use sees it.
- The fleet-wide and per-source bounds of S8a apply.
- A row whose `memberLabel` matches no member is counted, and shown on the source as "not attributed to a member", never added to a member.

**Detect, per group or cluster:**
- Klyx runs the existing discovery, with its proof of one installation, in each member.
- For each installation found, it reads a bounded sample of label names and values, never alert bodies, and proposes a source:
  - the host;
  - the Service;
  - the `memberLabel` whose values match members' names;
  - which members are present, and which are missing.
- The person reviews the proposal, edits it, and saves it. Several installations are listed with what each holds; none found offers an endpoint URL.
- For Loki, Detect suggests a Service by its labels in a member, as a suggestion only: it never claims a Loki is usable until Test has read its labels.

**Credentials** belong to the source (`source/<id>`), bound to its origin as C5's are. A host's Service proxy uses the host's Kubernetes authentication and no external credential.

**The fences:**
- A read for member b through host a checks a's pinned connection as well as b's subject: A-C2 gains the host as a component of the read.
- Provenance keeps both identities, the source revision, its tenant and match.
- Data a source holds for other members is never sent for b. The split happens before any AI read, and an A2 consent for b covers b's share only.

**Migration:**
- Today's per-cluster blocks keep working unchanged.
- When Settings sees one direct URL on several clusters, or a shared-instance warning, it offers "Make a shared source": a proposal, reviewed and saved by the person, never applied on its own. Secrets are never copied between accounts; the source's credential is stored once, by the person.

## 7. AI

**The section's sentence** names the interactive destination, the proactive destination and whether data leaves the machine.

**Profiles** replace the single provider block:

```yaml
ai:
  profiles:
    - {id: claude, provider: anthropic, model: claude-opus-5-5, keySource: keychain}
    - {id: local, provider: openaiCompatible, baseUrl: http://localhost:11434, model: qwen3}
  interactive: claude              # chat, Explain, Suggest-fix, investigations
  proactive: local                 # A2's default (owner decision 2)
  protectedEgress:                 # bound to a destination (finding 9)
    - {profile: claude, origin: https://api.anthropic.com, grantedAt: 2026-10-25T10:00:00Z}
  proposeActions: true
```

- **A profile** carries today's fields: provider, model, origin, key source, CLI path, TLS and refusal fallback. Its key is in the keychain under `ai/profile/<id>`.
- **Protected-cluster egress** is granted per profile and origin, with the interlock. Changing a profile's provider or origin ends its grant, and Settings says so before Save. This replaces the global flag the review found following any destination.
- **Investigations:** proposing actions, as A3 built it, with what turning it off does to offered plans.
- **Fixed bounds** (budgets, slots, sizes) are listed, not offered (§3).

**Migration:**
- The current block becomes profile `default`, with its key still read from `ai/<provider>`; `interactive: default`.
- An existing `allowProtectedEgress: true` becomes a grant bound to the current origin (owner decision §16.2).

## 8. Proactive (A2)

The owner's decisions 2 and 4, with the roadmap's §3 consent model. A2's own spec builds the runs; this section is what Settings holds:
- **Master switch:** off by default. It shows the running and queued counts, and its effect: turning it off stops queued work and fences running work at its next read or send. It cannot recall a request already sent.
- **Default model:** a profile (§7), local or a provider. The effect states the exact destination and whether data leaves the machine. There is no automatic fallback to another profile.
- **Consent** is per cluster and per trigger, off by default, recorded with the profile, its origin, the egress policy's version and the time:
  - a protected cluster needs its own consent, with the interlock;
  - a change that widens a consent's terms (another profile or origin, a local-only cluster sent remotely, a policy change) pauses the affected work as "needs consent again", said, never skipped silently.
- **Triggers,** each described with an example:
  - a new fault episode;
  - a degraded episode;
  - a critical alert firing;
  - a GitOps revision change followed by a failure.

  Notifications' opt-in is never consent: they are separate settings.
- **Limits:** runs per cluster per hour and per day, a fleet-wide daily cap, concurrency, queue size and expiry, and model calls per run (at most the investigation budget). Each shows what is used and what remains; a held run says which limit held it.
- **Retention** (owner decision 4):
  - reports kept by count and by age, with a hard size bound;
  - current usage, and what lowering a limit would delete, confirmed before it does;
  - delete one, delete all.
- **The egress audit:** what each run sent, as categories and identities, never bodies. It is stored with the reports, kept as long as they are, and deleted with them.

## 9. Git, Notifications, Appearance

- **Git:** as today, plus each field's contract.
  - `remoteBases` is not offered: the cache diff refuses remote bases (the review's finding), and offering the toggle would promise what Klyx does not do. Settings says so where it matters.
  - Cache usage shows the fixed caps.
- **Notifications:**
  - as today, plus the OS permission's state (allowed, denied, not asked) and what each scope covers;
  - A2's report notifications are separate, and never imply consent.
- **Appearance:**
  - theme;
  - zoom, now visible with a reset;
  - layout resets: inspector width, logs dock, lens index;
  - the shortcut help.

## 10. Data and storage

One list of what Klyx keeps, where, how big and for how long, with its action:

| Item | Where | Action |
|---|---|---|
| fleet.yaml | `~/.config/klyx` or `KLYX_CONFIG` | open the folder, validate |
| window state, Claude Code builds | beside fleet.yaml | reset |
| Git cache | beside fleet.yaml | per repository and all: clear (existing) |
| A2 reports and their audit | beside fleet.yaml, private | delete one or all, retention (§8) |
| keychain entries | the OS keychain, `io.moomora.klyx` | list by account (never values), remove |
| view preferences, Activity, palette ranking | the webview's storage | reset each |

Nothing is exported with a secret in it. A reset says what it resets before it does.

## 11. Diagnostics

- **The fleet file:**
  - its path, and whether `KLYX_CONFIG` set it;
  - its load state;
  - the warnings;
  - the drift (§4).
- **Tools:** the git, kubectl, helm and flux binaries in use, their versions, and any `KLYX_*_PATH` override.
- **The launch environment:**
  - the names (never values) of the proxy variables in use;
  - a note that a GUI launch may not see shell exports, which matters for a key read from the environment.
- **The fixed bounds** (§3).

## 12. Design (The Line)

- **Intent:** a platform engineer changing what Klyx does across a fleet, often under production protection. The page should feel like a configuration console: dense, exact, quiet. Every change says its blast radius before it happens.
- **Hierarchy:**
  - each section's sentence leads, at the lead size (15) and weight 600;
  - the fields follow in dense rows: 13 for labels and values, 12 for effects;
  - the effect is under each field, in secondary ink, never in a tooltip.
- **Surfaces:** The Line's ground, raised and sunk, with hairlines; inputs sunk. No new tokens: the route blue marks the selected section and the primary action, amber marks a difference between the file and what runs, and danger keeps ▲.
- **Signature:** the scope sentence (§1.1) with its source chips. On an observability source it reads as a small route: the member, then its host, then its scope, drawn as a typed relation with its legend (The Line §1.6). That is where "which member hosts the Alertmanager" becomes visible.
- **Space:** 4px base, 40px rows, 36px controls, 24px panel padding, 32px between sections.
- **Accessibility:**
  - labels associated with controls, effects through `aria-describedby`;
  - errors announced, and placed where the field is;
  - every explanation reachable by keyboard;
  - the rail and search fully keyboard-operable.

## 13. Security and honesty

- No secret is returned to the page, written to a file, or shown in an error (slice 0's redaction).
- Every new block decodes strictly. An unknown key in `sources`, `ai.profiles` or `proactive` is a load error with its place.
- Consent and grants are records with their terms, never booleans that follow a later choice.
- A host member, a profile's origin or a consent's terms changing ends the reads and sends that relied on them, through the fences (§1.7).
- Nothing in Settings runs a model or sends cluster data. Test sends what it says, and only what it says.

## 14. Slices

| Slice | Builds | Depends on |
|---|---|---|
| **S1** | the rail, addressing, ⌘,, search, the registry and every existing setting's contract, Diagnostics' read-only parts | slice 0 |
| **S2** | Fleet: the cluster editor, protection semantics and the interlock, reconnect, remove, "Use the file's values" | S1 |
| **S3a** | Observability sources for Alertmanager: the schema, resolution, the source read and split, the scope sentence, Detect, the migration offer | S1 |
| **S3b** | Loki and metrics on the same model | S3a |
| **S4** | AI profiles, destination-bound protected egress, the migration | S1 |
| **S5** | Proactive settings, with A2's own spec | S4, A2 |
| **S6** | Data and storage | S1 |
| **later** | cluster rename with its migration | S2 |

Each slice is reviewed by Codex before and after it is built, and its as-built note lives in its own spec.

## 15. Tests

- Each slice's own, plus a registry test: every control has an entry, every entry is rendered.
- A strict-decoding test for each new block.
- A write-policy test for each new writer (symlink, mode, compare-and-swap).
- A fence test for each change that ends running work.
- The native smoke gains a Settings walk: every section opens by its address, search finds a setting by its effect, and an Observability source's sentence names its host.

## 16. Owner decisions

1. **Settings edits cluster entries** (§5): context, kubeconfig, placement and protection, but not the name. Settings has never written the clusters list beyond appending. Proposed: yes.
2. **An existing protected-egress grant migrates** bound to the current origin, without asking again (§7). The alternative is asking again on upgrade. Proposed: migrate, and say so once.
3. **A source serves a group** (`serves: [{group: ...}]`), as the owner described a group sharing one Alertmanager (§6). Proposed: yes, with per-cluster overrides.
4. **A2's default limits and retention:** two runs per cluster per hour, six per day, twenty fleet-wide per day, one at a time, a queue of four; a hundred reports, thirty days, 64 MiB. Proposed as starting values, all user-set.

## 17. Not in this redesign

- Writing a cluster's name (a later slice).
- Raising a fixed safety bound.
- Exporting configuration with secrets.
- Anything that runs a model from Settings beyond Test.
- Remote bases for the cache diff.
