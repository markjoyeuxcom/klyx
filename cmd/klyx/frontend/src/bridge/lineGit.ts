import { Events } from "@wailsio/runtime";
import { GitService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { applyLineGit, type LineGitDTO } from "../store/lineGit";

// The line's Git ordering transport (The Line slice 6d): subscribe to
// line:git, THEN read the current ordering; the store keeps only newer
// versions. Never polled.
export const LINE_GIT_EVENT = "line:git";

let started = false;

export function startLineGit(): void {
  if (started) return;
  started = true;
  Events.On(LINE_GIT_EVENT, (ev: { data: LineGitDTO }) => { applyLineGit(ev?.data); });
  void GitService.LineGit()
    .then((dto) => { applyLineGit(dto); })
    .catch(() => { /* the events keep arriving */ });
}

// fetchDue is the fleet screen's cadence call (slice 6 note §6.3): the
// backend fetches each tracked repository whose last attempt is at least 5
// minutes old, in the background.
export function fetchDue(): void {
  try {
    void GitService.FetchDue().catch(() => { /* line:git names a failed fetch */ });
  } catch { /* no backend (a test or a preview) */ }
}

export function _resetLineGitBridgeForTest(): void {
  started = false;
}
