import { Events } from "@wailsio/runtime";
import { LineService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { applyLine, type LineDTO } from "../store/line";

// The line's transport (The Line slice 4 §5.1): subscribe to line:dto, THEN
// read the current line; the store keeps only newer versions, so a reply
// older than an event, or a duplicate event, never replaces what it holds.
export const LINE_EVENT = "line:dto";

let started = false;

// startLine is idempotent: the first caller starts it for the app's lifetime.
export function startLine(): void {
  if (started) return;
  started = true;
  Events.On(LINE_EVENT, (ev: { data: LineDTO }) => { applyLine(ev?.data); });
  void LineService.Line()
    .then((dto) => { applyLine(dto); })
    .catch(() => { /* the events keep arriving */ });
}

// _resetLineBridgeForTest lets a test start the bridge again.
export function _resetLineBridgeForTest(): void {
  started = false;
}
