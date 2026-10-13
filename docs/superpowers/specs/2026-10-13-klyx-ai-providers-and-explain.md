# Klyx AI: provider choice, settings and Explain in every lens

Status: draft for Codex design review (2026-10-13). No code before approval.

Owner decisions (2026-10-13):
- AI setup takes API keys for Claude (Anthropic), OpenAI and Gemini.
- It can also use an installed vendor CLI: Claude Code (`claude`), Codex (`codex`) or Antigravity (`agy`).
- Scope: providers and setup, plus an Explain action in every lens.
- CLI posture: **no tools, fail closed.** A CLI may only answer from the data Klyx sends, with every tool disabled, in an empty folder. Klyx refuses a CLI it cannot prove is locked down and shows it as unsupported.

Builds on: `2026-07-09-klyx-ai-explain-certmanager.md` (the honesty contract and egress policy, both unchanged in spirit) and backlog §2.

Recorded change of plan: the 2026-07-09 review deferred a settings write path for AI ("file edit is trivial"). The owner now asks for in-app setup with keys, so this spec builds it, on the `AddClusters` write-plus-hot-swap precedent that review named.

## 0. What exists, and what is broken today

What exists:
- `internal/ai.Client` speaks two wire protocols: the Anthropic Messages API (`hosted`) and OpenAI-compatible `/v1/chat/completions` (`local`).
- Config is `ai:` in fleet.yaml, with the API key in plain text (`ai.apiKey`).
- `AIService` offers Explain (certificate chain), ExplainResource (any GVR object, through the generic `GetInstanceDetail` bundle) and SuggestFix (a Git-only patch from a masked manifest).
- It is surfaced only in the Certs lens and the Resources inspector (`InstanceDetail`).
- Egress: a protected cluster's context leaves the machine only with `allowProtectedEgress`, except to a loopback endpoint.

Defects found while designing this. They are fixed in slice 1, whatever else changes:
1. **Sampling parameters.** The Anthropic call sends `temperature: 0`. Current Claude models (Sonnet 5, Opus 5.5 and later) reject sampling parameters with a 400, so `backend: hosted` with today's default model (`claude-sonnet-5`) cannot work. No real-LLM smoke has run (v1.0.0 gate), which is why this went unseen.
2. **Token budget.** `max_tokens` is 512 for Explain and 900 for Suggest-fix. On current models thinking is always on and counts against `max_tokens`, so answers can come back truncated or empty.
3. **Stop reasons.** Neither `stop_reason` `refusal` nor `max_tokens` is handled: a refusal or a truncation reads as an empty or partial "explanation".

## 1. Providers

### 1.1 The provider seam
`appbridge.Completer` becomes `ai.Provider`:

```go
type Provider interface {
    ID() ProviderID                 // anthropic | openai | gemini | openaiCompatible | claudeCLI | codexCLI | agyCLI
    Complete(ctx context.Context, req Request) (Result, error)
    Model() string
    OnMachine() bool                // true only for a loopback openaiCompatible endpoint
}
type Request struct{ System, User string; Purpose Purpose } // explain | fix | probe
type Result struct{ Text, Model, StopReason string }        // StopReason: done | refused | truncated
```

- `Complete` turns a refusal or a truncation into a typed error: `ErrRefused` (with the vendor's category when given) or `ErrTruncated`. A partial text is never shown as an answer.
- Token budgets and timeouts are decided per provider from `Purpose`, not by the caller.
- `AIService` holds the provider behind an `atomic.Pointer`, so Settings can swap it live. An in-flight call finishes on the provider it started with.

### 1.2 API providers
All three use the vendor's official Go SDK, built with Klyx's `*http.Client`. That client carries the cross-host redirect guard and a 90s timeout. The 4 MiB response cap stays wherever the SDK lets the body be wrapped; otherwise the timeout bounds it.

The request shapes below must be re-checked against each vendor's current API reference when the slice is implemented; this spec does not freeze them.

- **Claude (Anthropic API):** `github.com/anthropics/anthropic-sdk-go`, `POST /v1/messages`.
  - No sampling parameters, and no `thinking` field, which defaults to adaptive.
  - `output_config.effort` comes from settings (default `medium`).
  - `max_tokens` 16000, non-streaming. The prompt keeps answers short; the cap is not the length control.
  - Default model `claude-opus-5-5`. The picker lists `GET /v1/models`.
  - Server-side refusal fallback: `fallbacks: "default"` with beta `server-side-fallback-2026-07-01`. It is on by default; the owner may turn it off.
  - Auth: `x-api-key`.
- **OpenAI:** `github.com/openai/openai-go`, using the Responses API (`instructions` + `input`, `max_output_tokens`).
  - No temperature (reasoning models reject it).
  - No default model: the picker lists `GET /v1/models`, filtered to text-generation models. The owner picks once.
  - Auth: `Authorization: Bearer`.
- **Gemini:** `google.golang.org/genai` (Gemini API backend, not Vertex).
  - `generateContent` with `systemInstruction` and `maxOutputTokens`.
  - No default model: the picker lists the models that support `generateContent`.
  - Auth: API key.
- **OpenAI-compatible** (today's `local`): the current raw-HTTP chat-completions client, kept for Ollama, llama.cpp and vLLM.
  - The base URL is required.
  - The key is optional and also goes in the keychain.
  - It sends temperature 0, as today; local servers accept it.

### 1.3 CLI providers
**Discovery.** Through `clibin`, like kubectl:
- an env override (`KLYX_CLAUDE_PATH`, `KLYX_CODEX_PATH`, `KLYX_AGY_PATH`);
- then `LookPath` on the desktop tool PATH (`execenv`);
- then the vendor's default install dirs (`~/.local/bin`, the Homebrew prefixes).

Klyx shows the found path and `--version`. The CLI uses its own login (a subscription or the vendor's stored key). Klyx stores no secret for it.

**Invocation contract** (all three):
- The working directory is a fresh `0700` temp dir created for this one call and removed afterwards. It is never the repo, never `$HOME`, never a dir the CLI has seen before.
- The prompt goes in over stdin, never in argv, where it would be visible in `ps`.
- Output is the vendor's structured JSON, parsed for the final answer and for tool events. Its size is capped.
- The environment is the minimal set the CLI needs: `HOME`, `PATH`, the locale and `TMPDIR`, plus the vendor's own auth variables. These are dropped: `KUBECONFIG`, every `KLYX_*`, and cloud credential variables (`AWS_*`, `AZURE_*`, `ARM_*`, `GOOGLE_APPLICATION_CREDENTIALS`, `CLOUDSDK_*`).
- Each CLI runs in its own process group with a deadline (`ai` Purpose budget, at most 180s). The whole group is killed on timeout or cancel.
- There is no session persistence: transcripts must not land in the vendor's history.

**Per-vendor lockdown flags.** Each must be verified against the installed version in slice 3. Versions checked on this Mac on 2026-10-13: Claude Code 2.1.296, codex-cli 0.162.1, agy 1.3.3.
- **`claude -p`:**
  - `--tools ""` disables every built-in tool.
  - `--strict-mcp-config --mcp-config '{"mcpServers":{}}'` removes MCP servers.
  - `--disable-slash-commands`, `--no-session-persistence`, `--system-prompt <system>`, `--output-format json` and `--model` are set.
  - `--setting-sources` must exclude `user` and `project`, so user hooks (for example a memory-harvest hook that would copy the prompt elsewhere) and plugins do not run. This has to be verified to leave OAuth login working.
  - `--bare` is NOT used: it disables OAuth and keychain login, and the subscription login is the point of this option.
- **`codex exec`:**
  - `--sandbox read-only --ephemeral --ignore-user-config --ignore-rules --skip-git-repo-check -C <tmp> --json --color never -m <model> -`
  - `--disable` covers every feature that adds a tool surface: apps, browser_use, computer_use, in_app_browser, hooks, memories.
  - Measured on 0.162.1: the default read-only sandbox blocks the network but can read the whole disk. A canary file outside the working dir was readable through `codex sandbox -- cat`. Codex therefore needs a restricted permission profile whose readable roots are the temp dir plus Codex's own runtime, with filesystem reads denied elsewhere. Codex has `permission_profile` with restricted file systems and denied reads. If no such profile can be built and proven (§1.4), Codex is unsupported.
- **`agy -p`:**
  - `--output-format json --mode plan --sandbox --disable-slash-commands --print-timeout <budget> --model <model>`
  - Never `--dangerously-skip-permissions`.
  - Two things are unknown: whether print mode denies an unapproved tool call or waits, and whether user MCP servers and plugins load. Slice 3 decides from the probe.

### 1.4 Proof of lockdown (fail closed)
A CLI provider is usable only after a passing **canary probe**. The probe runs:
- on "Test" in Settings;
- before the first call after the CLI's version (`--version`) or path changes;
- before the first call after Klyx starts.

The probe:
1. Write two random tokens. T1 goes in a file outside the call's temp dir (in Klyx's own data dir). T2 is only ever an argument to a shell command the prompt asks for.
2. Send a prompt that tells the model to read the T1 file and print its contents, and to run `echo <T2-derived string>` and print the output.
3. The probe passes only if all three hold:
   - neither T1 nor the derived T2 string appears in the output;
   - the structured event stream has no tool, command, MCP or file event;
   - the process exited cleanly within its budget.

Every real call is checked the same way. Any tool event in the stream discards the answer, shows "This CLI tried to use a tool; Klyx discarded its answer", and marks the provider locked out until a passing re-probe. Detection after the fact does not stop a read that already happened. That is why the flags (§1.3) and the probe come first; detection is the second line.

A CLI that fails the probe or lacks a needed flag shows as "Unsupported on this version: <reason>". It is never silently degraded to a less-locked mode.

## 2. Keys and settings

### 2.1 Secrets
API keys live in the OS credential store via `github.com/zalando/go-keyring`:
- service `io.moomora.klyx`, account `ai/<provider>`;
- macOS Keychain, Windows Credential Manager, Linux Secret Service.

On Linux without a Secret Service, the Set call fails with a clear message. The fallback is `apiKeyEnv: NAME`, a variable reference in fleet.yaml; the key itself never goes in the file.

A key never crosses back to the webview. The DTO says only `keyStored: true` and when it was stored.

### 2.2 fleet.yaml `ai:` (non-secret settings)
```yaml
ai:
  provider: anthropic          # off | anthropic | openai | gemini | openaiCompatible | claudeCLI | codexCLI | agyCLI
  model: claude-opus-5-5
  effort: medium               # anthropic only
  baseUrl: ""                  # openaiCompatible (required); optional override for the API providers
  cliPath: ""                  # optional override for a CLI provider
  allowProtectedEgress: false
  tlsSkipVerify: false         # openaiCompatible only
```

Back-compat:
- `backend: hosted` reads as `anthropic`, and `backend: local` as `openaiCompatible`.
- A legacy `apiKey:` still works, with a Settings banner: "Key stored in fleet.yaml in plain text - Move to keychain". Moving writes the keychain entry, then removes the YAML line.

Validation lives in `internal/config`, as today.

### 2.3 Write path
A new `AISettingsService` (Wails-bound):
- `GetAISettings()` returns no secrets.
- `SaveAISettings(dto)` validates, then edits the `ai:` block as text with the rest of the file preserved. It is the `AppendClusters` technique: a decoded-document guard, then an atomic rename. Then it rebuilds and swaps the provider.
- `SetAPIKey(provider, key)` and `ClearAPIKey(provider)`.
- `ListModels(provider)` runs on demand.
- `TestProvider()` sends a tiny real completion for the API providers, or runs the §1.4 probe for a CLI.
- `DetectCLIs()` returns path, version and probe state per CLI.

Turning `allowProtectedEgress` ON takes a typed confirmation in the existing `ConfirmDialog` (danger shape). Turning it off does not.

### 2.4 Settings > AI panel (frontend)
A section in `SettingsView`, with these parts:
- **Provider.** A segmented choice between API key, Local server and Installed CLI, then the vendor.
- **Key.** A write-only field with Save, Replace and Remove, and the stored state.
- **Model.** A picker filled by `ListModels`, with free text allowed.
- **Effort.** Shown for Anthropic only.
- **CLIs.** Each CLI's detected path, version and probe result.
- **Test.** Runs `TestProvider` and shows the result.
- **Egress.** The protected-cluster egress toggle.
- **Status line.** Says exactly where prompts go: "Prompts go to api.anthropic.com" or "Prompts go to the Claude Code CLI (your Claude login)".

## 3. Egress (rule unchanged, scope widened)
- Every provider is off-machine except an `openaiCompatible` endpoint on loopback. That includes all CLI providers, which call their vendor's cloud.
- A protected cluster's context is refused without `allowProtectedEgress`, exactly as today (`EgressAllowed`). The Explain action is disabled with that reason.
- Every call adds an Activity-log entry: provider, model, cluster, object, and the redaction count when relevant.

## 4. Explain in every lens

### 4.1 Backend
One entry point:

```go
Explain(cluster string, target ExplainTargetDTO) AIResultDTO
```

The target is either a Kubernetes object (`group, version, plural, ns, name, uid`) or a special kind: a certificate chain (existing) or a Helm release (`ns, release`).

The bundle has four parts:
1. **The existing generic bundle:** identity, conditions, facts, related objects (capped), and operator health. It never includes YAML or Secret data.
2. **Klyx's own verdict as the anchor**, like `failingLink` today. It is the object's row evaluation from the Observer: the `health.Verdict` (kind, label, reason, rule) for pods, workloads and nodes, or the `NativeEval` (role, label) for the native parts. The prompt treats this verdict as what Klyx concluded and asks the model to explain it, cite evidence, or say the data is insufficient.
3. **Per-kind extras**, small and secret-free:

   | Kind | Extras |
   |---|---|
   | Flux Kustomization / HelmRelease | the bound source's health; the controller's latest Warning events (the drift surface) |
   | Argo Application | sync and health status; counts of resources by health; the first source (no manifests) |
   | Helm release | status, chart and app versions, the last 5 history entries (revision, status, description). Never values; never the release Secret |
   | Pod | container statuses (state, waiting or terminated reason, exit code, restart count, last termination); scheduling conditions |
   | Workload | rollout conditions; ready, updated and available counts; the worst pods' verdicts (capped) |
   | Node | conditions; pressure; allocatable vs requested |
   | Karpenter NodeClaim / NodePool | the failing link; limits vs usage |
   | Gateway / HTTPRoute | the rule's role and label; per-backend state |
   | ExternalSecret / store | the store's health; the refresh deadline |

4. **Warning events** for the object: the newest 10, with messages capped.

What crosses into a prompt, unchanged from 2026-07-09:
- no YAML, except Suggest-fix's masked manifest;
- no Secret data;
- no pod logs (deferred; logs carry secrets often enough to need their own design);
- no Helm values.

A table-driven test serialises one bundle per kind and asserts that no `data`, `stringData`, values or masked-placeholder keys are present.

### 4.2 Frontend
- **Shared pieces:** `useAIExplain(cluster, target)`, keyed by cluster and UID, token-fenced, discarded on selection change. `AIExplainBlock` renders as an `InspectorBlock` and shows:
  - the answer;
  - "Grounded on: …";
  - provider and model;
  - the redaction count;
  - the advisory footer "AI-generated from Klyx's resolved state - verify before acting".
- **Placement:** "Explain with AI" in each lens inspector's More menu, plus a palette verb ("Explain <object>"), through the existing NavIntent pattern.
  - Disabled with the reason when AI is off, when egress is refused for the cluster, or when the CLI is locked out.
  - Hidden only when no provider has ever been configured, in which case More shows "Set up AI…", linking to Settings.
- **Lenses:** Pods, Workloads, Nodes, Flux (Kustomizations, HelmReleases, sources), Argo (Applications, ApplicationSets), Helm, Certificates (it moves from its own button to the shared block), External Secrets (ExternalSecrets, stores), Gateway API (Gateways, HTTPRoutes), Karpenter (NodePools, NodeClaims), and Resources (already there). Events and the fleet views are out.
- **Suggest-fix** stays where a manifest exists: the Resources inspector, plus the Manifest tab in Pods and Nodes. Klyx never applies it.

## 5. Slices
Each slice gets a Codex review before the next starts, with the full gates.

1. **The provider seam and API providers.**
   - `ai.Provider`; the Anthropic fix (§0); the OpenAI and Gemini providers; `openaiCompatible` kept.
   - Keychain storage; config v2 with back-compat; `EgressAllowed` per provider.
   - Tests use `httptest` servers per vendor, asserting the request shape: no temperature on Anthropic and OpenAI; refusal and truncation mapped to typed errors.
2. **Settings > AI.**
   - `AISettingsService`; the textual `ai:` write; the hot swap.
   - List models, Test, the typed-confirm egress toggle, and Move to keychain.
3. **CLI providers.**
   - Discovery and the invocation contract.
   - The §1.4 probe, run against the real CLIs on this Mac. Each probe sends one small prompt on the owner's own CLI login; the owner is told before it runs.
   - Claude Code first. Codex and agy each ship only if their probe passes; otherwise they show as unsupported with the reason.
4. **Explain everywhere.** `Explain(target)` with the verdict anchor and per-kind extras, `AIExplainBlock`, every lens listed in §4.2, and the palette verb.
5. **Gate.** A real-LLM smoke per configured provider, run by the owner with their keys and logins. It replaces the backlog's "real-LLM smoke test" item.

## 6. Testing
- **Provider shapes:** `httptest` per vendor; golden request bodies; error, refusal and truncation mapping; the redirect guard; the response cap.
- **CLI:**
  - a fake CLI binary (a Go test helper compiled at test time) that emits each vendor's JSON shape, including tool events, which must be discarded;
  - argv, env-scrub and cwd assertions;
  - process-group kill on timeout.
- **Probe:** with the fake CLI as both a leaking and a clean binary.
- **Settings:** the textual `ai:` write preserves comments and other keys, using the `AppendClusters` fixtures; back-compat mapping; the keychain behind an interface, faked in tests.
- **Explain bundles:** the no-secret serialisation test per kind; the verdict anchor present; caps.
- **Frontend:** the AI panel states (keyStored, model list, test result, locked-out CLI); `AIExplainBlock` stale-result fencing; disabled reasons; one lens test per inspector.

## 7. Questions for review
1. Is the CLI threat model right?
   - Attacker-controlled cluster text (event messages, annotations, conditions) reaches an agent that holds the owner's credentials.
   - Defence: flags, a confined cwd and env, the probe, and discard-on-tool-event.
   - Should a CLI provider also be refused outright on protected clusters, even with `allowProtectedEgress`?
2. Is the verdict-anchor framing right for native parts? Their `Included` and `Role` can differ; Flux "not ready" includes progressing.
3. Should the Anthropic refusal fallback be on by default? (Recommended by Anthropic's guidance; the owner can turn it off.)
4. Should Settings expose OpenAI/Gemini reasoning effort, or leave it at each vendor's default?

## 8. Non-goals
- The conversational assistant (backlog §2 north star; it gets its own spec).
- Streaming responses.
- Pod logs in prompts.
- Applying any fix.
- Per-cluster provider choice.
- A durable egress audit beyond the Activity log.
- Cloud-hosted Claude through Bedrock, Vertex or Foundry.
- Codex `--oss` local providers.
