// LiveIndicator: the "live"/"○ manual" status marker beside a view's refresh
// button, extracted from the identical copies in PodsView/EventsView (and
// pending adoption in WorkloadsView). live=true means a watch-backed
// subscription is feeding the view; false means updates need a manual refresh.
export function LiveIndicator({ live }: { live: boolean }) {
  if (live) {
    return (
      <span style={{ display: "inline-flex", alignItems: "center", gap: 4, fontSize: 10, color: "var(--color-text-success)" }}>
        <span style={{ width: 6, height: 6, borderRadius: "50%", background: "var(--color-text-success)", display: "inline-block", flexShrink: 0 }} />
        live
      </span>
    );
  }
  return (
    <span
      style={{ fontSize: 10, color: "var(--color-text-tertiary)", cursor: "default" }}
      title="live updates unavailable - use refresh"
    >
      ○ manual
    </span>
  );
}
