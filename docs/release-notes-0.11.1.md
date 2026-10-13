# Klyx v0.11.1

v0.11.1 fixes the Claude Code CLI provider from v0.11.0, which refused every run on a real sign-in. It also closes a gap that refusal exposed: Klyx now confirms Claude Code's lockdown before any cluster evidence is sent.

## Claude Code CLI provider

- **Test and Explain work on a real sign-in.** v0.11.0 expected Claude Code to list no plugins at all at startup. Claude Code 2.1.296 always lists two of its own built-in plugins, even in safe mode, so every run was refused with "init lists plugins".
  - Klyx now accepts exactly the built-in plugins it has reviewed for that Claude Code version: `cc-plugin-agents-md` and `cc-plugin-plugin-authoring`. Both ship inside the pinned binary and expose nothing under Klyx's lockdown.
  - Any installed, synced or unreviewed plugin is still refused, and the CLI stays off until you press Test in Settings > AI.
- **The lockdown is checked before any evidence is sent.** Claude Code reports its startup configuration only after it has read its prompt. So in v0.11.0, a broken lockdown could only be noticed after the prompt had gone out.
  - Now, when Klyx first admits the CLI, it starts Claude Code once with a fixed prompt that carries no cluster data. It reads only the startup report, stops the run there, and checks the report against the contract.
  - On macOS this check runs with the network blocked, so even the fixed prompt stays on your Mac.
  - A startup report that breaks the contract switches the CLI off until Test.
- **Settings wording.** After the CLI has been switched off, Settings no longer says "the first call admits it". Only Test admits it again.

## Known limitations / not yet verified

- **No full real-model smoke yet.** This fix comes from the first real-sign-in Test. A complete Explain through each provider is still part of the v1.0.0 gate. Until then, treat AI as a preview.
- **Every v0.11.0 limitation still applies,** including the exact Claude Code version pin (2.1.296, macOS Apple silicon) and the unsupported Codex and Antigravity CLIs.
