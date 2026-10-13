# Klyx AI: provider choice, settings and Explain in every lens

Status:
- Codex APPROVE-WITH-CHANGES on a8c2a8a (2026-10-13, handoff 6382bde4).
- Amended below for its seven required changes:
  1. CLI admission by verified enforcement (§1.3, §1.4)
  2. the protected-cluster CLI policy (§3)
  3. one immutable provider and policy snapshot, plus serialized config writes (§1.1, §2.3)
  4. the credential lifecycle (§2.1)
  5. a UID-fenced Explain capture (§4.1)
  6. hard transport limits and failure mapping (§1.2)
  7. an allowlist-based, value-tested prompt contract (§4.2)
- Re-review on 39a3602: APPROVE-WITH-CHANGES with two narrow gaps, both fixed here:
  - managed policy is re-checked before every CLI invocation, and only self-contained native installs are admitted (§1.4);
  - the UID fence also covers unanchored generic Resources (§4.1).
- Codex judged every other section acceptable at design level.
- Implementation starts with slice 1, which these two fixes do not touch. Codex's note stands: the Claude adapter still needs implementation review and real contract tests, and this design verdict does not certify the installed CLI.

Owner decisions (2026-10-13):
- In-app AI setup takes API keys for Claude (Anthropic), OpenAI and Gemini.
- It can use an installed vendor CLI instead: Claude Code (`claude`), Codex (`codex`) or Antigravity (`agy`).
- Scope: providers and setup, plus an Explain action in every lens.
- **CLI posture: no tools, fail closed.**

Builds on `2026-07-09-klyx-ai-explain-certmanager.md` (the honesty contract and egress policy) and backlog §2.

Recorded change of plan: the 2026-07-09 review deferred an AI settings write path. The owner now asks for in-app setup with keys, so this spec builds it.

## 0. What exists, and what is broken today

What exists:
- `internal/ai.Client` speaks the Anthropic Messages API (`hosted`) and OpenAI-compatible `/v1/chat/completions` (`local`).
- Config is `ai:` in fleet.yaml, with the API key in plain text.
- `AIService` offers ExplainCertificate, ExplainResource (any GVR object through the generic `GetInstanceDetail` bundle) and SuggestFix (a Git-only patch from a masked manifest). They appear only in the Certs lens and the Resources inspector.
- Egress: a protected cluster's context leaves the machine only with `allowProtectedEgress`, except to a loopback endpoint.

Defects, confirmed by Codex. Slice 1 fixes them:
1. **Sampling.** The Anthropic call sends `temperature: 0` (`internal/ai/client.go:111-117`). Current Claude models (Sonnet 5, Opus 5.5 and later) reject non-default sampling parameters. So `backend: hosted` with today's default model (`claude-sonnet-5`) fails.
2. **Budgets.** Explain and Fix allow 512 and 900 tokens (`internal/appbridge/ai_service.go:17-20`). On current models thinking counts against `max_tokens`, so answers risk truncation.
3. **Stop reason.** Anthropic's `stop_reason` is never decoded (`client.go:126-140`). A refusal or truncation can come back as ordinary text and be shown as an explanation.

## 1. Providers

### 1.1 The provider seam and the active snapshot
```go
type Provider interface {
    Complete(ctx context.Context, req Request) (Result, error)
}
type Request struct{ System string; Evidence Evidence; Purpose Purpose } // explain | fix | probe
type Result struct{ Text, ModelUsed string }                             // only a confirmed, non-empty completion
```

Settings are applied as one **immutable snapshot**, swapped with `atomic.Pointer[aiState]`:

```go
type aiState struct {
    ProviderID  ProviderID // anthropic | openai | gemini | openaiCompatible | claudeCLI (| codexCLI | agyCLI when admitted, §1.4)
    Provider    Provider
    Model       string
    Origin      string     // where prompts go: "https://api.anthropic.com", "Claude Code CLI (your login)", "http://localhost:11434"
    OnMachine   bool       // loopback openaiCompatible only
    AllowEgress bool
    KeySource   KeySource  // keychain | env | legacy | none (§2.1)
    Admission   *CLIAdmission
    Rev         uint64
}
```

- Every call loads the snapshot once, then uses that same snapshot for the egress check, the completion and the returned provenance (provider, model used, origin, rev).
- This replaces today's separate fields (`ai_service.go:53-60`), where the check (`:153`) and the send (`:169`) read state separately. A swap between them must never pair a local check with a cloud send.
- In-flight calls finish on their snapshot.

### 1.2 API providers
**Clients** (amended at slice 1 implementation, 2026-10-13):
- **Claude** uses the official `github.com/anthropics/anthropic-sdk-go` (v1.76.0), built with `option.WithoutEnvironmentDefaults()` so no `ANTHROPIC_*` variable, profile or custom header joins a request.
- **OpenAI and Gemini** use small raw-HTTP clients in `internal/ai`, not their SDKs, because both SDKs break §2.1's one-explicit-source rule:
  - `openai-go` folds ambient `OPENAI_ORG_ID`, `OPENAI_PROJECT_ID`, `OPENAI_CUSTOM_HEADERS` and `OPENAI_BASE_URL` into every request, and its switch to disable that is internal-only.
  - `google.golang.org/genai` falls back to `GOOGLE_API_KEY`/`GEMINI_API_KEY`, environment base URLs and a Vertex backend switch when a field is empty. It also pulls grpc and opencensus in for one REST call.
- **`openaiCompatible`** keeps Klyx's raw chat-completions client.
- Each client is tested against recorded request shapes (§6).

**Transport limits** apply to all providers, through one Klyx `RoundTripper` handed to every SDK:
- The 4 MiB response-body cap applies to model replies, model lists and error bodies.
- Redirects:
  - a cross-host redirect is refused;
  - an HTTPS-to-HTTP redirect is refused;
  - more than 5 redirects is refused.
- One overall per-call deadline (`Purpose` budget) bounds the SDKs' own retries and backoff. The SDK retry count is set explicitly, at most 2.

**Failure mapping.** Each vendor's signals become a failure, never text:
- a refusal (Anthropic `stop_reason: refusal`, OpenAI refusal content, Gemini safety block or `finishReason` SAFETY);
- an incomplete answer or token exhaustion (`max_tokens`, `incomplete`, `MAX_TOKENS`);
- an unexpected tool output;
- an empty completion.

Text blocks are joined only after a confirmed successful completion. `Result.ModelUsed` is the model the vendor reports, not the configured one.

**Per vendor:**
- **Claude:**
  - No sampling parameters and no `thinking` field (it defaults to adaptive); vendor-default effort.
  - `max_tokens` 16000, non-streaming. The prompt keeps answers short.
  - Default model `claude-opus-5-5`. The picker lists `GET /v1/models`.
  - Server-side refusal fallback (`fallbacks: "default"`, beta `server-side-fallback-2026-07-01`) is ON by default and disclosed in Settings. It stays within the same vendor and egress contract, the result names the model actually used, and an explicit final refusal still fails closed.
- **OpenAI:** the Responses API; no temperature; vendor-default reasoning effort. No default model: the owner picks from `GET /v1/models`, filtered to text-generation models.
- **Gemini:** `generateContent` with `systemInstruction`. No default model: the owner picks from the models that support `generateContent`.
- **openaiCompatible:** a base URL is required; temperature 0 as today; the key is optional, from the keychain.

**Origins:**
- **Credentialed API providers** use the vendor's default origin. A custom origin must be HTTPS, carry no userinfo, query or fragment, and comes with an explicit disclosure: "Your <vendor> key will be sent to <origin>".
- **openaiCompatible** that receives a key must be HTTPS unless it is on loopback. A keyless server on the local network may use HTTP; this is the owner's LAN model server case. It counts as off-machine for egress, and Settings shows its unencrypted origin.
- Settings always shows the actual origin.

### 1.3 CLI adapters (verified contracts)
A CLI provider is a **versioned adapter contract**. It is not a set of hopeful flags. The contract is code in Klyx, per vendor, per platform and per supported version range.

**What the contract must disable before any cluster data is sent:**
- every command, shell and code-execution tool;
- file read and write tools;
- network and web tools;
- MCP servers;
- browser and computer use;
- subagents;
- hooks;
- user, project and local customizations (settings, skills, plugins, custom commands, output styles);
- automatic context discovery (CLAUDE.md, AGENTS.md and similar).

Managed (admin policy) settings that the CLI cannot override make the adapter fail closed: Klyx detects the vendor's managed-settings locations and refuses if any exist.

**How a contract is verified.** Without a model call:
- the executable is resolved;
- its identity is computed (sha256 of the resolved file, version string, platform);
- its help is parsed for every required flag, and the effective lockdown argv is assembled.

An unknown version, a missing flag, an unknown output schema or an unknown event type fails closed. Admission (§1.4) binds to the executable identity and the effective lockdown configuration, and is invalidated by any change.

**Invocation** (all adapters):
- **Working directory:** a fresh `0700` temp dir per call, removed afterwards.
- **Input:** the system prompt and the evidence go through stdin or `0600` files inside that dir (for example `--system-prompt-file`). Nothing from the prompt goes in argv, in diagnostics or in Klyx logs.
- **Environment:** minimal. `HOME`, `PATH`, the locale, `TMPDIR` and the vendor's own auth variables are kept. `KUBECONFIG`, every `KLYX_*` variable, and the cloud credential variables (`AWS_*`, `AZURE_*`, `ARM_*`, `GOOGLE_APPLICATION_CREDENTIALS`, `CLOUDSDK_*`) are dropped.
- **Output bounds:** stdout and stderr are each capped; stderr is never surfaced verbatim beyond a short, scrubbed excerpt.
- **Containment and cancel:**
  - POSIX: a new process group, and the whole group is killed on deadline or cancel.
  - Windows: a Job Object with kill-on-close, so the process tree dies with it.
- **No persistence:** no session persistence or local transcript, where the vendor allows that. A vendor that always persists transcripts fails the contract.

**Per vendor**, as of the versions on this Mac on 2026-10-13:
- **Claude Code 2.1.296:**
  - `claude -p --output-format json --tools "" --restricted --safe-mode --strict-mcp-config --mcp-config '{"mcpServers":{}}' --disable-slash-commands --no-session-persistence --system-prompt-file <tmp>/system.txt --model <model>`, with the evidence on stdin.
  - What the flags do, per its own help:
    - `--tools ""` removes all built-in tools.
    - `--restricted` removes command and code tools and WebFetch, ignores user, project and local settings, and confines file tools.
    - `--safe-mode` disables CLAUDE.md, skills, plugins, hooks, MCP, custom commands and agents, and output styles, while auth keeps working.
    - Strict MCP with an empty config removes MCP servers.
  - `--system-prompt-file` is real but hidden: the help names it only inside the `--bare` text ("--system-prompt[-file]"). Contract verification must therefore confirm it by a flag-parse check that makes no model call, not by its own help line. If it is missing, fail closed; the system prompt does not move into argv.
  - Managed settings still apply, so they are detected and refused as above.
  - `--bare` is not used: it disables OAuth and keychain login.
  - This is the one adapter expected to pass.
- **Codex 0.162.1 is UNSUPPORTED** until a contract that disables the command tool itself exists.
  - Measured: the read-only sandbox blocks network but reads the whole disk (a canary file outside the working dir was readable through `codex sandbox -- cat`).
  - A restricted filesystem profile would still leave commands available, so it is not "no tools".
- **agy 1.3.3 is UNSUPPORTED** until a no-tools contract exists. Plan mode and the sandbox flag are not "no tools", and its print-mode tool and MCP behaviour is undocumented.

API setup and Explain never wait on CLI support.

### 1.4 Admission and the regression probe
**Admission** is granted when, and only when, the §1.3 contract verifies for the exact executable identity and lockdown configuration. It is recorded as `CLIAdmission{ExeSHA256, Version, Platform, ArgvHash, AccountHash, CheckedAt}`; no managed policy is a precondition.

Admission is re-checked:
- on Settings "Test", or automatically before the first call in a Klyx run;
- before every call, against the executable identity (a cheap stat plus a hash cache) and the account identity. For Claude Code, §1.5 lists every per-call check (Codex design review of 274535f).

**Before every invocation, managed policy is re-checked** (Codex re-review, 39a3602). The executable and argv can stay identical while an admin hook or policy appears after admission. So, immediately before launch, Klyx re-reads every policy source the adapter's vendor supports:
- the managed-settings files and directories for the platform (for Claude Code on macOS, `/Library/Application Support/ClaudeCode/`; the Linux and Windows equivalents are listed in the adapter);
- the managed-policy environment variables.

If any policy source exists, or its absence cannot be established, the call fails closed. A vendor with a remote or admin-policy channel Klyx cannot inspect is unsupported. This is a configuration check, not a model probe.

**Self-contained installs only.** Admission covers the actual CLI runtime. Klyx admits only an executable that resolves, after following symlinks, to a self-contained native binary (Mach-O, ELF or PE), and hashes that binary. On this Mac, `~/.local/bin/claude` links to `~/.local/share/claude/versions/2.1.296`, a native arm64 binary. A launcher script or interpreter shim (for example an npm `node` wrapper) is refused, because an unchanged launcher can front a changed runtime.

Tests for both:
- A clean adapter is admitted, then a managed-policy file appears with the executable and version unchanged: no prompt is delivered.
- Undeterminable policy fails closed.
- A shim install is refused.

**The probe** is regression evidence, not proof. It runs on "Test", with the owner told that it sends one small prompt on their CLI login, and in slice 3's tests against the real CLI.
1. Create a randomly named file `R` inside the call's working dir, and a file containing token `T1` outside it.
2. Prompt the model to list the working directory's files, to read the T1 file, and to run a command.
3. Pass only if:
   - `R` and `T1` are absent from the output (neither can be produced without a tool);
   - the structured stream shows no tool, command, file or MCP event, or only events the CLI reports as denied;
   - the run exits cleanly.

A failed probe revokes admission.

**Every real call** is checked the same way. Any tool event, unknown event type or schema mismatch discards the answer, reports "This CLI attempted a tool or returned an unknown event; Klyx discarded the answer", and revokes admission until the next Test.

### 1.5 The Claude Code adapter contract (slice 3, from research on 2026-10-13)
Sources: the Claude Code docs (cli-reference, managed-settings, server-managed-settings, headless, env vars) and local evidence on 2.1.296. That evidence is its `--help` output, its string table, and runs of `auth status` and `doctor` in a throwaway `CLAUDE_CONFIG_DIR` with no login. No model call was made. Everything below is checked without one, except the optional probe (§1.4).

Codex's design review (274535f) required four changes before implementation. They are folded in below:
- per-call account and policy checks;
- exact reviewed versions;
- locked-down preflight helpers;
- a confirmed-completion output contract.

**Reviewed contracts (exact, not a range):**
- A contract is one reviewed tuple `{version, GOOS/GOARCH, sha256 of the binary}`. Today there is exactly one: Claude Code `2.1.296`, `darwin/arm64`, sha256 `c9b5341637becbd423ddffc5b254afb645682a3868cb708bbc6cc0e7bb419937`.
- Any other version, platform or hash is refused before any helper runs: "Claude Code <v> on <platform> has no reviewed Klyx contract (reviewed: 2.1.296 darwin/arm64)". A recognised flag set on a new binary proves neither its meaning nor its output or policy contract.
- Adding a version is a reviewed change that carries the version's bounded fixtures (`auth status`, `doctor`, the sentinel probe, and a captured stream-json run).
- Claude Code updates itself often. Until Klyx ships a reviewed entry, the owner can point `ai.cliPath` at a reviewed binary that is still in `~/.local/share/claude/versions/`.

**Pinned binary:**
- Resolve the executable through symlinks. It must be a native Mach-O, ELF or PE file, and it is hashed.
- A cache keyed on path, size, mtime and inode avoids re-hashing 240 MB per call. Any change to the key forces a re-hash.
- The sha256 must equal the reviewed tuple's.
- `--version` must print exactly `2.1.296 (Claude Code)`.

**Flag-recognition probe (no model call):**
- `--version` short-circuits even unknown flags, so it proves nothing.
- Instead, run the exact production argv followed by a random sentinel flag. Run it under the call environment, with stdin `/dev/null` and a proxy pointing at a closed port as a safety net.
- Require a non-zero exit, empty stdout, and stderr matching `unknown option '<sentinel>'` (case-insensitive). The parser reports the first unknown option, so that error proves every real flag was recognised.
- This proves recognition, not meaning: the meaning is pinned by the reviewed tuple.

**Production argv:**
`claude -p --output-format stream-json --verbose --tools "" --disallowedTools "*" --restricted --safe-mode --strict-mcp-config --mcp-config '{"mcpServers":{}}' --disable-slash-commands --no-session-persistence --system-prompt-file <tmp>/system.txt --model <model>`
- The evidence goes on stdin only, never as an argument. The prompt text never starts with `/` or `@`.

**Environment (allowlist, not denylist), for every process Klyx starts from this binary:**
- Built from scratch: `HOME`, `PATH`, `TMPDIR`, `LANG`/`LC_*`, `USER`/`LOGNAME`, plus `NO_COLOR=1` and `TERM=dumb`.
- Plus these, always set:
  - `CLAUDE_CODE_DISABLE_ATTACHMENTS=1`. Without it, `@path` text in the prompt is expanded into file contents even with every tool off, so cluster text containing `@~/.kube/config` would read the kubeconfig.
  - `CLAUDE_CODE_DISABLE_CLAUDE_MDS=1`, `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`, `CLAUDE_CODE_SKIP_PROMPT_HISTORY=1`, `ENABLE_CLAUDEAI_MCP_SERVERS=false`, `CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS=1`.
  - `DISABLE_AUTOUPDATER=1`, `DISABLE_TELEMETRY=1`, `DISABLE_ERROR_REPORTING=1`.
- Never passed through, whatever the parent has:
  - `ANTHROPIC_*`, and `CLAUDE_CODE_*` other than the above;
  - `CLAUDE_CONFIG_DIR`, which also re-keys the keychain login;
  - `CLAUDE_CODE_REMOTE_SETTINGS_PATH` and `CLAUDE_CODE_MANAGED_SETTINGS_PATH`, which inject policy;
  - `NODE_OPTIONS`, `OTEL_*`, proxies, `KUBECONFIG`, and cloud credentials.

**Preflight helpers (`auth status`, `doctor`): locked down too.**
- Both helpers run with:
  - the root lockdown switches placed before the subcommand: `claude --restricted --safe-mode auth status --json` and `claude --restricted --safe-mode doctor`;
  - the allowlisted environment above;
  - a fresh empty `0700` working dir that is not a git repo (doctor reads settings files in the working dir), and stdin `/dev/null`;
  - stdout and stderr each capped at 64 KiB, a 10 s deadline for `auth status` and 30 s for `doctor`, and process-group kill.
- Pinned by evidence on 2.1.296. A throwaway config dir had user settings that defined `apiKeyHelper`, `otelHeadersHelper`, `awsAuthRefresh`, `awsCredentialExport`, a command `statusLine`, `SessionStart`/`SessionEnd` hooks and an `env` block.
  - Neither helper ran any of those commands, with or without the switches.
  - With the switches, `auth status` ignored user settings: it reported `authMethod: none` instead of `api_key_helper`. So the helpers observe the same configuration as the `--restricted` call.
  - An opt-in contract test (`KLYX_CLAUDE_CONTRACT=1`) repeats this against the real binary: a throwaway config dir, no login, no model call. If a reviewed version ever runs a helper there, that version's contract fails and the adapter is refused.
- Helper output is parsed for the pinned fields only. It is never surfaced verbatim: email, organization name and paths stay out of the UI, logs and Activity. The account identity is kept only as a hash.
- No cluster evidence exists at preflight. The prompt is written only after every check has passed.

**Checks before every prompt-bearing call (fail closed), in this order:**
1. **Executable:** the reviewed tuple (above).
2. **Endpoint policy, before any helper runs.** Fail on any entry, or any unreadable path other than "missing", in:
   - `/Library/Application Support/ClaudeCode/`;
   - `/Library/Managed Preferences/com.anthropic.claudecode.plist`;
   - `/Library/Managed Preferences/<user>/com.anthropic.claudecode.plist`.

   The Linux (`/etc/claude-code/`) and Windows (`C:\Program Files\ClaudeCode\`, the `HKLM`/`HKCU\SOFTWARE\Policies\ClaudeCode` `Settings` value, and the WSL `/mnt/c/...` equivalents) locations join the check when a contract for those platforms is reviewed.
3. **Policy caches:** `~/.claude/remote-settings.json` and `~/.claude/policy-limits.json` must be absent. "Missing" is the only accepted error.
4. **Account** (`auth status`, pinned schema for 2.1.296):
   - The JSON object must have exactly the known keys: `loggedIn`, `authMethod`, `apiProvider`, `analyticsDisabled`, `projectsDirectory`, `configDirectory`, `email`, `orgId`, `orgName`, `subscriptionType`.
   - It must show `loggedIn: true`, `authMethod: "claude.ai"`, `apiProvider: "firstParty"`, and `subscriptionType` `"pro"` or `"max"`.
   - It must not have `apiKeySource`, `forcedLoginMethod` or `allowedProviders`; the last two come from policy.
   - `configDirectory` must be `~/.claude`.
   - An unknown key, a duplicate key, unparseable output, trailing data or a non-zero exit refuses.
   - Team and Enterprise accounts are refused. Their organisation can push server-managed policy, delivered at startup and hourly, which can include hooks and applies inside the `-p` run after Klyx's check.
   - API-key auth is refused as well; a Klyx API provider covers that case.
5. **Account identity binding:** the identity is `sha256(authMethod, apiProvider, subscriptionType, orgId, email)`.
   - Admission records it.
   - A call whose identity differs from the admitted one, or cannot be determined, is refused and revokes admission until the next Test.
6. **Remote policy** (`doctor`, pinned lines for 2.1.296):
   - The output must contain, each exactly once, these lines:
     - `Running: native (2.1.296)`;
     - `Platform: darwin-arm64`;
     - `Managed settings (remote): not fetched — requires an Enterprise or Team subscription`, with an em dash, from the reason `unsupported_subscription`;
     - `Organization policy: not applicable to Pro and Max accounts`, from the reason `prosumer_oauth`.
   - Any other line whose label mentions "managed" or "polic" refuses.
   - So do a missing or duplicate pinned line, a "checking", "loaded", "cached" or "fetch failed" value, truncated output, a deadline hit, or a non-zero exit.
   - Doctor is a diagnostic, not a documented machine interface, so this parse is pinned per reviewed version and never generalised.
7. **Re-check after the helpers:** the endpoint policy and the caches again.
8. **Launch, then re-check before releasing stdin.**
   - Write `system.txt` (`0600`) into a fresh `0700` call dir, and start the production argv with stdin held open and empty.
   - Re-check the endpoint policy and the caches.
   - Only then write the evidence to stdin and close it. If the re-check fails, the process group is killed and nothing is written.

Check-to-launch race (documented, accepted for Pro and Max only):
- Between check 6 and the CLI's own startup, the owner could switch the login, or Anthropic could start delivering policy to a personal account. The re-check in step 8 narrows that window, but Klyx cannot observe the CLI's own policy load.
- The output contract below catches some after-the-fact signs: a hook event, or a changed init. Rejecting the output after a hook ran is detection, not prevention. That is why Team and Enterprise accounts, which have a policy channel, are refused outright.

**Admission** is established on Settings "Test", or automatically before the first call in a Klyx run. It runs steps 1-7 plus the flag-recognition probe. It records `CLIAdmission{ExeSHA256, Version, Platform, ArgvHash, AccountHash, CheckedAt}`, in memory only.

Admission is revoked until the next Test by any of:
- an identity change;
- a tool or unknown event;
- a contract mismatch in the output.

A transient helper failure, such as a timeout, fails that call only.

**Per-call output contract** (stream-json, verbose, pinned for 2.1.296). The answer is accepted only as an explicitly confirmed, complete, non-refusal, text-only answer. Everything else discards it.

- **Framing:**
  - stdout is newline-delimited JSON, capped at 4 MiB, and every line is exactly one JSON object.
  - A malformed line, a blank line inside the stream, or data after the final newline refuses.
  - The process must exit 0. stderr is capped at 64 KiB and only a short, scrubbed excerpt is ever shown.
- **Sequence:**
  - exactly one `system/init`, and it is the first event;
  - then only `assistant`, `rate_limit_event` and `system/api_retry` events;
  - then exactly one `result`, the last event; anything after it refuses.
  - Every other type or subtype refuses, including `user`, `stream_event`, `tool_progress`, hook events, `system/status`, `system/informational`, `system/notification`, `system/compact_boundary` and `system/permission_denied`.
  - A second `init` or a second `result` refuses.
  - A model switch refuses: `system/model_fallback`, `system/model_refusal_fallback` and `system/model_consent_fallback` mean another model answered.
  - `system/model_refusal_no_fallback` maps to `RefusedError`.
- **`system/init`:**
  - `tools`, `mcp_servers`, `slash_commands`, `skills`, `plugins` and `agents` are empty or absent;
  - `claude_code_version` is `2.1.296`;
  - `cwd` is the call dir;
  - `model` is the requested model;
  - `apiKeySource` is `"none"`.
- **`assistant` events:**
  - `parent_tool_use_id` is null;
  - every event carries the same `message.id`, so there was one model message;
  - `message.model` is the requested model;
  - content blocks are only `text`, `thinking` or `redacted_thinking`. Thinking blocks are allowlisted reasoning, never part of the answer, as with the approved Gemini part allowlist;
  - `message.stop_reason` is null or `end_turn` on every event.
- **`result`:**
  - `subtype: "success"`, `is_error: false`, `num_turns: 1`;
  - `stop_reason: "end_turn"` and `terminal_reason: "completed"`, both present;
  - `permission_denials` empty;
  - no `errors`, `api_error_status`, `api_error`, `api_error_code`, `deferred_tool_use` or `structured_output`;
  - `safety_stops` absent or empty;
  - `subagent_stats` absent or empty;
  - every `modelUsage` key is the requested model;
  - `session_id` equals the init's;
  - `result` is non-empty and equals the concatenated text blocks of the assistant events.
- **Failure mapping:**
  - `stop_reason` `refusal` → `RefusedError`;
  - `max_tokens` or `model_context_window_exceeded` → `ErrTruncated`;
  - empty text → `ErrEmpty`;
  - a missing or unknown `stop_reason` or `terminal_reason` → `ErrUnconfirmed`, even when `subtype` is `success`, the exit is 0 and `num_turns` is 1;
  - tool blocks or events → `ErrUnexpectedOutput`, which also revokes admission;
  - contradictory metadata (for example `is_error` true with subtype `success`, or `result` not matching the streamed text) → `ErrUnexpectedOutput`.

The stream-json fixtures for slice 3 are synthesized from the 2.1.296 emitter in the binary's string table. They are marked as such, and the slice 5 smoke replaces them with a captured run.

**Containment:**
- a fresh 0700 call dir that is not a git repo, removed afterwards;
- capped stdout and stderr;
- a POSIX process group, killed as a whole on deadline or cancel;
- the deadline from §1.2.

Windows (a Job Object) waits for a reviewed Windows contract; until then the adapter refuses there before starting anything.

**Open risks the owner accepts or vetoes:**
- The `claude doctor` text is not a documented machine contract. It is pinned per reviewed version and fails closed on any drift.
- Server-side feature flags can change behaviour without a version bump. The output contract fails closed on what it can see.
- The check-to-launch race above.
- Subscription auth under `--restricted --safe-mode` is documented for `--safe-mode` but unproven together. The slice 5 smoke settles it, with a before/after diff of `~/.claude`. The same smoke also covers:
  - embedded-`@path` regression evidence, in both the system text and the evidence;
  - startup-hook and memory sentinels;
  - the no-transcript checks.

## 2. Keys and settings

### 2.1 Credential lifecycle
**One explicit source per provider.** `keySource` is one of `keychain | env | legacy | none`. It is stored in fleet.yaml and never inferred by falling back.
- **keychain** is `github.com/zalando/go-keyring`: service `io.moomora.klyx`, account `ai/<provider>`.
- **env** is `apiKeyEnv: NAME`, intended for Linux without a Secret Service.
- **legacy** is today's `ai.apiKey`. It still loads with a Settings banner and a "Move to keychain" action.

**Lifecycle rules:**
- **Set or Replace:** write the keychain entry, set `keySource: keychain`, rebuild and swap the snapshot. The key the UI describes is the key in use.
- **Clear:** delete the keychain entry, set `keySource: none`, rebuild and swap. Clearing never reactivates an env or legacy key.
- **Move to keychain:** write the keychain entry first, then remove the YAML `apiKey` line, then set the source. A failed removal is reported ("Key copied to the keychain, but fleet.yaml still holds it in plain text: <error>") and the source stays `legacy` until the removal succeeds.
- **Keychain locked, unavailable or denied:** the provider fails closed, reporting "keychain unavailable: <reason>", which is distinct from "no key stored".

**Never echoed.** A key never appears in a DTO, the Activity log or a returned error. Provider errors pass through a scrubber that removes the active key string and anything shaped like a bearer token before they reach the UI.

### 2.2 fleet.yaml `ai:` (non-secret settings)
```yaml
ai:
  provider: anthropic        # off | anthropic | openai | gemini | openaiCompatible | claudeCLI
  model: claude-opus-5-5
  keySource: keychain        # keychain | env | legacy | none
  apiKeyEnv: ""              # keySource env only
  baseUrl: ""                # openaiCompatible (required); custom HTTPS origin for an API provider (disclosed)
  cliPath: ""                # optional override for a CLI provider
  allowProtectedEgress: false
  tlsSkipVerify: false       # openaiCompatible only
```

Back-compat:
- `backend: hosted` reads as `anthropic` with `keySource: legacy` when `apiKey` is set.
- `backend: local` reads as `openaiCompatible`.

### 2.3 Write path, serialization and failure behaviour
A new `AISettingsService` (Wails-bound) offers:
- `GetAISettings()`: the persisted settings, the active snapshot's rev, provider and origin, and any divergence.
- `SaveAISettings(dto)`, `SetAPIKey(provider, key)`, `ClearAPIKey(provider)`, `MoveKeyToKeychain()`.
- `ListModels()`, `TestProvider()`, `DetectCLIs()`.
- `ArmAIEgress(typed)`: see the egress rule at the end of this section.

**One config write lock.** `SaveAISettings`, the key operations and `ConfigService.AddClusters` share a single fleet.yaml write lock. Each write also reads the file's hash at the start and refuses ("fleet.yaml changed on disk; reload Settings") if the file changed before the atomic rename. That closes the lost-update gap that the atomic rename alone leaves (`internal/config/append.go:91-134`).

**Order for a save:**
1. Validate.
2. Build the new provider and snapshot in memory. This includes resolving the key source and CLI admission; nothing is persisted yet.
3. Write fleet.yaml under the lock.
4. Swap the snapshot.

What happens on failure:
- **A failed validation or build** writes nothing and keeps the active snapshot.
- **A failed write** keeps the active snapshot and reports the error.
- **A successful write** that is somehow followed by a failed swap reports "settings saved but not active" until the next successful build. `GetAISettings` always shows both sides, so the persisted and active states never diverge silently.

**Egress authorization.** Turning `allowProtectedEgress` ON is authorized at the backend, not only in the dialog. `ArmAIEgress(typed)` records a one-shot 30s grant, typed as "allow protected egress", and `SaveAISettings` consumes it when that flag goes false to true. Turning it off needs no grant.

### 2.4 Settings > AI panel (frontend)
A section in `SettingsView` with:
- **Provider:** API key, Local server or Installed CLI, then the vendor.
- **Key:** write-only, showing its stored state and source, with Set, Replace, Clear and Move to keychain.
- **Model:** a picker filled by `ListModels`, with free text allowed.
- **Origin:** shown, with the custom-origin disclosure when one is set.
- **CLI admission:** the detected path, version, executable identity (short hash), and admission state with the reason.
- **Test.**
- **Egress:** the protected-cluster egress toggle, with a typed confirmation.
- **Status line:** says exactly where prompts go.

## 3. Egress and the protected-cluster CLI policy
- Every provider is off-machine except an `openaiCompatible` endpoint on loopback. CLI providers are off-machine.
- A protected cluster's context is refused without `allowProtectedEgress`, as today. The check uses the call's snapshot (§1.1).
- **`allowProtectedEgress` authorizes sending the selected evidence to the provider. It never authorizes host filesystem or tool access.**
  - An unadmitted CLI is refused on every cluster, protected or not, whatever the toggle says.
  - An admitted CLI is an ordinary off-machine provider under the same opt-in.
- Every call adds an Activity entry: provider, model used, origin, cluster, object, and the scrub and redaction counts. Never prompt text.

## 4. Explain in every lens

### 4.1 The capture (UID-fenced, with provenance)
One entry point: `Explain(cluster string, target ExplainTargetDTO) AIResultDTO`.

The target names a `(task, part, uid)` row, a Helm release `(ns, name)`, or a generic object `(group, version, plural, ns, name, uid)` from the Resources browser.

**For an Observer-backed target**, the backend acquires the anchor itself from the Observer. It never takes the frontend's row, and never pairs a fresh GET with an unidentified current row. The anchor records:
- task, part, incarnation, `Ver` and `evaluatedAt`, from the captured `Rows` (`internal/observe/types.go:159-167`);
- the served GVR, UID and generation;
- the part's counted and dependency coverage (live, stale, checking or unreadable, with as-of times);
- the evaluation:
  - for verdict parts, the `health.Verdict` (kind, label, reason, rule, rank);
  - for native parts, `NativeEval` with `Included` kept separate from `Role` and `Label`.

`Included` is the lens's attention predicate and is never translated into "fault". `internal/health/native.go:5-17` documents why `Included` and `Role` deliberately disagree; for example, Flux "not ready" includes progressing. The prompt is told what each field means.

**Then** the generic detail and the per-kind extras are fetched. Before any model call:
- If the fetched object's UID differs from the anchor's, Explain is refused: "The object was replaced since Klyx evaluated it."
- A different generation is recorded as "Klyx's verdict is for generation N; the object is now at M", and the prompt must say the verdict may be out of date.
- Independently fetched evidence is labelled "fetched at <time>". Evidence from stale or unreadable dependencies is labelled last-observed or unverified, with its as-of time.

**Identity is fenced on every path, anchored or not** (Codex re-review, 39a3602). A missing verdict is allowed; missing identity fencing is not.
- Every target carries the UID of the object the operator selected.
- Before any model call, the backend compares the fetched main object's UID against `target.uid`.
- A mismatch is refused, the same as a replaced anchored object.
- A target without a UID fails closed. The inspector always has the UID of the object it shows, and Explain is disabled until it does.

**Missing anchors:**
- **Generic Resources** (no Observer row): the prompt states "Klyx has no verdict for this kind", and the model explains only from conditions, facts and events. The UID fence above still applies. Test: the resource is recreated under the same name between selection and the GET, with no Observer row. Then there is no provider call, and no explanation of the replacement appears under the old selection.
- **Helm releases:** the anchor is the Observer's Helm snapshot entry (release, revision, status, read time). If the history read shows a newer revision, the result is qualified as "the release changed since Klyx's last read (revision N to M)". A release absent from the snapshot gets no anchor, the same as generic Resources.

### 4.2 The prompt contract (allowlisted, value-tested, untrusted)
**Allowlist.** Each kind declares exactly which fields enter the prompt, in one Go table (`aiEvidenceFields`). Nothing else crosses. Per kind:

| Kind | Fields (beyond identity, the anchor, conditions (type/status/reason/message), and up to 10 Warning events (reason, count, message, last seen)) |
|---|---|
| Flux Kustomization / HelmRelease | the bound source's identity and ready reason; the latest 5 controller Warning messages |
| Argo Application | sync status, health status, operation phase and message; resource counts by health |
| Helm release | status, chart and app version, the last 5 history entries (revision, status, updated, description) |
| Pod | per container: state, waiting or terminated reason, exit code, restart count, last termination reason; scheduling conditions |
| Workload | rollout conditions; ready, updated and available counts; the 3 worst pods' verdict labels |
| Node | conditions; allocatable vs requested CPU and memory |
| Karpenter NodeClaim / NodePool | the failing link and reason; limits vs usage |
| Gateway / HTTPRoute | rule role and label; per-backend state |
| ExternalSecret / store | store ready reason; refresh deadline |

Never sent: YAML (except Suggest-fix's masked manifest, as today), Secret data, Helm values, pod logs, source URLs with userinfo, and annotations other than the explicitly allowlisted ones.

**Scrub.** Every free-text value passes a scrubber before it enters the prompt. It removes:
- URL userinfo;
- bearer and basic auth headers;
- JWT-shaped tokens;
- PEM blocks;
- AWS access keys;
- Azure, GCP and GitHub token shapes;
- `password=`, `token=`, `secret=` and `key=` style pairs.

**Caps.** Each free-text field is capped (500 characters) and so is the whole evidence block (24 KiB). Truncation is marked.

**Untrusted.** Cluster text is quoted evidence:
- The evidence is one JSON document inside a delimited block.
- The system prompt states that everything inside it is data written by the cluster and its users, possibly hostile, and never instructions.
- No cluster text goes in the system prompt.
- No event text is claimed to be secret-free. The UI says "scrubbed", never "secret-free".

**Value tests** (table-driven, per kind): a distinct secret sentinel is planted in every allowed free-text surface, in each scrubbed shape (bearer, PEM, userinfo URL, `password=`), and in every disallowed field. The test asserts that no sentinel appears in the serialized prompt, and that caps hold at the limits.

### 4.3 Frontend
- **Shared pieces:** `useAIExplain(cluster, target)`, keyed by cluster and UID, token-fenced, discarded on selection change. `AIExplainBlock`, an `InspectorBlock`, shows:
  - the answer;
  - "Grounded on: …" with Klyx's verdict and its as-of time;
  - the provider, model used and origin;
  - the scrub and redaction counts;
  - any generation or staleness qualification;
  - the footer "AI-generated from Klyx's resolved state - verify before acting".
- **Placement:** "Explain with AI" goes in each lens inspector's More menu, plus a palette verb through NavIntent.
  - It is disabled with the reason when AI is off, egress is refused, or the CLI is unadmitted.
  - With no provider ever configured, More shows "Set up AI…", linking to Settings.
- **Lenses:**
  - Pods, Workloads, Nodes
  - Flux (Kustomizations, HelmReleases, sources)
  - Argo (Applications, ApplicationSets)
  - Helm
  - Certificates (onto the shared block)
  - External Secrets (ExternalSecrets, stores)
  - Gateway API (Gateways, HTTPRoutes)
  - Karpenter (NodePools, NodeClaims)
  - Resources (generic; no anchor)

  Events and the fleet views are out.
- **Suggest-fix** stays where a manifest exists, and Klyx never applies it.

## 5. Slices
Codex review per slice, with full gates. CLI work never blocks API setup or Explain.

Order as built (2026-10-13):
- Slices 1 and 2 are accepted.
- Slice 4 runs before slice 3 and is split in two:
  - **4a** is the backend: the anchored capture, the evidence contract, `Explain`.
  - **4b** is the frontend: the shared block in every lens, and the palette verb.
- Slice 3 waits on research into Claude Code's managed and remote policy channels, which its admission must inspect.

1. **Transport and config.**
   - `ai.Provider` and the immutable snapshot.
   - The Anthropic fix (§0), the OpenAI and Gemini providers, `openaiCompatible`.
   - The transport limits and failure mapping (§1.2), the keychain and credential lifecycle (§2.1), config v2 with back-compat, and the shared config write lock and hash check.
2. **Settings > AI.** `AISettingsService`, the save order and divergence reporting, list models, Test, the backend-authorized egress toggle, and Move to keychain.
3. **CLI adapters.** The Claude Code contract and admission, process containment per platform, and the probe (one small prompt on the owner's login, announced first). Codex and agy stay unsupported unless a no-tools contract appears.
4. **Explain everywhere.** The anchored capture, the evidence allowlist, scrubber and caps, the per-kind extras, `AIExplainBlock`, the lenses and the palette verb.
5. **Gate.** The owner's real-model smoke per configured provider. It replaces the backlog's "real-LLM smoke".

## 6. Testing
- **Providers:**
  - `httptest` per vendor with golden request bodies (no sampling on Claude or OpenAI);
  - refusal, safety, incomplete, empty and tool-output mapping;
  - the 4 MiB cap on reply, list and error bodies;
  - redirect rules (cross-host, HTTPS to HTTP);
  - the deadline bounding retries;
  - `ModelUsed` from the response;
  - key scrubbing of errors.
- **Snapshot:** concurrent calls during swaps, where every call's check, send and provenance share one rev.
- **Config writes:**
  - `AddClusters` racing `SaveAISettings` under the lock;
  - external edits rejected by the hash check;
  - failed write and failed build leave the active snapshot, with divergence reported;
  - back-compat mapping;
  - the keychain faked behind an interface (locked, unavailable, denied, absent);
  - the Clear, Move and failed-removal paths.
- **CLI:**
  - Contract verification is tested against recorded outputs: an unreviewed version, platform or hash; a failed sentinel probe; managed policy present; a policy cache present.
  - Pinned `auth status` and `doctor` fixtures: Pro admission followed by a Team, Enterprise or API-key login; an unknown, duplicate or missing key or line; undeterminable auth. In each case no evidence is delivered.
  - The stream-json contract: one fixture per refusal path in §1.5 (max tokens, refusal, missing or unknown finish data, duplicate init or result, events after the result, malformed or trailing data, contradictory metadata, a model switch, tool and unknown events).
  - Policy appearing between the helpers and launch, and between launch and stdin: the process is killed and stdin is never written.
  - A fake CLI binary emits each event shape: clean, tool event, unknown event, oversized stdout or stderr, hang.
  - argv carries no prompt content.
  - The environment is scrubbed and the working dir confined.
  - Group or Job kill on deadline.
  - Admission is invalidated when the executable hash changes.
- **Explain:**
  - replacement between the row capture and the GET (refused);
  - differing generation (qualified);
  - stale and unreadable dependencies (labelled);
  - `Included=true` with Role progressing (never "fault");
  - a time-rule crossing between capture and send;
  - the missing anchor for Resources and Helm;
  - the per-kind sentinel and cap tests (§4.2).
- **Frontend:**
  - the AI panel states (key source, divergence, admission, disclosure);
  - `AIExplainBlock` fencing;
  - disabled reasons;
  - one inspector test per lens.

## 7. Settled in review
- Vendor-default reasoning effort; no effort control for now.
- The Claude refusal fallback is on by default and disclosed. The model actually used is shown; an explicit final refusal fails closed.
- An admitted, no-tools CLI is not categorically refused on protected clusters (the normal opt-in applies). An unadmitted one is refused everywhere.

## 8. Non-goals
- The conversational assistant (its own spec).
- Streaming.
- Pod logs in prompts.
- Applying any fix.
- Per-cluster provider choice.
- A durable egress audit beyond the Activity log.
- Bedrock, Vertex or Foundry hosting of Claude.
- Codex `--oss`.
- Exposing reasoning effort.
