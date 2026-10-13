# Claude Code 2.1.296 contract fixtures

Bounded outputs for the reviewed Claude Code build (spec 2026-10-13 §1.5). Every file says how it was made:

- `*.captured.*`: printed by the real 2.1.296 binary (darwin/arm64) on 2026-10-13.
  - Each ran with no model call, in a throwaway `CLAUDE_CONFIG_DIR` with no login, and with Klyx's allowlisted environment.
  - Home and config paths are replaced with `/HOME` and `/HOME/.claude-throwaway`.
  - `auth-status-apikeyhelper` ran without the root lockdown switches, against user settings that define an `apiKeyHelper`.
  - `auth-status-locked-nologin` and `doctor-locked-nologin` ran with `--restricted --safe-mode` before the subcommand, so user settings are ignored.
- `*.synth.*`: synthesized for a signed-in Pro or Max account. Klyx makes no model or account calls during development.
  - `auth-status-max` follows the field set of the binary's `auth status` emitter for a claude.ai login. The account values are fake.
  - `doctor-promax` is the captured doctor output with its two policy lines replaced. The replacements are the binary's `unsupported_subscription` and `prosumer_oauth` strings.
- `stream-offline-init.captured.jsonl`: the real `system/init` and the first two `system/api_retry` events from the production argv and environment. It was captured on the owner's Max sign-in on 2026-10-13 under `sandbox-exec` with all outbound network denied, so no prompt left the machine. The cwd is replaced with `/CALLDIR`.
  - This capture is the source of the reviewed built-in plugin list (`cc-plugin-agents-md`, `cc-plugin-plugin-authoring`). Those plugins appear even under `--safe-mode`, which keeps built-in plugins on.
  - The stream tests build their init event from it.
- The `assistant` and `result` events in `claudecli_stream_test.go` are still synthesized from the binary's emitters.

The slice 5 smoke on the owner's login replaces the synthesized fixtures with captured ones. Do not relax the contract to make a capture pass without review.
