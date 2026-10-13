# Klyx v0.11.0

v0.11.0 rebuilds AI assist. Choose your provider in Settings: a Claude, OpenAI or Gemini API key, a model server on your network, or the Claude Code CLI you already have installed. Explain with AI is now in every lens inspector. AI stays off until you choose a provider, and Git remains the source of truth: Suggest-fix proposes Git changes and never applies them. This is a pre-1.0 release; see "Known limitations" before relying on it.

## Providers and setup (Settings > AI)

- **Five ways to connect:**
  - **API keys:** Claude (Anthropic), OpenAI or Gemini.
  - **A local model server:** an OpenAI-compatible endpoint such as Ollama.
  - **The installed Claude Code CLI**, on your Claude sign-in.
- **Keys live in the OS keychain**, or come from an environment variable you name. A key goes in through a write-only field and never comes back to the page. Klyx uses exactly the key source you chose and never falls back to another one. Removing a key stops the running provider at once.
- **Model picker** filled from the provider's own model list. Free text is still allowed.
- **Test** sends one short prompt with no cluster data and shows the reply, the model and where it went.
- **Settings save safely.** A setting is saved only after the provider builds. An edit made to `fleet.yaml` meanwhile is refused rather than overwritten, and Settings says when the file and the running provider disagree.
- **Where prompts go is always stated:** the status line names the origin. A custom origin for a vendor key must use HTTPS, and Settings says your key will go there instead of the vendor.
- **Protected clusters:** context leaves the machine only with protected egress turned on. Turning it on needs a typed confirmation, which the backend enforces too. A model on this machine needs no opt-in.

## Claude API fixes

- The old call sent `temperature: 0`, which current Claude models reject. It now sends no sampling parameters.
- The old output budget was too small for models that think before answering. It now leaves room for that.
- The stop reason is decoded. A refusal, a truncated answer or an unconfirmed finish is shown as an error, never as an explanation.
- **Refusal fallback** (on by default, and disclosed): a refused request can be retried on another Claude model, and the result names the model that answered.
- OpenAI (Responses API, nothing stored) and Gemini follow the same rule: only a confirmed, complete text answer is shown.

## Explain with AI, everywhere

- **Every lens inspector has Explain with AI:** pods, workloads, nodes, Flux, Argo CD, Helm, Certificates and issuers, External Secrets and stores, Gateways and routes, Karpenter, and any object in Resources. The command palette offers `explain` for loaded objects too.
- **Anchored on Klyx's own verdict.** The model is asked to explain the verdict Klyx already reached ("CrashLoopBackOff", "Stalled"), not to diagnose from scratch. The answer says what it rests on: the verdict, the model, where the prompt went, and how many values were scrubbed.
- **The object you selected, not one that replaced it.** Each request is tied to the selected object's UID. If the object was recreated under the same name before the call, Klyx refuses. A certificate's issuance chain is checked the same way, against the chain's root.
- **Narrow, scrubbed, capped evidence.** Only a per-lens allowlist of fields is sent, framed as untrusted data: verdicts, conditions, facts, related health and recent warning events. YAML, Secret data and pod logs are never sent. Every string is scrubbed of credential shapes, and the evidence is capped per field, per list and in total.
- **Stale replies are dropped.** A reply that arrives after you selected something else, or closed the inspector, is never shown and never logged.
- **Suggest-fix** is held to the same UID check and untrusted framing. It still only proposes a Git change.

## The Claude Code CLI as a provider

Klyx runs your installed `claude` as a plain completion backend, under a reviewed contract:
- **Locked down.** Claude Code runs with no tools, no MCP servers, no hooks, no settings, no CLAUDE.md, no skills and no slash commands. `@path` expansion is switched off, and the environment is an allowlist. The prompt goes on stdin, never in the command line.
- **Only a reviewed build.** Klyx admits a binary only if it is exactly a build it has reviewed. For this release that is 2.1.296 on macOS (Apple silicon).
- **Only a personal Pro or Max sign-in.** Before every call, Klyx re-checks the sign-in and administrator policy, and refuses if either changed. Team and Enterprise accounts are refused, because their organisation can push policy Klyx cannot inspect.
- **Fails closed.** Klyx throws the answer away, and switches the CLI off until you press Test, if any of these happens:
  - a tool attempt;
  - an unexpected event or malformed output;
  - a model switch;
  - a change of account.
- **Test** re-admits the CLI and sends one short prompt on your Claude sign-in. The prompt asks Claude Code to read a file and run a command. An answer that shows it could fails the Test.

## Upgrading

- An existing `ai:` block in `fleet.yaml` keeps working: `backend: hosted` maps to Claude, and `backend: local` maps to the local server. A plain-text `apiKey` keeps working too, and Settings offers to move it into the keychain.

## Known limitations / not yet verified

- **No real-model smoke yet.** The new providers are tested against recorded and fake vendor responses, and Claude Code is tested against a fake CLI plus a check of the real binary with no sign-in. No live call to Anthropic, OpenAI, Gemini or Claude Code has been part of this release's verification. Treat AI as a preview until that smoke runs; it is part of the v1.0.0 gate.
- **The Claude Code CLI needs a reviewed build.**
  - Claude Code updates itself often, and Klyx refuses any version it has not reviewed. After an update, Explain through the CLI stays off until a Klyx release reviews the new version.
  - Until then, you can point the CLI path in Settings at `~/.local/share/claude/versions/2.1.296` while that copy exists.
  - The CLI provider works on macOS Apple silicon only for now.
- **The Codex and Antigravity (agy) CLIs are not supported.** Neither has a mode with every tool switched off.
- Everything listed under v0.10.0's known limitations still applies, including the rest of the v1.0.0 gate, unsigned builds, and no Linux arm64 build.
