# Klyx v0.12.1

v0.12.1 fixes the Claude Code CLI provider. In v0.12.0 every real answer was refused with "subagent use", and the CLI was switched off until the next Test.

## Claude Code CLI provider

- **Test and Explain get past the final result.** Claude Code 2.1.296 writes a `subagent_stats` object on every result: running totals of the subagents it started, all zero when none ran. Klyx treated only an absent or empty object as "no subagents", so it discarded every real answer.
  - Klyx now accepts the object when every counter in it is zero.
  - Any non-zero counter, at any depth, still counts as subagent use: the answer is discarded and the CLI stays off until Test.
  - The shape was read from the shipped 2.1.296 binary, not guessed.
- **Nothing else in the result changed.** The rest of the success result was checked against the same binary and matches the contract.

## Known limitations / not yet verified

- **One real Test is the confirmation.** This is the first fix past the final result, and no full real run has been recorded as a fixture yet. Press Test in Settings > AI after updating. If it fails, the message names the check that refused it.
- **A second model in the usage report** would still fail an answer with "Claude Code used another model than the one configured". The checks in this release found no side call to another model in a print-mode run, but that is not yet confirmed by a real run.
- **Every v0.12.0 limitation still applies.**
