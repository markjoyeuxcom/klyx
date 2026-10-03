// WatchErrorBanner — the shared "this list may be lying to you" strip for
// watch-backed lenses. Error honesty rule: a failing watch renders as an
// error, never as an empty list. Previously copy-pasted verbatim in the
// Flux/Certs/ESO views. `failure` names what failed: the default is a failing
// watch; list views pass "read failed" when a one-shot list errored (the rows
// shown, if any, are the last good read).
export function WatchErrorBanner({ tool, error, failure = "watch is failing" }: { tool: string; error: string; failure?: string }) {
  return (
    <div
      role="alert"
      style={{
        padding: "8px 12px",
        borderRadius: "var(--border-radius-md)",
        border: "0.5px solid var(--color-text-danger)",
        color: "var(--color-text-danger)",
        fontSize: 12,
      }}
    >
      {tool} {failure} - this list may be empty or stale, not clean: {error}
    </div>
  );
}
