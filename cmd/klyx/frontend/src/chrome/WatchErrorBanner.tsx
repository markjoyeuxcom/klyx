// WatchErrorBanner — the shared "this list may be lying to you" strip for
// watch-backed lenses. Error honesty rule: a failing watch renders as an
// error, never as an empty list. Previously copy-pasted verbatim in the
// Flux/Certs/ESO views.
export function WatchErrorBanner({ tool, error }: { tool: string; error: string }) {
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
      {tool} watch is failing - this list may be empty or stale, not clean: {error}
    </div>
  );
}
