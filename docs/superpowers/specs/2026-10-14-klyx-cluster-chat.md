# Klyx cluster chat: ask about one cluster, read-only

**Owner decisions (2026-10-14):**
- A chat panel for asking about everything in a cluster. The cluster is the boundary: one conversation reads one cluster.
- **Data access depends on the provider.**
  - API-key providers (Claude, OpenAI, Gemini, and `openaiCompatible` when it accepts tools) get read-only tools: the model asks Klyx for what it needs.
  - The Claude Code CLI gets a snapshot per question. Its reviewed no-tools contract stays unchanged.
- **Pod logs are allowed:** capped, scrubbed, and every read recorded.

**Builds on:** `docs/superpowers/specs/2026-10-13-klyx-ai-providers-and-explain.md`: the provider seam, egress, scrubbing and the CLI contract.

**Recorded crossings of that spec's §4.2 "never sent" list, for chat only:**
- **Pod logs:** the owner's decision.
- **Spec fields beyond Explain's allowlist:** chat sends a structural projection of an object's spec and status (§2). It never sends the YAML itself.

Explain and Suggest-fix keep their rules unchanged.

Revised after Codex's design review of 162b9ec (eight findings, §§2-8 below).

## 0. What exists

- **Explain** sends one object's allowlisted, scrubbed evidence in one single-turn completion. It is UID-fenced and anchored on Klyx's verdict. **Suggest-fix** adds a masked manifest.
- **The provider seam** (`internal/ai/provider.go`) is single-turn text. `Complete` discards reasoning and signature data (`anthropic.go:102`, `openai.go:109`, `gemini.go:127`) and treats `tool_use` as `ErrUnexpectedOutput`. OpenAI uses the Responses API with `store:false`.
- **The Claude Code CLI** runs one turn per process with no tools. Its stream contract revokes on any tool event.
- **What existing reads are not:**
  - `GetInstanceDetail` masks only core Secret data. It returns YAML with every annotation (`internal/fleet/crd.go:1192`).
  - `redactManifestForAI` drops last-applied and masks credential-like keys, but keeps Helm values and arbitrary annotations (`internal/appbridge/ai_service.go:309`).
  - `ScrubEvidence` is a heuristic; it reduces risk, it does not prove text is secret-free (`internal/ai/evidence_scrub.go:34`).
  - `PodLogStream` follows the log, sets no byte limit and addresses the pod by name (`internal/fleet/logs.go:20`).
  - `Registry.Conn` returns a connection without checking its current state (`internal/fleet/registry.go:104`).

## 1. Boundary and what Klyx guarantees

- **One cluster per conversation.** The cluster and its connection identity are fixed when the conversation starts. No tool takes a cluster argument, and every read uses the pinned connection. A disconnect ends the conversation (§5.3).
- **Read-only.** No tool changes anything, and chat never runs a verb. It may suggest a palette command or a Git change as text; the operator runs it.
- **Egress is the same gate as Explain.** A protected cluster needs `allowProtectedEgress`. The gate is checked with each model call's own settings snapshot (§5.3).
- **Guarantees, stated as they are:**
  - **Enforced structurally,** so tests can prove it: never sent are Secret data, ConfigMap values, Helm values (inline in a HelmRelease or Application, or in release storage), binary data, `env` values, `command` and `args`, managed fields, the last-applied annotation, and every annotation outside the allowlist.
  - **Heuristic:** logs, condition and event messages, labels, and the string fields that cross pass the scrubber and caps. They are scrubbed, not proven secret-free.
  - **Injection.** Cluster text can contain instructions. It travels only as tool results or the snapshot's data block, and the system prompt says it is data. Even so, an injected instruction can lead the model to read more of the same cluster. Those reads go to the configured provider. They are recorded and visible (§5.4), and they are never written back to the cluster.
- **The drawer says this once, plainly:** "Chat sends what it reads to \<provider\>. Logs and text are scrubbed, not guaranteed secret-free. Secret values and Helm values are never sent."
- **Nothing on disk.** A conversation lives in memory until the app quits, the operator clears it, or it ends (§5.3).

## 2. The chat projection (one contract for tools and snapshots)

Every object that chat reads goes through one projection, `chatProject(gvr, obj) ChatObject`. It is built from the live unstructured object. It never uses the inspector's YAML.

**Metadata:**
- Name, namespace, UID, generation, creation time and owner references (kind and name).
- Labels, with values scrubbed.
- **Annotations:** an allowlist only. That is `deployment.kubernetes.io/revision`, `argocd.argoproj.io/sync-wave`, `argocd.argoproj.io/sync-options`, `kustomize.toolkit.fluxcd.io/reconcile` and `helm.sh/resource-policy`. The rest are dropped, and their count is reported.

**Status:**
- Conditions: type, status, reason, message (scrubbed) and the last transition time.
- The kind's facts, using the existing per-kind fact builders, run on a **structurally sanitized copy**, never the raw object. The copy keeps only the allowlisted annotations and drops every data map (`data`, `stringData`, `binaryData`) and every dropped subtree below. Fact values are then scrubbed. Some builders copy ownership annotations into fact values (`internal/crd/facts.go:138`), so without the sanitized copy facts would bypass the annotation allowlist (Codex re-review of f012dc1).
- Klyx's verdict, when the Observer carries one (§3.3).

**Spec, per kind:**

| Kind | Sent | Dropped structurally |
|---|---|---|
| Pod, and the workload templates of Deployment, StatefulSet, DaemonSet, ReplicaSet, Job and CronJob | Replicas, selector and strategy. Per container: name, image, ports, resources, probe types and ports. `env` names, with `valueFrom` reference names only. `envFrom` reference names. Volume types, with reference names only. Node selector, tolerations, affinity summary, service account | `env` values, `command` and `args`, probe `httpHeaders` values |
| Secret | Type and key names | `data`, `stringData` |
| ConfigMap | Key names and value sizes only | Every `data` and `binaryData` value. A ConfigMap can carry Helm values through a HelmRelease `valuesFrom` under any key (`values.yaml`, `app.conf`), so no ConfigMap value is classified as safe |
| Service, Ingress, Gateway API routes and Gateways, NetworkPolicy, HPA, PDB, Node | Their spec, with string values scrubbed and capped | Annotations, per the allowlist |
| Flux HelmRelease | Chart reference, interval, install and upgrade remediation settings, `valuesFrom` reference names | `values`, `postRenderers` patches |
| Argo Application and ApplicationSet | Source and destination, sync policy, Helm parameter names | `helm.values`, `helm.valuesObject`, parameter values, plugin env values |
| Any other kind (CRDs) | Scalar leaves of the spec up to depth 4: numbers and booleans as they are, strings key-aware masked, then scrubbed and capped at 200 characters | Any subtree whose key matches `values`, `valuesObject`, `data`, `stringData`, `binaryData`, or the sensitive-key pattern |

- **Helm release storage** (Secrets or ConfigMaps owned by Helm) is never projected beyond its name.
- **A projection that cannot be built** returns an error result, never the raw object.

## 3. Tools (API-key providers)

### 3.1 Catalogue

| Tool | Arguments | Returns |
|---|---|---|
| `cluster_overview` | none | The Observer cluster summary: areas and verdicts, capabilities, version, node and pod counts, coverage gaps |
| `list_attention` | `area?` | Observer rows that Klyx counts as needing attention, with verdict |
| `list_resources` | `kind`, `group?`, `namespace?`, `label_selector?`, `limit?` (1-100, default 50) | One page of rows: name, namespace, age, Klyx's verdict where the Observer has one |
| `get_resource` | `kind`, `group?`, `namespace?`, `name` | One `ChatObject` (§2), with warning events about it |
| `get_events` | `namespace?`, `kind?` and `name?` together, `warnings_only` (default true), `limit` (1-50) | Events, newest first |
| `get_pod_logs` | `namespace`, `pod`, `container?`, `previous?`, `tail` (1-200, default 100) | A finite log snapshot (§4) |
| `gitops_status` | `namespace?`, `name?` | Flux and Argo units with Klyx's ready, sync and drift verdicts |

### 3.2 Resolution and validation
- **Arguments are validated server-side before any read.**
  - Strict JSON: unknown members, wrong types and out-of-range numbers are refused.
  - Names follow Kubernetes rules, at most 253 characters.
  - A refused call returns an error result naming the problem.
- **Kinds resolve through discovery** to exactly one top-level resource that supports `get` and `list`. Kind, plural and short name all work, with an optional group.
  - An ambiguous kind (two groups) returns the choices and reads nothing.
  - An unknown kind says so.
  - No subresources, proxy, exec, or arbitrary path. The only exception is the dedicated log reader (§4).
- **Names are exact.** There is no substring matching. A name that is not found says "not found in \<namespace\>", never "does not exist in the cluster".

### 3.3 The result envelope
Every tool result is one JSON object:

```json
{
  "tool": "...",
  "source": "observer | api | informer",
  "readAt": "RFC3339",
  "identity": { "gvr": "...", "namespace": "...", "name": "...", "uid": "...", "generation": 0 },
  "coverage": { "state": "live | stale | checking | unreadable", "asOf": "RFC3339" },
  "complete": true,
  "total": { "count": 0, "exact": true },
  "truncated": false,
  "errors": ["component: message"],
  "data": {}
}
```

- **Observer-backed results** keep the Explain anchor fields: incarnation, part version, `evaluatedAt` and per-kind coverage. Native `Included` stays separate from `Role`. Controller issues stay separate from object counts.
- **Lists** report `total.exact: false` with a lower bound when the API returned a continue token.
- **Events** come from a read that reports its own errors and sorts newest-first across what it read. A partial read says it is a sample. `instanceEvents` is not reused as it is: it drops read errors and sorts only its first page (`crd.go:1519`).
- **A failed component sits beside a successful read** in `errors`. It is never an empty value.

## 4. The finite log read

A new data-layer read: `PodLogSnapshot(ctx, ns, pod, container, previous, tail) (LogSnapshot, error)`.

**Bounds:**
- The log is not followed.
- `TailLines` is `tail`, and the server-side `LimitBytes` is 128 KiB.
- The client also caps reading at 128 KiB.
- Each read has its own 10-second deadline, and the stream is closed on every path.

**Identity:**
- The pod is read first, for its UID and containers, and again after the log read.
- A changed UID discards the log: "the pod was replaced while reading".
- The log subresource is not claimed to be atomic with the UID.

**Containers:**
- When the pod has more than one container and none is named, the result lists the containers and reads nothing.
- `previous` on a container that never restarted returns its own error.

**Output:**
- The whole text is scrubbed first, so a credential split across lines is still matched.
- Then each line is capped at 2,000 characters, and the serialized result at 16 KiB.
- The result records the pod UID, container, current or previous instance, read time, line count and redaction count.

## 5. The turn loop, budgets and lifetime

### 5.1 One turn
A user message starts one turn: a model call, tool calls, tool results, and repeat until the model ends with text.
- **Model calls:** at most 6 per turn.
- **Tool calls:** at most 8 per turn, counted per batch before dispatch.
  - A batch that would pass the limit runs only the calls that fit. The rest get "tool budget for this question used up" results.
  - One final model call may then answer.
- **Concurrency:** at most 4 reads at once.
- **Deadline:** 120 seconds for the whole turn, covering every retry and fallback.
- **The operator's Stop** cancels the turn's context. Every read and model call shares that context.

**Outcome rules carry over from Explain:**
- A refusal is shown as a refusal.
- A truncated answer shows nothing partial.
- An unknown stop reason, a call to a tool Klyx did not offer, a duplicate call ID or a missing call ID ends the turn with an error.

### 5.2 Budgets (bytes, not rows)
- **Question:** 4 KiB.
- **Each tool result:** 16 KiB serialized.
- **Each stored answer:** 32 KiB. The model's output limit stays at the existing per-purpose value.
- **Each response's continuation:** at most 256 KiB. A larger one ends the turn with an error. Continuation is never sliced.
- **What one model call carries:** at most 384 KiB, measured as the serialized size of everything sent - questions, answers, tool-call arguments, tool results, and the continuation state of the exchanges included.
  - Whole old exchanges are dropped oldest-first, each with its own continuation. An exchange is a question, its tool calls and results, its continuation and its answer.
  - A tool call is never separated from its result, and the current exchange is never cut.
  - The drawer shows "earlier messages are no longer sent to the model".
  - If the current exchange's complete serialized representation passes 384 KiB on its own, the turn ends with an error before sending.
- **What the backend retains per conversation:** at most 20 exchanges and 1 MiB, counting failed and cancelled turns and their ledgers. Beyond that the oldest exchanges are removed from the backend and the drawer, with a notice.
- **Conversations:** one per cluster, at most 8 at once.

### 5.3 Lifetime, settings changes and fencing
- **The backend owns conversations.** Each has an ID, the cluster, the pinned connection, and the AI settings revision it started with (`AIState.Rev`, which changes on every settings publication, `internal/appbridge/ai_state.go:26`). Only the operator's question text comes from the frontend. System prompts, tool calls and results, and continuation state stay in the backend.
- **One active turn per conversation.** A second question while a turn runs is refused.
- **Turns are fenced.**
  - Each turn has an ID.
  - Stop, Clear and a disconnect cancel it.
  - A result or event that arrives after cancellation is dropped by turn ID.
  - Clear ends the conversation, and a new one starts fresh.
- **Disconnects.** Before every read and every model call, the turn checks that the registry's connection for the cluster is the pinned one and that it is connected. If not, the turn ends and the conversation closes with "the cluster disconnected". A reconnect starts a new conversation.
- **Settings changes.** Each model call loads the current immutable AI state, and that one snapshot decides both the egress check and the provider for that send.
  - If its revision differs from the conversation's pinned revision, the turn stops before sending. A provider change, a model change, a replaced key for the same provider, and a change away and back while idle all bump the revision.
  - The conversation then ends with "AI settings changed; start a new conversation". Its history is never sent under other settings, and continuation state is never migrated.
  - If egress for this cluster is no longer allowed, the turn stops the same way.

### 5.4 The read ledger
- Every attempted read is recorded with its tool, its arguments (names only) and an outcome: completed, failed, truncated or cancelled. This includes reads in turns that later error or are cancelled.
- The drawer shows the ledger under each turn, whether the turn answered or failed.
- Activity gets one entry per model call: provider, model, cluster, mode, tool names and counts, and redaction counts. Never question, result or answer text.

## 6. Providers: the conversation seam

`Converse(ctx, ConvRequest{System, Messages, Tools}) (Turn, error)` is new, beside `Complete`. Explain stays on `Complete`, unchanged.

**A `Turn` carries:**
- the text, if any;
- the tool calls, each with an ID, a name and arguments;
- an opaque, provider-tagged continuation (at most 256 KiB). It goes back on the next call exactly as received, and is never shown or parsed for the answer.

**Continuation, per provider:**
- **Anthropic:** the assistant content blocks as received, including thinking and redacted-thinking blocks with their signatures. `tool_result` blocks reference `tool_use` IDs.
- **OpenAI** (Responses, `store:false`): the output items as received, including reasoning items with `encrypted_content` (requested through `include`). `function_call_output` references `call_id`.
- **Gemini:** the model's parts as received, including `thoughtSignature`. `functionResponse` parts answer `functionCall` parts in order.
- **`openaiCompatible`:** the Chat Completions `tool_calls` shape.

**Mixed text and tool calls** in one response keep both: the text is not shown as the answer until the turn ends.

**Unsupported tools, for `openaiCompatible` only.**
- `ErrToolsUnsupported` is a typed outcome, returned only when the server positively says so: an HTTP 400 or 422 whose error names `tools`, `tool_choice` or `function` as unsupported.
- Only that outcome switches the conversation to snapshot mode, with a notice.
- Authentication errors, refusals, truncations, malformed output, cancellations and generic HTTP failures stay errors. They never trigger a snapshot or a retry.

**Unchanged:** OpenAI keeps `store:false`, the transport's response caps and key scrubbing apply, and `Complete`'s behaviour does not change.

## 7. Snapshot mode (the Claude Code CLI, and `openaiCompatible` without tools)

The CLI contract stays exactly as reviewed: one process per question, no tools, the same lockdown and stream contract.

**Each question sends one document of at most 48 KiB,** in the snapshot's delimited data block:
- the cluster overview and the attention rows;
- the newest 20 Warning events (marked as a sample if the read was partial);
- resolved references (below), at most 5, each as a `ChatObject`;
- for a resolved Pod, its finite log snapshot (§4, at most 100 lines);
- the conversation so far as question and answer text, at most 16 KiB, oldest out first.

**Resolving references from the question only:**
- The question is split into tokens.
- An explicit reference is `kind/name`, `namespace/name`, or `kind namespace/name`.
- A bare token counts only when it exactly matches one name in Klyx's bounded index: the Observer's rows plus the informer-cached Pods, Deployments, StatefulSets, DaemonSets, Services and Nodes.
- An ambiguous match lists its choices in the snapshot and reads nothing more.
- Earlier AI answers never authorize a read. A seed from Explain keeps its cluster, GVR, namespace, UID and provenance.
- A miss means "not in this snapshot", never "does not exist".

**The model is told it sees a snapshot.** It must say when the answer needs something the snapshot does not contain.

**The UI labels each answer "Snapshot"** and lists what was included and what was ambiguous.

## 8. Frontend

**The drawer:**
- It sits in the inspector's slot, one conversation per cluster.
- **Entry points:**
  - an "Ask" button in an open cluster's mast;
  - "Ask about \<cluster\>" in ⌘K;
  - "Ask a follow-up" under any Explain result, which seeds the conversation with that object's identity.
- **The header** names the cluster, provider and model, the mode, and "read-only", plus the guarantee line from §1. A protected cluster also shows its egress notice.
- **Under each turn:** the read ledger (§5.4). Stop cancels and Clear ends.

**Rendering answers.** Klyx renders its own small markdown subset as React elements: paragraphs, headings, lists, emphasis, inline code and code blocks. There is no `dangerouslySetInnerHTML` and no third-party markdown library.
- Raw HTML, SVG and embedded content are shown as literal text.
- Images are never loaded. An image renders as its alt text, with its URL as text.
- No answer-controlled styles, classes or event handlers.
- **External links:**
  - Only `https:` and `http:` are clickable. Other schemes (`javascript:`, `data:`, `file:`, custom app schemes) render as plain text.
  - A clickable link shows its real destination host next to its label.
  - It opens only on an explicit click, through the OS browser opener.
  - The webview is never navigated, and model text never triggers an app action.
  - Even an explicit click can send data embedded in the URL to that host. The link shows its full URL for that reason.
- **Internal resource links** are chips built only from validated references in this conversation's read ledger: same cluster, GVR, namespace, name and UID. They open the object in Klyx. Text that only looks like a reference stays text.

**Unavailable states:** AI off, egress refused, the cluster disconnected, or settings changed. Each says why, with the fix.

## 9. Slices (each Codex-reviewed)

1. **This spec.**
2. **Projection and tools.**
   - `chatProject` and the tool executors over a narrow `ChatConn`.
   - The finite log read in `internal/fleet`.
   - Argument validation and resolution, and the result envelope.
   - No model calls.
3. **Provider conversation:** `Converse` with continuation for Anthropic, OpenAI and Gemini, plus `openaiCompatible` with `ErrToolsUnsupported`. Fake-server tests per vendor.
4. **The chat service:** conversations and turns, fencing, budgets and trimming, the ledger, the settings and disconnect rules, snapshot mode, and Activity entries.
5. **Frontend:** the drawer, the entry points, the safe renderer, the link rules, the ledger, and the unavailable states. Tests and headless screens.
6. **Smoke** on the kind lab with the owner's key, after the owner's go-ahead for the real calls.

## 10. Testing

- **Projection sentinels.** Two kinds of sentinel are planted in every never-sent field: arbitrary strings with no credential shape, and recognized credential shapes. The fields are Secret data, ConfigMap values (including one referenced by a HelmRelease `valuesFrom` under a custom `valuesKey`), Helm values (HelmRelease `values`, Argo `helm.values` and `valuesObject`, Helm storage), `env` values, `command` and `args`, non-allowlisted annotations (including the ownership annotations fact builders read) and `binaryData`. None may appear in any tool result, fact, snapshot or Activity entry. The heuristic fields are tested with recognized shapes only, and the tests say so.
- **Identity joins.** A verdict joins an object only by UID equality, as Explain does. A seeded read checks the seed's UID against the object it reads, so an old UID's verdict never attaches to a replacement.
- **The log read:**
  - a quiet pod returns promptly;
  - one oversized line is capped;
  - a credential split across two lines is redacted;
  - cancellation is clean;
  - a pod replaced during the read is discarded;
  - a multi-container pod without a named container reads nothing.
- **Resolution:**
  - namespace and group collisions return choices;
  - an incidental substring does not match;
  - malformed arguments and unknown members are refused;
  - a same-name recreation is caught by identity.
- **Envelope:**
  - an empty but stale read is not shown as empty;
  - a forbidden dependency appears in `errors`;
  - partial pagination gives a lower-bound total;
  - an absent kind and an unreadable event component appear beside a successful object read.
- **Lifetime:**
  - navigating from cluster A to B, and a disconnect then reconnect;
  - Clear and a new turn, and late responses after Stop;
  - egress disabled between two model calls, a provider change mid-conversation, a replaced key for the same provider, and a change away and back while idle;
  - a second question during an active turn.
- **Budgets:**
  - a parallel batch over the limit, and the 7th model call;
  - history trimming with and without tool results;
  - a small visible answer with a large continuation, counted in the request budget;
  - a current exchange over budget, rejected before sending;
  - repeated turns past the retention bound, including failed and cancelled ones.
- **Providers:**
  - two sequential tool steps, and parallel calls;
  - signed continuation round-trips per vendor;
  - mixed text and calls;
  - duplicate and missing call IDs, and unoffered tool names;
  - refusal, truncation and cancellation;
  - for `openaiCompatible`: the positive tools-unsupported case, and every error that must not fall back.
- **Renderer:**
  - raw HTML, SVG and images;
  - `javascript:`, `data:`, `file:` and custom schemes;
  - a link whose label disguises its destination;
  - action-like links;
  - internal chips only from the ledger.

## 11. Non-goals

- **No write verbs from chat,** ever.
- **No fleet-wide conversation.** One cluster is the boundary.
- **Nothing persisted:** no transcript on disk and no cross-session memory.
- **No web access, URL fetching or MCP exposure.**
- **No change to Explain or Suggest-fix,** or to the Claude Code CLI contract.
