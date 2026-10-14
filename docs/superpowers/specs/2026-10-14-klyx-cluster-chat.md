# Klyx cluster chat: ask about one cluster, read-only

**Owner decisions (2026-10-14):**
- A chat panel for asking about everything in a cluster. The cluster is the boundary: one conversation reads one cluster.
- **Data access depends on the provider.**
  - API-key providers (Claude, OpenAI, Gemini) get read-only tools: the model asks Klyx for what it needs.
  - The Claude Code CLI gets a snapshot per question, because its contract forbids tools.
- **Pod logs are allowed:** capped, scrubbed, and every read listed in the chat.

**Builds on:** `docs/superpowers/specs/2026-10-13-klyx-ai-providers-and-explain.md` (the provider seam, egress, scrubbing, the CLI contract).

**Recorded crossing:** that spec lists pod logs under "never sent". The owner's 2026-10-14 decision allows them in chat only. Explain and Suggest-fix keep the rule.

## 0. What exists

- **Explain** sends one object's allowlisted, scrubbed evidence in one single-turn completion, UID-fenced and anchored on Klyx's verdict. **Suggest-fix** adds the masked manifest.
- **The provider seam** (`internal/ai/provider.go`) is single-turn text. `Complete(System, User)` returns text or an error. A `tool_use` stop is `ErrUnexpectedOutput`.
- **The Claude Code CLI** runs one turn per process with no tools. Its stream contract revokes on any tool event.
- **Klyx's data layer** already holds the reads chat needs:
  - Observer rows and the cluster summary;
  - informer-cached lists;
  - generic instance detail with masked manifests;
  - events;
  - pod log streams.

## 1. Boundary and posture

- **One cluster per conversation.** The cluster is fixed when the conversation starts. No tool takes a cluster argument: every read runs on that conversation's connection. A disconnect ends the conversation with a notice.
- **Read-only.** No tool changes anything. Chat never runs a verb. It may suggest a palette command or a Git change as text; the operator runs it.
- **Egress is the same gate as Explain.**
  - A protected cluster needs `allowProtectedEgress`.
  - The gate is checked when the conversation starts and again before every model turn, against that turn's settings snapshot.
  - A loopback `openaiCompatible` endpoint stays on the Mac, as today.
- **Untrusted data.** Everything a tool returns is cluster data, possibly hostile.
  - It travels only inside tool results (or the snapshot's delimited block), never in the system prompt.
  - The system prompt says it is data, never instructions.
  - Because every tool is read-only and fenced to one cluster, the worst an injected instruction can do is make the model read more of the same cluster, or answer wrongly.
- **No exfiltration path in the answer.**
  - Answers render as markdown with remote images and auto-loading content disabled.
  - Links are shown as text and open only on an explicit click.
  - Klyx never fetches a URL from an answer.
- **Never sent:** Secret data (names, types and key names only), Helm values, kubeconfig or credentials of any kind, and annotations other than an allowlist.
- **Scrubbed and capped:** every free-text value (messages, log lines, manifest values) goes through `ai.ScrubEvidence`. Each tool result has a size cap, and truncation is marked.
- **Nothing on disk.** A conversation lives in memory until the app quits or the operator clears it. Each model turn adds an Activity entry:
  - provider, model, cluster and mode;
  - the names of the tools called and their counts;
  - scrub and redaction counts.

  Never prompt or answer text.

## 2. Tool mode (API-key providers)

### 2.1 Tools
A fixed set, each with a JSON schema, served from Klyx's data layer.

| Tool | Returns | Cap |
|---|---|---|
| `cluster_overview` | The Observer cluster summary: areas and their verdicts, installed capabilities, Kubernetes version, node and pod counts, coverage gaps | 8 KiB |
| `list_attention` (`area?`) | Rows Klyx counts as needing attention, with verdict, label and reason | 100 rows |
| `list_resources` (`kind`, `namespace?`, `label_selector?`, `limit?` ≤100) | Name, namespace, age, Klyx's verdict where it has one, and a status summary | 100 rows, total count stated |
| `get_resource` (`kind`, `namespace`, `name`) | Conditions, facts, owner and GitOps owner, and the masked manifest with managed fields stripped | 16 KiB |
| `get_events` (`namespace?`, `object?`, `warnings_only` default true, `limit` ≤50) | Reason, count, message, involved object, last seen | 50 rows |
| `get_pod_logs` (`namespace`, `pod`, `container?`, `previous?`, `tail` ≤200 default 100) | The last lines, scrubbed | 16 KiB |
| `gitops_status` (`namespace?`, `name?`) | Flux and Argo units with Klyx's ready, sync and drift verdicts and messages | 50 units |

**Kind resolution.** Kinds go through discovery: built-ins and CRDs, singular, plural or short name. An unknown kind returns an error result that says so.

**Errors are results.** A failed read returns an honest error result ("forbidden: …", "not found") to the model. It is never an empty list.

**Secrets:**
- `list_resources` returns Secret names, types and key names.
- `get_resource` on a Secret returns its metadata and key names. The data is never sent.

**ConfigMaps:** data values are scrubbed and capped at 500 characters each. The model is told they were scrubbed.

### 2.2 The turn loop
A user message starts one turn: model call, tool calls, tool results, and again, until the model ends with text. A turn is bounded:
- at most 8 tool calls per user message;
- at most 6 model calls per user message;
- a 120-second deadline for the whole turn;
- the operator's Stop, which cancels the turn.

**Outcome rules carry over from Explain:**
- A refusal is shown as a refusal.
- A truncated answer shows nothing partial.
- An unknown stop reason, or a tool name Klyx did not offer, ends the turn with an error.

**Context budget.**
- The conversation's tool results are trimmed oldest-first once the history passes 96 KiB. A trimmed result is replaced by "[earlier tool output dropped]".
- The operator's messages and the answers are kept.

### 2.3 Providers
A new conversation interface beside `Provider`. Explain stays on `Complete`, unchanged.
- `Converse(ctx, ConvRequest{System, Messages, Tools}) (Turn, error)`.
- A `Turn` is either text (the end of the turn) or tool calls.

Per provider:
- **Anthropic:** Messages with tools; `tool_use` and `tool_result` blocks.
- **OpenAI:** function calling.
- **Gemini:** function declarations.
- **`openaiCompatible`:** tools when the server accepts them. When it refuses tools, the conversation falls back to snapshot mode (§3) with a notice.

## 3. Snapshot mode (Claude Code CLI, and providers without tools)

The CLI contract stays exactly as reviewed: one process per question, no tools, the same lockdown and stream contract.

**Each question sends one bounded context document (48 KiB).** Klyx builds it from:
- the cluster overview and the attention rows;
- the last 20 Warning events;
- objects the question names: names in the question are matched against Klyx's cached object names, at most 5 objects, each with its `get_resource` view;
- for a pod the question names, its last 100 log lines, scrubbed;
- the conversation so far, as prior question and answer text, capped at 16 KiB, oldest first out.

**The model is told it sees a snapshot.** It must say when the answer needs something the snapshot does not contain, never guess.

**The UI labels each answer "Snapshot".** It lists what the snapshot included, and suggests naming an object to bring it in.

## 4. Frontend

- **Entry points:**
  - an "Ask" button in the mast of an open cluster;
  - "Ask about \<cluster\>" in ⌘K;
  - "Ask a follow-up" under any Explain result, which starts a conversation seeded with that object and its explanation.
- **A drawer on the right,** the inspector's slot.
  - One conversation per cluster.
  - The header names the cluster, the provider and model, the mode (tools or snapshot), and "read-only".
  - A protected cluster shows the egress notice.
- **Each answer lists its reads** in a collapsed list, for example "get_pod_logs shop/web-7f…/app, 100 lines, 2 redacted".
- **Resource names in an answer** that match a known object open it in Klyx.
- **Controls:** Stop cancels the turn in flight; Clear ends the conversation.
- **Unavailable states:** AI off, egress refused, or the cluster disconnected each say why, with the fix (Settings > AI, or the protected toggle).

## 5. Slices (each Codex-reviewed)

1. **This spec.**
2. **The tool layer** (`internal/appbridge`): tool registry and schemas, executors over a narrow `ChatConn`, allowlist, scrub and caps. Value tests with sentinels. No model calls.
3. **Provider conversation** (`internal/ai`): `Converse` for Anthropic, then OpenAI, Gemini and `openaiCompatible`. The outcome rules apply. Fake-server tests per vendor.
4. **The chat service:** the per-cluster conversation, the egress gate per turn, the turn loop with caps, deadline and cancel, snapshot mode for the CLI, and Activity entries.
5. **Frontend:** the drawer, the entry points, the reads list and the safe markdown rendering. Tests and headless screens.
6. **Smoke** on the kind lab with the owner's key, after the owner's go-ahead for the real calls.

## 6. Testing

- **Sentinels.** A secret sentinel is planted in every free-text surface each tool can read, in each scrubbed shape, and in every never-sent field (Secret data, Helm values). No sentinel may appear in any tool result, snapshot or Activity entry.
- **Fence.** A tool cannot read another cluster. The conversation's connection is the only one its executors receive. A disconnect ends the conversation.
- **Injection.** A pod log or event that tells the model to read Secrets. The model may call `get_resource` on a Secret, but no Secret data is in the result.
- **Caps and loop limits:** the 9th tool call and the 7th model call end the turn with a clear error; the deadline and Stop cancel cleanly.
- **Providers:** fake servers per vendor for text, tool calls, refusal, truncation, unknown stop reasons and unoffered tool names.
- **Snapshot:** the CLI path through the existing fake CLI. The snapshot obeys its cap and states what it left out.

## 7. Non-goals

- **No write verbs from chat,** ever.
- **No fleet-wide conversation.** One cluster is the boundary. Fleet questions stay with the fleet views.
- **Nothing persisted:** no transcript on disk and no cross-session memory.
- **No web access, URL fetching or MCP exposure.**
- **No change to Explain or Suggest-fix,** or to the Claude Code CLI contract.
