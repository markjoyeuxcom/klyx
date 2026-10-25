# Klyx Settings: the redesign

Opened 2026-10-25, on `feat/settings-s0`, after slice 0 (`2026-10-25-klyx-settings-slice0.md`, accepted at 2a985ad); revisions 1 and 2 the same day, after Codex's review and re-review (§18, §19); the design is accepted (§20). The owner asked for considerable enhancements to the Settings page (2026-10-25):
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
- **File-only:** cluster identity, placement, protection and endpoints; the Line's comparison source; the metadata index's kinds and budgets.
- **Missing:**
  - a shared observability source with its host member;
  - a description of each setting's effect;
  - file-versus-running state with an Apply model;
  - section navigation and search;
  - A2's settings.

**What slice 0 left in place, which this redesign builds on:**
- one strict decoder, and the fleet file's YAML errors through an allowlist;
- one write policy for the fleet file: a symlink kept, mode kept, compare-and-swap on content, target and mode, a lock across Klyx's writers;
- each running cluster's drift from the file, said;
- the AI panel's draft generation and read sequencing.

**What the running app enforces today, which Apply must respect** (Codex's review, R1):
- `RunStep` consumes protection when it starts; its run fence then checks only the pinned connection and its usability, so a protection change alone does not stop a step already past its guard.
- `ClusterConfig.IsProtected` reads the `protected` flag, the environment, `tags.env`, the name and the context.
- AI settings saves compare a proposed egress grant with the one on disk, not with the one in use.

## 1. Principles

1. **One sentence leads every section** (The Line §1.1): the effective state in words, before any control.
   - "Chat, Explain, Suggest-fix and investigations use Claude through api.anthropic.com. Proactive runs use qwen3 on this machine."
   - "Viewing aks-we-dev: alerts come from lgtm-we, hosted by aks-we-prd, tenant ops, filtered by cluster=aks-we-dev."
2. **No setting without its effect.** Every control carries, always visible and never on hover alone, the field contract of §3.
3. **Effective, with its source.** A value shows where it comes from: the file, a default, inferred (protection from a prd token), discovered (an Alertmanager found in the cluster), or a group's source.
4. **File and running are kept apart.** Each value says when it takes effect, and a difference between the file and what runs is said. Apply always plans from what runs (§4).
5. **Danger keeps its shape.** Loosening protection, granting protected or unattended egress, and removing a cluster take the ▲ and the typed interlock. Tightening never does.
6. **Writes are Klyx's own,** each through slice 0's write policy, each touching only its block or its cluster's entry. Comments inside a rewritten block are not kept, and Settings says so where it writes.
7. **Every change has an epoch the fences check.** A change that alters what a running read, verb or send may do advances a revision for exactly what it changed:
   - a cluster's policy epoch: protection, connection;
   - a source's or subscription's revision;
   - a profile's grant terms;
   - a consent's terms.

   The existing fences check that revision before the next read, write or send. Settings never lets work continue under terms it did not start under.

## 2. Structure and navigation

**The section rail** takes the lens index's slot: 232px, collapsible with mod+\, the same component and keys. The sections, in order:

| Section | Holds |
|---|---|
| **Fleet** | clusters (identity, placement, protection, connection), adding and removing them, kubeconfig contexts, the line's comparison source |
| **Observability** | metrics, Alertmanager and Loki installations, the clusters subscribed to them, credentials |
| **AI** | profiles, interactive use, proactive use (A2), protected-cluster egress, local-only clusters, investigations |
| **Git** | approvals, aliases, web links, cache, the git binary |
| **Notifications** | OS permission, fault scope and recoveries, report notifications (A2) |
| **Appearance** | theme, zoom, layout resets, shortcuts |
| **Data and storage** | what Klyx keeps on disk and in the keychain, its size and lifetime, the search index, delete |
| **Diagnostics** | the fleet file's path, load state and drift; tool paths and versions; the launch environment; the fixed bounds |

**Addressing:**
- The Settings route takes a section and an optional anchor: `settings/ai/proactive`, `settings/fleet/aks-we-prd`.
- Every link that today opens Settings at the top names its section ("Set up AI" goes to `settings/ai`).
- ⌘, opens Settings, and the history arrows work inside it.

**Search:** a field at the top of the rail filters every setting by its label and its effect. The command console lists "Settings: <section>" and "Settings: <setting>", from the same registry (§3).

**Saving:**
- Each section has one Save or Apply and a Revert for its draft, and says "unsaved", "saved", "applied", "saved, not active", "applies at restart" or "a newer read failed".
- Revert discards the draft; it never undoes an applied change, which takes a new Apply (§4).
- Leaving a section with unsaved changes asks to save or discard them.
- Immediate actions say they are immediate: a credential stored or removed, a theme chosen, a cache cleared.

## 3. The field contract

Every setting is described once, in a registry that the page, search and the console read:

| Part | Says |
|---|---|
| **Label** | what it is, in the product's vocabulary |
| **Effect** | what changing it does, in one or two sentences, including what it does not do |
| **Scope** | the fleet, a group, a cluster, an installation, a subscription, a profile |
| **Default** | the value when nothing is set, and why |
| **Validation** | allowed values, units, and what zero, empty and unset mean (`index.kinds: []` indexes nothing; a zero budget means the default) |
| **Effective** | the value in use, with its source (§1.3) |
| **Takes effect** | now, on Apply, at reconnect, at restart |
| **Test** | what Test checks and sends, when there is one |
| **Save** | what Save writes, and where |

**The registry** (`settings/registry.ts`) holds the static parts. The backend supplies each setting's effective value and source, so the frontend never infers them. The registry never holds a secret: a credential field exposes only stored, missing or stored for another origin.

**Coverage starts from the schema, not from the page** (R6). Every field of the configuration schema, every environment variable Klyx reads and every local store in the inventories has a disposition:

| Disposition | Means |
|---|---|
| **editable** | a control in its section, with its contract |
| **effective, file-only** | shown with its effective value and contract; changed in the file |
| **deferred** | named in its section as "not yet editable", with the slice that brings it |
| **fixed** | a safety ceiling, listed in Diagnostics with its value, never offered |

A test derives the list from the schema (by reflection over `config.Config`) and the inventories, and fails on any item with no disposition. A second test fails on any rendered control with no registry entry. The dispositions at the start:
- **`line.source`:** effective and file-only in Fleet ("the line's comparison source"), with what an override that matches no station does; editable later.
- **The search index's kinds and budgets:** effective and file-only in Data and storage, with their zero and empty semantics; editable in S6.
- **`git.repos[].remoteBases`:** deferred, said as not supported (the cache diff refuses remote bases).

**Configured limits and fixed ceilings are told apart.** The index budgets are configured limits. Chat and investigation budgets, the Git cache caps, and the rows and alerts bounds are fixed safety ceilings: listed, never raised.

## 4. File and running: the Apply model

Every change that affects running work, whether made in Settings or offered from a hand edit ("Use the file's values"), is applied as a plan:
1. **Plan from what runs:** Settings asks the backend for an activation plan from the active runtime state to the validated candidate.
   - It is never planned from the file to the file.
   - It lists each effective change with its inputs. A cluster's protection is derived from its `protected` flag, environment, `tags.env`, name and context, never from a field-name list.
   - It names what the change ends: forwards, streams, AI turns, offered steps, armed grants.
2. **Confirm the plan, not the form:** a confirmation, and an interlock where §1.5 requires one, binds to the plan's digest, the reviewed file version and the runtime revisions it was planned from. If any of them changes while the dialog is open, the confirmation is refused, and Settings plans again.
3. **Apply in order:**
   - write the file, through the write policy;
   - advance the affected epochs (§1.7);
   - invalidate outstanding one-shot arms and step reviews for the affected clusters;
   - activate: rebuild a service, or reconnect a cluster.
4. **When activation fails:** Settings says both states ("saved; not active: <reason>") and offers Retry and Revert. The affected work stays fenced, because its epoch moved, until activation succeeds or the change is reverted. It never resumes silently under either the old or the new terms.

**The policy epoch** is per cluster. It moves when protection, the connection or a source subscription that feeds the cluster changes.
- **The ProtectGuard:** it records the epoch when a grant is armed, and refuses if it has moved.
- **Each verb's run fence:** it checks the epoch before the write, not only the connection. A step that passed its guard while the cluster was unprotected is refused at its write if protection has tightened since.
- **AI fences:** they check it with the settings revision.

| What changed | How it applies |
|---|---|
| AI, Git, notifications blocks | on Apply: the plan compares with what runs, so "Use the file's values" turning an egress grant on takes the interlock even though the file already says so |
| A cluster's group, region, tags that do not affect protection | on Apply, without reconnecting: the line rescans, summaries relabel |
| A cluster's protection, tightened (by any of its inputs) | on Apply, without confirmation; the epoch moves, so pending writes re-check |
| A cluster's protection, loosened (by any of its inputs) | on Apply, after the interlock naming the cluster |
| A cluster's context or kubeconfig | at reconnect: the plan names what ends; if the reconnection fails, the cluster is "saved, not connected", with Retry and Revert |
| An installation or a subscription | on Apply: the affected snapshots re-key, and retained data of the old lineage is never sent again (§6.6) |
| AI profiles, egress grants | on Apply: the settings revision and the grant terms change, and running turns end |
| Proactive (A2) consent, triggers, limits | on Apply: queued work is re-checked; revoked work stops before its next read or send, and a request already sent cannot be recalled |

A cluster's name and the fleet file's location stay restart-only, and are said as such.

**Tests:**
- a hand edit turning egress on, then "Use the file's values", takes the interlock;
- protection tightened after a step's guard and before its write: the write is refused;
- a stale arm or review after a reconnect is refused;
- the file changes while a confirmation is open;
- activation fails after the write.

## 5. Fleet

**The section's sentence:** "12 clusters running, 4 protected; fleet.yaml agrees", or names what differs.

**Clusters** are a real table (header semantics, rows at least 40px): name, context, region, group, environment, protection and connection. Selecting one opens its editor:
- **name:** read-only, and the field says why. The keychain's endpoint credentials, the view preferences and saved state are keyed by it, so a rename needs their migration: a later slice (§14).
- **context and kubeconfig:** "Save and reconnect" (§4).
- **region, group, environment, tags:**
  - region and group place the cluster on the line (region first, then group);
  - environment ranks the station and feeds protection's inference;
  - each field says so.

  A change to any input of protection shows its effect on protection in the plan.
- **protection:** explicit, inferred (with the input that infers it), or none. The effect is said: typed confirmation before destructive verbs and AI egress, which is a guard against mistakes, not authorization (RBAC still decides). Loosening takes the interlock.
- **endpoints:** links to Observability, filtered to this cluster.

**The line's comparison source** (`line.source`): effective and file-only for now (§3).

**Adding:** kubeconfig contexts, as today, now inside Fleet.

**Removing a cluster:**
- It is checked against the candidate configuration first, file-only installations included. While an installation names the cluster as its host, removal is refused, saying which, before anything disconnects or is deleted.
- The plan names the sessions it ends, the subscriptions it removes and the keychain accounts it offers to delete (only those accounts).
- It takes the interlock on a protected cluster.

**Writes:** a cluster's entry is edited as a YAML node: the changed fields only, other fields and comments kept, through slice 0's write policy. An entry Klyx cannot edit node-wise (an alias, a flow mapping) is refused with what to change by hand.

## 6. Observability sources

This is the owner's Alertmanager design (§0). It applies to Loki and metrics too: the owner's LGTM stack is one Mimir and one Loki for many clusters, with a tenant per cluster.

### 6.1 Installations and subscriptions

**An installation** is what can be reached: one record, written once.

```yaml
sources:
  - id: lgtm-we
    kind: alertmanager           # alertmanager | loki | metrics
    name: Shared Alertmanager, West Europe
    host: aks-we-prd             # reached through this cluster's API server:
    serviceRef: {namespace: monitoring, name: alertmanager-main, port: web}
    # or discover: true (Alertmanager and metrics), or endpoint: https://...
    tenant: ops                  # the default tenant for its subscriptions
    memberLabel: cluster         # the default scope: memberLabel = the member's name
    serves: [{group: we}]        # groups, or clusters by name
```

Each kind is its own installation, even when one LGTM deployment serves them all:

```yaml
  - id: lgtm-we-logs
    kind: loki
    host: aks-we-prd
    serviceRef: {namespace: monitoring, name: loki-gateway, port: http}
    serves: [{group: we}]
```

**A subscription** is a cluster's use of an installation for one kind: the installation, a tenant and a scope. It is implied for each cluster the installation serves, and written under the cluster when it differs:

```yaml
clusters:
  - name: aks-we-stg
    loki: {source: lgtm-we-logs, tenant: stg-logs, scope: tenant}  # its own tenant, nothing else in it
  - name: aks-we-dev
    alertmanager: {source: lgtm-we, match: {cluster: dev-we}}      # the label's value differs from the name
```

### 6.2 Resolution and validation

A cluster's subscription, for each kind, is resolved in this order:
1. its own block: `{endpoint | serviceRef}` as today, `{source: <id>, tenant?, match?, scope?}`, or `{off: true}`;
2. an installation serving it by name;
3. an installation serving its group;
4. discovery in itself (Alertmanager and metrics; Loki is never discovered);
5. none, said as such.

Each of these is a load error, naming what it names, never a fall-through to discovery:
- a duplicate installation id;
- a `source:` naming no installation, or one of another kind;
- a host that is not a cluster of the fleet;
- an installation with both a host and an endpoint;
- two installations serving one cluster for one kind at the same step;
- an invalid scope (§6.3).

### 6.3 One effective scope per subscription

A subscription's effective scope is its tenant (its own, else the installation's) and one effective match:
- **The default match** is `{<memberLabel>: <member name>}` when the installation has a `memberLabel`.
- **A subscription's `match` extends the default:** a key it names replaces that key's value, so `match: {cluster: dev-we}` changes the value; any other key adds an equality. The member label is never removed by an override.
- **`scope: tenant`** means everything in the subscription's tenant is the member's. It is allowed only when no other subscription of the installation uses that tenant.

**Subscriptions that share an installation and a tenant must have provably disjoint scopes:** each carries the member label, and with distinct values. An empty match, a missing member label or two equal values there is a load error naming both subscriptions. An installation without a `memberLabel` can be shared only by tenant (`scope: tenant`, one tenant each).

Label attribution stays what it is: a configured correlation, never UID identity (`alertmanager/subject.go`). The scope sentence says "filtered by", never "owned by".

### 6.4 Reading: buckets, and the split

- **Buckets:** subscriptions are grouped by the installation's transport, its credential and their tenant. Each bucket reads through the host's pinned connection or the endpoint, with its tenant's `X-Scope-OrgID`. Federated reads across tenants are not assumed.
- **Alertmanager:** one read per bucket per interval. The filter is the bucket's member-label values (escaped). Every returned row is checked again against each subscription's full effective match. Disjoint scopes mean at most one matches; a row matching none is counted as "not attributed" on the installation, and never added to a member.
- **Loki and metrics:** their reads are per object, window and template, so each is acquired per subscription, through its bucket's transport and tenant, with the existing per-kind bounds.
- **Caps:** a bucket's cap qualifies every member in it ("lower bound: lgtm-we's read reached its cap"). A member's share is never presented as complete when its bucket was cut. Counts of unattributed rows are of rows actually read, with what was omitted said.

### 6.5 What each cluster says

- **The scope sentence** appears in the Alerts lens header, the inspector's evidence and Observability's row: "from lgtm-we, hosted by aks-we-prd, tenant ops, filtered by cluster=aks-we-dev".
- **The host:** when it is disconnected, its subscribers read "lgtm-we's host aks-we-prd is not connected", never "no alerts".

### 6.6 The fences (A-C2)

The host relationship is operator-fixed transport, nothing more:
- It grants no general reads of the host's objects.
- It never adds the host to a conversation's comparison set.
- A model never chooses or widens it.

**Lineage:** every read through an installation records it: the host's pinned identity, the installation's and subscription's revisions, the tenant and the effective match.

**The checks:** before every component read, and before every later send that carries data of that lineage, whether a result, history, an answer or a continuation (in tools mode, snapshot mode and A2 alike), the fence checks:
- the host's pinned connection and its usability;
- that the installation and subscription are unchanged;
- the subject member's connection and consent.

A host that disconnects or loses its access, or a subscription that changes, after a read ends every later send of the retained data, even with no new read. Recovery starts new work under the new lineage.

**Attribution:** a member's share is split out before any AI read sees it. Data the installation holds for other members, or other tenants, is never sent for this member, and a member's consent covers its own share only.

### 6.7 Detect

Detect, per group or cluster, proposes; it never proves:
- Klyx runs the existing discovery, with its proof of one installation, in each member.
- For each installation found, it reads a bounded sample of label names and values. It never reads alert bodies, and never Loki lines merely to fill the page.
- It proposes an installation:
  - the host;
  - the Service;
  - a member label whose values match members' names;
  - the members seen in the sample.

  "Seen in this sample" never means a missing member has no data.
- It never infers that two clusters share an installation from equal Service names.
- The proposal shows its sample's time, its bounds and what was ambiguous or unavailable. The person reviews it, edits it and saves it.

### 6.8 Credentials and migration

- **Credentials** belong to the installation (`source/<id>`), bound to its origin as C5's are. A host's Service proxy uses the host's Kubernetes authentication and no external credential.
- **Migration:**
  - Today's per-cluster blocks keep their behaviour unchanged.
  - When Settings sees one direct URL on several clusters, or a shared-instance warning, it offers "Make a shared installation": a proposal with subscriptions and scopes, reviewed and saved by the person, never applied on its own.
  - Equal URLs are never merged automatically. Secrets are never copied between accounts: the installation's credential is stored once, by the person.

## 7. AI

**The section's sentence** names the interactive destination, the proactive destination and whether data leaves the machine.

**Profiles** replace the single provider block. Each has an id the person chooses and a `uid` Klyx assigns when it is created:

```yaml
ai:
  profiles:
    - {id: claude, uid: p-7f3a, provider: anthropic, model: claude-opus-5-5, keySource: keychain}
    - {id: local, uid: p-91c2, provider: openaiCompatible, baseUrl: http://localhost:11434, model: qwen3}
  interactive: claude              # chat, Explain, Suggest-fix, investigations
  proactive: local                 # A2's default (owner decision 2)
  protectedEgress:                 # each grant carries its own terms (R5)
    - profile: p-7f3a
      terms: {provider: anthropic, origin: https://api.anthropic.com, tlsVerify: true, policy: 1}
      grantedAt: 2026-10-25T10:00:00Z
  proposeActions: true
clusters:
  - name: aks-we-prd
    ai: {localOnly: true}          # this cluster's data goes to a loopback model only
```

- **A profile** carries today's fields: provider, model, origin, key source, CLI path, TLS and refusal fallback. Its key is in the keychain under `ai/profile/<uid>`.
- **A grant holds:**
  - the profile's `uid`;
  - the terms it was granted under, persisted with it: the provider, the canonical resolved origin, whether TLS is verified, and the egress policy's version.

  A grant applies only while the profile's current resolved terms equal its own. This is checked at load, at Apply and before every send, so a provider change at the same origin, a TLS weakening or a hand edit made while Klyx was closed leaves the profile without a grant. Deleting a profile deletes its grants. A profile re-created with the same id gets a new `uid`, and no old grant.
- **Local-only clusters:** `localOnly` is a persisted per-cluster policy, enforced from the provider's resolved on-machine classification: a loopback OpenAI-compatible endpoint, today's `OnMachine`. It is never enforced from a profile's name. The Claude Code CLI is off-machine, since it sends prompts to Anthropic through the person's sign-in, so it never satisfies a local-only cluster. Any future truly local CLI provider would need its own reviewed destination classification.
- **Investigations:** proposing actions, as A3 built it, with what turning it off does to offered plans.
- **Fixed bounds** (budgets, slots, sizes) are listed, not offered (§3).

**Migration:**
- The current block becomes profile `default`, with a new `uid`, its key still read from `ai/<provider>`; `interactive: default`.
- An existing `allowProtectedEgress: true` becomes a grant with the current resolved terms, but only if AI is on and its destination resolves (owner decision §16.2). With AI off, or a destination that does not resolve, nothing is granted, and the grant is asked for when it is first needed.
- No migration creates proactive consent.

## 8. Proactive (A2)

The owner's decisions 2 and 4, with the roadmap's §3 consent model. A2's own spec builds the runs, and S5 and A2 are one release boundary: no unattended run is possible before its consent, limits, retention and their backend enforcement exist. This section is what Settings holds:
- **Master switch:** off by default. It shows the running and queued counts, and its effect: turning it off stops queued work and fences running work at its next read or send. It cannot recall a request already sent. Interactive AI and proactive AI are each visibly on or off.
- **Default model:** a profile (§7), local or a provider. The effect states the exact destination and whether data leaves the machine. There is no automatic fallback to another profile.
- **Consent** is per cluster and per trigger, off by default, and persisted with its terms as a grant is (§7):
  - the profile's `uid`, provider, canonical origin, TLS verification and the egress policy's version;
  - the cluster's local-only policy;
  - the cluster's protection when consent was given, and whether the protected interlock was completed;
  - the time.

  A protected cluster needs its own consent, given with the interlock. A consent given while the cluster was unprotected does not cover it once it is protected: when protection tightens, through any of its inputs, in Settings or by a hand edit found at load, the consent becomes "needs consent again" until the person completes the interlock. This is checked at load, before a queued or fresh run, at Apply and before every send. A protected-egress grant for interactive use (§7) never stands in for it. A consent given under protection and its interlock stays usable across an ordinary restart. A change that widens a consent's terms pauses the affected work as "needs consent again", said, never skipped silently. Group membership and notifications never imply consent.
- **Triggers,** each described with an example:
  - a new fault episode;
  - a degraded episode;
  - a critical alert firing;
  - a GitOps revision change followed by a failure.

  Their episodes (baselines at connect and after gaps, deduplication across restarts) are A2's spec; Settings explains them.
- **Limits:**
  - runs per cluster per hour and per day, a fleet-wide daily cap, concurrency, and the queue's size and expiry;
  - model calls per run, at most the investigation budget.

  Each shows what is used and what remains, and a held run says which limit held it. Zero means "none allowed", not "unlimited". Every limit has a maximum.
- **Retention** (owner decision 4), fleet-wide:
  - reports kept by count and by age, both user-set (at least one report, at least one day);
  - a size limit, user-set up to an absolute ceiling Klyx keeps (512 MiB);
  - pruning runs at start, before the reports are listed; when a report is written; when a retention limit is applied; and while Klyx runs, on a schedule (every fifteen minutes), so a report past its age goes within that interval;
  - Settings says that while Klyx is not running nothing is removed, and that a report past its age goes at the next start;
  - a deletion that fails is said, with the report it left, never counted as enforced;
  - a report and its audit are deleted together, on one path;
  - current usage, and what lowering a limit would delete, confirmed before it does;
  - delete one, delete all.
- **The report and its audit:** a report keeps the sanitized result and a minimal audit, never raw reads, a reusable continuation or a secret. The audit records:
  - what each run sent, as categories and identities;
  - its consent and profile terms;
  - whether each send was not made, uncertain or answered.

  It is kept and deleted with its report.

## 9. Git, Notifications, Appearance

- **Git:** as today, plus each field's contract.
  - `remoteBases` is deferred and said as not supported (§3).
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

One list of what Klyx keeps, where, how big and for how long, with its action. S6 defines each action's scope, what it does to something in use, and what it reports if it fails:

| Item | Where | Action |
|---|---|---|
| fleet.yaml | `~/.config/klyx` or `KLYX_CONFIG` | open the folder, validate |
| window state, Claude Code builds | beside fleet.yaml | reset (a reset of the builds revokes the CLI's admission, said first) |
| Git cache | beside fleet.yaml | per repository and all: clear (existing); in use, cleared after the read |
| A2 reports and their audit | beside fleet.yaml, private | delete one or all, retention (§8) |
| the search index | memory, rebuilt at start | its kinds and budgets (§3), editable in S6 |
| keychain entries | the OS keychain, `io.moomora.klyx` | list by account (never values), remove the reviewed accounts |
| view preferences, Activity, palette ranking | the webview's storage | reset each |

Nothing new is written with a secret in it. A legacy plaintext token or key already in the file is kept until its migration, as today, and said. Export is out of scope until a projected format without secrets is designed.

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
  - the fields follow: 13 for labels and values, 12 for effects;
  - the effect is under each field, in secondary ink, never in a tooltip.
- **Surfaces:** The Line's ground, raised and sunk, with hairlines; inputs sunk. No new tokens: the route blue marks the selected section and the primary action, amber marks a difference between the file and what runs, and danger keeps ▲.
- **Signature:** the scope sentence (§1.1) with its source chips. On a subscription it reads as a small route: the member, then the host, then its tenant and scope, drawn as a typed relation with its legend (The Line §1.6). That is where "which member hosts the Alertmanager" becomes visible.
- **Space:** 4px base, 36px controls, 24px panel padding, 32px between sections. Table rows are at least 40px. A field with its effect, default and error grows to fit.
- **Accessibility:**
  - labels associated with controls, effects through `aria-describedby`;
  - errors announced, and placed where the field is;
  - every explanation reachable by keyboard;
  - the rail and search fully keyboard-operable.
- **Fixtures for every slice:** long names and URLs, wrapping, 200% zoom, a narrow window, keyboard and focus, unsaved navigation, night and day.

## 13. Security and honesty

- No secret is returned to the page or shown in an error (slice 0's redaction). Nothing new is written with a secret in it (§10).
- Every new block decodes strictly. An unknown key in `sources`, a subscription, `ai.profiles` or `proactive` is a load error with its place.
- Grants and consents are records with their own terms, checked against the current terms before they apply, never booleans that follow a later choice.
- A host member, an installation, a subscription, a profile's terms or a consent's terms changing ends the reads and sends that relied on them, through the epochs and fences (§1.7, §4, §6.6).
- Nothing in Settings runs a model or sends cluster data. Test sends what it says, and only what it says.

## 14. Slices

| Slice | Builds | Depends on |
|---|---|---|
| **S1** | the rail, addressing, ⌘,, search, the registry and its schema coverage, every existing setting's contract and disposition, Diagnostics' read-only parts | slice 0 |
| **F** | the activation foundation: plans from the running state, per-cluster policy epochs in the guard and the run fences, arm and review invalidation, saved-not-active states | S1 |
| **S2** | Fleet: the cluster editor, protection through any input, the interlock, reconnect, remove | F |
| **S3a** | Alertmanager installations and subscriptions: schema, resolution and validation, scopes, buckets and the split, the scope sentence, the host fence on reads and sends, Detect, the migration offer | F |
| **S3b** | Loki and metrics on the same model, with their per-subscription acquisition | S3a |
| **S4** | AI profiles, grants with terms, local-only clusters, the migration | F |
| **S5 + A2** | proactive settings and A2, one release boundary | S4 |
| **S6** | Data and storage, the index editable | S1 |
| **later** | cluster rename with its migration | S2 |

Each slice is reviewed by Codex before and after it is built, and its as-built note lives in its own spec. The accumulated slice 0 probes stay in every gate.

**Sequencing (owner, 2026-10-25):** after S4, the one investigation workflow comes before S5 + A2. The order is:
1. S1, F, S2, S3a, S3b, S4;
2. the investigation workflow (AI roadmap §5);
3. the onboarding milestone: an unfamiliar platform engineer configures Klyx and resolves a representative incident unaided;
4. S5 + A2;
5. S6, which may follow A2.

The table's dependencies are unchanged: A2 still builds on this redesign's consent, model, limits and retention foundation.

## 15. Tests

**Every slice:**
- its own tests;
- the registry's two coverage tests (§3);
- a strict-decoding test for each new block;
- a write-policy test for each new writer (symlink, mode, compare-and-swap);
- fixtures for the cases of §12.

**Named acceptance tests, from the review:**
- **R1:**
  - a hand edit before Apply;
  - protection tightened after a guard and before the write;
  - a stale arm or review after a reconnect;
  - a change while a confirmation is open;
  - activation failing after the write.
- **R2:**
  - one URL with different tenants;
  - a tenant-isolated member without a cluster label;
  - a scope change ending a retained read.
- **R3:**
  - wrong or missing labels;
  - an empty override;
  - overlapping matches;
  - duplicate values;
  - escaped values;
  - a hot member filling its bucket's cap.
- **R4:**
  - the host disconnecting or losing access between a read and a send;
  - a failed component read;
  - retained history;
  - an installation changing;
  - recovery after a fence ended the old work.
- **R5:**
  - local-only with an admitted Claude Code CLI (interactive, snapshot and A2), refused; a loopback model, allowed;
  - a consent given unprotected: a pending turn ends when protection tightens, a later episode cannot use it, a queued run is held, the same change made while Klyx was closed is found at load; a consent given under the interlock survives a restart;
  - a provider change at the same origin;
  - a cold-start hand edit;
  - a deleted and re-created profile id;
  - a local-only cluster resolving to a remote endpoint;
  - a stale grant after a policy change;
  - the legacy migration with AI off or unresolvable.

- **Retention:** the clock past a report's age with no start and no new report; a limit lowered on an idle store; a deletion that fails.

**The native smoke** gains a Settings walk: every section opens by its address, search finds a setting by its effect, and a subscription's sentence names its host.

## 16. Owner decisions

**Confirmed by the owner, 2026-10-25: all four, as proposed, with the conditions below.**

1. **Settings edits cluster entries** (§5): context, kubeconfig, placement and protection, but not the name, under §4's plan, epoch and interlock contract. Settings has never written the clusters list beyond appending. Proposed: yes.
2. **An existing protected-egress grant migrates** to a grant with the current resolved terms, but only if AI is on and its destination resolves (§7). The alternative is asking again on upgrade. Proposed: migrate, and say so once.
3. **An installation serves a group** (`serves: [{group: ...}]`), as the owner described a group sharing one Alertmanager (§6). Each member has its own subscription, tenant and scope, with explicit precedence. A membership change re-keys its data and ends the old lineage, and group membership never grants consent. Proposed: yes.
4. **A2's starting values,** with A2 off, all user-set within maximums:
   - **runs:** two per cluster per hour and six per day; twenty fleet-wide per day;
   - **concurrency:** one at a time, with a queue of four expiring after an hour;
   - **retention:** a hundred reports, thirty days and 64 MiB, under the 512 MiB ceiling.

   Proposed as starting values. They are not evidence of a run's cost or quality, which A2's measured runs will give.

## 17. Not in this redesign

- Writing a cluster's name (a later slice).
- Raising a fixed safety ceiling.
- Exporting configuration (until a format without secrets is designed).
- Anything that runs a model from Settings beyond Test.
- Remote bases for the cache diff.
- Federated reads across tenants.

## 17a. Implementation notes from the review

These are acceptance details for the slices, from Codex's re-review.

**Epochs and A3's guards (slice F):**
- The policy epoch is captured at the guard, an unprotected pass included, and compared at the write. A3's one-shot `checkProtected` stays consumed at entry, on refused paths too; the epoch check spends no second grant.
- "Saved, not active" blocks new work as well as old: a new operation never pins a new epoch while using an old service.
- A write refused before it is forwarded stays refused and not sent. A request already forwarded keeps its written or unverified outcome, since ending verification undoes nothing. A failed or reverted activation never makes a withdrawn plan or a stale arm usable again.

**Buckets and S8a (S3a):**
- Only resolved subscriptions are read: a shadowed configuration creates no reader.
- S8a's single fleet-wide limiter of four, its deadline (which includes limiter wait and discovery) and its single flight across re-keys stay as they are. Buckets add no slots.
- Alerts and groups take the same bucket filter. Mute reduction stays complete. Identity and matching facts are split before member rows are projected, and the opaque identity carries the installation, tenant and revision.
- §6.4's cap is S8a's 5,000-entry truncation, a lower bound. Its 8 MiB response limit and 20,000 group appearances stay whole-read failures, keeping the prior rows stale or unreadable.
- Retained bucket data and per-member rows are measured at the admitted shapes; no full copy is retained per member.

**Existing per-cluster blocks (S3a):**
- `alertmanager: {tenant: ..., match: ...}` with no locator means local discovery with that scope today. A group installation never takes it over, and S3a's resolution table spells it out as a legacy mode.
- Empty discovery-only blocks, a source-less Loki, a block with both endpoint and serviceRef (the endpoint wins, with a warning) and unscoped direct blocks are each handled deliberately. The new installation rules neither merge nor reject them.
- A direct endpoint with no host has no host connection to check: it keeps C5's endpoint and credential revision fences, plus the member, installation and subscription lineage. §6.6's host check applies to hosted transports.

## 18. Revision 1: Codex's review (2026-10-25)

Codex requested changes: five P2 and one P3, all on contracts the first draft left open. It found the field contract, navigation, the scope sentence and separate proactive consent sound, and ruled the four owner decisions reasonable with the conditions now in §16. Each finding is addressed:

| Finding | Change |
|---|---|
| **R1, P2:** Apply against the running policy, with a revision that fences pending work | §4 rewritten: plans from the running state, confirmations bound to the plan and its revisions, an ordered Apply, saved-not-active states, and per-cluster policy epochs in the ProtectGuard, every verb's run fence and the AI fences. Protection is derived from all its inputs. A new foundation slice, F (§14). |
| **R2, P2:** a tenant per cluster cannot be one source-level tenant | §6.1 separates installations from subscriptions, the tenant on the subscription; §6.4 reads in buckets by transport, credential and tenant, with per-subscription acquisition for Loki and metrics. |
| **R3, P2:** member scopes can be emptied or overlap | §6.3: one effective scope per subscription, an override extending the default and never removing the member label, `scope: tenant` for tenant isolation, and provably disjoint scopes within a shared tenant as a load rule. Every row is checked again after the read; caps qualify every member. |
| **R4, P2:** the host fence must cover later sends | §6.6: recorded lineage, checked before every component read and every later send of retained data, in every mode; the host relation is transport only. |
| **R5, P2:** grants need immutable terms | §7: profiles have a `uid`; grants and consents persist their terms and apply only while they equal the current resolved terms, checked at load, Apply and send; local-only clusters are a persisted policy enforced from the resolved endpoint; the migration only narrows. |
| **R6, P3:** registry coverage from the schema | §3: a disposition for every schema field, environment variable and store; a coverage test from the schema; validation, units and zero semantics in the contract; homes for `line.source` and the index; configured limits told apart from fixed ceilings. |

**Also adopted from the review:**
- semantic validation for installations (§6.2), and Detect as a proposal with sample semantics (§6.7);
- S5 and A2 as one release boundary (§8, §14);
- variable-height fields and the fixture list (§12);
- the retention ceiling, zero semantics and queue expiry (§8, §16);
- per-item scope for Data and storage, and legacy secrets kept until migration (§10);
- the named acceptance tests (§15).

## 19. Revision 2: Codex's re-review (2026-10-25)

Codex ruled revision 1's six fixes resolved as design contracts, and requested changes: three P2 and one P3.

| Finding | Change |
|---|---|
| **RR1, P2:** local-only accepted the Claude Code CLI, which sends prompts to Anthropic | §7: local-only follows the provider's on-machine classification (`OnMachine`, a loopback endpoint); the CLI is off-machine and never satisfies it. |
| **RR2, P2:** a consent given while unprotected could authorize a protected run | §8: a consent records the cluster's protection and whether its interlock was completed. Tightening, from any input, makes it "needs consent again", checked at load, before queued and fresh runs, at Apply and before sends. |
| **RR3, P2:** age retention unenforced in an idle session | §8: pruning at start, on write, on a retention Apply and every fifteen minutes while running; a failed deletion is said; what a stopped app cannot do is said. |
| **RR4, P3:** an example subscribed Loki to an Alertmanager installation | §6.1: a Loki installation of its own; each kind is its own installation. |

The re-review's implementation details on epochs, S8a buckets and existing per-cluster blocks are §17a's acceptance notes. The tests for RR1 to RR3 are in §15.

## 20. ACCEPT (2026-10-25)

Codex accepted the design at revision 2 (5f21e40), after the review of 17890a2 and two re-reviews, with no remaining findings. It ruled RR1 to RR4 resolved, and the original R1 to R6 contracts still addressed with no new conflict. It found §17a a faithful record of its implementation notes, which are acceptance requirements for slices F and S3a. A temporary test confirmed that the §6.1 examples parse and that each subscription names an installation of its kind. The owner then confirmed the four decisions (§16) as proposed, with their conditions.
