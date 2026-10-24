# Klyx v0.13.1

v0.13.1 keeps the Claude Code CLI provider working through Claude Code's own updates, and fixes the resource chips under chat answers. Both changes were reviewed by Codex.

## Claude Code CLI: any build, one Test per update

Claude Code updates itself every day or two. v0.13.0 ran only the one exact build Klyx had reviewed (2.1.296), so every update switched the provider off until a Klyx release.

- **Any build from 2.1.296 on is accepted once Test has checked it on this Mac.**
  - After Claude Code updates, Explain and chat pause with "This Claude Code build (…) has not been checked by Klyx on this machine yet. Press Test".
  - Press Test once in Settings > AI. Test checks the new build and runs its canary: a prompt asking Claude Code to read a file and run a command.
  - A build that passes is remembered in `claude-builds.json` beside `fleet.yaml`, so it keeps working across restarts.
- **Every check that does not depend on the exact build still runs on every call:**
  - the lockdown-flag probe;
  - the allowlisted environment, with file attachments off;
  - administrator policy, before every call;
  - a personal Pro or Max sign-in;
  - Claude Code's own report that it has no tools, MCP servers, skills or agents;
  - the full output check, where any tool or unknown event discards the answer.
- **A build that breaks the rules loses its standing.** Klyx turns it off and removes it from the record, so it needs Test again, even after a restart. This holds whatever the record file's permissions, and a removal that fails says which file to delete.
- **What this trades:** Klyx no longer proves that each flag of a new build means what it relies on. A changed meaning would be caught after the fact. Test's canary is the check on each new build before any cluster data goes through it. Spec: `docs/superpowers/specs/2026-10-13-klyx-ai-providers-and-explain.md` §1.5, revision of 2026-10-14.

## Cluster chat

- **Chips name their kind.** A Deployment and an HTTPRoute that are both `shop/api` no longer show as two identical chips.
- **Chips say how many they hold back.** Past 12, a "+N more" button shows the rest. Before, the row stopped at 12 without saying so.

## Known limitations / not yet verified

- **After updating Klyx, press Test once** in Settings > AI if you use the Claude Code CLI. The offline contract check (no sign-in, no model call, network blocked) passes on Claude Code 2.1.300.
- **Switching the CLI path between two tested builds** while Klyx runs needs one Test. After a restart, the path's build is admitted without one.
- **Chat has run against one real model so far:** Claude Haiku 5.5 on an Anthropic key, in tools mode, on the kind lab. These are still unexercised against a real model:
  - OpenAI, Gemini and an OpenAI-compatible server;
  - the Claude Code snapshot mode;
  - pod-log reads;
  - Explain follow-ups;
  - Stop and Clear.
- **Every v0.13.0 limitation still applies.**
