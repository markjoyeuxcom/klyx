// newStreamId mints the id a log or drain stream will emit on. The FRONTEND
// chooses it and subscribes to `<prefix>:<id>` BEFORE asking the backend to
// open the stream: the backend emits the first batch - often the whole tail,
// or a short previous-container log end to end - before the open call returns,
// so a listener attached after the open lost it (review 2026-10-03 #8).
// Backend contract (appbridge.validStreamID): 1-128 chars of [A-Za-z0-9._:-].
let seq = 0;

export function newStreamId(kind: string): string {
  seq += 1;
  const rand =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `${kind}-${seq}-${rand}`;
}
