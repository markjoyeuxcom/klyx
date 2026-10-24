# Klyx v0.13.0

v0.13.0 adds cluster chat: ask questions about one cluster and get answers grounded in what Klyx reads from it. Chat is read-only. The cluster is the boundary, and the drawer shows every read it made. Each slice was reviewed by Codex before it landed. Spec: `docs/superpowers/specs/2026-10-14-klyx-cluster-chat.md`.

## Cluster chat

- **Three ways in:**
  - The "Ask about \<cluster\>" button in an open cluster's mast.
  - "Ask about \<cluster\>" in ⌘K.
  - "Ask a follow-up" under any Explain result. The question carries Explain's own target, fenced by its UID.
- **One conversation per cluster**, at most 8 at once. The cluster and its connection are fixed when the conversation starts.
  - A disconnect ends the conversation, and so does a change in Settings > AI. History is never sent under other settings.
  - Stop cancels the running question. Clear ends the conversation, and a new one starts only when you ask for it.
- **Read-only, always.** No tool changes anything, and chat never runs a verb. It can suggest a command or a Git change as text; you run it.

## How the model reads the cluster

- **API-key providers (Claude, OpenAI, Gemini)** get seven read-only tools:
  - `cluster_overview`
  - `list_attention`
  - `list_resources`
  - `get_resource`
  - `get_events`
  - `get_pod_logs`
  - `gitops_status`
- **Tool limits.** Arguments are validated strictly before any read. Kinds resolve through discovery to exactly one resource; an ambiguous kind lists its choices and reads nothing. Names are exact.
- **The Claude Code CLI** keeps its reviewed no-tools contract. Each question gets a snapshot of at most 48 KiB: the overview, attention rows, recent Warning events, and up to 5 objects you named as `kind/name` or `namespace/name`.
- **A local OpenAI-compatible server** uses tools when it supports them. It switches to snapshots only when the server positively says it cannot take tools.
- **Pod logs** are a finite read: not followed, at most 200 lines and 128 KiB from the server, scrubbed, then capped at 16 KiB. A pod replaced during the read discards the log.

## What is sent, and what never is

- **Never sent:**
  - Secret data, ConfigMap values and Helm values (inline, in `valuesFrom`, or in release storage).
  - `env` values, `command` and `args`.
  - Managed fields and the last-applied annotation.
  - Any annotation outside a short allowlist.
- **Objects are sent as a structural projection** built from the live object, never the YAML.
- **Logs, messages, labels and other text** are scrubbed and capped. They are not guaranteed secret-free, and the drawer says so.
- **Protected clusters** follow the same egress gate as Explain. The drawer names the provider and its address before you ask.
- **Every read is listed** under its answer, with a redaction count. Activity records one entry per model call: provider, model, mode, tool names and outcome. It never records question or answer text.
- **Nothing is written to disk.** A conversation lives in memory until you clear it, it ends, or the app quits.

## The drawer

- **Answers render through Klyx's own small markdown renderer.**
  - Raw HTML stays text, and images are never loaded.
  - Only `http:` and `https:` links are clickable. Each shows its full URL and opens in the OS browser on an explicit click.
- **Resource chips** come only from objects this conversation actually read, so text that merely looks like a reference stays text. A chip opens the object in Klyx.
- **The editor counts UTF-8 bytes** against the 4 KiB question limit, so a long question is never refused after sending. A refused question comes back to the editor.
- **Replies are ordered by the backend's version, not by arrival.**
  - A late reply never replaces a newer conversation.
  - A request's own refusal or failure stays visible until your next request.

## Known limitations / not yet verified

- **Chat has not run against a real model.** Every provider path is tested against mocked providers, including tool loops, continuation state, refusals and truncation. The first real conversations are the confirmation. If a provider refuses a request, the message names the check that refused it.
- **Snapshot answers see only the snapshot.** Name objects as `kind/name` or `namespace/name` to bring them in. The model is told to say when it needs something the snapshot lacks.
- **Crossings of the v0.11.0 AI spec, for chat only:**
  - Pod logs may be sent (the owner's decision).
  - Spec fields beyond Explain's allowlist are sent as a structural projection.
  - Explain and Suggest-fix are unchanged.
- **Every v0.12.1 limitation still applies.**
